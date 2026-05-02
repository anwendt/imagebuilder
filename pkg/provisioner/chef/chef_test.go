package chef_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/chef"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

func TestProvisioner_Run_UploadsInlineRecipeAndRunsChefApply(t *testing.T) {
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	runner := &recordingRunner{}
	p := chef.Provisioner{Runner: runner}

	result, err := p.Run(context.Background(), request(workspace, keyPath, v1alpha1.ProvisionerSpec{
		Type:   "chef",
		Inline: "package 'nginx'",
	}))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(runner.commands) != 2 || runner.commands[0].Name != "scp" || runner.commands[1].Name != "ssh" {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if !containsString(runner.commands[1].Args, "sudo chef-apply /tmp/imagebuilder-chef-recipe.rb") {
		t.Fatalf("ssh args = %#v", runner.commands[1].Args)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
}

type recordingRunner struct {
	commands []sshutil.Command
}

func (r *recordingRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.commands = append(r.commands, cmd)
	return nil
}

func request(workspace, keyPath string, spec v1alpha1.ProvisionerSpec) *provisioner.RunRequest {
	return &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "imagebuilder",
		SSHPort:      2222,
		SSHKeyPath:   keyPath,
		Spec:         spec,
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
