// pkg/controller/buildpod/assembler_test.go
//
// Unit tests for buildpod.Assemble().
//
// TDD approach (DR-001–DR-005):
//   Red  — each test was written to describe the required behaviour.
//   Green — Assemble() was implemented to satisfy all tests.
//   Refactor — security context helpers were extracted to restrictedSecCtx().
//
// Test categories covered:
//   - Job naming and labels
//   - Init container creation per provisioner type
//   - In-process provisioner types do NOT produce init containers
//   - Complex provisioner types produce restartable init containers
//   - Workspace volume present and mounted
//   - Owner reference set on the Job
//   - Security contexts (pod-level and container-level)
//   - Resource limits forwarded from BuildSpec
//   - Cache PVC volume added when CacheRef is set

package buildpod_test

import (
	"encoding/json"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/buildpod"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatalf("add batchv1 scheme: %v", err)
	}
	return s
}

func envMap(env []corev1.EnvVar) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}

func durationPtr(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

func boolPtr(value bool) *bool {
	return &value
}

func baseImage() *v1alpha1.VMImage {
	return &v1alpha1.VMImage{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "imagebuilder.io/v1alpha1",
			Kind:       "VMImage",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ubuntu-2404",
			Namespace: "default",
			UID:       "test-uid-1234",
		},
		Spec: v1alpha1.VMImageSpec{
			OS: v1alpha1.OSSpec{
				Family:       "linux",
				Distribution: "ubuntu",
				Version:      "24.04",
				Arch:         "amd64",
			},
			Source: v1alpha1.SourceSpec{
				Type:     "cloud-image",
				URL:      "https://example.com/ubuntu-24.04.img",
				Checksum: "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
			},
			Targets: []v1alpha1.TargetSpec{
				{
					ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-prod"},
					Format:            "vmdk",
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Job name and labels
// ---------------------------------------------------------------------------

func TestAssemble_JobName(t *testing.T) {
	img := baseImage()
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	want := "ubuntu-2404-build"
	if job.Name != want {
		t.Errorf("job name = %q, want %q", job.Name, want)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 7200 {
		t.Fatalf("activeDeadlineSeconds = %v, want 7200", job.Spec.ActiveDeadlineSeconds)
	}
}

func TestAssemble_RevisionChangesJobAndWorkspaceNames(t *testing.T) {
	first := baseImage()
	first.Spec.Build.Revision = "v1"
	second := first.DeepCopy()
	second.Spec.Build.Revision = "v2"

	firstJob, err := buildpod.Assemble(first, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble first revision: %v", err)
	}
	secondJob, err := buildpod.Assemble(second, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble second revision: %v", err)
	}
	if firstJob.Name == secondJob.Name {
		t.Fatalf("revision job names are equal: %q", firstJob.Name)
	}
	firstClaim := buildpod.WorkspaceClaimName(first)
	secondClaim := buildpod.WorkspaceClaimName(second)
	if firstClaim == secondClaim {
		t.Fatalf("revision workspace names are equal: %q", firstClaim)
	}
	if firstJob.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != firstClaim {
		t.Fatalf("first workspace claim = %q, want %q", firstJob.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName, firstClaim)
	}
}

func TestAssemble_JobNamespace(t *testing.T) {
	img := baseImage()
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if job.Namespace != img.Namespace {
		t.Errorf("job namespace = %q, want %q", job.Namespace, img.Namespace)
	}
}

func TestAssemble_JobLabels(t *testing.T) {
	img := baseImage()
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if job.Labels["imagebuilder.io/vmimage"] != img.Name {
		t.Errorf("label imagebuilder.io/vmimage = %q, want %q",
			job.Labels["imagebuilder.io/vmimage"], img.Name)
	}
	if job.Labels["app.kubernetes.io/managed-by"] != "imagebuilder" {
		t.Errorf("label app.kubernetes.io/managed-by missing or wrong")
	}
	if job.Labels["imagebuilder.io/job-kind"] != "build" {
		t.Errorf("label imagebuilder.io/job-kind = %q, want build", job.Labels["imagebuilder.io/job-kind"])
	}
}

// ---------------------------------------------------------------------------
// Owner reference
// ---------------------------------------------------------------------------

func TestAssemble_OwnerReference(t *testing.T) {
	img := baseImage()
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(job.OwnerReferences))
	}
	or := job.OwnerReferences[0]
	if or.Name != img.Name {
		t.Errorf("ownerRef.Name = %q, want %q", or.Name, img.Name)
	}
	if or.Kind != "VMImage" {
		t.Errorf("ownerRef.Kind = %q, want VMImage", or.Kind)
	}
	if !*or.Controller {
		t.Errorf("ownerRef.Controller should be true")
	}
}

// ---------------------------------------------------------------------------
// Job spec invariants
// ---------------------------------------------------------------------------

func TestAssemble_BackoffLimitZero(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("BackoffLimit should be 0, operator controls retry logic")
	}
}

func TestAssemble_RestartPolicyNever(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	got := job.Spec.Template.Spec.RestartPolicy
	if got != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", got)
	}
}

func TestAssemble_DelegatesPlacementToKubeScheduler(t *testing.T) {
	img := baseImage()
	img.Spec.Build.NodeSelector = map[string]string{"imagebuilder.io/build-node": "true"}
	img.Status.ScheduledNodeName = "node-a"

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if job.Spec.Template.Spec.NodeName != "" {
		t.Fatalf("nodeName = %q, want empty so kube-scheduler performs binding", job.Spec.Template.Spec.NodeName)
	}
	if job.Spec.Template.Spec.NodeSelector["imagebuilder.io/build-node"] != "true" {
		t.Fatalf("nodeSelector = %#v", job.Spec.Template.Spec.NodeSelector)
	}
	affinity := job.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.PodAntiAffinity == nil || len(affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("pod anti-affinity = %#v", affinity)
	}
	term := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	if term.Weight != 100 || term.PodAffinityTerm.TopologyKey != corev1.LabelHostname || term.PodAffinityTerm.NamespaceSelector == nil {
		t.Fatalf("anti-affinity term = %#v", term)
	}
	if term.PodAffinityTerm.LabelSelector.MatchLabels["imagebuilder.io/job-kind"] != "build" {
		t.Fatalf("anti-affinity selector = %#v", term.PodAffinityTerm.LabelSelector)
	}
}

// ---------------------------------------------------------------------------
// Main container
// ---------------------------------------------------------------------------

func TestAssemble_MainContainerPresent(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 main container, got %d", len(containers))
	}
	if containers[0].Name != "build" {
		t.Errorf("main container name = %q, want build", containers[0].Name)
	}
}

func TestAssemble_MainContainerImageFromEnvironment(t *testing.T) {
	t.Setenv("BUILDER_IMAGE", "registry.example.test/imagebuilder-builder@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	image := job.Spec.Template.Spec.Containers[0].Image
	if image != "registry.example.test/imagebuilder-builder@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("builder image = %q", image)
	}
}

func TestAssemble_MainContainerEnvVars(t *testing.T) {
	img := baseImage()
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	checks := map[string]string{
		"OS_FAMILY":              "linux",
		"OS_DISTRIBUTION":        "ubuntu",
		"OS_VERSION":             "24.04",
		"OS_ARCH":                "amd64",
		"SOURCE_TYPE":            "cloud-image",
		"SOURCE_URL":             "https://example.com/ubuntu-24.04.img",
		"SOURCE_CHECKSUM":        "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
		"WORKSPACE_DIR":          "/workspace",
		"TARGET_FORMAT":          "vmdk",
		"TARGET_PROVIDER_CONFIG": "aws-prod",
		"VMIMAGE_NAME":           "ubuntu-2404",
		"VMIMAGE_NAMESPACE":      "default",
	}
	for k, want := range checks {
		if got := envMap[k]; got != want {
			t.Errorf("env %s = %q, want %q", k, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// No init containers when all provisioners are in-process
// ---------------------------------------------------------------------------

func TestAssemble_NoInitContainers_InProcessProvisioners(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "shell", Inline: "echo hello"},
		{Type: "file", Inline: "content"},
		{Type: "cloud-init", Inline: "#cloud-config"},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if n := len(job.Spec.Template.Spec.InitContainers); n != 0 {
		t.Errorf("expected 0 init containers for in-process provisioners, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Complex provisioners run as restartable init containers
// ---------------------------------------------------------------------------

func TestAssemble_InitContainer_AnsibleProvisioner(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "ansible", Image: "ghcr.io/anwendt/imagebuilder-provisioner-ansible:v1.0.0"},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	ics := job.Spec.Template.Spec.InitContainers
	if n := len(ics); n != 1 {
		t.Fatalf("expected 1 init container, got %d", n)
	}
	if ics[0].Name != "provisioner-0-ansible" {
		t.Fatalf("init container name = %q", ics[0].Name)
	}
	if ics[0].RestartPolicy == nil || *ics[0].RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatalf("init container restartPolicy = %#v, want Always", ics[0].RestartPolicy)
	}
}

func TestAssemble_InitContainers_Order(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "shell", Inline: "echo first"},
		{Type: "ansible", Image: "ansible:latest"},
		{Type: "chef", Image: "chef:latest"},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	ics := job.Spec.Template.Spec.InitContainers
	if len(ics) != 2 {
		t.Fatalf("expected 2 init containers, got %d", len(ics))
	}
	if ics[0].Name != "provisioner-0-ansible" || ics[1].Name != "provisioner-1-chef" {
		t.Fatalf("init container order = %#v", []string{ics[0].Name, ics[1].Name})
	}
}

func TestAssemble_InitContainer_StepEnvVar(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "external-one", Image: "external-one:latest"},
		{Type: "external-two", Image: "external-two:latest"},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for i, ic := range job.Spec.Template.Spec.InitContainers {
		var stepVal string
		for _, e := range ic.Env {
			if e.Name == "PROVISIONER_STEP" {
				stepVal = e.Value
			}
		}
		want := string(rune('0' + i))
		if stepVal != want {
			t.Errorf("init container %d: PROVISIONER_STEP = %q, want %q", i, stepVal, want)
		}
		envMap := envMap(ic.Env)
		if envMap["PROVISIONER_CONFIG_PATH"] == "" || envMap["PROVISIONER_STATUS_PATH"] == "" {
			t.Errorf("init container %d missing config/status path env: %#v", i, envMap)
		}
	}
}

func TestAssemble_InitContainer_MissingImage_ReturnsError(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "external"},
	}
	_, err := buildpod.Assemble(img, newScheme(t))
	if err == nil {
		t.Error("expected error when init-container provisioner has no image, got nil")
	}
}

// ---------------------------------------------------------------------------
// Workspace volume
// ---------------------------------------------------------------------------

func TestAssemble_WorkspaceVolume_DefaultsToPVC(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	var found bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "workspace" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "ubuntu-2404-workspace" {
			found = true
		}
	}
	if !found {
		t.Error("default workspace PVC volume not found in job spec")
	}
}

