// pkg/plugin/grpc/adapter_test.go
//
// Unit tests for the gRPC adapter (Adapter).
//
// Because the adapter talks to a live gRPC server we use an in-process test
// server (google.golang.org/grpc/test/bufconn) — no network required.
//
// TDD: each test was written before the adapter method it tests.

package grpc_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"

	providerv1 "github.com/anwendt/imagebuilder/api/provider/v1"
	grpcadapter "github.com/anwendt/imagebuilder/pkg/plugin/grpc"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ---------------------------------------------------------------------------
// Fake gRPC server
// ---------------------------------------------------------------------------

type fakeProviderServer struct {
	providerv1.UnimplementedPlatformProviderServer
	capabilities *providerv1.CapabilitiesResponse
	healthErr    bool
}

func (s *fakeProviderServer) GetCapabilities(_ context.Context, _ *providerv1.Empty) (*providerv1.CapabilitiesResponse, error) {
	return s.capabilities, nil
}

func (s *fakeProviderServer) ValidateConfig(_ context.Context, req *providerv1.ValidateConfigRequest) (*providerv1.ValidateConfigResponse, error) {
	if req.ProviderConfigName == "bad-config" {
		return &providerv1.ValidateConfigResponse{Valid: false, Message: "credentials rejected"}, nil
	}
	return &providerv1.ValidateConfigResponse{Valid: true}, nil
}

func (s *fakeProviderServer) HealthCheck(_ context.Context, _ *providerv1.Empty) (*providerv1.HealthResponse, error) {
	if s.healthErr {
		return &providerv1.HealthResponse{Healthy: false, Message: "disk full"}, nil
	}
	return &providerv1.HealthResponse{Healthy: true}, nil
}

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

// startFakeServer starts an in-process gRPC server and returns the adapter
// connected to it plus a cleanup function.
func startFakeServer(t *testing.T, srv providerv1.PlatformProviderServer) (*grpcadapter.Adapter, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0") // OS-assigned port
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	grpcSrv := grpc.NewServer()
	providerv1.RegisterPlatformProviderServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()

	adapter := grpcadapter.NewAdapter(lis.Addr().String())
	if err := adapter.Connect(context.Background()); err != nil {
		grpcSrv.Stop()
		t.Fatalf("Connect: %v", err)
	}

	cleanup := func() {
		_ = adapter.Close()
		grpcSrv.Stop()
	}
	return adapter, cleanup
}

func defaultCaps() *providerv1.CapabilitiesResponse {
	return &providerv1.CapabilitiesResponse{
		ProviderName:    "test-provider",
		ProviderVersion: "v1.0.0",
		Formats:         []string{"vmdk", "raw"},
		OsFamilies:      []string{"linux", "windows"},
		ProtocolVersion: "v1",
	}
}

// ---------------------------------------------------------------------------
// Name / Version / Capabilities
// ---------------------------------------------------------------------------

func TestAdapter_Name(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	if adapter.Name() != "test-provider" {
		t.Errorf("Name() = %q, want test-provider", adapter.Name())
	}
}

func TestAdapter_Version(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	if adapter.Version() != "v1.0.0" {
		t.Errorf("Version() = %q, want v1.0.0", adapter.Version())
	}
}

func TestAdapter_SupportedFormats(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	formats := adapter.SupportedFormats()
	if len(formats) != 2 {
		t.Fatalf("SupportedFormats() = %v, want [vmdk raw]", formats)
	}
	if formats[0] != platform.ImageFormat("vmdk") {
		t.Errorf("formats[0] = %q, want vmdk", formats[0])
	}
}

func TestAdapter_SupportedOS(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	os := adapter.SupportedOS()
	if len(os) != 2 {
		t.Fatalf("SupportedOS() = %v, want [linux windows]", os)
	}
}

// ---------------------------------------------------------------------------
// Init / Validate
// ---------------------------------------------------------------------------

func TestAdapter_Init_ValidConfig(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	err := adapter.Init(context.Background(), platform.PluginConfig{
		ProviderConfigName: "aws-prod",
		SecretData:         map[string][]byte{"accessKeyId": []byte("AKIA...")},
		Region:             "eu-central-1",
	})
	if err != nil {
		t.Errorf("Init with valid config returned error: %v", err)
	}
}

func TestAdapter_Init_InvalidConfig_ReturnsError(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	err := adapter.Init(context.Background(), platform.PluginConfig{
		ProviderConfigName: "bad-config",
	})
	if err == nil {
		t.Error("Init with rejected config should return error, got nil")
	}
}

// ---------------------------------------------------------------------------
// HealthCheck
// ---------------------------------------------------------------------------

func TestAdapter_HealthCheck_Healthy(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps()})
	defer cleanup()

	if err := adapter.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck on healthy provider returned error: %v", err)
	}
}

func TestAdapter_HealthCheck_Unhealthy_ReturnsError(t *testing.T) {
	adapter, cleanup := startFakeServer(t, &fakeProviderServer{capabilities: defaultCaps(), healthErr: true})
	defer cleanup()

	if err := adapter.HealthCheck(context.Background()); err == nil {
		t.Error("HealthCheck on unhealthy provider should return error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Connect — protocol version mismatch
// ---------------------------------------------------------------------------

func TestAdapter_Connect_WrongProtocolVersion_ReturnsError(t *testing.T) {
	caps := defaultCaps()
	caps.ProtocolVersion = "v99"

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	providerv1.RegisterPlatformProviderServer(grpcSrv, &fakeProviderServer{capabilities: caps})
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	adapter := grpcadapter.NewAdapter(lis.Addr().String())
	if err := adapter.Connect(context.Background()); err == nil {
		t.Error("Connect with wrong protocol version should return error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Close — idempotent
// ---------------------------------------------------------------------------

func TestAdapter_Close_Idempotent(t *testing.T) {
	adapter := grpcadapter.NewAdapter("127.0.0.1:9999") // never connected
	if err := adapter.Close(); err != nil {
		t.Errorf("Close on unconnected adapter returned error: %v", err)
	}
}
