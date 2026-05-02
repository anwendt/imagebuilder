package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/builder"
)

func TestRequestFromEnv_BuildsRequest(t *testing.T) {
	t.Setenv("WORKSPACE_DIR", "/tmp/workspace")
	t.Setenv("OS_FAMILY", "linux")
	t.Setenv("OS_DISTRIBUTION", "ubuntu")
	t.Setenv("OS_VERSION", "24.04")
	t.Setenv("OS_ARCH", "amd64")
	t.Setenv("SOURCE_TYPE", "cloud-image")
	t.Setenv("SOURCE_URL", "https://images.example.test/ubuntu.img")
	t.Setenv("SOURCE_CHECKSUM", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("TARGET_PROVIDER_CONFIG", "aws-prod")
	t.Setenv("TARGET_FORMAT", "raw")
	t.Setenv("CACHE_DIR", "/cache")
	t.Setenv("CACHE_TTL", "24h")
	t.Setenv("CACHE_RETAIN_POLICY", "Never")
	t.Setenv("GUEST_CREDENTIALS_DIR", "/credentials/generated")
	t.Setenv("BOOT_COMMAND", `["<tab>"," inst.ks=http://example.test/ks.cfg","<enter>"]`)
	t.Setenv("GUEST_ACCESS_PROTOCOL", "ssh")
	t.Setenv("GUEST_ACCESS_HOST", "127.0.0.1")
	t.Setenv("GUEST_ACCESS_HOST_PORT", "2222")
	t.Setenv("GUEST_ACCESS_GUEST_PORT", "22")
	t.Setenv("GUEST_ACCESS_USER", "imagebuilder")
	t.Setenv("GUEST_ACCESS_SSH_KEY_PATH", "/workspace/id_ed25519")
	t.Setenv("GUEST_ACCESS_TIMEOUT", "3m")
	t.Setenv("PROVISIONERS", `[{"type":"shell","inline":"echo ok"}]`)
	t.Setenv("QEMU_ENABLE_KVM", "true")

	req, workspace, err := requestFromEnv()
	if err != nil {
		t.Fatalf("requestFromEnv returned error: %v", err)
	}
	if workspace != "/tmp/workspace" {
		t.Fatalf("workspace = %q, want /tmp/workspace", workspace)
	}
	if req.Image.Spec.Source.Type != "cloud-image" {
		t.Fatalf("source type = %q, want cloud-image", req.Image.Spec.Source.Type)
	}
	if req.Image.Spec.Targets[0].Format != "raw" {
		t.Fatalf("target format = %q, want raw", req.Image.Spec.Targets[0].Format)
	}
	if req.CacheDir != "/cache" {
		t.Fatalf("cache dir = %q, want /cache", req.CacheDir)
	}
	if req.CacheTTL.String() != "24h0m0s" {
		t.Fatalf("cache ttl = %s, want 24h", req.CacheTTL)
	}
	if req.CacheRetain != "Never" {
		t.Fatalf("cache retain = %q, want Never", req.CacheRetain)
	}
	if req.CredentialDir != "/credentials/generated" {
		t.Fatalf("credential dir = %q, want /credentials/generated", req.CredentialDir)
	}
	if len(req.Image.Spec.Source.BootCommand) != 3 {
		t.Fatalf("boot command = %#v, want 3 entries", req.Image.Spec.Source.BootCommand)
	}
	if req.Image.Spec.Build.GuestAccess == nil {
		t.Fatal("guest access should be set")
	}
	if req.Image.Spec.Build.GuestAccess.Protocol != "ssh" ||
		req.Image.Spec.Build.GuestAccess.HostPort != 2222 ||
		req.Image.Spec.Build.GuestAccess.GuestPort != 22 ||
		req.Image.Spec.Build.GuestAccess.User != "imagebuilder" ||
		req.Image.Spec.Build.GuestAccess.SSHKeyPath != "/workspace/id_ed25519" {
		t.Fatalf("guest access = %#v", req.Image.Spec.Build.GuestAccess)
	}
	if len(req.Image.Spec.Provisioners) != 1 || req.Image.Spec.Provisioners[0].Type != "shell" {
		t.Fatalf("provisioners = %#v", req.Image.Spec.Provisioners)
	}
	if req.Image.Spec.Build.Security == nil || !req.Image.Spec.Build.Security.EnableKVM {
		t.Fatalf("security = %#v, want enableKVM", req.Image.Spec.Build.Security)
	}
}

func TestRequestFromEnv_RequiresChecksum(t *testing.T) {
	t.Setenv("OS_FAMILY", "linux")
	t.Setenv("SOURCE_TYPE", "cloud-image")
	t.Setenv("SOURCE_URL", "https://images.example.test/ubuntu.img")

	if _, _, err := requestFromEnv(); err == nil {
		t.Fatal("requestFromEnv should require SOURCE_CHECKSUM")
	}
}

func TestRequestFromEnv_InvalidGuestAccessPortReturnsError(t *testing.T) {
	t.Setenv("OS_FAMILY", "linux")
	t.Setenv("SOURCE_TYPE", "iso")
	t.Setenv("SOURCE_URL", "https://images.example.test/ubuntu.iso")
	t.Setenv("SOURCE_CHECKSUM", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("GUEST_ACCESS_PROTOCOL", "ssh")
	t.Setenv("GUEST_ACCESS_HOST_PORT", "not-a-port")

	if _, _, err := requestFromEnv(); err == nil {
		t.Fatal("requestFromEnv should reject invalid guest access port")
	}
}

func TestRequestFromEnv_InvalidCacheTTLReturnsError(t *testing.T) {
	t.Setenv("OS_FAMILY", "linux")
	t.Setenv("SOURCE_TYPE", "cloud-image")
	t.Setenv("SOURCE_URL", "https://images.example.test/ubuntu.img")
	t.Setenv("SOURCE_CHECKSUM", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("CACHE_TTL", "0s")

	if _, _, err := requestFromEnv(); err == nil {
		t.Fatal("requestFromEnv should reject non-positive cache TTL")
	}
}

func TestWriteResult_UsesPrivateFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	result := resultFile{
		Path:      "/workspace/artifact.raw",
		Format:    "raw",
		Checksum:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 12,
		OS:        "linux",
	}

	if err := writeResult(path, result); err != nil {
		t.Fatalf("writeResult returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat result: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("result file mode = %v, want 0600", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var decoded resultFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if decoded.Path != result.Path {
		t.Fatalf("decoded path = %q, want %q", decoded.Path, result.Path)
	}
}

func TestWriteFailure_WritesClassifiedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.json")
	t.Setenv("GUEST_CREDENTIALS_DIR", "/credentials/generated")
	err := builder.Classify(builder.ReasonProvisionerFailed, os.ErrPermission)

	if err := writeFailure(path, err); err != nil {
		t.Fatalf("writeFailure returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read failure: %v", err)
	}
	var decoded resultFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failure is not valid JSON: %v", err)
	}
	if decoded.Reason != builder.ReasonProvisionerFailed || decoded.Error == "" {
		t.Fatalf("decoded failure = %#v", decoded)
	}
}

func TestWriteFailure_RedactsCredentialDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.json")
	t.Setenv("GUEST_CREDENTIALS_DIR", "/credentials/generated")
	err := builder.Classify(builder.ReasonProvisionerFailed, fmt.Errorf("password=supersecret path=/credentials/generated/guest-credentials/password"))

	if err := writeFailure(path, err); err != nil {
		t.Fatalf("writeFailure returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read failure: %v", err)
	}
	if strings.Contains(string(data), "supersecret") || strings.Contains(string(data), "/credentials/generated") {
		t.Fatalf("failure detail leaked sensitive content: %s", data)
	}
}
