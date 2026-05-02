// pkg/plugin/grpc/adapter_streaming_test.go
//
// Tests for the streaming RPCs of the gRPC adapter:
// Upload (client+server streaming), Register, Cleanup, Validate.
//
// The fake server implements UploadArtifact by reading all chunks and
// returning a final UploadProgress with provider_ref set.

package grpc_test

import (
	"context"
	"io"
	"net"
	"os"
	"testing"

	"google.golang.org/grpc"

	providerv1 "github.com/anwendt/imagebuilder/api/provider/v1"
	"github.com/anwendt/imagebuilder/api/v1alpha1"
	grpcadapter "github.com/anwendt/imagebuilder/pkg/plugin/grpc"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

// ---------------------------------------------------------------------------
// Extended fake server with streaming support
// ---------------------------------------------------------------------------

type streamingFakeServer struct {
	providerv1.UnimplementedPlatformProviderServer
	uploadRef        string // provider_ref to return in final UploadProgress
	registerID       string // image ID to return in RegisterImage
	deleteOK         bool
	remoteCleanupReq *providerv1.RemoteBuildRequest
}

func (s *streamingFakeServer) GetCapabilities(_ context.Context, _ *providerv1.Empty) (*providerv1.CapabilitiesResponse, error) {
	return &providerv1.CapabilitiesResponse{
		ProviderName:    "stream-provider",
		ProviderVersion: "v1.0.0",
		Formats:         []string{"vmdk"},
		OsFamilies:      []string{"linux"},
		ProtocolVersion: "v1",
		BuildModes:      []string{"local", "remote"},
	}, nil
}

func (s *streamingFakeServer) ValidateConfig(_ context.Context, req *providerv1.ValidateConfigRequest) (*providerv1.ValidateConfigResponse, error) {
	if req.ProviderConfigName == "bad" {
		return &providerv1.ValidateConfigResponse{Valid: false, Message: "bad config"}, nil
	}
	return &providerv1.ValidateConfigResponse{Valid: true}, nil
}

// UploadArtifact reads all chunks, then sends progress updates and a final
// message with provider_ref set.
func (s *streamingFakeServer) UploadArtifact(stream providerv1.PlatformProvider_UploadArtifactServer) error {
	var totalBytes int64
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		totalBytes += int64(len(chunk.Data))
		if chunk.Last {
			break
		}
	}
	// Send one progress message, then the final one with provider_ref.
	_ = stream.Send(&providerv1.UploadProgress{
		BytesWritten: totalBytes,
		TotalBytes:   totalBytes,
		Phase:        "uploading",
	})
	return stream.Send(&providerv1.UploadProgress{
		BytesWritten: totalBytes,
		TotalBytes:   totalBytes,
		Phase:        "done",
		ProviderRef:  s.uploadRef,
	})
}

func (s *streamingFakeServer) RegisterImage(_ context.Context, req *providerv1.RegisterRequest) (*providerv1.ImageRef, error) {
	return &providerv1.ImageRef{
		Id:       s.registerID,
		Name:     req.ImageName,
		Location: "eu-central-1",
	}, nil
}

func (s *streamingFakeServer) DeleteArtifact(_ context.Context, _ *providerv1.DeleteRequest) (*providerv1.DeleteResponse, error) {
	return &providerv1.DeleteResponse{Deleted: s.deleteOK}, nil
}

func (s *streamingFakeServer) CleanupRemoteBuild(_ context.Context, req *providerv1.RemoteBuildRequest) (*providerv1.RemoteBuildCleanupResponse, error) {
	s.remoteCleanupReq = req
	return &providerv1.RemoteBuildCleanupResponse{Cleaned: true, Message: "cleaned"}, nil
}

func (s *streamingFakeServer) ReconcileRemoteBuild(_ context.Context, _ *providerv1.RemoteBuildRequest) (*providerv1.RemoteBuildResponse, error) {
	return &providerv1.RemoteBuildResponse{
		OperationRef: "provider://operation/123",
		Phase:        string(platform.RemoteBuildPhaseReady),
		Message:      "registered",
		Done:         true,
		Hygiene: &providerv1.RemoteHygieneResult{
			Status:    "passed",
			Message:   "bootstrap residue absent",
			Checks:    []string{"temporary-user-removed", "bootstrap-files-removed"},
			ResultRef: "provider://hygiene/report-1",
		},
		Images: []*providerv1.RemoteImageRef{
			{
				Provider:           "stream-provider",
				ProviderConfigName: "aws-prod",
				Format:             "ami",
				ImageRef:           "ami-123",
				Location:           "eu-central-1",
			},
		},
	}, nil
}

func (s *streamingFakeServer) HealthCheck(_ context.Context, _ *providerv1.Empty) (*providerv1.HealthResponse, error) {
	return &providerv1.HealthResponse{Healthy: true}, nil
}

// startStreamingServer starts a fake server with streaming support.
func startStreamingServer(t *testing.T, srv providerv1.PlatformProviderServer) *grpcadapter.Adapter {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	providerv1.RegisterPlatformProviderServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() { grpcSrv.Stop() })

	adapter := grpcadapter.NewAdapter(lis.Addr().String())
	if err := adapter.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestAdapter_Validate_ValidConfig(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{uploadRef: "s3-key", registerID: "ami-123", deleteOK: true})

	spec := v1alpha1.TargetSpec{
		ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "good-config"},
		Format:            "vmdk",
	}
	if err := adapter.Validate(context.Background(), spec); err != nil {
		t.Errorf("Validate with valid config returned error: %v", err)
	}
}

