// pkg/controller/buildpod/assembler.go
//
// Assembles the Kubernetes Job that runs the VM image build.
//
// Job structure:
//   Init containers  — restartable sidecar-style init containers for complex provisioners
//   Main container   — QEMU build engine + in-process provisioners + upload
//
// Filesystem contract (ADR-003):
//   /workspace/provisioners/step-N/config.json  — builder writes ProvisionerInput
//   /workspace/provisioners/step-N/status.json  — init-container writes ProvisionerOutput
//
// The workspace volume is either a per-build emptyDir or an artifact PVC,
// depending on spec.build.artifactStorage.

package buildpod

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	workspaceMount      = "/workspace"
	workspaceVol        = "workspace"
	cacheMount          = "/cache"
	cacheVol            = "build-cache"
	guestCredsMount     = "/credentials/guest"
	guestCredsVol       = "guest-credentials" // #nosec G101 -- Kubernetes volume name, not credential material.
	generatedCredsMount = "/credentials/generated"
	generatedCredsVol   = "generated-credentials"
	gitCredsMount       = "/credentials/git"
	gitCredsVolPrefix   = "git-credentials-"
	tmpMount            = "/tmp"
	tmpVol              = "tmp"
	kvmMount            = "/dev/kvm"
	kvmVol              = "dev-kvm"

	// defaultBuilderImage is used when BUILDER_IMAGE is not set in the
	// operator deployment.
	defaultBuilderImage = "ghcr.io/anwendt/imagebuilder-builder:0.3.0"
)

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
					NodeName:       img.Status.ScheduledNodeName,
					Tolerations:    buildTolerations(img),
					// AS-028: do not mount a service account token — the build pod
					// has no API server access; mounting a token is a needless attack surface.
					AutomountServiceAccountToken: boolPtr(false),
					// AS-053: explicitly forbid host namespace sharing.
					HostNetwork: false,
					HostPID:     false,
					HostIPC:     false,
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
	restartPolicy := corev1.ContainerRestartPolicyAlways
	for _, p := range img.Spec.Provisioners {
		if !isInitContainer(p.Type) {
			continue
		}
		image := p.Image
		if image == "" {
			image = defaultImageForProvisioner(p.Type)
		}
		if image == "" {
			return nil, fmt.Errorf("provisioner %q has no image and no built-in default", p.Type)
		}

		envVars := []corev1.EnvVar{
			{
				Name:  "PROVISIONER_STEP",
				Value: fmt.Sprintf("%d", step),
			},
			{Name: "PROVISIONER_CONFIG_PATH", Value: fmt.Sprintf("%s/provisioners/step-%d/config.json", workspaceMount, step)},
			{Name: "PROVISIONER_STATUS_PATH", Value: fmt.Sprintf("%s/provisioners/step-%d/status.json", workspaceMount, step)},
		}
		for _, e := range p.Env {
			envVars = append(envVars, translateEnvVar(e))
		}

		volumeMounts := []corev1.VolumeMount{
			{Name: workspaceVol, MountPath: workspaceMount},
		}
		if hasGuestCredentials(img) {
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      guestCredsVol,
				MountPath: guestCredsMount,
				ReadOnly:  true,
			})
		}
		if generatesGuestCredentials(img) {
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      generatedCredsVol,
				MountPath: generatedCredsMount,
			})
		}
		volumeMounts = append(volumeMounts, gitAuthVolumeMounts(img)...)

		containers = append(containers, corev1.Container{
			Name:            fmt.Sprintf("provisioner-%d-%s", step, p.Type),
			Image:           image,
			Args:            p.Args,
			Env:             envVars,
			VolumeMounts:    volumeMounts,
			RestartPolicy:   &restartPolicy,
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
	builderImage := builderImage()

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

	// Encode boot_command as a JSON array so the build engine can parse it
	// without needing to understand any quoting or escaping conventions.
	// Empty slice → empty JSON array "[]"; nil slice → omitted env var.
	bootCmdEnv := bootCommandEnv(img.Spec.Source.BootCommand)
	provisionersEnv := provisionersEnv(withGitAuthMountPaths(img.Spec.Provisioners))

	env := []corev1.EnvVar{
		{Name: "BUILD_ID", Value: buildID(img)},
		{Name: "VMIMAGE_NAME", Value: img.Name},
		{Name: "VMIMAGE_NAMESPACE", Value: img.Namespace},
		{Name: "OS_FAMILY", Value: img.Spec.OS.Family},
		{Name: "OS_DISTRIBUTION", Value: img.Spec.OS.Distribution},
		{Name: "OS_VERSION", Value: img.Spec.OS.Version},
		{Name: "OS_ARCH", Value: img.Spec.OS.Arch},
		{Name: "SOURCE_TYPE", Value: img.Spec.Source.Type},
		{Name: "SOURCE_URL", Value: img.Spec.Source.URL},
		{Name: "SOURCE_CHECKSUM", Value: img.Spec.Source.Checksum},
		{Name: "WORKSPACE_DIR", Value: workspaceMount},
	}
	if len(img.Spec.Targets) > 0 {
		env = append(env,
			corev1.EnvVar{Name: "TARGET_PROVIDER_CONFIG", Value: img.Spec.Targets[0].ProviderConfigRef.Name},
			corev1.EnvVar{Name: "TARGET_FORMAT", Value: img.Spec.Targets[0].Format},
		)
	}
	if bootCmdEnv != nil {
		env = append(env, *bootCmdEnv)
	}
	if provisionersEnv != nil {
		env = append(env, *provisionersEnv)
	}
	env = append(env, guestAccessEnv(img.Spec.Build.GuestAccess)...)

	volumeMounts := []corev1.VolumeMount{
		{Name: workspaceVol, MountPath: workspaceMount},
		{Name: tmpVol, MountPath: tmpMount},
	}
	if hasGuestCredentials(img) {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      guestCredsVol,
			MountPath: guestCredsMount,
			ReadOnly:  true,
		})
	}
	volumeMounts = append(volumeMounts, gitAuthVolumeMounts(img)...)
	if generatesGuestCredentials(img) {
		env = append(env, corev1.EnvVar{Name: "GUEST_CREDENTIALS_DIR", Value: generatedCredsMount})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      generatedCredsVol,
			MountPath: generatedCredsMount,
		})
	}
	if hasCacheRef(img) {
		env = append(env, corev1.EnvVar{Name: "CACHE_DIR", Value: cacheMount})
		if img.Spec.Build.Cache != nil {
			if img.Spec.Build.Cache.TTL != nil {
				env = append(env, corev1.EnvVar{Name: "CACHE_TTL", Value: img.Spec.Build.Cache.TTL.Duration.String()})
			}
			if img.Spec.Build.Cache.RetainPolicy != "" {
				env = append(env, corev1.EnvVar{Name: "CACHE_RETAIN_POLICY", Value: img.Spec.Build.Cache.RetainPolicy})
			}
		}
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: cacheVol, MountPath: cacheMount})
	}
	if kvmEnabled(img) {
		env = append(env, corev1.EnvVar{Name: "QEMU_ENABLE_KVM", Value: "true"})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      kvmVol,
			MountPath: kvmMount,
		})
	}

	return corev1.Container{
		Name:            "build",
		Image:           builderImage,
		Env:             env,
		Resources:       res,
		VolumeMounts:    volumeMounts,
		SecurityContext: restrictedSecCtx(),
	}
}