func TestAssemble_WorkspaceVolume_ExplicitEmptyDir(t *testing.T) {
	img := baseImage()
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{Type: "emptyDir"}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "workspace" && v.EmptyDir != nil {
			return
		}
	}
	t.Fatal("explicit workspace emptyDir volume not found in job spec")
}

func TestAssemble_WorkspaceVolume_UsesArtifactPVC(t *testing.T) {
	img := baseImage()
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{
		Type: "pvc",
		PVC: &v1alpha1.ArtifactPVCSpec{
			AccessMode: "ReadWriteOnce",
		},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "workspace" {
			if v.PersistentVolumeClaim == nil {
				t.Fatalf("workspace volume should use PVC, got %#v", v.VolumeSource)
			}
			if v.PersistentVolumeClaim.ClaimName != "ubuntu-2404-workspace" {
				t.Fatalf("claimName = %q, want ubuntu-2404-workspace", v.PersistentVolumeClaim.ClaimName)
			}
			return
		}
	}
	t.Fatal("workspace volume not found")
}

func TestAssemble_WorkspaceVolume_UsesExistingArtifactPVC(t *testing.T) {
	img := baseImage()
	img.Spec.Build.ArtifactStorage = &v1alpha1.ArtifactStorageSpec{
		Type: "pvc",
		PVC: &v1alpha1.ArtifactPVCSpec{
			ClaimName:  "shared-image-workspace",
			AccessMode: "ReadWriteMany",
		},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "workspace" {
			if v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "shared-image-workspace" {
				t.Fatalf("workspace PVC = %#v, want shared-image-workspace", v.PersistentVolumeClaim)
			}
			return
		}
	}
	t.Fatal("workspace volume not found")
}

