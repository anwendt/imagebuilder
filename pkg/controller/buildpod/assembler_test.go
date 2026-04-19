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
//   - Workspace volume present and mounted
//   - Owner reference set on the Job
//   - Security contexts (pod-level and container-level)
//   - Resource limits forwarded from BuildSpec
//   - Cache PVC volume added when CacheRef is set

package buildpod_test

import (
	"testing"

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
		"OS_FAMILY":        "linux",
		"OS_DISTRIBUTION":  "ubuntu",
		"OS_VERSION":       "24.04",
		"OS_ARCH":          "amd64",
		"SOURCE_TYPE":      "cloud-image",
		"SOURCE_URL":       "https://example.com/ubuntu-24.04.img",
		"VMIMAGE_NAME":     "ubuntu-2404",
		"VMIMAGE_NAMESPACE": "default",
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
// Init containers for init-container provisioners
// ---------------------------------------------------------------------------

func TestAssemble_InitContainers_AnsibleProvisioner(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "ansible", Image: "ghcr.io/anwendt/imagebuilder-provisioner-ansible:v1.0.0"},
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	if n := len(job.Spec.Template.Spec.InitContainers); n != 1 {
		t.Fatalf("expected 1 init container, got %d", n)
	}
	ic := job.Spec.Template.Spec.InitContainers[0]
	if ic.Image != "ghcr.io/anwendt/imagebuilder-provisioner-ansible:v1.0.0" {
		t.Errorf("init container image = %q, unexpected", ic.Image)
	}
}

func TestAssemble_InitContainers_Order(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "shell", Inline: "echo first"},          // in-process — no init container
		{Type: "ansible", Image: "ansible:latest"},     // init-container step 0
		{Type: "chef", Image: "chef:latest"},           // init-container step 1
	}
	job, err := buildpod.Assemble(img, newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	ics := job.Spec.Template.Spec.InitContainers
	if len(ics) != 2 {
		t.Fatalf("expected 2 init containers, got %d", len(ics))
	}
	if ics[0].Image != "ansible:latest" {
		t.Errorf("first init container image = %q, want ansible:latest", ics[0].Image)
	}
	if ics[1].Image != "chef:latest" {
		t.Errorf("second init container image = %q, want chef:latest", ics[1].Image)
	}
}

func TestAssemble_InitContainer_StepEnvVar(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "ansible", Image: "ansible:latest"},
		{Type: "puppet", Image: "puppet:latest"},
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
	}
}

func TestAssemble_InitContainer_MissingImage_ReturnsError(t *testing.T) {
	img := baseImage()
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "custom"}, // custom has no built-in default image and no image specified
	}
	_, err := buildpod.Assemble(img, newScheme(t))
	if err == nil {
		t.Error("expected error when init-container provisioner has no image, got nil")
	}
}

// ---------------------------------------------------------------------------
// Workspace volume
// ---------------------------------------------------------------------------

func TestAssemble_WorkspaceVolume(t *testing.T) {
	job, err := buildpod.Assemble(baseImage(), newScheme(t))
	if err != nil {
		t.Fatalf("Assemble failed: %v", err)
	}
	var found bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "workspace" && v.EmptyDir != nil {
			found = true
		}
	}
	if !found {
		t.Error("workspace emptyDir volume not found in job spec")
	}
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
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
		t.Error("Capabilities.Drop should be set")
	}
	if sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop[0] = %q, want ALL", sc.Capabilities.Drop[0])
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
