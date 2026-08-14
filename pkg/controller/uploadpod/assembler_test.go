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
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 3 {
		t.Fatalf("backoffLimit = %v, want 3", job.Spec.BackoffLimit)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Fatalf("activeDeadlineSeconds = %v, want positive remaining deadline", job.Spec.ActiveDeadlineSeconds)
	}
	if job.Spec.PodFailurePolicy == nil || len(job.Spec.PodFailurePolicy.Rules) != 1 {
		t.Fatalf("podFailurePolicy = %#v", job.Spec.PodFailurePolicy)
	}
	rule := job.Spec.PodFailurePolicy.Rules[0]
	if rule.Action != batchv1.PodFailurePolicyActionFailJob || rule.OnExitCodes == nil || len(rule.OnExitCodes.Values) != 1 || rule.OnExitCodes.Values[0] != 1 {
		t.Fatalf("terminal failure rule = %#v", rule)
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
	deadlineEnv := ""
	for _, env := range container.Env {
		if env.Name == "UPLOAD_DEADLINE_SECONDS" {
			deadlineEnv = env.Value
		}
	}
	if deadlineEnv == "" {
		t.Fatal("UPLOAD_DEADLINE_SECONDS is missing")
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

func TestAssembleWithProviderConnections_AddsGRPCRoute(t *testing.T) {
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
	job, err := uploadpod.AssembleWithProviderConnections(img, []v1alpha1.ProviderConfig{cfg}, map[string]uploadpod.ProviderConnection{
		"aws": {Address: "provider-aws.imagebuilder-system.svc:50051"},
	}, testScheme(t))
	if err != nil {
		t.Fatalf("AssembleWithProviderConnections returned error: %v", err)
	}
	var targets []uploadpod.TargetConfig
	env := envMap(job.Spec.Template.Spec.Containers[0].Env)
	if err := json.Unmarshal([]byte(env["UPLOAD_TARGETS_JSON"]), &targets); err != nil {
		t.Fatalf("UPLOAD_TARGETS_JSON invalid: %v", err)
	}
	if len(targets) != 1 || targets[0].GRPC == nil {
		t.Fatalf("targets = %#v, want one gRPC target", targets)
	}
	if targets[0].GRPC.Address != "provider-aws.imagebuilder-system.svc:50051" || targets[0].GRPC.TLS != nil {
		t.Fatalf("gRPC config = %#v", targets[0].GRPC)
	}
	if len(job.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("volumes = %#v, want workspace and credentials only", job.Spec.Template.Spec.Volumes)
	}
}

func TestAssembleWithProviderConnections_MountsMTLSSecret(t *testing.T) {
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build: v1alpha1.BuildSpec{ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"}},
			Targets: []v1alpha1.TargetSpec{
				{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "custom-cfg"}, Format: "raw"},
			},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-cfg", Namespace: "default"},
		Spec: v1alpha1.ProviderConfigSpec{
			Provider:    "custom",
			Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "custom-secret"}},
		},
	}
	job, err := uploadpod.AssembleWithProviderConnections(img, []v1alpha1.ProviderConfig{cfg}, map[string]uploadpod.ProviderConnection{
		"custom": {
			Address:       "provider-custom.imagebuilder-system.svc:50051",
			TLSSecretName: "imagebuilder-upload-tls-test",
			ServerName:    "provider-custom.imagebuilder-system.svc",
		},
	}, testScheme(t))
	if err != nil {
		t.Fatalf("AssembleWithProviderConnections returned error: %v", err)
	}
	container := job.Spec.Template.Spec.Containers[0]
	var targets []uploadpod.TargetConfig
	if err := json.Unmarshal([]byte(envMap(container.Env)["UPLOAD_TARGETS_JSON"]), &targets); err != nil {
		t.Fatalf("UPLOAD_TARGETS_JSON invalid: %v", err)
	}
	tlsConfig := targets[0].GRPC.TLS
	if tlsConfig == nil || tlsConfig.ServerName != "provider-custom.imagebuilder-system.svc" ||
		tlsConfig.CAPath != "/provider-tls/custom/ca.crt" ||
		tlsConfig.ClientCertPath != "/provider-tls/custom/tls.crt" ||
		tlsConfig.ClientKeyPath != "/provider-tls/custom/tls.key" {
		t.Fatalf("TLS config = %#v", tlsConfig)
	}
	if len(container.VolumeMounts) != 3 || container.VolumeMounts[2].MountPath != "/provider-tls/custom" {
		t.Fatalf("volume mounts = %#v", container.VolumeMounts)
	}
	volumes := job.Spec.Template.Spec.Volumes
	if len(volumes) != 3 || volumes[2].Secret == nil || volumes[2].Secret.SecretName != "imagebuilder-upload-tls-test" {
		t.Fatalf("volumes = %#v", volumes)
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

func TestAssemble_RevisionChangesUploadResourceNames(t *testing.T) {
	img := &v1alpha1.VMImage{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "default", UID: "uid"},
		Spec: v1alpha1.VMImageSpec{
			Build:   v1alpha1.BuildSpec{Revision: "v2", ArtifactStorage: &v1alpha1.ArtifactStorageSpec{Type: "pvc"}},
			Targets: []v1alpha1.TargetSpec{{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-cfg"}, Format: "vmdk"}},
		},
	}
	cfg := v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cfg", Namespace: "default"},
		Spec:       v1alpha1.ProviderConfigSpec{Provider: "aws", Credentials: v1alpha1.CredentialsSpec{SecretRef: v1alpha1.SecretRef{Name: "aws-secret"}}},
	}
	job, err := uploadpod.Assemble(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("Assemble returned error: %v", err)
	}
	cleanup, err := uploadpod.AssembleCleanup(img, []v1alpha1.ProviderConfig{cfg}, testScheme(t))
	if err != nil {
		t.Fatalf("AssembleCleanup returned error: %v", err)
	}
	if job.Name == "ubuntu-upload" || cleanup.Name == "ubuntu-upload-cleanup" || job.Name == cleanup.Name {
		t.Fatalf("revision names: upload=%q cleanup=%q", job.Name, cleanup.Name)
	}
}

func envMap(env []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		out[e.Name] = e.Value
	}
	return out
}