func TestAssemble_WorkspaceVolumeMountedInMainContainer(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	c := job.Spec.Template.Spec.Containers[0]
	var found bool
	for _, m := range c.VolumeMounts {
		if m.Name == "workspace" && m.MountPath == "/workspace" {
			found = true
		}
	}
	if !found {
		t.Error("workspace volume not mounted in main container at /workspace")
	}
}

// ---------------------------------------------------------------------------
// Cache PVC (FR-003)
// ---------------------------------------------------------------------------

func TestAssemble_CacheVolume_AddedWhenCacheRefSet(t *testing.T) {
	img := baseImage()
	ref := "source-cache"
	img.Spec.Build.CacheRef = &ref

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	var found bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "build-cache" && v.PersistentVolumeClaim != nil &&
			v.PersistentVolumeClaim.ClaimName == "source-cache" {
			found = true
		}
	}
	if !found {
		t.Error("build-cache PVC volume not found when CacheRef is set")
	}
}

func TestAssemble_CacheVolume_MountedInMainContainer(t *testing.T) {
	img := baseImage()
	ref := "source-cache"
	img.Spec.Build.CacheRef = &ref

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	c := job.Spec.Template.Spec.Containers[0]
	var found bool
	for _, m := range c.VolumeMounts {
		if m.Name == "build-cache" && m.MountPath == "/cache" {
			found = true
		}
	}
	if !found {
		t.Error("build-cache volume not mounted in main container at /cache")
	}
}

