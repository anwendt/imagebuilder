// pkg/controller/buildpod/assembler.go
//
// Assembles the Kubernetes Job that runs the VM image build.
//
// Job structure:
//   Init containers  — one per init-container provisioner (ansible, chef, …)
//   Main container   — QEMU build engine + in-process provisioners + upload
//
// Filesystem contract (ADR-003):
//   /workspace/config.json  — operator writes ProvisionerInput before each init-container
//   /workspace/status.json  — init-container writes ProvisionerOutput before exit
//
// The workspace volume is a shared emptyDir mounted into every container.

package buildpod

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
)

const (
	workspaceMount = "/workspace"
	workspaceVol   = "workspace"

	// defaultBuilderImage is used when no custom image is specified in BuildSpec.
	// Override via BUILDER_IMAGE env var in the operator deployment.
	defaultBuilderImage = "ghcr.io/anwendt/imagebuilder-builder:latest"
)

// initContainerTypes lists provisioner types that run as Kubernetes init containers.
// This mirrors the kubebuilder enum in ProvisionerSpec.Type (REQ-001, ADR-003).
// Kept here as a static map so the assembler does not depend on runtime plugin
// registration — the set of types is fixed at compile time by the CRD schema.
var initContainerTypes = map[string]bool{
	"ansible":   true,
	"chef":      true,
	"puppet":    true,
	"saltstack": true,
	"custom":    true,
}

// inProcessTypes lists provisioner types that run inside the main build container.
var inProcessTypes = map[string]bool{
	"cloud-init":  true,
	"shell":       true,
	"file":        true,
	"powershell":  true,
	"sysprep":     true,
}

// Assemble builds the Kubernetes Job spec for a VMImage build.
// The Job is owned by img so it is garbage-collected when the VMImage is deleted.
func Assemble(img *v1alpha1.VMImage, scheme *runtime.Scheme) (*batchv1.Job, error) {
	initContainers, err := buildInitContainers(img)
	if err != nil {
		return nil, fmt.Errorf("build init containers: %w", err)
	}

	mainContainer := buildMainContainer(img)
	volumes := buildVolumes(img)

	backoffLimit := int32(0) // no automatic retries — the operator controls retry logic
	ttl := int32(3600)       // clean up completed jobs after 1 hour

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName(img),
			Namespace: img.Namespace,
			Labels:    jobLabels(img),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: jobLabels(img),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:  corev1.RestartPolicyNever,
					InitContainers: initContainers,
					Containers:     []corev1.Container{mainContainer},
					Volumes:        volumes,
					NodeSelector:   img.Spec.Build.NodeSelector,
					// SR-011: drop all capabilities, non-root UID, read-only root fs where possible.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
		},
	}

	// Set the VMImage as owner so the Job is deleted with the VMImage.
	if err := controllerutil.SetControllerReference(img, job, scheme); err != nil {
		return nil, fmt.Errorf("set owner reference: %w", err)
	}

	return job, nil
}

// ---------------------------------------------------------------------------
// Init containers (one per init-container provisioner, in spec order)
// ---------------------------------------------------------------------------

func buildInitContainers(img *v1alpha1.VMImage) ([]corev1.Container, error) {
	var containers []corev1.Container
	step := 0
	for _, p := range img.Spec.Provisioners {
		if !isInitContainer(p.Type) {
			continue
		}
		img := p.Image
		if img == "" {
			img = defaultImageForProvisioner(p.Type)
		}
		if img == "" {
			return nil, fmt.Errorf("provisioner %q has no image and no built-in default", p.Type)
		}

		envVars := []corev1.EnvVar{
			{
				// Tell the init-container which step it is so it can read /workspace/config.json
				Name:  "PROVISIONER_STEP",
				Value: fmt.Sprintf("%d", step),
			},
		}
		for _, e := range p.Env {
			envVars = append(envVars, translateEnvVar(e))
		}

		containers = append(containers, corev1.Container{
			Name:  fmt.Sprintf("provisioner-%d-%s", step, p.Type),
			Image: img,
			Args:  p.Args,
			Env:   envVars,
			VolumeMounts: []corev1.VolumeMount{
				{Name: workspaceVol, MountPath: workspaceMount},
			},
			SecurityContext: restrictedSecCtx(),
		})
		step++
	}
	return containers, nil
}

