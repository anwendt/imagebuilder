package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/security/netguard"
)

type fakeResolver map[string][]string

func (r fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	addrs, ok := r[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return addrs, nil
}

func TestReadSecretData_ExpandsJSONCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"accessKeyId":"id","secretAccessKey":"secret","sessionToken":"token"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials"), raw, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	data, err := readSecretData(dir)
	if err != nil {
		t.Fatalf("readSecretData returned error: %v", err)
	}
	if string(data["accessKeyId"]) != "id" {
		t.Fatalf("accessKeyId = %q, want id", string(data["accessKeyId"]))
	}
	if string(data["secretAccessKey"]) != "secret" {
		t.Fatalf("secretAccessKey = %q, want secret", string(data["secretAccessKey"]))
	}
}

func TestReadSecretData_KeepsProviderSpecificFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "username"), []byte("admin"), 0o600); err != nil {
		t.Fatalf("write username: %v", err)
	}

	data, err := readSecretData(dir)
	if err != nil {
		t.Fatalf("readSecretData returned error: %v", err)
	}
	if string(data["username"]) != "admin" {
		t.Fatalf("username = %q, want admin", string(data["username"]))
	}
}

func TestRecordUploadOperation_UpsertsByProviderConfigAndRef(t *testing.T) {
	workspace := t.TempDir()
	first := uploadOperationRecord{
		Provider:           "aws",
		ProviderConfigName: "aws-prod",
		Format:             "vmdk",
		ProviderRef:        "imagebuilder/build/disk.vmdk",
		Metadata:           map[string]string{"bucket": "old"},
	}
	if err := recordUploadOperation(workspace, first); err != nil {
		t.Fatalf("record first operation: %v", err)
	}
	first.Metadata = map[string]string{"bucket": "new"}
	if err := recordUploadOperation(workspace, first); err != nil {
		t.Fatalf("record updated operation: %v", err)
	}
	ops, err := readUploadOperations(filepath.Join(workspace, operationsName))
	if err != nil {
		t.Fatalf("read operations: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("operations len = %d, want 1", len(ops))
	}
	if ops[0].Metadata["bucket"] != "new" {
		t.Fatalf("metadata bucket = %q, want new", ops[0].Metadata["bucket"])
	}
}

func TestValidateProviderEndpoint_RejectsDNSNameResolvingToBlockedRange(t *testing.T) {
	err := validateProviderEndpointWithOptions(context.Background(), uploadpod.TargetConfig{
		ProviderConfigName: "vsphere-prod",
		Endpoint:           "https://vcenter.example.test/sdk",
	}, netguard.Options{Resolver: fakeResolver{
		"vcenter.example.test": {"10.0.0.5"},
	}})
	if err == nil {
		t.Fatal("validateProviderEndpointWithOptions returned nil, want error")
	}
	if !strings.Contains(err.Error(), "blocked range") {
		t.Fatalf("error = %q, want blocked range rejection", err.Error())
	}
}

func TestValidateProviderEndpoint_RejectsUnresolvedHostsAtRuntime(t *testing.T) {
	err := validateProviderEndpointWithOptions(context.Background(), uploadpod.TargetConfig{
		ProviderConfigName: "openstack-prod",
		Endpoint:           "https://openstack.example.test:5000/v3",
	}, netguard.Options{Resolver: fakeResolver{}})
	if err == nil {
		t.Fatal("validateProviderEndpointWithOptions returned nil, want error")
	}
	if !strings.Contains(err.Error(), "could not be resolved") {
		t.Fatalf("error = %q, want unresolved host rejection", err.Error())
	}
}

func TestValidateProviderEndpoint_AllowsEmptyEndpoint(t *testing.T) {
	err := validateProviderEndpointWithOptions(context.Background(), uploadpod.TargetConfig{
		ProviderConfigName: "aws-prod",
	}, netguard.Options{Resolver: fakeResolver{}})
	if err != nil {
		t.Fatalf("empty endpoint should be accepted: %v", err)
	}
}

func TestRun_ReportsUploadBytes(t *testing.T) {
	workspace := t.TempDir()
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{
		Path:      "/workspace/artifact.vmdk",
		Format:    "vmdk",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 1234,
		OS:        "linux",
	}); err != nil {
		t.Fatalf("write build result: %v", err)
	}
	creds := filepath.Join(workspace, "creds")
	if err := os.Mkdir(creds, 0o700); err != nil {
		t.Fatalf("mkdir creds: %v", err)
	}
	if err := os.WriteFile(filepath.Join(creds, "token"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	targets := `[{"provider":"upload-bytes","providerConfigName":"upload-bytes-cfg","format":"vmdk","credentialsPath":"` + creds + `"}]`

	registry := plugin.Default()
	if err := registry.Register(&uploadBytesProvider{}); err != nil {
		t.Fatalf("register upload bytes provider: %v", err)
	}
	t.Cleanup(func() {
		registry.Deregister("upload-bytes")
	})

	result, err := run(context.Background(), func(key string) string {
		switch key {
		case "WORKSPACE_DIR":
			return workspace
		case "UPLOAD_TARGETS_JSON":
			return targets
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("operations len = %d, want 1", len(result.Operations))
	}
	if result.Operations[0].UploadBytes != 1234 {
		t.Fatalf("uploadBytes = %d, want 1234", result.Operations[0].UploadBytes)
	}
}

func TestFallbackUploadOperations_UsesBuildResultAndTargetMetadata(t *testing.T) {
	workspace := t.TempDir()
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{
		Path:     "/workspace/artifact.vmdk",
		Format:   "vmdk",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OS:       "linux",
		Metadata: map[string]string{
			"buildID":   "build-123",
			"imageName": "ubuntu-prod",
		},
	}); err != nil {
		t.Fatalf("write build result: %v", err)
	}

	ops, err := fallbackUploadOperations(workspace, []uploadpod.TargetConfig{
		{
			Provider:           "aws",
			ProviderConfigName: "aws-prod",
			Format:             "vmdk",
			Extra:              map[string]string{"s3Bucket": "images"},
			Tags:               map[string]string{"env": "prod"},
		},
	})
	if err != nil {
		t.Fatalf("fallbackUploadOperations returned error: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("operations len = %d, want 1", len(ops))
	}
	if ops[0].Metadata["s3Bucket"] != "images" {
		t.Fatalf("s3Bucket metadata = %q, want images", ops[0].Metadata["s3Bucket"])
	}
	if ops[0].Metadata["target.tag.env"] != "prod" {
		t.Fatalf("target tag metadata = %q, want prod", ops[0].Metadata["target.tag.env"])
	}
	if ops[0].Metadata["buildID"] != "build-123" {
		t.Fatalf("buildID metadata = %q, want build-123", ops[0].Metadata["buildID"])
	}
}

type uploadBytesProvider struct{}

func (p *uploadBytesProvider) Name() string    { return "upload-bytes" }
func (p *uploadBytesProvider) Version() string { return "v0.0.0-test" }
func (p *uploadBytesProvider) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{platform.FormatVMDK}
}
func (p *uploadBytesProvider) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{platform.OSFamilyLinux}
}
func (p *uploadBytesProvider) Init(context.Context, platform.PluginConfig) error { return nil }
func (p *uploadBytesProvider) Validate(context.Context, v1alpha1.TargetSpec) error {
	return nil
}
func (p *uploadBytesProvider) Upload(context.Context, *platform.BuildArtifact) (*platform.UploadResult, error) {
	return &platform.UploadResult{
		ProviderRef: "provider://upload-bytes/artifact",
	}, nil
}
func (p *uploadBytesProvider) Register(context.Context, *platform.UploadResult) (*platform.ImageRef, error) {
	return &platform.ImageRef{ID: "image-123", Location: "test"}, nil
}
func (p *uploadBytesProvider) Cleanup(context.Context, *platform.BuildArtifact) error {
	return nil
}
func (p *uploadBytesProvider) HealthCheck(context.Context) error {
	return nil
}