func TestAssemble_CacheDirEnv_AddedWhenCacheRefSet(t *testing.T) {
	img := baseImage()
	ref := "source-cache"
	img.Spec.Build.CacheRef = &ref

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CACHE_DIR" {
			if e.Value != "/cache" {
				t.Errorf("CACHE_DIR = %q, want /cache", e.Value)
			}
			return
		}
	}
	t.Error("CACHE_DIR env var missing when CacheRef is set")
}

func TestAssemble_SourceCacheSpec_AddsVolumeAndPolicyEnv(t *testing.T) {
	img := baseImage()
	img.Spec.Build.Cache = &v1alpha1.SourceCacheSpec{
		Ref:          "structured-source-cache",
		TTL:          &metav1.Duration{Duration: 6 * time.Hour},
		RetainPolicy: "Never",
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	var foundVolume bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "build-cache" && v.PersistentVolumeClaim != nil &&
			v.PersistentVolumeClaim.ClaimName == "structured-source-cache" {
			foundVolume = true
		}
	}
	if !foundVolume {
		t.Fatal("build-cache PVC volume not found for spec.build.cache.ref")
	}
	env := envMap(job.Spec.Template.Spec.Containers[0].Env)
	if env["CACHE_DIR"] != "/cache" {
		t.Fatalf("CACHE_DIR = %q, want /cache", env["CACHE_DIR"])
	}
	if env["CACHE_TTL"] != "6h0m0s" {
		t.Fatalf("CACHE_TTL = %q, want 6h0m0s", env["CACHE_TTL"])
	}
	if env["CACHE_RETAIN_POLICY"] != "Never" {
		t.Fatalf("CACHE_RETAIN_POLICY = %q, want Never", env["CACHE_RETAIN_POLICY"])
	}
}