func builderImage() string {
	if image := strings.TrimSpace(os.Getenv("BUILDER_IMAGE")); image != "" {
		return image
	}
	return defaultBuilderImage
}

func buildID(img *v1alpha1.VMImage) string {
	if img.UID != "" {
		return string(img.UID)
	}
	if img.Namespace != "" {
		return img.Namespace + "/" + img.Name
	}
	return img.Name
}

// ---------------------------------------------------------------------------
// Volumes
// ---------------------------------------------------------------------------

func buildVolumes(img *v1alpha1.VMImage) []corev1.Volume {
	volumes := []corev1.Volume{
		workspaceVolume(img),
		{
			Name: tmpVol,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Optional build cache PVC (FR-003, NFR-024).
	if hasCacheRef(img) {
		volumes = append(volumes, corev1.Volume{
			Name: cacheVol,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: cacheRef(img),
				},
			},
		})
	}
	if hasGuestCredentials(img) {
		volumes = append(volumes, guestCredentialsVolume(img))
	}
	volumes = append(volumes, gitAuthVolumes(img)...)
	if generatesGuestCredentials(img) {
		volumes = append(volumes, corev1.Volume{
			Name: generatedCredsVol,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		})
	}
	if kvmEnabled(img) {
		deviceType := corev1.HostPathCharDev
		volumes = append(volumes, corev1.Volume{
			Name: kvmVol,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: kvmMount,
					Type: &deviceType,
				},
			},
		})
	}

	return volumes
}

