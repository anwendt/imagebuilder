package shell_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/shell"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

func TestProvisioner_Run_ExecutesSSHWithHardenedOptions(t *testing.T) {
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	runner := &recordingShellRunner{}
	p := shell.Provisioner{Runner: runner}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "imagebuilder",
		SSHPort:      2222,
		SSHKeyPath:   keyPath,
		Spec: v1alpha1.ProvisionerSpec{
			Type:   "shell",
			Inline: "sudo systemctl enable nginx",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.command.Name != "ssh" {
		t.Fatalf("command = %#v", runner.command)
	}
	if !containsString(runner.command.Args, "BatchMode=yes") ||
		!containsString(runner.command.Args, "PasswordAuthentication=no") ||
		!containsString(runner.command.Args, "StrictHostKeyChecking=accept-new") ||
		!containsString(runner.command.Args, "imagebuilder@127.0.0.1") ||
		!containsString(runner.command.Args, "sudo systemctl enable nginx") {
		t.Fatalf("ssh args = %#v", runner.command.Args)
	}
}

func TestProvisioner_Run_RejectsWorldReadableKey(t *testing.T) {
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	p := shell.Provisioner{Runner: &recordingShellRunner{}}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "imagebuilder",
		SSHPort:      2222,
		SSHKeyPath:   keyPath,
		Spec:         v1alpha1.ProvisionerSpec{Type: "shell", Inline: "echo ok"},
	}); err == nil {
		t.Fatal("Run should reject world-readable private key")
	}
}

type recordingShellRunner struct {
	command sshutil.Command
}

func (r *recordingShellRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.command = cmd
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