func TestAssemble_CacheVolume_AbsentWhenCacheRefNil(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "build-cache" {
			t.Error("unexpected build-cache volume when CacheRef is nil")
		}
	}
}

// ---------------------------------------------------------------------------
// Security contexts (SR-011, REQ-004)
// ---------------------------------------------------------------------------

func TestAssemble_PodSecurityContext_NonRoot(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	psc := job.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod security context is nil")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("pod security context: RunAsNonRoot should be true")
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("pod security context: SeccompProfile should be RuntimeDefault")
	}
}

func TestAssemble_ContainerSecurityContext_DropAllCapabilities(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	sc := job.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil {
		t.Fatal("container security context is nil")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be false")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem should be true")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("RunAsNonRoot should be true")
	}
	if sc.Privileged == nil || *sc.Privileged {
		t.Error("Privileged should be false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
		t.Error("Capabilities.Drop should be set")
	}
	if sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop[0] = %q, want ALL", sc.Capabilities.Drop[0])
	}
}

func TestAssemble_ReadOnlyRootFilesystem_HasWritableTmpVolume(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	var foundTmpVolume, foundTmpMount bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "tmp" && v.EmptyDir != nil {
			foundTmpVolume = true
		}
	}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "tmp" && m.MountPath == "/tmp" {
			foundTmpMount = true
		}
	}
	if !foundTmpVolume || !foundTmpMount {
		t.Fatalf("tmp volume=%v mount=%v", foundTmpVolume, foundTmpMount)
	}
}

func TestAssemble_KVMDisabledByDefault(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "dev-kvm" {
			t.Fatal("/dev/kvm must not be mounted by default")
		}
	}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "dev-kvm" || m.MountPath == "/dev/kvm" {
			t.Fatal("/dev/kvm mount must not be present by default")
		}
	}
}

func TestAssemble_KVMEnabledMountsHostDeviceWithoutPrivilege(t *testing.T) {
	img := baseImage()
	img.Spec.Source.Type = "iso"
	img.Spec.Build.Security = &v1alpha1.BuildSecuritySpec{EnableKVM: true}
	img.Spec.Build.NodeSelector = map[string]string{
		"imagebuilder.io/kvm":       "true",
		"imagebuilder.io/dedicated": "imagebuilder",
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	var foundVolume, foundMount bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "dev-kvm" && v.HostPath != nil && v.HostPath.Path == "/dev/kvm" &&
			v.HostPath.Type != nil && *v.HostPath.Type == corev1.HostPathCharDev {
			foundVolume = true
		}
	}
	envMap := map[string]string{}
	container := job.Spec.Template.Spec.Containers[0]
	for _, e := range container.Env {
		envMap[e.Name] = e.Value
	}
	for _, m := range container.VolumeMounts {
		if m.Name == "dev-kvm" && m.MountPath == "/dev/kvm" {
			foundMount = true
		}
	}
	if !foundVolume || !foundMount || envMap["QEMU_ENABLE_KVM"] != "true" {
		t.Fatalf("kvm volume=%v mount=%v env=%q", foundVolume, foundMount, envMap["QEMU_ENABLE_KVM"])
	}
	if container.SecurityContext == nil || container.SecurityContext.Privileged == nil || *container.SecurityContext.Privileged {
		t.Fatalf("kvm build container must remain unprivileged: %#v", container.SecurityContext)
	}
	if len(job.Spec.Template.Spec.Tolerations) != 1 ||
		job.Spec.Template.Spec.Tolerations[0].Key != "imagebuilder.io/dedicated" ||
		job.Spec.Template.Spec.Tolerations[0].Value != "imagebuilder" {
		t.Fatalf("KVM tolerations = %#v, want dedicated build-node toleration", job.Spec.Template.Spec.Tolerations)
	}
}