func guestCredentialsVolume(img *v1alpha1.VMImage) corev1.Volume {
	ref := img.Spec.Build.GuestAccess.Credentials.SecretRef
	defaultMode := int32(0o400)
	items := []corev1.KeyToPath{}
	if key := sshPrivateKeyKey(img); key != "" {
		items = append(items, corev1.KeyToPath{Key: key, Path: "id_ed25519"})
	}
	if key := passwordKey(img); key != "" {
		items = append(items, corev1.KeyToPath{Key: key, Path: "password"})
	}
	return corev1.Volume{
		Name: guestCredsVol,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  ref.Name,
				Items:       items,
				DefaultMode: &defaultMode,
			},
		},
	}
}

func gitAuthVolumes(img *v1alpha1.VMImage) []corev1.Volume {
	refs := gitAuthSecretRefs(img)
	volumes := make([]corev1.Volume, 0, len(refs))
	defaultMode := int32(0o400)
	for i, ref := range refs {
		volumes = append(volumes, corev1.Volume{
			Name: gitAuthVolumeName(i),
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  ref.Name,
					DefaultMode: &defaultMode,
				},
			},
		})
	}
	return volumes
}

func gitAuthVolumeMounts(img *v1alpha1.VMImage) []corev1.VolumeMount {
	refs := gitAuthSecretRefs(img)
	mounts := make([]corev1.VolumeMount, 0, len(refs))
	for i := range refs {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      gitAuthVolumeName(i),
			MountPath: gitAuthMountPath(i),
			ReadOnly:  true,
		})
	}
	return mounts
}

func withGitAuthMountPaths(provisioners []v1alpha1.ProvisionerSpec) []v1alpha1.ProvisionerSpec {
	out := make([]v1alpha1.ProvisionerSpec, 0, len(provisioners))
	secretIndex := map[string]int{}
	for _, spec := range provisioners {
		spec = *spec.DeepCopy()
		if spec.Source == nil || spec.Source.Git == nil || spec.Source.Git.Auth == nil ||
			spec.Source.Git.Auth.SecretRef == nil || spec.Source.Git.Auth.SecretRef.Name == "" {
			out = append(out, spec)
			continue
		}
		ref := spec.Source.Git.Auth.SecretRef
		key := gitAuthSecretIdentity(ref)
		index, ok := secretIndex[key]
		if !ok {
			index = len(secretIndex)
			secretIndex[key] = index
		}
		spec.Source.Git.Auth.TokenPath = gitAuthSecretFile(index, gitAuthTokenKey(ref))
		spec.Source.Git.Auth.UsernamePath = gitAuthSecretFile(index, gitAuthUsernameKey(ref))
		spec.Source.Git.Auth.PasswordPath = gitAuthSecretFile(index, gitAuthPasswordKey(ref))
		out = append(out, spec)
	}
	return out
}

func gitAuthSecretRefs(img *v1alpha1.VMImage) []v1alpha1.GitProvisionerAuthSecretRef {
	var refs []v1alpha1.GitProvisionerAuthSecretRef
	seen := map[string]bool{}
	for _, spec := range img.Spec.Provisioners {
		if spec.Source == nil || spec.Source.Git == nil || spec.Source.Git.Auth == nil ||
			spec.Source.Git.Auth.SecretRef == nil || spec.Source.Git.Auth.SecretRef.Name == "" {
			continue
		}
		ref := *spec.Source.Git.Auth.SecretRef
		key := gitAuthSecretIdentity(&ref)
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, ref)
	}
	return refs
}

func gitAuthSecretIdentity(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	return ref.Name + "/" + gitAuthTokenKey(ref) + "/" + gitAuthUsernameKey(ref) + "/" + gitAuthPasswordKey(ref)
}

func gitAuthVolumeName(index int) string {
	return fmt.Sprintf("%s%d", gitCredsVolPrefix, index)
}

func gitAuthMountPath(index int) string {
	return fmt.Sprintf("%s/%d", gitCredsMount, index)
}

func gitAuthSecretFile(index int, key string) string {
	return gitAuthMountPath(index) + "/" + key
}

func gitAuthTokenKey(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	if ref.TokenKey != "" {
		return ref.TokenKey
	}
	return "token"
}

func gitAuthUsernameKey(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	if ref.UsernameKey != "" {
		return ref.UsernameKey
	}
	return "username"
}

func gitAuthPasswordKey(ref *v1alpha1.GitProvisionerAuthSecretRef) string {
	if ref.PasswordKey != "" {
		return ref.PasswordKey
	}
	return "password"
}

