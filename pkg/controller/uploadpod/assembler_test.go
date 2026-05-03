package uploadpod_test

import (
	"encoding/json"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add v1alpha1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatalf("add batchv1 scheme: %v", err)
	}
	return s
}

func TestAssemble_MountsWorkspacePVCAndCredentials(t *testing.T) {
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build: v1alpha1.BuildSpec{
				ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"},
				Upload:          &v1alpha1.UploadSpec{Image: "registry.example.test/uploader:v1"},
			},
			Targets: []v1alpha1.TargetSpec{
				{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "aws",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "aws-secret"}},
			Region:      "eu-west-1",
		},
	}

	job, err := uploadpod.Assemble(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("Assemble returned error: %v", err)
	}
	if job.Name != "ubuntu-upload" {
		t.Fatalf("job name = %q, want ubuntu-upload", job.Name)
	}
	vols := job.Spec.Template.Spec.Volumes
	if vols[0].PersistentVolumeClaim == nil || vols[0].PersistentVolumeClaim.ClaimName != "ubuntu-workspace" {
		t.Fatalf("workspace volume = %#v", vols[0])
	}
	if vols[1].Secret == nil || vols[1].Secret.SecretName != "aws-secret" {
		t.Fatalf("credentials secret volume = %#v", vols[1])
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Name != "upload" {
		t.Fatalf("container name = %q, want upload", container.Name)
	}
	if container.Image != "registry.example.test/uploader:v1" {
		t.Fatalf("container image = %q", container.Image)
	}
	env := envMap(container.Env)
	var targets []uploadpod.TargetConfig
	if err := json.Unmarshal([]byte(env["UPLOAD_TARGETS_JSON"]), &targets); err != nil {
		t.Fatalf("UPLOAD_TARGETS_JSON invalid: %v", err)
	}
	if len(targets) != 1 || targets[0].Provider != "aws" || targets[0].CredentialsPath != "/credentials/aws-cfg" {
		t.Fatalf("targets = %#v", targets)
	}
	if len(container.VolumeMounts) != 2 || container.VolumeMounts[1].MountPath != "/credentials/aws-cfg" {
		t.Fatalf("volume mounts = %#v", container.VolumeMounts)
	}
}

func TestAssemble_CredentialsSecretRefKeyMountsSingleCredentialsFile(t *testing.T) {
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build: v1alpha1.BuildSpec{ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"}},
			Targets: []v1alpha1.TargetSpec{
				{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider: "aws",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{
				Name: "aws-secret",
				Key:  "credentials.json",
			}},
		},
	}
	job, err := uploadpod.Assemble(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("Assemble returned error: %v", err)
	}
	secret := job.Spec.Template.Spec.Volumes[1].Secret
	if secret == nil || len(secret.Items) != 1 {
		t.Fatalf("secret volume = %#v", secret)
	}
	if secret.Items[0].Key != "credentials.json" || secret.Items[0].Path != "credentials" {
		t.Fatalf("secret item = %#v", secret.Items[0])
	}
}

func TestAssemble_UploaderImageFromEnvironment(t *testing.T) {
	t.Setenv("UPLOADER_IMAGE", "registry.example.test/imagebuilder-uploader@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build: v1alpha1.BuildSpec{ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"}},
			Targets: []v1alpha1.TargetSpec{
				{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "aws",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "aws-secret"}},
		},
	}

	job, err := uploadpod.Assemble(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("Assemble returned error: %v", err)
	}
	image := job.Spec.Template.Spec.Containers[0].Image
	if image != "registry.example.test/imagebuilder-uploader@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("uploader image = %q", image)
	}
}

func TestAssemble_UploaderSpecImageOverridesEnvironment(t *testing.T) {
	t.Setenv("UPLOADER_IMAGE", "registry.example.test/imagebuilder-uploader@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build: v1alpha1.BuildSpec{
				ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"},
				Upload:          &v1alpha1.UploadSpec{Image: "registry.example.test/custom-uploader@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
			},
			Targets: []v1alpha1.TargetSpec{
				{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "aws",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "aws-secret"}},
		},
	}

	job, err := uploadpod.Assemble(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("Assemble returned error: %v", err)
	}
	image := job.Spec.Template.Spec.Containers[0].Image
	if image != "registry.example.test/custom-uploader@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("uploader image = %q", image)
	}
}

func TestAssembleCleanup_SetsCleanupMode(t *testing.T) {
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build: v1alpha1.BuildSpec{ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"}},
			Targets: []v1alpha1.TargetSpec{
				{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"},
			},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "aws",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "aws-secret"}},
		},
	}
	job, err := uploadpod.AssembleCleanup(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("AssembleCleanup returned error: %v", err)
	}
	if job.Name != "ubuntu-upload-cleanup" {
		t.Fatalf("job name = %q, want ubuntu-upload-cleanup", job.Name)
	}
	env := envMap(job.Spec.Template.Spec.Containers[0].Env)
	if env["UPLOAD_CLEANUP_ONLY"] != "true" {
		t.Fatalf("UPLOAD_CLEANUP_ONLY = %q, want true", env["UPLOAD_CLEANUP_ONLY"])
	}
}

func envMap(env []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}
