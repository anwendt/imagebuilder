package vsphere

import (
	"context"
	"os"
	"testing"

	"github.com/vmware/govmomi/simulator"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestGovmomiClientUploadCleanupWithSimulator(t *testing.T) {
	if os.Getenv("IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS") != "1" {
		t.Skip("set IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS=1 to run the govmomi simulator integration test")
	}
	ctx := context.Background()
	model := simulator.VPX()
	defer model.Remove()
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	server := model.Service.NewServer()
	defer server.Close()

	artifact, err := os.CreateTemp(t.TempDir(), "image-*.vmdk")
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if _, err := artifact.WriteString("disk"); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatalf("close artifact: %v", err)
	}

	plugin := &Plugin{}
	if err := plugin.Init(ctx, platform.PluginConfig{
		ProviderConfigName: "vsphere-sim",
		Endpoint:           server.URL.String(),
		SecretData: map[string][]byte{
			"username": []byte("user"),
			"password": []byte("pass"),
		},
		Extra: map[string]string{
			"datacenter":       "DC0",
			"datastore":        "LocalDS_0",
			"uploadPathPrefix": "imagebuilder-test",
		},
	}); err != nil {
		t.Fatalf("init plugin: %v", err)
	}
	if err := plugin.HealthCheck(ctx); err != nil {
		t.Fatalf("health check: %v", err)
	}
	buildArtifact := &platform.BuildArtifact{
		Path:     artifact.Name(),
		Format:   platform.FormatVMDK,
		Checksum: "sha256:abc123",
		Metadata: map[string]string{
			"buildID":   "sim-build",
			"imageName": "sim-template",
		},
	}
	result, err := plugin.Upload(ctx, buildArtifact)
	if err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
	if result.ProviderRef == "" {
		t.Fatal("upload returned empty provider reference")
	}
	ref, err := plugin.Register(ctx, result)
	if err != nil {
		t.Fatalf("register artifact: %v", err)
	}
	if ref.ID != result.ProviderRef {
		t.Fatalf("image ref ID = %q, want %q", ref.ID, result.ProviderRef)
	}
	if err := plugin.Cleanup(ctx, buildArtifact); err != nil {
		t.Fatalf("cleanup artifact: %v", err)
	}
}