func workspaceVolume(img *v1alpha1.VMImage) corev1.Volume {
	if usesArtifactPVC(img) {
		return corev1.Volume{
			Name: workspaceVol,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: WorkspaceClaimName(img),
				},
			},
		}
	}
	return corev1.Volume{
		Name: workspaceVol,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}

// WorkspaceClaimName returns the PVC claim mounted as /workspace for a VMImage.
func WorkspaceClaimName(img *v1alpha1.VMImage) string {
	if img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.PVC != nil &&
		img.Spec.Build.ArtifactStorage.PVC.ClaimName != "" {
		return img.Spec.Build.ArtifactStorage.PVC.ClaimName
	}
	return fmt.Sprintf("%s-workspace", img.Name)
}

func usesArtifactPVC(img *v1alpha1.VMImage) bool {
	return img.Spec.Build.ArtifactStorage != nil &&
		img.Spec.Build.ArtifactStorage.Type == "pvc"
}

func hasCacheRef(img *v1alpha1.VMImage) bool {
	return cacheRef(img) != ""
}

func cacheRef(img *v1alpha1.VMImage) string {
	if img.Spec.Build.Cache != nil && img.Spec.Build.Cache.Ref != "" {
		return img.Spec.Build.Cache.Ref
	}
	if img.Spec.Build.CacheRef != nil {
		return *img.Spec.Build.CacheRef
	}
	return ""
}

func kvmEnabled(img *v1alpha1.VMImage) bool {
	return img.Spec.Build.Security != nil && img.Spec.Build.Security.EnableKVM
}

func buildTolerations(img *v1alpha1.VMImage) []corev1.Toleration {
	if !kvmEnabled(img) {
		return nil
	}
	return []corev1.Toleration{
		{
			Key:      "imagebuilder.io/dedicated",
			Operator: corev1.TolerationOpEqual,
			Value:    "imagebuilder",
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}
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
		"imagebuilder.io/job-kind":     "build",
	}
}

func isInitContainer(provisionerType string) bool {
	return provisioner.IsInitContainer(provisionerType)
}