// ---------------------------------------------------------------------------
// Resource requirements
// ---------------------------------------------------------------------------

func TestAssemble_ResourceLimits_FromBuildSpec(t *testing.T) {
	img := baseImage()
	img.Spec.Build.Resources = &v1alpha1.ResourceRequirements{
		CPU:    "4",
		Memory: "8Gi",
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	res := job.Spec.Template.Spec.Containers[0].Resources
	if res.Limits.Cpu().IsZero() {
		t.Error("CPU limit should be set")
	}
	if res.Limits.Memory().IsZero() {
		t.Error("Memory limit should be set")
	}
}

func TestAssemble_ResourceLimits_EmptyWhenNotSet(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	res := job.Spec.Template.Spec.Containers[0].Resources
	if !res.Limits.Cpu().IsZero() {
		t.Error("CPU limit should be zero when not set in BuildSpec")
	}
}

// ---------------------------------------------------------------------------
// automountServiceAccountToken / host namespaces (AS-028, AS-053)
// ---------------------------------------------------------------------------

func TestAssemble_AutomountServiceAccountToken_False(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	sat := job.Spec.Template.Spec.AutomountServiceAccountToken
	if sat == nil || *sat != false {
		t.Error("AutomountServiceAccountToken should be explicitly false (AS-028)")
	}
}

func TestAssemble_HostNamespaces_AllFalse(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	spec := job.Spec.Template.Spec
	if spec.HostNetwork {
		t.Error("HostNetwork should be false (AS-053)")
	}
	if spec.HostPID {
		t.Error("HostPID should be false (AS-053)")
	}
	if spec.HostIPC {
		t.Error("HostIPC should be false (AS-053)")
	}
}

// ---------------------------------------------------------------------------
// boot_command
// ---------------------------------------------------------------------------

func TestAssemble_BootCommand_AbsentWhenEmpty(t *testing.T) {
	// baseImage uses cloud-image source with no boot command.
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	if _, ok := envMap["BOOT_COMMAND"]; ok {
		t.Error("BOOT_COMMAND env var should be absent when bootCommand is empty")
	}
}

func TestAssemble_BootCommand_PresentAsJSONArray(t *testing.T) {
	img := baseImage()
	img.Spec.Source.Type = "iso"
	img.Spec.Source.BootCommand = []string{
		"<tab>",
		" inst.ks=http://192.0.2.1/ks.cfg",
		"<enter><wait10>",
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	val, ok := envMap["BOOT_COMMAND"]
	if !ok {
		t.Fatal("BOOT_COMMAND env var missing when bootCommand is set")
	}
	// Must be a valid JSON array.
	var parsed []string
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		t.Fatalf("BOOT_COMMAND value %q is not valid JSON: %v", val, err)
	}
	if len(parsed) != 3 {
		t.Errorf("decoded BOOT_COMMAND has %d entries, want 3", len(parsed))
	}
	if parsed[0] != "<tab>" {
		t.Errorf("BOOT_COMMAND[0] = %q, want <tab>", parsed[0])
	}
}

func TestAssemble_BootCommand_SingleEntry(t *testing.T) {
	img := baseImage()
	img.Spec.Source.Type = "iso"
	img.Spec.Source.BootCommand = []string{"<enter>"}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "BOOT_COMMAND" {
			if e.Value != `["<enter>"]` {
				t.Errorf("BOOT_COMMAND = %q, want [\"<enter>\"]", e.Value)
			}
			return
		}
	}
	t.Error("BOOT_COMMAND env var not found")
}

