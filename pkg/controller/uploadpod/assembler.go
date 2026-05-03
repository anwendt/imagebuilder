package uploadpod

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/buildpod"
)

const (
	workspaceMount = "/workspace"
	workspaceVol   = "workspace"

	defaultUploaderImage = "ghcr.io/anwendt/imagebuilder-uploader:0.1.0"
)

type TargetConfig struct {
	ProviderConfigName string            `json:"providerConfigName"`
	Provider           string            `json:"provider"`
	Region             string            `json:"region,omitempty"`
	Endpoint           string            `json:"endpoint,omitempty"`
	Insecure           bool              `json:"insecure,omitempty"`
	Extra              map[string]string `json:"extra,omitempty"`
	Format             string            `json:"format"`
	Tags               map[string]string `json:"tags,omitempty"`
	CredentialsPath    string            `json:"credentialsPath"`
}

func Assemble(img *v1alpha1.VMImage, configs []v1alpha1.ProviderConfig, scheme *runtime.Scheme) (*batchv1.Job, error) {
	return assemble(img, configs, scheme, false)
}

func AssembleCleanup(img *v1alpha1.VMImage, configs []v1alpha1.ProviderConfig, scheme *runtime.Scheme) (*batchv1.Job, error) {
	return assemble(img, configs, scheme, true)
}

func assemble(img *v1alpha1.VMImage, configs []v1alpha1.ProviderConfig, scheme *runtime.Scheme, cleanupOnly bool) (*batchv1.Job, error) {
	targets, err := targetConfigs(img, configs)
	if err != nil {
		return nil, err
	}
	targetData, err := json.Marshal(targets)
	if err != nil {
		return nil, fmt.Errorf("marshal upload targets: %w", err)
	}

	backoffLimit := int32(0)
	ttl := int32(3600)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobNameForMode(img, cleanupOnly),
			Namespace: img.Namespace,
			Labels:    jobLabels(img),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: jobLabels(img)},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: boolPtr(false),
					HostNetwork:                  false,
					HostPID:                      false,
					HostIPC:                      false,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "upload",
							Image: uploaderImage(img),
							Env: []corev1.EnvVar{
								{Name: "WORKSPACE_DIR", Value: workspaceMount},
								{Name: "UPLOAD_TARGETS_JSON", Value: string(targetData)},
								{Name: "UPLOAD_CLEANUP_ONLY", Value: fmt.Sprintf("%t", cleanupOnly)},
							},
							VolumeMounts:    uploadVolumeMounts(configs),
							SecurityContext: restrictedSecCtx(),
						},
					},
					Volumes:      uploadVolumes(img, configs),
					NodeSelector: img.Spec.Build.NodeSelector,
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(img, job, scheme); err != nil {
		return nil, fmt.Errorf("set owner reference: %w", err)
	}
	return job, nil
}

func uploadVolumeMounts(configs []v1alpha1.ProviderConfig) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{{Name: workspaceVol, MountPath: workspaceMount}}
	seen := map[string]bool{}
	for _, cfg := range configs {
		if cfg.Spec.Credentials.SecretRef.Name == "" || seen[cfg.Name] {
			continue
		}
		seen[cfg.Name] = true
		mounts = append(mounts, corev1.VolumeMount{
			Name:      credentialVolumeName(cfg.Name),
			MountPath: fmt.Sprintf("/credentials/%s", cfg.Name),
			ReadOnly:  true,
		})
	}
	return mounts
}

func uploadVolumes(img *v1alpha1.VMImage, configs []v1alpha1.ProviderConfig) []corev1.Volume {
	volumes := []corev1.Volume{workspaceVolume(img)}
	seen := map[string]bool{}
	for _, cfg := range configs {
		secretName := cfg.Spec.Credentials.SecretRef.Name
		if secretName == "" || seen[cfg.Name] {
			continue
		}
		seen[cfg.Name] = true
		volumes = append(volumes, corev1.Volume{
			Name: credentialVolumeName(cfg.Name),
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretName,
					Optional:   boolPtr(false),
					Items:      credentialItems(cfg),
				},
			},
		})
	}
	return volumes
}

func credentialItems(cfg v1alpha1.ProviderConfig) []corev1.KeyToPath {
	key := cfg.Spec.Credentials.SecretRef.Key
	if key == "" {
		return nil
	}
	return []corev1.KeyToPath{{Key: key, Path: "credentials"}}
}

func targetConfigs(img *v1alpha1.VMImage, configs []v1alpha1.ProviderConfig) ([]TargetConfig, error) {
	byName := make(map[string]v1alpha1.ProviderConfig, len(configs))
	for _, cfg := range configs {
		byName[cfg.Name] = cfg
	}
	targets := make([]TargetConfig, 0, len(img.Spec.Targets))
	for _, target := range img.Spec.Targets {
		cfg, ok := byName[target.ProviderConfigRef.Name]
		if !ok {
			return nil, fmt.Errorf("provider config %q not supplied", target.ProviderConfigRef.Name)
		}
		targets = append(targets, TargetConfig{
			ProviderConfigName: cfg.Name,
			Provider:           cfg.Spec.Provider,
			Region:             cfg.Spec.Region,
			Endpoint:           cfg.Spec.Endpoint,
			Insecure:           cfg.Spec.Insecure,
			Extra:              cfg.Spec.Extra,
			Format:             target.Format,
			Tags:               target.Tags,
			CredentialsPath:    fmt.Sprintf("/credentials/%s", cfg.Name),
		})
	}
	return targets, nil
}

func workspaceVolume(img *v1alpha1.VMImage) corev1.Volume {
	return corev1.Volume{
		Name: workspaceVol,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: buildpod.WorkspaceClaimName(img),
			},
		},
	}
}

func JobName(img *v1alpha1.VMImage) string {
	return fmt.Sprintf("%s-upload", img.Name)
}

func CleanupJobName(img *v1alpha1.VMImage) string {
	return fmt.Sprintf("%s-upload-cleanup", img.Name)
}

func jobNameForMode(img *v1alpha1.VMImage, cleanupOnly bool) string {
	if cleanupOnly {
		return CleanupJobName(img)
	}
	return JobName(img)
}

func uploaderImage(img *v1alpha1.VMImage) string {
	if img.Spec.Build.Upload != nil && img.Spec.Build.Upload.Image != "" {
		return img.Spec.Build.Upload.Image
	}
	if image := strings.TrimSpace(os.Getenv("UPLOADER_IMAGE")); image != "" {
		return image
	}
	return defaultUploaderImage
}

func credentialVolumeName(providerConfigName string) string {
	return fmt.Sprintf("credentials-%s", providerConfigName)
}

func jobLabels(img *v1alpha1.VMImage) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "imagebuilder",
		"imagebuilder.io/vmimage":      img.Name,
		"imagebuilder.io/job-kind":     "upload",
	}
}

func restrictedSecCtx() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		ReadOnlyRootFilesystem:   boolPtr(true),
		RunAsNonRoot:             boolPtr(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func boolPtr(b bool) *bool { return &b }
