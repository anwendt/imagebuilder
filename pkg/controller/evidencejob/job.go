// Package evidencejob builds the ADR-011 evidence publication Job: the core
// controller publishes the VM image descriptor of a finished remote build to
// OCI, signs it, attaches SLSA provenance, CycloneDX SBOM and Trivy report as
// cosign attestations and merges it into the per-version OCI index.
package evidencejob

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DefaultOrasImage and DefaultCosignImage are the step images of the
	// ImageFactory (artifactbuild ADR-0010); both contain a POSIX shell.
	DefaultOrasImage   = "ghcr.io/oras-project/oras:v1.3.0@sha256:6ce045ce069a89934d6666b8b49f9c4c0145201bd6de6dbe2aee267814c55468"
	DefaultCosignImage = "cgr.dev/chainguard/cosign:latest-dev@sha256:a790a4f273f25d95e3a1df67a48b792f92a75ca230f31d2cea95371401fe40e1"

	// Secret keys of the per-build input Secret.
	BundleKey     = "bundle.tar.gz"
	DescriptorKey = "descriptor.json"
	ProvenanceKey = "provenance.json"

	// ComponentLabel value of publication Jobs, Pods and Secrets.
	Component = "evidence-publisher"

	defaultActiveDeadline = 30 * time.Minute
	finishedJobTTL        = int32(24 * 60 * 60)
	nonRootUID            = int64(65532)
	terminationMessageMax = 1024
)

// Labels shared by all publication objects.
const (
	LabelManagedBy  = "app.kubernetes.io/managed-by"
	LabelComponent  = "app.kubernetes.io/component"
	LabelVMImage    = "imagebuilder.io/vmimage"
	LabelVMImageNS  = "imagebuilder.io/vmimage-namespace"
	LabelVMImageUID = "imagebuilder.io/vmimage-uid"
)

// Config is the controller-wide publication configuration (chart values
// evidencePublisher.* / controller flags --evidence-*).
type Config struct {
	Enabled   bool
	Namespace string

	OrasImage   string
	CosignImage string

	// RegistrySecret is a kubernetes.io/dockerconfigjson Secret with push
	// credentials for the evidence registry.
	RegistrySecret string
	// CosignSecret holds the cosign private key (CosignKeyKey) and its
	// password (CosignPasswordKey, optional).
	CosignSecret      string
	CosignKeyKey      string
	CosignPasswordKey string

	Resources       corev1.ResourceRequirements
	HTTPSProxy      string
	NoProxy         string
	ActiveDeadline  time.Duration
	ImagePullPolicy corev1.PullPolicy

	// BuilderVersion is recorded in descriptor and provenance.
	BuilderVersion string
}

// Validate reports a configuration that cannot publish evidence.
func (c Config) Validate() error {
	if !c.Enabled {
		return fmt.Errorf("evidence publisher is disabled; enable evidencePublisher in the chart (--evidence-publisher-enabled)")
	}
	var missing []string
	if strings.TrimSpace(c.Namespace) == "" {
		missing = append(missing, "namespace")
	}
	if strings.TrimSpace(c.CosignSecret) == "" {
		missing = append(missing, "cosign key Secret")
	}
	if strings.TrimSpace(c.RegistrySecret) == "" {
		missing = append(missing, "registry credential Secret")
	}
	if len(missing) > 0 {
		return fmt.Errorf("evidence publisher is not configured: %s missing", strings.Join(missing, ", "))
	}
	return nil
}

func (c Config) withDefaults() Config {
	if c.OrasImage == "" {
		c.OrasImage = DefaultOrasImage
	}
	if c.CosignImage == "" {
		c.CosignImage = DefaultCosignImage
	}
	if c.CosignKeyKey == "" {
		c.CosignKeyKey = "cosign.key"
	}
	if c.CosignPasswordKey == "" {
		c.CosignPasswordKey = "cosign.password"
	}
	if c.ActiveDeadline <= 0 {
		c.ActiveDeadline = defaultActiveDeadline
	}
	if c.ImagePullPolicy == "" {
		c.ImagePullPolicy = corev1.PullIfNotPresent
	}
	if c.Resources.Requests == nil && c.Resources.Limits == nil {
		c.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		}
	}
	return c
}

// Target names where one platform build is published.
type Target struct {
	// Repository is registry/path/<subject name> without scheme.
	Repository string
	Name       string
	Version    string
	Platform   string
	Provider   string
}

// DescriptorTag returns <version>-<platform>.
func (t Target) DescriptorTag() string { return t.Version + "-" + t.Platform }

// RefFor returns the immutable oci:// reference of a digest in Repository.
func (t Target) RefFor(digest string) string { return "oci://" + t.Repository + "@" + digest }