func defaultImageForProvisioner(provisionerType string) string {
	defaults := map[string]string{
		"ansible":   envOrDefault("PROVISIONER_ANSIBLE_IMAGE", "ghcr.io/anwendt/imagebuilder-provisioner-ansible:0.3.0"),
		"chef":      envOrDefault("PROVISIONER_CHEF_IMAGE", "ghcr.io/anwendt/imagebuilder-provisioner-chef:0.3.0"),
		"custom":    envOrDefault("PROVISIONER_CUSTOM_IMAGE", "ghcr.io/anwendt/imagebuilder-provisioner-custom:0.3.0"),
		"puppet":    envOrDefault("PROVISIONER_PUPPET_IMAGE", "ghcr.io/anwendt/imagebuilder-provisioner-puppet:0.3.0"),
		"saltstack": envOrDefault("PROVISIONER_SALTSTACK_IMAGE", "ghcr.io/anwendt/imagebuilder-provisioner-saltstack:0.3.0"),
	}
	return defaults[provisionerType]
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
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
		Privileged:               boolPtr(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

// bootCommandEnv encodes the boot command slice as a JSON array and returns
// the corresponding EnvVar. Returns nil when the slice is empty (no env var
// added — the build engine treats an absent BOOT_COMMAND as "no boot commands").
//
// Uses SetEscapeHTML(false) so boot-command tokens like <enter>, <tab> are
// preserved literally instead of being escaped to \u003center\u003e.
func bootCommandEnv(cmds []string) *corev1.EnvVar {
	if len(cmds) == 0 {
		return nil
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cmds); err != nil {
		// Encoding []string never errors; guard defensively.
		return nil
	}
	// json.Encoder appends a trailing newline; strip it.
	value := strings.TrimRight(buf.String(), "\n")
	return &corev1.EnvVar{
		Name:  "BOOT_COMMAND",
		Value: value,
	}
}

func provisionersEnv(provisioners []v1alpha1.ProvisionerSpec) *corev1.EnvVar {
	if len(provisioners) == 0 {
		return nil
	}
	data, err := marshalJSONEnv(provisioners)
	if err != nil {
		return nil
	}
	return &corev1.EnvVar{Name: "PROVISIONERS", Value: data}
}

func guestAccessEnv(access *v1alpha1.GuestAccessSpec) []corev1.EnvVar {
	if access == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: "GUEST_ACCESS_PROTOCOL", Value: access.Protocol},
		{Name: "GUEST_ACCESS_HOST", Value: access.Host},
		{Name: "GUEST_ACCESS_HOST_PORT", Value: fmt.Sprintf("%d", access.HostPort)},
		{Name: "GUEST_ACCESS_GUEST_PORT", Value: fmt.Sprintf("%d", access.GuestPort)},
		{Name: "GUEST_ACCESS_USER", Value: access.User},
		{Name: "GUEST_ACCESS_SSH_KEY_PATH", Value: guestSSHKeyPath(access)},
		{Name: "GUEST_ACCESS_PASSWORD_PATH", Value: guestPasswordPath(access)},
	}
	if access.Timeout != nil {
		env = append(env, corev1.EnvVar{Name: "GUEST_ACCESS_TIMEOUT", Value: access.Timeout.Duration.String()})
	}
	if access.WinRM != nil {
		if access.WinRM.HTTPS != nil {
			env = append(env, corev1.EnvVar{Name: "GUEST_ACCESS_WINRM_HTTPS", Value: fmt.Sprintf("%t", *access.WinRM.HTTPS)})
		}
		env = append(env, corev1.EnvVar{Name: "GUEST_ACCESS_WINRM_INSECURE_SKIP_VERIFY", Value: fmt.Sprintf("%t", access.WinRM.InsecureSkipVerify)})
	}
	if access.Credentials != nil && access.Credentials.Generate != nil {
		env = append(env,
			corev1.EnvVar{Name: "GUEST_CREDENTIALS_GENERATE_SSH_KEY", Value: fmt.Sprintf("%t", access.Credentials.Generate.SSHKey)},
			corev1.EnvVar{Name: "GUEST_CREDENTIALS_GENERATE_PASSWORD", Value: fmt.Sprintf("%t", access.Credentials.Generate.Password)},
		)
		if access.Credentials.Generate.PasswordLength != 0 {
			env = append(env, corev1.EnvVar{
				Name:  "GUEST_CREDENTIALS_GENERATE_PASSWORD_LENGTH",
				Value: fmt.Sprintf("%d", access.Credentials.Generate.PasswordLength),
			})
		}
	}
	if access.Credentials != nil && access.Credentials.Injection != nil && access.Credentials.Injection.Method != "" {
		env = append(env, corev1.EnvVar{Name: "GUEST_CREDENTIALS_INJECTION_METHOD", Value: access.Credentials.Injection.Method})
	}
	return env
}

func hasGuestCredentials(img *v1alpha1.VMImage) bool {
	return img.Spec.Build.GuestAccess != nil &&
		img.Spec.Build.GuestAccess.Credentials != nil &&
		img.Spec.Build.GuestAccess.Credentials.SecretRef != nil &&
		img.Spec.Build.GuestAccess.Credentials.SecretRef.Name != ""
}

func generatesGuestCredentials(img *v1alpha1.VMImage) bool {
	return img.Spec.Build.GuestAccess != nil &&
		img.Spec.Build.GuestAccess.Credentials != nil &&
		img.Spec.Build.GuestAccess.Credentials.Generate != nil
}

func sshPrivateKeyKey(img *v1alpha1.VMImage) string {
	if !hasGuestCredentials(img) {
		return ""
	}
	key := img.Spec.Build.GuestAccess.Credentials.SecretRef.SSHPrivateKeyKey
	if key == "" {
		key = "id_ed25519"
	}
	return key
}

func passwordKey(img *v1alpha1.VMImage) string {
	if !hasGuestCredentials(img) {
		return ""
	}
	key := img.Spec.Build.GuestAccess.Credentials.SecretRef.PasswordKey
	if key == "" {
		key = "password"
	}
	return key
}

func guestSSHKeyPath(access *v1alpha1.GuestAccessSpec) string {
	if access.SSHKeyPath != "" {
		return access.SSHKeyPath
	}
	if access.Credentials != nil && access.Credentials.SecretRef != nil && access.Credentials.SecretRef.Name != "" {
		return guestCredsMount + "/id_ed25519"
	}
	return ""
}

func guestPasswordPath(access *v1alpha1.GuestAccessSpec) string {
	if access.PasswordPath != "" {
		return access.PasswordPath
	}
	if access.Credentials != nil && access.Credentials.SecretRef != nil && access.Credentials.SecretRef.Name != "" {
		return guestCredsMount + "/password"
	}
	return ""
}

func marshalJSONEnv(value any) (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
