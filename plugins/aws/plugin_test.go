// plugins/aws/plugin_test.go
//
// Unit tests for the AWS platform plugin.
//
// Covered behaviours:
//   - Name() and Version() return correct values
//   - Init() rejects missing region
//   - Init() rejects partial static credentials
//   - Init() succeeds with valid credentials
//   - Validate() rejects unsupported image formats
//   - Validate() accepts supported formats (ami, vmdk, raw, vhd)
//   - Upload() returns error when buildID metadata is missing
//   - Upload() returns S3 key with correct format when buildID present
//   - HealthCheck() always returns nil (placeholder)
//   - SupportedFormats() includes ami, vmdk, raw, vhd
//   - SupportedOS() includes linux, windows

package aws

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func validConfig() platform.PluginConfig {
	return platform.PluginConfig{
		ProviderConfigName: "aws-prod",
		Region:             "eu-central-1",
		SecretData: map[string][]byte{
			"accessKeyId":     []byte("AKIAIOSFODNN7EXAMPLE"),
			"secretAccessKey": []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
		},
		Extra: map[string]string{
			"s3Bucket": "my-image-bucket",
		},
	}
}

func newInitializedPlugin(t *testing.T) *AWSPlugin {
	t.Helper()
	p := &AWSPlugin{}
	if err := p.Init(context.Background(), validConfig()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	p.localClient = &fakeAWSLocalImageClient{}
	return p
}

type fakeAWSLocalImageClient struct {
	uploadedBucket  string
	uploadedKey     string
	uploadedPath    string
	registerInput   awsLocalRegisterInput
	cleanupMetadata map[string]string
	healthErr       error
}

func (f *fakeAWSLocalImageClient) UploadObject(_ context.Context, bucket, key, filePath string) error {
	f.uploadedBucket = bucket
	f.uploadedKey = key
	f.uploadedPath = filePath
	return nil
}

func (f *fakeAWSLocalImageClient) RegisterAMI(_ context.Context, input awsLocalRegisterInput) (*platform.ImageRef, error) {
	f.registerInput = input
	return &platform.ImageRef{ID: "ami-0123456789abcdef0", Name: input.ImageName, Location: "eu-central-1", Tags: input.Tags}, nil
}

func (f *fakeAWSLocalImageClient) CleanupLocalImage(_ context.Context, metadata map[string]string) error {
	f.cleanupMetadata = metadata
	return nil
}

func (f *fakeAWSLocalImageClient) HealthCheck(_ context.Context) error {
	return f.healthErr
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

func TestAWSPlugin_Name(t *testing.T) {
	p := &AWSPlugin{}
	if p.Name() != "aws" {
		t.Errorf("Name() = %q, want aws", p.Name())
	}
}

func TestAWSPlugin_Version(t *testing.T) {
	p := &AWSPlugin{}
	if p.Version() == "" {
		t.Error("Version() should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

func TestAWSPlugin_SupportedFormats_IncludesVMDK(t *testing.T) {
	p := &AWSPlugin{}
	formats := p.SupportedFormats()
	found := false
	for _, f := range formats {
		if f == platform.FormatVMDK {
			found = true
		}
	}
	if !found {
		t.Errorf("SupportedFormats() does not include vmdk: %v", formats)
	}
}

func TestAWSPlugin_SupportedFormats_IncludesRawAndVHD(t *testing.T) {
	p := &AWSPlugin{}
	formats := p.SupportedFormats()
	have := make(map[platform.ImageFormat]bool)
	for _, f := range formats {
		have[f] = true
	}
	for _, want := range []platform.ImageFormat{platform.FormatAMI, platform.FormatRaw, platform.FormatVHD} {
		if !have[want] {
			t.Errorf("SupportedFormats() missing %q", want)
		}
	}
}

func TestAWSPlugin_SupportedOS_IncludesLinuxAndWindows(t *testing.T) {
	p := &AWSPlugin{}
	families := p.SupportedOS()
	have := make(map[platform.OSFamily]bool)
	for _, f := range families {
		have[f] = true
	}
	for _, want := range []platform.OSFamily{platform.OSFamilyLinux, platform.OSFamilyWindows} {
		if !have[want] {
			t.Errorf("SupportedOS() missing %q", want)
		}
	}
}

func TestAWSPlugin_SupportedBuildModes_IncludesRemote(t *testing.T) {
	p := &AWSPlugin{}
	modes := p.SupportedBuildModes()
	have := make(map[string]bool)
	for _, mode := range modes {
		have[mode] = true
	}
	for _, want := range []string{v1alpha1.BuildModeLocal, v1alpha1.BuildModeRemote} {
		if !have[want] {
			t.Errorf("SupportedBuildModes() missing %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func TestAWSPlugin_Init_MissingRegion_ReturnsError(t *testing.T) {
	p := &AWSPlugin{}
	cfg := validConfig()
	cfg.Region = ""
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Error("Init with missing region should return error")
	}
}

func TestAWSPlugin_Init_MissingStaticCredentials_UsesDefaultChain(t *testing.T) {
	p := &AWSPlugin{}
	cfg := validConfig()
	delete(cfg.SecretData, "accessKeyId")
	delete(cfg.SecretData, "secretAccessKey")
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Errorf("Init without static credentials returned error: %v", err)
	}
}

func TestAWSPlugin_Init_PartialStaticCredentials_ReturnsError(t *testing.T) {
	p := &AWSPlugin{}
	cfg := validConfig()
	delete(cfg.SecretData, "accessKeyId")
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Error("Init with partial static credentials should return error")
	}
}

func TestAWSPlugin_Init_PartialStaticCredentialsWithoutSecret_ReturnsError(t *testing.T) {
	p := &AWSPlugin{}
	cfg := validConfig()
	delete(cfg.SecretData, "secretAccessKey")
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Error("Init with partial static credentials should return error")
	}
}

func TestAWSPlugin_Init_ValidConfig_ReturnsNil(t *testing.T) {
	p := &AWSPlugin{}
	if err := p.Init(context.Background(), validConfig()); err != nil {
		t.Errorf("Init with valid config returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestAWSPlugin_Validate_AcceptsVMDK(t *testing.T) {
	p := newInitializedPlugin(t)
	spec := v1alpha1.TargetSpec{Format: "vmdk"}
	if err := p.Validate(context.Background(), spec); err != nil {
		t.Errorf("Validate vmdk returned error: %v", err)
	}
}

func TestAWSPlugin_Validate_AcceptsRaw(t *testing.T) {
	p := newInitializedPlugin(t)
	spec := v1alpha1.TargetSpec{Format: "raw"}
	if err := p.Validate(context.Background(), spec); err != nil {
		t.Errorf("Validate raw returned error: %v", err)
	}
}

func TestAWSPlugin_Validate_AcceptsVHD(t *testing.T) {
	p := newInitializedPlugin(t)
	spec := v1alpha1.TargetSpec{Format: "vhd"}
	if err := p.Validate(context.Background(), spec); err != nil {
		t.Errorf("Validate vhd returned error: %v", err)
	}
}

func TestAWSPlugin_Validate_AcceptsAMI(t *testing.T) {
	p := newInitializedPlugin(t)
	spec := v1alpha1.TargetSpec{Format: "ami"}
	if err := p.Validate(context.Background(), spec); err != nil {
		t.Errorf("Validate ami returned error: %v", err)
	}
}

func TestAWSPlugin_Validate_RejectsUnsupportedFormat(t *testing.T) {
	p := newInitializedPlugin(t)
	unsupported := []string{"ova", "ovf", "qcow2", "gcetarball"}
	for _, f := range unsupported {
		spec := v1alpha1.TargetSpec{Format: f}
		if err := p.Validate(context.Background(), spec); err == nil {
			t.Errorf("Validate %q should return error for AWS plugin", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Upload
// ---------------------------------------------------------------------------

func TestAWSPlugin_Upload_MissingBuildID_ReturnsError(t *testing.T) {
	p := newInitializedPlugin(t)
	artifact := &platform.BuildArtifact{
		Path:     "/workspace/disk.vmdk",
		Format:   platform.FormatVMDK,
		Metadata: map[string]string{}, // no buildID
	}
	if _, err := p.Upload(context.Background(), artifact); err == nil {
		t.Error("Upload without buildID should return error")
	}
}

func TestAWSPlugin_Upload_ReturnS3Key(t *testing.T) {
	p := newInitializedPlugin(t)
	artifactPath := filepath.Join(t.TempDir(), "disk.vmdk")
	if err := os.WriteFile(artifactPath, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := &platform.BuildArtifact{
		Path:   artifactPath,
		Format: platform.FormatVMDK,
		Metadata: map[string]string{
			"buildID": "build-abc123",
		},
	}
	result, err := p.Upload(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.ProviderRef == "" {
		t.Error("Upload result ProviderRef should not be empty")
	}
	wantPrefix := "imagebuilder/build-abc123/disk."
	if len(result.ProviderRef) < len(wantPrefix) || result.ProviderRef[:len(wantPrefix)] != wantPrefix {
		t.Errorf("ProviderRef = %q, want prefix %q", result.ProviderRef, wantPrefix)
	}
}

func TestAWSPlugin_Upload_S3BucketInMetadata(t *testing.T) {
	p := newInitializedPlugin(t)
	artifactPath := filepath.Join(t.TempDir(), "disk.vmdk")
	if err := os.WriteFile(artifactPath, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := &platform.BuildArtifact{
		Path:   artifactPath,
		Format: platform.FormatVMDK,
		Metadata: map[string]string{
			"buildID": "build-xyz",
		},
	}
	result, err := p.Upload(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.Metadata["bucket"] != "my-image-bucket" {
		t.Errorf("result.Metadata[bucket] = %q, want my-image-bucket", result.Metadata["bucket"])
	}
}

// ---------------------------------------------------------------------------
// HealthCheck
// ---------------------------------------------------------------------------

func TestAWSPlugin_HealthCheck_ReturnsNil(t *testing.T) {
	p := newInitializedPlugin(t)
	if err := p.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

func TestAWSPlugin_Cleanup_ReturnsNil(t *testing.T) {
	p := newInitializedPlugin(t)
	artifact := &platform.BuildArtifact{
		Format:   platform.FormatVMDK,
		Metadata: map[string]string{"aws.s3Bucket": "bucket", "aws.s3Key": "key"},
	}
	if err := p.Cleanup(context.Background(), artifact); err != nil {
		t.Errorf("Cleanup returned error: %v", err)
	}
}

func TestAWSPlugin_Cleanup_DerivesStagingObjectFromBuildMetadata(t *testing.T) {
	p := newInitializedPlugin(t)
	fake := &fakeAWSLocalImageClient{}
	p.localClient = fake
	artifact := &platform.BuildArtifact{
		Format: platform.FormatVMDK,
		Metadata: map[string]string{
			"buildID":  "build-123",
			"s3Bucket": "my-image-bucket",
		},
	}

	if err := p.Cleanup(context.Background(), artifact); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	if fake.cleanupMetadata["bucket"] != "my-image-bucket" {
		t.Fatalf("cleanup bucket = %q, want my-image-bucket", fake.cleanupMetadata["bucket"])
	}
	if fake.cleanupMetadata["key"] != "imagebuilder/build-123/disk.vmdk" {
		t.Fatalf("cleanup key = %q, want imagebuilder/build-123/disk.vmdk", fake.cleanupMetadata["key"])
	}
}

func TestAWSPlugin_Register_UsesImportedSnapshotFlow(t *testing.T) {
	p := newInitializedPlugin(t)
	fake := &fakeAWSLocalImageClient{}
	p.localClient = fake
	ref, err := p.Register(context.Background(), &platform.UploadResult{
		ProviderRef: "imagebuilder/build/disk.vmdk",
		Metadata: map[string]string{
			"bucket":         "my-image-bucket",
			"key":            "imagebuilder/build/disk.vmdk",
			"buildID":        "build-123",
			"format":         "vmdk",
			"imageName":      "ubuntu-prod",
			"target.tag.env": "prod",
		},
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if ref.ID != "ami-0123456789abcdef0" {
		t.Fatalf("Image ID = %q", ref.ID)
	}
	if fake.registerInput.Bucket != "my-image-bucket" || fake.registerInput.Key != "imagebuilder/build/disk.vmdk" {
		t.Fatalf("register input bucket/key = %q/%q", fake.registerInput.Bucket, fake.registerInput.Key)
	}
	if fake.registerInput.Tags["env"] != "prod" {
		t.Fatalf("register input env tag = %q, want prod", fake.registerInput.Tags["env"])
	}
}

func TestLocalRegisterInput_UsesLocalKMSKey(t *testing.T) {
	cfg := awsConfig{
		providerConfigName: "aws-prod",
		extraConfig: map[string]string{
			"local.kmsKeyId": "alias/imagebuilder-local",
		},
	}
	input, err := localRegisterInput(cfg, &platform.UploadResult{
		ProviderRef: "imagebuilder/build/disk.raw",
		Metadata: map[string]string{
			"bucket":    "my-image-bucket",
			"key":       "imagebuilder/build/disk.raw",
			"buildID":   "build-123",
			"format":    "raw",
			"imageName": "ubuntu-prod",
		},
	})
	if err != nil {
		t.Fatalf("localRegisterInput returned error: %v", err)
	}
	if input.KMSKeyID != "alias/imagebuilder-local" {
		t.Fatalf("KMSKeyID = %q, want alias/imagebuilder-local", input.KMSKeyID)
	}
}