// ---------------------------------------------------------------------------
// Main build container
// ---------------------------------------------------------------------------

func buildMainContainer(img *v1alpha1.VMImage) corev1.Container {
	builderImage := defaultBuilderImage

	res := corev1.ResourceRequirements{}
	if r := img.Spec.Build.Resources; r != nil {
		if r.CPU != "" {
			res.Requests = corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(r.CPU),
			}
			res.Limits = corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(r.CPU),
			}
		}
		if r.Memory != "" {
			if res.Requests == nil {
				res.Requests = corev1.ResourceList{}
				res.Limits = corev1.ResourceList{}
			}
			res.Requests[corev1.ResourceMemory] = resource.MustParse(r.Memory)
			res.Limits[corev1.ResourceMemory] = resource.MustParse(r.Memory)
		}
	}

	return corev1.Container{
		Name:  "build",
		Image: builderImage,
		Env: []corev1.EnvVar{
			{Name: "VMIMAGE_NAME", Value: img.Name},
			{Name: "VMIMAGE_NAMESPACE", Value: img.Namespace},
			{Name: "OS_FAMILY", Value: img.Spec.OS.Family},
			{Name: "OS_DISTRIBUTION", Value: img.Spec.OS.Distribution},
			{Name: "OS_VERSION", Value: img.Spec.OS.Version},
			{Name: "OS_ARCH", Value: img.Spec.OS.Arch},
			{Name: "SOURCE_TYPE", Value: img.Spec.Source.Type},
			{Name: "SOURCE_URL", Value: img.Spec.Source.URL},
			{Name: "SOURCE_CHECKSUM", Value: img.Spec.Source.Checksum},
		},
		Resources: res,
		VolumeMounts: []corev1.VolumeMount{
			{Name: workspaceVol, MountPath: workspaceMount},
		},
		SecurityContext: restrictedSecCtx(),
	}
}

// ---------------------------------------------------------------------------
// Volumes
// ---------------------------------------------------------------------------

func buildVolumes(img *v1alpha1.VMImage) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: workspaceVol,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Optional build cache PVC (FR-003, NFR-024).
	if img.Spec.Build.CacheRef != nil && *img.Spec.Build.CacheRef != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "build-cache",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: *img.Spec.Build.CacheRef,
				},
			},
		})
	}

	return volumes
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jobName(img *v1alpha1.VMImage) string {
	return fmt.Sprintf("%s-build", img.Name)
}

func jobLabels(img *v1alpha1.VMImage) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "imagebuilder",
		"imagebuilder.io/vmimage":      img.Name,
	}
}

func isInitContainer(provisionerType string) bool {
	if initContainerTypes[provisionerType] {
		return true
	}
	if inProcessTypes[provisionerType] {
		return false
	}
	// Unknown type: fall back to the runtime provisioner registry.
	// If not registered as in-process there either, treat as init-container.
	return provisioner.IsInitContainer(provisionerType)
}

func defaultImageForProvisioner(provisionerType string) string {
	defaults := map[string]string{
		"ansible":   "ghcr.io/anwendt/imagebuilder-provisioner-ansible:latest",
		"chef":      "ghcr.io/anwendt/imagebuilder-provisioner-chef:latest",
		"puppet":    "ghcr.io/anwendt/imagebuilder-provisioner-puppet:latest",
		"saltstack": "ghcr.io/anwendt/imagebuilder-provisioner-saltstack:latest",
	}
	return defaults[provisionerType]
}

func translateEnvVar(e v1alpha1.EnvVar) corev1.EnvVar {
	env := corev1.EnvVar{Name: e.Name, Value: e.Value}
	if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
		env.ValueFrom = &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: e.ValueFrom.SecretKeyRef.Name,
				},
				Key: e.ValueFrom.SecretKeyRef.Key,
			},
		}
	}
	return env
}

// restrictedSecCtx returns a minimal SecurityContext that satisfies Pod Security
// Standard "restricted" (SR-011, REQ-004).
func restrictedSecCtx() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: boolPtr(false),
		ReadOnlyRootFilesystem:   boolPtr(true),
		RunAsNonRoot:             boolPtr(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func boolPtr(b bool) *bool { return &b }
