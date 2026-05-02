package custom_test

import (
	"context"
	"os"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/custom"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

func TestProvisioner_Run_ExecutesSSHCommand(t *testing.T) {
	runner := &recordingRunner{}
	p := custom.Provisioner{Runner: runner}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: t.TempDir(),
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "imagebuilder",
		SSHPort:      2222,
		SSHKeyPath:   privateKey(t),
		Spec:         v1alpha1.ProvisionerSpec{Type: "custom", Inline: "echo ok"},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.command.Name != "ssh" || !containsString(runner.command.Args, "echo ok") {
		t.Fatalf("command = %#v", runner.command)
	}
}

func TestProvisioner_Run_ExecutesWinRMCommand(t *testing.T) {
	winrm := &recordingWinRM{}
	p := custom.Provisioner{WinRM: winrm}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir:   "/workspace",
		Protocol:       "winrm",
		VMAddress:      "127.0.0.1",
		VMUser:         "Administrator",
		SSHPort:        55986,
		VMPasswordPath: "/workspace/password",
		WinRMHTTPS:     true,
		Spec:           v1alpha1.ProvisionerSpec{Type: "custom", Inline: "Write-Host ok"},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if winrm.script != "Write-Host ok" || winrm.access.User != "Administrator" {
		t.Fatalf("winrm = %#v script=%q", winrm.access, winrm.script)
	}
}

type recordingRunner struct {
	command sshutil.Command
}

func (r *recordingRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.command = cmd
	return nil
}

type recordingWinRM struct {
	access winrmexec.Access
	script string
}

func (r *recordingWinRM) ExecutePowerShell(_ context.Context, access winrmexec.Access, script string) error {
	r.access = access
	r.script = script
	return nil
}

func privateKey(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/id_ed25519"
	if err := os.WriteFile(path, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
