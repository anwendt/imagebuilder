package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	providerv1 "github.com/anwendt/imagebuilder/api/provider/v1"
	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/controller/uploadpod"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
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

func testSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest[:])
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

func TestVerifyArtifactFileRejectsChecksumMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.raw")
	if err := os.WriteFile(path, []byte("actual artifact"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	err := verifyArtifactFile(&platform.BuildArtifact{Path: path, SizeBytes: int64(len("actual artifact")), Checksum: testSHA256([]byte("different artifact"))})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("error = %v, want checksum mismatch", err)
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
	if !strings.Contains(err.Error(), "outside the configured CIDR allowlist") {
		t.Fatalf("error = %q, want private CIDR rejection", err.Error())
	}
}

func TestValidateProviderEndpoint_AllowsScopedPrivateEndpoint(t *testing.T) {
	target := uploadpod.TargetConfig{
		ProviderConfigName: "vsphere-prod", Endpoint: "https://vcenter.corp.example/sdk",
		AllowedPrivateCIDRs: []string{"10.20.0.0/24"}, AllowedDNSNames: []string{"*.corp.example"},
	}
	err := validateProviderEndpointWithOptions(context.Background(), target, netguard.Options{
		Resolver:            fakeResolver{"vcenter.corp.example": {"10.20.0.5"}},
		AllowedPrivateCIDRs: target.AllowedPrivateCIDRs, AllowedDNSNames: target.AllowedDNSNames,
	})
	if err != nil {
		t.Fatalf("scoped private endpoint rejected: %v", err)
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
	artifactPath := filepath.Join(workspace, "artifact.vmdk")
	artifactData := make([]byte, 1234)
	if err := os.WriteFile(artifactPath, artifactData, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{
		Path:      artifactPath,
		Format:    "vmdk",
		Checksum:  testSHA256(artifactData),
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
	provider := &uploadBytesProvider{}
	if err := registry.Register(provider); err != nil {
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
	retried, err := run(context.Background(), func(key string) string {
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
		t.Fatalf("retried run returned error: %v", err)
	}
	if provider.uploads != 1 || provider.registers != 1 {
		t.Fatalf("provider calls after retry: uploads=%d registers=%d", provider.uploads, provider.registers)
	}
	if len(retried.Images) != 1 || len(retried.Operations) != 1 {
		t.Fatalf("retried result = %#v", retried)
	}
}

func TestRun_RegisterRetryUsesStableIdempotencyKey(t *testing.T) {
	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "artifact.vmdk")
	artifactData := make([]byte, 1234)
	if err := os.WriteFile(artifactPath, artifactData, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{Path: artifactPath, Format: "vmdk", Checksum: testSHA256(artifactData), SizeBytes: 1234, OS: "linux", Metadata: map[string]string{"buildID": "register-retry"}}); err != nil {
		t.Fatalf("write build result: %v", err)
	}
	credentials := filepath.Join(workspace, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatalf("mkdir credentials: %v", err)
	}
	targets := `[{"provider":"register-retry","providerConfigName":"register-retry-cfg","format":"vmdk","credentialsPath":"` + credentials + `"}]`
	provider := &registerRetryProvider{}
	registry := plugin.Default()
	if err := registry.Register(provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	t.Cleanup(func() { registry.Deregister("register-retry") })
	getenv := func(key string) string {
		if key == "WORKSPACE_DIR" {
			return workspace
		}
		if key == "UPLOAD_TARGETS_JSON" {
			return targets
		}
		return ""
	}
	if _, err := run(context.Background(), getenv); err == nil || !providererrors.IsTransient(err) {
		t.Fatalf("first run error = %v, want transient", err)
	}
	result, err := run(context.Background(), getenv)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if provider.uploads != 1 || provider.registers != 2 || len(provider.keys) != 2 || provider.keys[0] == "" || provider.keys[0] != provider.keys[1] {
		t.Fatalf("uploads=%d registers=%d keys=%#v", provider.uploads, provider.registers, provider.keys)
	}
	if len(result.Images) != 1 || result.Images[0].ImageRef != "image-idempotent" {
		t.Fatalf("result = %#v", result)
	}
}

func TestUploadSessions_RecoversPreviousCheckpointFromBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionsName)
	first := []uploadSessionRecord{{Provider: "aws", ProviderConfigName: "aws-a", Format: "vmdk", IdempotencyKey: "key-1", Phase: "uploading"}}
	second := []uploadSessionRecord{{Provider: "aws", ProviderConfigName: "aws-a", Format: "vmdk", IdempotencyKey: "key-1", Phase: "uploaded", ProviderRef: "artifact-1"}}
	if err := writeUploadSessions(path, first); err != nil {
		t.Fatalf("write first checkpoint: %v", err)
	}
	if err := writeUploadSessions(path, second); err != nil {
		t.Fatalf("write second checkpoint: %v", err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("corrupt primary checkpoint: %v", err)
	}
	recovered, err := readUploadSessions(path)
	if err != nil {
		t.Fatalf("readUploadSessions returned error: %v", err)
	}
	if len(recovered) != 1 || recovered[0].Phase != "uploading" {
		t.Fatalf("recovered sessions = %#v, want prior valid checkpoint", recovered)
	}
}

func TestUploadOperations_RecoversBackupAndSessions(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, operationsName)
	first := uploadOperationRecord{Provider: "aws", ProviderConfigName: "aws-a", Format: "vmdk", ProviderRef: "artifact-1"}
	second := uploadOperationRecord{Provider: "azure", ProviderConfigName: "azure-a", Format: "vhd", ProviderRef: "artifact-2"}
	if err := recordUploadOperation(workspace, first); err != nil {
		t.Fatalf("record first operation: %v", err)
	}
	if err := recordUploadOperation(workspace, second); err != nil {
		t.Fatalf("record second operation: %v", err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("corrupt operations: %v", err)
	}
	recovered, err := readUploadOperations(path)
	if err != nil || len(recovered) != 1 || recovered[0].ProviderRef != first.ProviderRef {
		t.Fatalf("backup recovery = %#v, error=%v", recovered, err)
	}
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatalf("remove operations backup: %v", err)
	}
	sessions := []uploadSessionRecord{{Provider: "gcp", ProviderConfigName: "gcp-a", Format: "gcetarball", ProviderRef: "gs://bucket/object", IdempotencyKey: "key", Phase: "uploaded", Metadata: map[string]string{"bucket": "bucket"}}}
	if err := writeUploadSessions(filepath.Join(workspace, sessionsName), sessions); err != nil {
		t.Fatalf("write sessions: %v", err)
	}
	recovered, err = readUploadOperations(path)
	if err != nil || len(recovered) != 1 || recovered[0].ProviderRef != "gs://bucket/object" {
		t.Fatalf("session reconstruction = %#v, error=%v", recovered, err)
	}
}

func TestRun_ContinuesIndependentTargetsAfterTransientFailure(t *testing.T) {
	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "artifact.vmdk")
	artifactData := make([]byte, 1234)
	if err := os.WriteFile(artifactPath, artifactData, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{Path: artifactPath, Format: "vmdk", Checksum: testSHA256(artifactData), SizeBytes: 1234, OS: "linux", Metadata: map[string]string{"buildID": "multi-target"}}); err != nil {
		t.Fatalf("write build result: %v", err)
	}
	for _, name := range []string{"multi-bad", "multi-good"} {
		if err := os.Mkdir(filepath.Join(workspace, name), 0o700); err != nil {
			t.Fatalf("mkdir credentials: %v", err)
		}
	}
	targets := `[` +
		`{"provider":"multi-target","providerConfigName":"multi-bad","format":"vmdk","credentialsPath":"` + filepath.Join(workspace, "multi-bad") + `"},` +
		`{"provider":"multi-target","providerConfigName":"multi-good","format":"vmdk","credentialsPath":"` + filepath.Join(workspace, "multi-good") + `"}` +
		`]`
	provider := &multiTargetProvider{uploads: map[string]int{}, registers: map[string]int{}}
	registry := plugin.Default()
	if err := registry.Register(provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	t.Cleanup(func() { registry.Deregister("multi-target") })
	getenv := func(key string) string {
		if key == "WORKSPACE_DIR" {
			return workspace
		}
		if key == "UPLOAD_TARGETS_JSON" {
			return targets
		}
		return ""
	}
	first, err := run(context.Background(), getenv)
	if err == nil || !providererrors.IsTransient(err) {
		t.Fatalf("first run error = %v, want transient aggregate", err)
	}
	if len(first.Images) != 1 || first.Images[0].ProviderConfig != "multi-good" {
		t.Fatalf("first result = %#v, independent target did not complete", first)
	}
	second, err := run(context.Background(), getenv)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if len(second.Images) != 2 || provider.uploads["multi-good"] != 1 || provider.registers["multi-good"] != 1 || provider.uploads["multi-bad"] != 2 {
		t.Fatalf("second=%#v uploads=%#v registers=%#v", second, provider.uploads, provider.registers)
	}
}

func TestRun_UsesPlatformProviderGRPCService(t *testing.T) {
	server := &uploaderGRPCServer{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	providerv1.RegisterPlatformProviderServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "artifact.vmdk")
	artifactData := []byte("stream-this-artifact")
	if err := os.WriteFile(artifactPath, artifactData, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{
		Path:      artifactPath,
		Format:    "vmdk",
		Checksum:  testSHA256(artifactData),
		SizeBytes: int64(len(artifactData)),
		OS:        "linux",
		Metadata:  map[string]string{"imageName": "ubuntu-grpc"},
	}); err != nil {
		t.Fatalf("write build result: %v", err)
	}
	credentialsPath := filepath.Join(workspace, "credentials")
	if err := os.Mkdir(credentialsPath, 0o700); err != nil {
		t.Fatalf("mkdir credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credentialsPath, "token"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	targetData, err := json.Marshal([]uploadpod.TargetConfig{{
		Provider:           "external",
		ProviderConfigName: "external-prod",
		Region:             "eu-central-1",
		Format:             "vmdk",
		Tags:               map[string]string{"env": "test"},
		CredentialsPath:    credentialsPath,
		GRPC:               &uploadpod.GRPCConfig{Address: listener.Addr().String()},
	}})
	if err != nil {
		t.Fatalf("marshal targets: %v", err)
	}

	result, err := run(context.Background(), func(key string) string {
		switch key {
		case "WORKSPACE_DIR":
			return workspace
		case "UPLOAD_TARGETS_JSON":
			return string(targetData)
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if string(server.uploaded) != string(artifactData) {
		t.Fatalf("uploaded data = %q, want %q", string(server.uploaded), string(artifactData))
	}
	if server.initialized == nil || server.initialized.GetProviderConfigName() != "external-prod" || string(server.initialized.GetCredentials()["token"]) != "secret" {
		t.Fatalf("initial ValidateConfig request = %#v", server.initialized)
	}
	if server.firstChunk == nil || server.firstChunk.GetProviderConfigName() != "external-prod" || server.firstChunk.GetFormat() != "vmdk" {
		t.Fatalf("first upload chunk = %#v", server.firstChunk)
	}
	if server.registered == nil || server.registered.GetProviderConfigName() != "external-prod" || server.registered.GetFormat() != "vmdk" || server.registered.GetTags()["target.tag.env"] != "test" {
		t.Fatalf("RegisterImage request = %#v", server.registered)
	}
	if len(result.Images) != 1 || result.Images[0].ImageRef != "image-external-123" {
		t.Fatalf("images = %#v", result.Images)
	}
}

func TestRun_TransientUploadFailureResumesDurableSession(t *testing.T) {
	server := &retryUploaderGRPCServer{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	providerv1.RegisterPlatformProviderServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })

	workspace := t.TempDir()
	artifactPath := filepath.Join(workspace, "artifact.vmdk")
	artifactData := []byte("retry-this-artifact")
	if err := os.WriteFile(artifactPath, artifactData, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := writeJSON(filepath.Join(workspace, resultFileName), v1alpha1.ArtifactStatus{
		Path: artifactPath, Format: "vmdk", Checksum: testSHA256(artifactData),
		SizeBytes: int64(len(artifactData)), OS: "linux", Metadata: map[string]string{"buildID": "retry-build"},
	}); err != nil {
		t.Fatalf("write build result: %v", err)
	}
	credentialsPath := filepath.Join(workspace, "retry-credentials")
	if err := os.Mkdir(credentialsPath, 0o700); err != nil {
		t.Fatalf("create credentials directory: %v", err)
	}
	targetData, err := json.Marshal([]uploadpod.TargetConfig{{
		Provider: "retry-external", ProviderConfigName: "retry-config", Format: "vmdk",
		CredentialsPath: credentialsPath, GRPC: &uploadpod.GRPCConfig{Address: listener.Addr().String()},
	}})
	if err != nil {
		t.Fatalf("marshal targets: %v", err)
	}
	getenv := func(key string) string {
		switch key {
		case "WORKSPACE_DIR":
			return workspace
		case "UPLOAD_TARGETS_JSON":
			return string(targetData)
		default:
			return ""
		}
	}

	if _, err := run(context.Background(), getenv); err == nil || !strings.Contains(err.Error(), "temporary disconnect") || !providererrors.IsTransient(err) {
		t.Fatalf("first run error = %v", err)
	}
	sessions, err := readUploadSessions(filepath.Join(workspace, sessionsName))
	if err != nil || len(sessions) != 1 || sessions[0].ResumeToken == "" || sessions[0].Phase != "uploading" {
		t.Fatalf("session after failed attempt = %#v, error = %v", sessions, err)
	}
	result, err := run(context.Background(), getenv)
	if err != nil {
		t.Fatalf("retried run returned error: %v", err)
	}
	if server.attempts != 2 || server.registers != 1 || len(server.tokens) != 2 || server.tokens[0] != server.tokens[1] {
		t.Fatalf("attempts=%d registers=%d tokens=%#v", server.attempts, server.registers, server.tokens)
	}
	if len(result.Images) != 1 || result.Images[0].ImageRef != "retry-image" {
		t.Fatalf("result = %#v", result)
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

type uploadBytesProvider struct {
	uploads   int
	registers int
}

type registerRetryProvider struct {
	uploads   int
	registers int
	keys      []string
}

type multiTargetProvider struct {
	uploads   map[string]int
	registers map[string]int
}

func (p *multiTargetProvider) Name() string    { return "multi-target" }
func (p *multiTargetProvider) Version() string { return "v0.0.0-test" }
func (p *multiTargetProvider) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{platform.FormatVMDK}
}
func (p *multiTargetProvider) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{platform.OSFamilyLinux}
}
func (p *multiTargetProvider) Init(context.Context, platform.PluginConfig) error   { return nil }
func (p *multiTargetProvider) Validate(context.Context, v1alpha1.TargetSpec) error { return nil }
func (p *multiTargetProvider) Upload(_ context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	config := artifact.Metadata["providerConfigName"]
	p.uploads[config]++
	if config == "multi-bad" && p.uploads[config] == 1 {
		return nil, providererrors.Transient(errors.New("temporary target outage"), 0)
	}
	return &platform.UploadResult{ProviderRef: "artifact-" + config, Metadata: map[string]string{"providerConfigName": config}}, nil
}
func (p *multiTargetProvider) Register(_ context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	config := result.Metadata["providerConfigName"]
	p.registers[config]++
	return &platform.ImageRef{ID: "image-" + config}, nil
}
func (p *multiTargetProvider) Cleanup(context.Context, *platform.BuildArtifact) error { return nil }
func (p *multiTargetProvider) HealthCheck(context.Context) error                      { return nil }

func (p *registerRetryProvider) Name() string    { return "register-retry" }
func (p *registerRetryProvider) Version() string { return "v0.0.0-test" }
func (p *registerRetryProvider) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{platform.FormatVMDK}
}
func (p *registerRetryProvider) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{platform.OSFamilyLinux}
}
func (p *registerRetryProvider) Init(context.Context, platform.PluginConfig) error   { return nil }
func (p *registerRetryProvider) Validate(context.Context, v1alpha1.TargetSpec) error { return nil }
func (p *registerRetryProvider) Upload(context.Context, *platform.BuildArtifact) (*platform.UploadResult, error) {
	p.uploads++
	return &platform.UploadResult{ProviderRef: "artifact-idempotent", Metadata: map[string]string{}}, nil
}
func (p *registerRetryProvider) Register(_ context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	p.registers++
	p.keys = append(p.keys, result.Metadata["register.idempotencyKey"])
	if p.registers == 1 {
		return nil, providererrors.Transient(errors.New("registration response lost"), 0)
	}
	return &platform.ImageRef{ID: "image-idempotent"}, nil
}
func (p *registerRetryProvider) Cleanup(context.Context, *platform.BuildArtifact) error { return nil }
func (p *registerRetryProvider) HealthCheck(context.Context) error                      { return nil }

type uploaderGRPCServer struct {
	providerv1.UnimplementedPlatformProviderServer
	initialized *providerv1.ValidateConfigRequest
	firstChunk  *providerv1.UploadChunk
	registered  *providerv1.RegisterRequest
	uploaded    []byte
}

type retryUploaderGRPCServer struct {
	providerv1.UnimplementedPlatformProviderServer
	attempts  int
	registers int
	tokens    []string
}

func (s *retryUploaderGRPCServer) GetCapabilities(context.Context, *providerv1.Empty) (*providerv1.CapabilitiesResponse, error) {
	return &providerv1.CapabilitiesResponse{
		ProviderName: "retry-external", ProviderVersion: "v1.0.0", ProtocolVersion: "v1",
		Formats: []string{"vmdk"}, OsFamilies: []string{"linux"}, UploadResumeMode: "restart",
	}, nil
}

func (s *retryUploaderGRPCServer) ValidateConfig(context.Context, *providerv1.ValidateConfigRequest) (*providerv1.ValidateConfigResponse, error) {
	return &providerv1.ValidateConfigResponse{Valid: true}, nil
}

func (s *retryUploaderGRPCServer) UploadArtifact(stream providerv1.PlatformProvider_UploadArtifactServer) error {
	header, err := stream.Recv()
	if err != nil {
		return err
	}
	s.attempts++
	token := header.GetSessionToken()
	if token == "" {
		token = header.GetIdempotencyKey()
	}
	s.tokens = append(s.tokens, token)
	if err := stream.Send(&providerv1.UploadProgress{
		Phase: "session", TotalBytes: header.GetTotalSizeBytes(), SessionToken: token, ResumeMode: "restart",
	}); err != nil {
		return err
	}
	for {
		chunk, err := stream.Recv()
		if err != nil {
			return err
		}
		if chunk.GetLast() {
			break
		}
	}
	if s.attempts == 1 {
		return status.Error(codes.Unavailable, "temporary disconnect")
	}
	return stream.Send(&providerv1.UploadProgress{
		Phase: "done", ProviderRef: "retry-provider-ref", TotalBytes: header.GetTotalSizeBytes(),
		BytesWritten: header.GetTotalSizeBytes(), SessionToken: token, CommittedOffset: header.GetTotalSizeBytes(), ResumeMode: "restart",
	})
}

func (s *retryUploaderGRPCServer) RegisterImage(context.Context, *providerv1.RegisterRequest) (*providerv1.ImageRef, error) {
	s.registers++
	return &providerv1.ImageRef{Id: "retry-image"}, nil
}

func (s *retryUploaderGRPCServer) DeleteArtifact(context.Context, *providerv1.DeleteRequest) (*providerv1.DeleteResponse, error) {
	return &providerv1.DeleteResponse{Deleted: true}, nil
}

func (s *uploaderGRPCServer) GetCapabilities(context.Context, *providerv1.Empty) (*providerv1.CapabilitiesResponse, error) {
	return &providerv1.CapabilitiesResponse{
		ProviderName:    "external",
		ProviderVersion: "v1.0.0",
		Formats:         []string{"vmdk"},
		OsFamilies:      []string{"linux"},
		ProtocolVersion: "v1",
	}, nil
}

func (s *uploaderGRPCServer) ValidateConfig(_ context.Context, req *providerv1.ValidateConfigRequest) (*providerv1.ValidateConfigResponse, error) {
	if len(req.GetCredentials()) > 0 {
		s.initialized = req
	}
	return &providerv1.ValidateConfigResponse{Valid: true}, nil
}

func (s *uploaderGRPCServer) UploadArtifact(stream providerv1.PlatformProvider_UploadArtifactServer) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if s.firstChunk == nil {
			s.firstChunk = chunk
		}
		s.uploaded = append(s.uploaded, chunk.GetData()...)
		if chunk.GetLast() {
			break
		}
	}
	return stream.Send(&providerv1.UploadProgress{
		BytesWritten: int64(len(s.uploaded)),
		TotalBytes:   int64(len(s.uploaded)),
		Phase:        "done",
		ProviderRef:  "provider://external/upload-123",
	})
}

func (s *uploaderGRPCServer) RegisterImage(_ context.Context, req *providerv1.RegisterRequest) (*providerv1.ImageRef, error) {
	s.registered = req
	return &providerv1.ImageRef{Id: "image-external-123", Location: "eu-central-1"}, nil
}

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
	p.uploads++
	return &platform.UploadResult{
		ProviderRef: "provider://upload-bytes/artifact",
	}, nil
}
func (p *uploadBytesProvider) Register(context.Context, *platform.UploadResult) (*platform.ImageRef, error) {
	p.registers++
	return &platform.ImageRef{ID: "image-123", Location: "test"}, nil
}
func (p *uploadBytesProvider) Cleanup(context.Context, *platform.BuildArtifact) error {
	return nil
}
func (p *uploadBytesProvider) HealthCheck(context.Context) error {
	return nil
}
