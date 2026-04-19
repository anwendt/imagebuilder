// plugins/aws/plugin_test.go
//
// Unit tests for the AWS platform plugin.
//
// Covered behaviours:
//   - Name() and Version() return correct values
//   - Init() rejects missing region
//   - Init() rejects missing accessKeyId / secretAccessKey
//   - Init() succeeds with valid credentials
//   - Validate() rejects unsupported image formats
//   - Validate() accepts supported formats (vmdk, raw, vhd)
//   - Upload() returns error when buildID metadata is missing
//   - Upload() returns S3 key with correct format when buildID present
//   - HealthCheck() always returns nil (placeholder)
//   - SupportedFormats() includes vmdk, raw, vhd
//   - SupportedOS() includes linux, windows

package aws_test

import (
	"context"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	awsplugin "github.com/anwendt/imagebuilder/plugins/aws"
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

func newInitializedPlugin(t *testing.T) *awsplugin.AWSPlugin {
	t.Helper()
	p := &awsplugin.AWSPlugin{}
	if err := p.Init(context.Background(), validConfig()); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

func TestAWSPlugin_Name(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
	if p.Name() != "aws" {
		t.Errorf("Name() = %q, want aws", p.Name())
	}
}

func TestAWSPlugin_Version(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
	if p.Version() == "" {
		t.Error("Version() should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

func TestAWSPlugin_SupportedFormats_IncludesVMDK(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
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
	p := &awsplugin.AWSPlugin{}
	formats := p.SupportedFormats()
	have := make(map[platform.ImageFormat]bool)
	for _, f := range formats {
		have[f] = true
	}
	for _, want := range []platform.ImageFormat{platform.FormatRaw, platform.FormatVHD} {
		if !have[want] {
			t.Errorf("SupportedFormats() missing %q", want)
		}
	}
}

func TestAWSPlugin_SupportedOS_IncludesLinuxAndWindows(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
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

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func TestAWSPlugin_Init_MissingRegion_ReturnsError(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
	cfg := validConfig()
	cfg.Region = ""
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Error("Init with missing region should return error")
	}
}

func TestAWSPlugin_Init_MissingAccessKeyId_ReturnsError(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
	cfg := validConfig()
	delete(cfg.SecretData, "accessKeyId")
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Error("Init with missing accessKeyId should return error")
	}
}

func TestAWSPlugin_Init_MissingSecretAccessKey_ReturnsError(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
	cfg := validConfig()
	delete(cfg.SecretData, "secretAccessKey")
	if err := p.Init(context.Background(), cfg); err == nil {
		t.Error("Init with missing secretAccessKey should return error")
	}
}

func TestAWSPlugin_Init_ValidConfig_ReturnsNil(t *testing.T) {
	p := &awsplugin.AWSPlugin{}
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

func TestAWSPlugin_Validate_RejectsUnsupportedFormat(t *testing.T) {
	p := newInitializedPlugin(t)
	unsupported := []string{"ova", "ovf", "qcow2", "gcetarball", "ami"}
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
	artifact := &platform.BuildArtifact{
		Path:   "/workspace/disk.vmdk",
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
	artifact := &platform.BuildArtifact{
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
		Metadata: map[string]string{},
	}
	if err := p.Cleanup(context.Background(), artifact); err != nil {
		t.Errorf("Cleanup returned error: %v", err)
	}
}
