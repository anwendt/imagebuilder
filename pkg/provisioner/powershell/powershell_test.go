package powershell_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/powershell"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

func TestProvisioner_Run_ExecutesEncodedPowerShellOverSSH(t *testing.T) {
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	runner := &recordingRunner{}
	p := powershell.Provisioner{Runner: runner}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "Administrator",
		SSHPort:      2222,
		SSHKeyPath:   keyPath,
		Spec: v1alpha1.ProvisionerSpec{
			Type:   "powershell",
			Inline: "Write-Host 'ok'",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.command.Name != "ssh" {
		t.Fatalf("command = %#v", runner.command)
	}
	if !containsPrefix(runner.command.Args, "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ") {
		t.Fatalf("ssh args = %#v", runner.command.Args)
	}
}

func TestEncodedCommand_UsesPowerShellEncoding(t *testing.T) {
	got := powershell.EncodedCommand("A")
	if !strings.HasSuffix(got, "QQA=") {
		t.Fatalf("encoded command = %q, want UTF-16LE base64 suffix QQA=", got)
	}
}

func TestProvisioner_Run_ExecutesPowerShellOverWinRM(t *testing.T) {
	winrm := &recordingWinRM{}
	p := powershell.Provisioner{WinRM: winrm}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir:   "/workspace",
		Protocol:       "winrm",
		VMAddress:      "127.0.0.1",
		VMUser:         "Administrator",
		SSHPort:        55986,
		VMPasswordPath: "/workspace/password",
		WinRMHTTPS:     true,
		Spec: v1alpha1.ProvisionerSpec{
			Type:   "powershell",
			Inline: "Write-Host ok",
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if winrm.script != "Write-Host ok" || winrm.access.User != "Administrator" || !winrm.access.HTTPS {
		t.Fatalf("winrm call = %#v script=%q", winrm.access, winrm.script)
	}
}

type recordingRunner struct {
	command sshutil.Command
}

func (r *recordingRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.command = cmd
	return nil
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
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