func TestAdapter_Validate_InvalidConfig_ReturnsError(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{})

	spec := v1alpha1.TargetSpec{
		ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "bad"},
		Format:            "vmdk",
	}
	if err := adapter.Validate(context.Background(), spec); err == nil {
		t.Error("Validate with bad config should return error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Upload
// ---------------------------------------------------------------------------

func TestAdapter_Upload_ReturnsProviderRef(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{uploadRef: "imagebuilder/build-123/disk.vmdk", registerID: "ami-123"})

	// Write a small temp file to act as the artifact.
	f, err := os.CreateTemp(t.TempDir(), "artifact-*.vmdk")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString("fake-vmdk-data"); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()

	artifact := &platform.BuildArtifact{
		Path:      f.Name(),
		Format:    platform.FormatVMDK,
		Checksum:  "sha256:abc",
		SizeBytes: 14,
		OS:        platform.OSFamilyLinux,
		Metadata:  map[string]string{"buildID": "build-123"},
	}

	result, err := adapter.Upload(context.Background(), artifact)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.ProviderRef != "imagebuilder/build-123/disk.vmdk" {
		t.Errorf("ProviderRef = %q, want imagebuilder/build-123/disk.vmdk", result.ProviderRef)
	}
}

func TestAdapter_Upload_MissingProviderRef_ReturnsError(t *testing.T) {
	// Server returns UploadProgress with empty provider_ref
	adapter := startStreamingServer(t, &streamingFakeServer{uploadRef: ""})

	f, err := os.CreateTemp(t.TempDir(), "artifact-*.vmdk")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.WriteString("data") //nolint:errcheck
	f.Close()

	artifact := &platform.BuildArtifact{
		Path:     f.Name(),
		Format:   platform.FormatVMDK,
		Metadata: map[string]string{},
	}

	_, err = adapter.Upload(context.Background(), artifact)
	if err == nil {
		t.Error("Upload should return error when server sends no provider_ref")
	}
}

// ---------------------------------------------------------------------------
// Register
// ---------------------------------------------------------------------------

func TestAdapter_Register_ReturnsImageRef(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{registerID: "ami-abc123"})

	result, err := adapter.Register(context.Background(), &platform.UploadResult{
		ProviderRef: "s3-key",
		Metadata: map[string]string{
			"imageName":          "ubuntu-2404",
			"providerConfigName": "aws-prod",
		},
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if result.ID != "ami-abc123" {
		t.Errorf("ImageRef.ID = %q, want ami-abc123", result.ID)
	}
	if result.Location != "eu-central-1" {
		t.Errorf("ImageRef.Location = %q, want eu-central-1", result.Location)
	}
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

func TestAdapter_Cleanup_WithProviderRef(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{deleteOK: true})

	artifact := &platform.BuildArtifact{
		Format: platform.FormatVMDK,
		Metadata: map[string]string{
			"providerRef":        "s3-key",
			"providerConfigName": "aws-prod",
		},
	}
	if err := adapter.Cleanup(context.Background(), artifact); err != nil {
		t.Errorf("Cleanup returned error: %v", err)
	}
}

func TestAdapter_Cleanup_NoProviderRef_Noop(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{})

	// Empty metadata → no provider_ref → Cleanup is a no-op
	artifact := &platform.BuildArtifact{
		Format:   platform.FormatVMDK,
		Metadata: map[string]string{},
	}
	if err := adapter.Cleanup(context.Background(), artifact); err != nil {
		t.Errorf("Cleanup with no provider_ref should be a no-op, got: %v", err)
	}
}

func TestAdapter_CleanupRemoteBuild_ForwardsOperationRef(t *testing.T) {
	server := &streamingFakeServer{}
	adapter := startStreamingServer(t, server)

	err := adapter.CleanupRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID:           "build-123",
		OperationRef:      "provider://operation/123",
		ImageName:         "ubuntu-remote",
		Namespace:         "default",
		SourceProviderRef: "ami-0123456789abcdef0",
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-prod"},
			Format:            "ami",
		},
	})
	if err != nil {
		t.Fatalf("CleanupRemoteBuild returned error: %v", err)
	}
	if server.remoteCleanupReq == nil {
		t.Fatal("CleanupRemoteBuild was not called on server")
	}
	if server.remoteCleanupReq.GetBuildId() != "build-123" || server.remoteCleanupReq.GetOperationRef() != "provider://operation/123" {
		t.Fatalf("remote cleanup request = %#v", server.remoteCleanupReq)
	}
	if server.remoteCleanupReq.GetSourceProviderRef() != "ami-0123456789abcdef0" {
		t.Fatalf("source provider ref = %q", server.remoteCleanupReq.GetSourceProviderRef())
	}
}

func TestAdapter_ReconcileRemoteBuild_MapsHygieneAttestation(t *testing.T) {
	adapter := startStreamingServer(t, &streamingFakeServer{})

	result, err := adapter.ReconcileRemoteBuild(context.Background(), &platform.RemoteBuildRequest{
		BuildID: "build-123",
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: "aws-prod"},
			Format:            "ami",
		},
	})
	if err != nil {
		t.Fatalf("ReconcileRemoteBuild returned error: %v", err)
	}
	if result.Hygiene == nil || result.Hygiene.Status != "passed" {
		t.Fatalf("Hygiene = %#v, want passed", result.Hygiene)
	}
	if result.Hygiene.ResultRef != "provider://hygiene/report-1" {
		t.Fatalf("Hygiene resultRef = %q, want provider://hygiene/report-1", result.Hygiene.ResultRef)
	}
	if len(result.Images) != 1 || result.Images[0].ImageRef.ID != "ami-123" {
		t.Fatalf("Images = %#v, want ami-123", result.Images)
	}
}
