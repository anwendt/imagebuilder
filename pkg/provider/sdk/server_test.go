package sdk_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	providerv1 "github.com/anwendt/imagebuilder/api/provider/v1"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestServer_ImplementsProviderContract(t *testing.T) {
	provider := &fakeProvider{}
	client, cleanup := startServer(t, provider)
	defer cleanup()
	ctx := context.Background()

	caps, err := client.GetCapabilities(ctx, &providerv1.Empty{})
	if err != nil {
		t.Fatalf("GetCapabilities returned error: %v", err)
	}
	if caps.ProviderName != "example" || caps.ProtocolVersion != "v1" {
		t.Fatalf("capabilities = %#v", caps)
	}

	valid, err := client.ValidateConfig(ctx, &providerv1.ValidateConfigRequest{
		ProviderConfigName: "example-config",
		Credentials:        map[string][]byte{"token": []byte("secret")},
		Region:             "eu-central-1",
		Extra:              map[string]string{"project": "platform"},
	})
	if err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	if !valid.Valid || provider.config.ProviderConfigName != "example-config" || string(provider.config.Credentials["token"]) != "secret" {
		t.Fatalf("validation result = %#v config = %#v", valid, provider.config)
	}

	stream, err := client.UploadArtifact(ctx)
	if err != nil {
		t.Fatalf("UploadArtifact returned error: %v", err)
	}
	if err := stream.Send(&providerv1.UploadChunk{
		Data:               []byte("hello "),
		Offset:             0,
		Format:             "qcow2",
		Checksum:           "sha256:test",
		TotalSizeBytes:     11,
		OsFamily:           "linux",
		ProviderConfigName: "example-config",
	}); err != nil {
		t.Fatalf("send first chunk: %v", err)
	}
	if err := stream.Send(&providerv1.UploadChunk{Data: []byte("world"), Offset: 6, Last: true}); err != nil {
		t.Fatalf("send second chunk: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close upload stream: %v", err)
	}
	var done *providerv1.UploadProgress
	for {
		progress, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("receive upload progress: %v", err)
		}
		if progress.ProviderRef != "" {
			done = progress
		}
	}
	if done == nil || done.ProviderRef != "uploaded/example-config/qcow2" {
		t.Fatalf("done progress = %#v", done)
	}
	if provider.uploaded.String() != "hello world" {
		t.Fatalf("uploaded body = %q", provider.uploaded.String())
	}

	ref, err := client.RegisterImage(ctx, &providerv1.RegisterRequest{
		ProviderRef:        done.ProviderRef,
		ImageName:          "ubuntu-template",
		ProviderConfigName: "example-config",
		Format:             "qcow2",
		Tags:               map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("RegisterImage returned error: %v", err)
	}
	if ref.Id != "img-uploaded/example-config/qcow2" || ref.Tags["team"] != "platform" {
		t.Fatalf("image ref = %#v", ref)
	}

	deleted, err := client.DeleteArtifact(ctx, &providerv1.DeleteRequest{ProviderRef: done.ProviderRef})
	if err != nil {
		t.Fatalf("DeleteArtifact returned error: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("delete response = %#v", deleted)
	}

	cleaned, err := client.CleanupRemoteBuild(ctx, &providerv1.RemoteBuildRequest{
		BuildId:            "build-123",
		OperationRef:       "provider://operation/123",
		ProviderConfigName: "example-config",
		SourceProviderRef:  "ami-0123456789abcdef0",
	})
	if err != nil {
		t.Fatalf("CleanupRemoteBuild returned error: %v", err)
	}
	if !cleaned.Cleaned || provider.remoteCleanup.BuildID != "build-123" || provider.remoteCleanup.OperationRef != "provider://operation/123" {
		t.Fatalf("cleanup response = %#v input = %#v", cleaned, provider.remoteCleanup)
	}
	if provider.remoteCleanup.SourceProviderRef != "ami-0123456789abcdef0" {
		t.Fatalf("source provider ref = %q", provider.remoteCleanup.SourceProviderRef)
	}

	remote, err := client.ReconcileRemoteBuild(ctx, &providerv1.RemoteBuildRequest{
		BuildId:            "build-123",
		OperationRef:       "provider://operation/123",
		ProviderConfigName: "example-config",
		SourceProviderRef:  "ami-0123456789abcdef0",
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if remote.Hygiene == nil || remote.Hygiene.Status != "passed" {
		t.Fatalf("remote hygiene = %#v, want passed", remote.Hygiene)
	}
	if remote.Hygiene.ResultRef != "provider://hygiene/report-1" {
		t.Fatalf("remote hygiene resultRef = %q", remote.Hygiene.ResultRef)
	}
}

func startServer(t *testing.T, provider sdk.Provider) (providerv1.PlatformProviderClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server, err := sdk.NewServer(provider)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	grpcServer := grpc.NewServer()
	server.Register(grpcServer)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	return providerv1.NewPlatformProviderClient(conn), func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	}
}

func TestServerOptionsFromEnv_DisabledByDefault(t *testing.T) {
	t.Setenv("PROVIDER_GRPC_TLS_MODE", "")

	opts, err := sdk.ServerOptionsFromEnv()
	if err != nil {
		t.Fatalf("ServerOptionsFromEnv returned error: %v", err)
	}
	if opts != nil {
		t.Fatalf("ServerOptionsFromEnv returned %d options, want nil", len(opts))
	}
}

func TestServerOptionsFromEnv_RejectsIncompleteMutualTLS(t *testing.T) {
	t.Setenv("PROVIDER_GRPC_TLS_MODE", "Mutual")

	_, err := sdk.ServerOptionsFromEnv()
	if err == nil || !strings.Contains(err.Error(), "required for mTLS") {
		t.Fatalf("ServerOptionsFromEnv error = %v, want missing file error", err)
	}
}

func TestServerOptionsFromEnv_LoadsMutualTLSCredentials(t *testing.T) {
	certPEM, keyPEM := testCertificatePEM(t)
	dir := t.TempDir()
	certFile := dir + "/tls.crt"
	keyFile := dir + "/tls.key"
	caFile := dir + "/ca.crt"
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	t.Setenv("PROVIDER_GRPC_TLS_MODE", "Mutual")
	t.Setenv("PROVIDER_GRPC_TLS_CERT_FILE", certFile)
	t.Setenv("PROVIDER_GRPC_TLS_KEY_FILE", keyFile)
	t.Setenv("PROVIDER_GRPC_TLS_CLIENT_CA_FILE", caFile)

	opts, err := sdk.ServerOptionsFromEnv()
	if err != nil {
		t.Fatalf("ServerOptionsFromEnv returned error: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("ServerOptionsFromEnv returned %d options, want 1", len(opts))
	}
}

func testCertificatePEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "provider-test",
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"provider-test.default.svc"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

type fakeProvider struct {
	config        sdk.Config
	uploaded      bytes.Buffer
	remoteCleanup sdk.RemoteBuildInput
}

func (p *fakeProvider) Capabilities(context.Context) (sdk.Capabilities, error) {
	return sdk.Capabilities{
		ProviderName:    "example",
		ProviderVersion: "v0.1.0",
		Formats:         []string{"qcow2"},
		OSFamilies:      []string{"linux"},
	}, nil
}

func (p *fakeProvider) ValidateConfig(_ context.Context, config sdk.Config) error {
	p.config = config
	return nil
}

func (p *fakeProvider) UploadArtifact(ctx context.Context, artifact sdk.ArtifactInfo, body io.Reader, progress sdk.ProgressReporter) (sdk.UploadResult, error) {
	if _, err := io.Copy(&p.uploaded, body); err != nil {
		return sdk.UploadResult{}, err
	}
	if err := progress.Report(ctx, sdk.Progress{BytesWritten: artifact.TotalSizeBytes, TotalBytes: artifact.TotalSizeBytes, Phase: "verifying"}); err != nil {
		return sdk.UploadResult{}, err
	}
	return sdk.UploadResult{ProviderRef: "uploaded/" + artifact.ProviderConfigName + "/" + artifact.Format}, nil
}

func (p *fakeProvider) RegisterImage(_ context.Context, input sdk.RegisterInput) (sdk.ImageRef, error) {
	return sdk.ImageRef{ID: "img-" + input.ProviderRef, Name: input.ImageName, Tags: input.Tags}, nil
}

func (p *fakeProvider) DeleteArtifact(context.Context, sdk.DeleteInput) (bool, string, error) {
	return true, "deleted", nil
}

func (p *fakeProvider) HealthCheck(context.Context) (string, error) {
	return "ok", nil
}

func (p *fakeProvider) ReconcileRemoteBuild(context.Context, sdk.RemoteBuildInput) (sdk.RemoteBuildResult, error) {
	return sdk.RemoteBuildResult{
		OperationRef: "provider://operation/123",
		Phase:        "Ready",
		Done:         true,
		Hygiene: &sdk.RemoteHygieneResult{
			Status:    "passed",
			Message:   "bootstrap residue absent",
			Checks:    []string{"temporary-user-removed", "bootstrap-files-removed"},
			ResultRef: "provider://hygiene/report-1",
		},
	}, nil
}

func (p *fakeProvider) CleanupRemoteBuild(_ context.Context, input sdk.RemoteBuildInput) (sdk.RemoteBuildCleanupResult, error) {
	p.remoteCleanup = input
	return sdk.RemoteBuildCleanupResult{Cleaned: true, Message: "cleaned"}, nil
}
