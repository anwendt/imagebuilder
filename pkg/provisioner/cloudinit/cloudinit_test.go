package cloudinit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/cloudinit"
)

func TestProvisioner_Run_WritesCloudInitSeed(t *testing.T) {
	workspace := t.TempDir()
	p := cloudinit.Provisioner{}

	result, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Spec: v1alpha1.ProvisionerSpec{
			Type:   "cloud-init",
			Inline: "#cloud-config\npackages: [nginx]\n",
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	userDataPath := filepath.Join(workspace, "cloud-init", "user-data")
	got, err := os.ReadFile(userDataPath)
	if err != nil {
		t.Fatalf("read user-data: %v", err)
	}
	if string(got) != "#cloud-config\npackages: [nginx]\n" {
		t.Fatalf("user-data = %q", got)
	}
	info, err := os.Stat(userDataPath)
	if err != nil {
		t.Fatalf("stat user-data: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("user-data mode = %v, want 0600", info.Mode().Perm())
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
}