func TestAssemble_GuestAccess_ExposedAsEnv(t *testing.T) {
	img := baseImage()
	img.Spec.Source.Type = "iso"
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol:   "winrm",
		Host:       "127.0.0.1",
		HostPort:   55986,
		User:       "Administrator",
		SSHKeyPath: "/workspace/id_ed25519",
		GuestPort:  5986,
		Timeout:    durationPtr(12 * time.Minute),
		WinRM: &v1alpha1.WinRMAccessSpec{
			HTTPS:              boolPtr(true),
			InsecureSkipVerify: true,
		},
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	if envMap["GUEST_ACCESS_PROTOCOL"] != "winrm" ||
		envMap["GUEST_ACCESS_HOST"] != "127.0.0.1" ||
		envMap["GUEST_ACCESS_HOST_PORT"] != "55986" ||
		envMap["GUEST_ACCESS_GUEST_PORT"] != "5986" ||
		envMap["GUEST_ACCESS_USER"] != "Administrator" ||
		envMap["GUEST_ACCESS_SSH_KEY_PATH"] != "/workspace/id_ed25519" ||
		envMap["GUEST_ACCESS_TIMEOUT"] != "12m0s" ||
		envMap["GUEST_ACCESS_WINRM_HTTPS"] != "true" ||
		envMap["GUEST_ACCESS_WINRM_INSECURE_SKIP_VERIFY"] != "true" {
		t.Fatalf("guest access env = %#v", envMap)
	}
}

func TestAssemble_GuestAccess_CredentialsSecretMounted(t *testing.T) {
	img := baseImage()
	img.Spec.Source.Type = "iso"
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "winrm",
		HostPort: 55986,
		User:     "Administrator",
		Credentials: &v1alpha1.GuestCredentialsSpec{
			SecretRef: &v1alpha1.GuestCredentialsSecretRef{
				Name:             "guest-credentials",
				SSHPrivateKeyKey: "ssh-key",
				PasswordKey:      "winrm-password",
			},
		},
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	if envMap["GUEST_ACCESS_SSH_KEY_PATH"] != "/credentials/guest/id_ed25519" ||
		envMap["GUEST_ACCESS_PASSWORD_PATH"] != "/credentials/guest/password" {
		t.Fatalf("guest credential env = %#v", envMap)
	}
	var foundVolume, foundMount bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "guest-credentials" && v.Secret != nil && v.Secret.SecretName == "guest-credentials" {
			foundVolume = true
			if v.Secret.DefaultMode == nil || *v.Secret.DefaultMode != 0o400 {
				t.Fatalf("secret defaultMode = %#v, want 0400", v.Secret.DefaultMode)
			}
			if len(v.Secret.Items) != 2 || v.Secret.Items[0].Path != "id_ed25519" || v.Secret.Items[1].Path != "password" {
				t.Fatalf("secret items = %#v", v.Secret.Items)
			}
		}
	}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "guest-credentials" && m.MountPath == "/credentials/guest" && m.ReadOnly {
			foundMount = true
		}
	}
	if !foundVolume || !foundMount {
		t.Fatalf("guest credentials volume=%v mount=%v", foundVolume, foundMount)
	}
}

func TestAssemble_GuestAccess_GeneratedCredentialsExposedAsNonSecretEnv(t *testing.T) {
	img := baseImage()
	img.Spec.Source.Type = "iso"
	img.Spec.Build.GuestAccess = &v1alpha1.GuestAccessSpec{
		Protocol: "ssh",
		HostPort: 2222,
		Credentials: &v1alpha1.GuestCredentialsSpec{
			Generate: &v1alpha1.GuestGeneratedCredentialsSpec{
				SSHKey:         true,
				Password:       true,
				PasswordLength: 40,
			},
			Injection: &v1alpha1.GuestCredentialInjectionSpec{Method: "cloud-init"},
		},
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	checks := map[string]string{
		"GUEST_CREDENTIALS_GENERATE_SSH_KEY":         "true",
		"GUEST_CREDENTIALS_GENERATE_PASSWORD":        "true",
		"GUEST_CREDENTIALS_GENERATE_PASSWORD_LENGTH": "40",
		"GUEST_CREDENTIALS_INJECTION_METHOD":         "cloud-init",
	}
	for name, want := range checks {
		if envMap[name] != want {
			t.Fatalf("%s = %q, want %q; env=%#v", name, envMap[name], want, envMap)
		}
	}
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "guest-credentials" {
			t.Fatal("generated credentials must not mount a Kubernetes Secret volume")
		}
	}
	var foundGeneratedVolume, foundGeneratedMount bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "generated-credentials" && v.EmptyDir != nil &&
			v.EmptyDir.Medium == corev1.StorageMediumMemory {
			foundGeneratedVolume = true
		}
	}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "generated-credentials" && m.MountPath == "/credentials/generated" {
			foundGeneratedMount = true
		}
	}
	if envMap["GUEST_CREDENTIALS_DIR"] != "/credentials/generated" ||
		!foundGeneratedVolume || !foundGeneratedMount {
		t.Fatalf("generated credentials dir=%q volume=%v mount=%v",
			envMap["GUEST_CREDENTIALS_DIR"], foundGeneratedVolume, foundGeneratedMount)
	}
}