// NewTarget derives the publication target from registryRepository
// (oci://registry/path) and the evidence subject.
func NewTarget(registryRepository, name, version, platform, provider string) (Target, error) {
	repo := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(registryRepository), "oci://"), "/")
	if !repositoryPattern.MatchString(repo) {
		return Target{}, fmt.Errorf("invalid evidence registry repository %q; want oci://<registry>/<path>", registryRepository)
	}
	if name == "" || version == "" || platform == "" {
		return Target{}, fmt.Errorf("evidence subject name, version and platform are required")
	}
	return Target{Repository: repo + "/" + name, Name: name, Version: version, Platform: platform, Provider: provider}, nil
}

// Owner identifies the VMImage a publication belongs to.
type Owner struct {
	Namespace string
	Name      string
	UID       string
	BuildID   string
}

// ObjectName returns the deterministic Job and Secret name of a build.
func ObjectName(owner Owner) string {
	sum := sha256.Sum256([]byte(owner.UID + "/" + owner.BuildID))
	return "ib-evidence-" + hex.EncodeToString(sum[:])[:16]
}

// Labels returns the labels of all publication objects of owner.
func Labels(owner Owner) map[string]string {
	return map[string]string{
		LabelManagedBy:  "imagebuilder",
		LabelComponent:  Component,
		LabelVMImage:    truncateLabel(owner.Name),
		LabelVMImageNS:  truncateLabel(owner.Namespace),
		LabelVMImageUID: truncateLabel(owner.UID),
	}
}

func truncateLabel(value string) string {
	if len(value) > 63 {
		value = value[:63]
	}
	return strings.Trim(value, "-_.")
}

// BuildSecret returns the per-build input Secret.
func BuildSecret(cfg Config, owner Owner, bundle, descriptor, provenance []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ObjectName(owner), Namespace: cfg.Namespace, Labels: Labels(owner)},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			BundleKey:     bundle,
			DescriptorKey: descriptor,
			ProvenanceKey: provenance,
		},
	}
}

// BuildJob returns the publication Job.
func BuildJob(cfg Config, owner Owner, target Target, bundleSHA256 string) (*batchv1.Job, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(bundleSHA256) {
		return nil, fmt.Errorf("evidence bundle sha256 %q is invalid", bundleSHA256)
	}
	cfg = cfg.withDefaults()
	name := ObjectName(owner)
	labels := Labels(owner)
	env := []corev1.EnvVar{
		{Name: "HOME", Value: "/work/home"},
		{Name: "TMPDIR", Value: "/work/tmp"},
		{Name: "REPO", Value: target.Repository},
		{Name: "DESCRIPTOR_TAG", Value: target.DescriptorTag()},
		{Name: "INDEX_TAG", Value: target.Version},
		{Name: "SUBJECT_NAME", Value: target.Name},
		{Name: "SUBJECT_VERSION", Value: target.Version},
		{Name: "SUBJECT_PLATFORM", Value: target.Platform},
		{Name: "PROVIDER", Value: target.Provider},
		{Name: "BUNDLE_SHA256", Value: bundleSHA256},
		{Name: "REGISTRY_CONFIG", Value: "/var/run/imagebuilder/registry/config.json"},
		{Name: "COSIGN_KEY_PATH", Value: "/var/run/imagebuilder/cosign/cosign.key"},
	}
	if cfg.HTTPSProxy != "" {
		env = append(env, corev1.EnvVar{Name: "HTTPS_PROXY", Value: cfg.HTTPSProxy}, corev1.EnvVar{Name: "https_proxy", Value: cfg.HTTPSProxy})
	}
	if cfg.NoProxy != "" {
		env = append(env, corev1.EnvVar{Name: "NO_PROXY", Value: cfg.NoProxy}, corev1.EnvVar{Name: "no_proxy", Value: cfg.NoProxy})
	}
	cosignEnv := append(append([]corev1.EnvVar(nil), env...), corev1.EnvVar{
		Name: "COSIGN_PASSWORD",
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: cfg.CosignSecret},
			Key:                  cfg.CosignPasswordKey,
			Optional:             ptr(true),
		}},
	})
	work := corev1.VolumeMount{Name: "work", MountPath: "/work"}
	input := corev1.VolumeMount{Name: "input", MountPath: "/input", ReadOnly: true}
	registry := corev1.VolumeMount{Name: "registry", MountPath: "/var/run/imagebuilder/registry", ReadOnly: true}
	cosignKey := corev1.VolumeMount{Name: "cosign", MountPath: "/var/run/imagebuilder/cosign", ReadOnly: true}
	step := func(stepName, image, script string, stepEnv []corev1.EnvVar, mounts ...corev1.VolumeMount) corev1.Container {
		return corev1.Container{
			Name:                     stepName,
			Image:                    image,
			ImagePullPolicy:          cfg.ImagePullPolicy,
			Command:                  []string{"/bin/sh", "-c", script},
			Env:                      stepEnv,
			VolumeMounts:             mounts,
			Resources:                cfg.Resources,
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             ptr(true),
				RunAsUser:                ptr(nonRootUID),
				RunAsGroup:               ptr(nonRootUID),
				ReadOnlyRootFilesystem:   ptr(true),
				AllowPrivilegeEscalation: ptr(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
		}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(2)),
			ActiveDeadlineSeconds:   ptr(int64(cfg.ActiveDeadline.Seconds())),
			TTLSecondsAfterFinished: ptr(finishedJobTTL),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr(false),
					EnableServiceLinks:           ptr(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr(true),
						RunAsUser:      ptr(nonRootUID),
						RunAsGroup:     ptr(nonRootUID),
						FSGroup:        ptr(nonRootUID),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					InitContainers: []corev1.Container{
						step("prepare", cfg.CosignImage, PrepareScript, env, work, input),
						step("push-descriptor", cfg.OrasImage, PushDescriptorScript, env, work, registry),
						step("sign-descriptor", cfg.CosignImage, SignDescriptorScript, cosignEnv, work, registry, cosignKey),
						step("publish-index", cfg.OrasImage, PublishIndexScript, env, work, registry),
						step("sign-index", cfg.CosignImage, SignIndexScript, cosignEnv, work, registry, cosignKey),
					},
					Containers: []corev1.Container{
						step("report", cfg.OrasImage, ReportScript, env, work, registry),
					},
					Volumes: []corev1.Volume{
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse("256Mi"))}}},
						{Name: "input", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: ptr(int32(0o440))}}},
						{Name: "registry", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName:  cfg.RegistrySecret,
							DefaultMode: ptr(int32(0o440)),
							Items:       []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}},
						}}},
						{Name: "cosign", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
							SecretName:  cfg.CosignSecret,
							DefaultMode: ptr(int32(0o440)),
							Items:       []corev1.KeyToPath{{Key: cfg.CosignKeyKey, Path: "cosign.key"}},
						}}},
					},
				},
			},
		},
	}
	return job, nil
}