func TestAssemble_Provisioners_PresentAsJSONArray(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "cloud-init", Inline: "#cloud-config"},
		{Type: "shell", Inline: "echo ok"},
		{Type: "ansible", Playbook: "site.yml"},
	}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	value, ok := envMap["PROVISIONERS"]
	if !ok {
		t.Fatal("PROVISIONERS env var missing")
	}
	var parsed []v1alpha1.ProvisionerSpec
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		t.Fatalf("PROVISIONERS value %q is not valid JSON: %v", value, err)
	}
	if len(parsed) != 3 || parsed[0].Type != "cloud-init" || parsed[1].Inline != "echo ok" || parsed[2].Type != "ansible" {
		t.Fatalf("decoded provisioners = %#v", parsed)
	}
}

func TestAssemble_GitProvisionerAuthSecretMountedAndReferencedByPath(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{
		Type: "shell",
		Source: &v1alpha1.ProvisionerSourceSpec{Git: &v1alpha1.GitProvisionerSourceSpec{
			URL:  "https://example.com/private.git",
			Ref:  "main",
			Path: "scripts",
			Auth: &v1alpha1.GitProvisionerAuthSpec{
				SecretRef: &v1alpha1.GitProvisionerAuthSecretRef{
					Name:        "private-git",
					TokenKey:    "pat",
					UsernameKey: "user",
					PasswordKey: "pass",
				},
			},
		}},
	}}

	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}

	var foundVolume bool
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == "git-credentials-0" && volume.Secret != nil &&
			volume.Secret.SecretName == "private-git" && volume.Secret.DefaultMode != nil &&
			*volume.Secret.DefaultMode == 0o400 {
			foundVolume = true
		}
	}
	if !foundVolume {
		t.Fatalf("git auth secret volume not mounted correctly: %#v", job.Spec.Template.Spec.Volumes)
	}

	main := job.Spec.Template.Spec.Containers[0]
	var foundMount bool
	for _, mount := range main.VolumeMounts {
		if mount.Name == "git-credentials-0" && mount.MountPath == "/credentials/git/0" && mount.ReadOnly {
			foundMount = true
		}
	}
	if !foundMount {
		t.Fatalf("git auth secret mount not found: %#v", main.VolumeMounts)
	}

	envMap := make(map[string]string)
	for _, e := range main.Env {
		envMap[e.Name] = e.Value
	}
	var parsed []v1alpha1.ProvisionerSpec
	if err := json.Unmarshal([]byte(envMap["PROVISIONERS"]), &parsed); err != nil {
		t.Fatalf("PROVISIONERS value is not valid JSON: %v", err)
	}
	auth := parsed[0].Source.Git.Auth
	if auth.TokenPath != "/credentials/git/0/pat" ||
		auth.UsernamePath != "/credentials/git/0/user" ||
		auth.PasswordPath != "/credentials/git/0/pass" {
		t.Fatalf("git auth paths = %#v", auth)
	}
	if auth.RuntimeToken != "" || auth.RuntimeUsername != "" || auth.RuntimePassword != "" {
		t.Fatalf("runtime credentials must not be serialized into build pod env: %#v", auth)
	}
}