// Result holds the digests reported by the publication Job.
type Result struct {
	DescriptorDigest          string `json:"descriptorDigest"`
	IndexDigest               string `json:"indexDigest"`
	SignatureDigest           string `json:"signatureDigest"`
	ProvenanceDigest          string `json:"provenanceDigest"`
	SBOMDigest                string `json:"sbomDigest"`
	VulnerabilityReportDigest string `json:"vulnerabilityReportDigest"`
	IndexSignatureDigest      string `json:"indexSignatureDigest"`
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+$`)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ParseResult parses and validates the report container termination message.
func ParseResult(message string) (Result, error) {
	var result Result
	if err := json.Unmarshal([]byte(strings.TrimSpace(message)), &result); err != nil {
		return Result{}, fmt.Errorf("parse evidence publication result: %w", err)
	}
	for field, digest := range map[string]string{
		"descriptorDigest":          result.DescriptorDigest,
		"indexDigest":               result.IndexDigest,
		"signatureDigest":           result.SignatureDigest,
		"provenanceDigest":          result.ProvenanceDigest,
		"sbomDigest":                result.SBOMDigest,
		"vulnerabilityReportDigest": result.VulnerabilityReportDigest,
		"indexSignatureDigest":      result.IndexSignatureDigest,
	} {
		if !digestPattern.MatchString(digest) {
			return Result{}, fmt.Errorf("evidence publication result %s %q is not a sha256 digest", field, digest)
		}
	}
	return result, nil
}

// PodResult returns the report container termination message of a succeeded
// pod, or the failure message of the first failed step.
func PodResult(pod *corev1.Pod) (message string, failed bool) {
	statuses := append(append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
	for _, status := range statuses {
		terminated := status.State.Terminated
		if terminated == nil {
			terminated = status.LastTerminationState.Terminated
		}
		if terminated == nil {
			continue
		}
		if terminated.ExitCode != 0 {
			return fmt.Sprintf("step %s failed: %s", status.Name, Truncate(firstNonEmpty(terminated.Message, terminated.Reason))), true
		}
		if status.Name == "report" {
			return terminated.Message, false
		}
	}
	return "", false
}

// Truncate shortens a termination message for status and events.
func Truncate(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > terminationMessageMax {
		message = "..." + message[len(message)-terminationMessageMax:]
	}
	return message
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ptr[T any](value T) *T { return &value }
