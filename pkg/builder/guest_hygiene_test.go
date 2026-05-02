package builder

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

func TestRemoteGuestHygieneChecker_ChecksLinuxBootstrapResidue(t *testing.T) {
	workspace := t.TempDir()
	keyPath := workspace + "/id_ed25519"
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	runner := &recordingHygieneSSHRunner{}
	checker := RemoteGuestHygieneChecker{SSHRunner: runner}

	err := checker.Check(context.Background(), GuestHygieneRequest{
		GuestAccess: GuestAccess{
			Protocol:   guestProtocolSSH,
			Host:       "127.0.0.1",
			HostPort:   2222,
			User:       "imagebuilder",
			SSHKeyPath: keyPath,
		},
		WorkspaceDir:    workspace,
		GeneratedUser:   "imagebuilder",
		GeneratedSSHKey: true,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	cmd := strings.Join(runner.commands[0].Args, " ")
	for _, want := range []string{"/var/lib/cloud/seed/nocloud", "/var/lib/cloud/seed/nocloud-net", "getent passwd", "user:$user"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("linux hygiene command missing %q:\n%s", want, cmd)
		}
	}
}

func TestRemoteGuestHygieneChecker_ChecksWindowsBootstrapResidue(t *testing.T) {
	winrm := &recordingHygieneWinRM{}
	checker := RemoteGuestHygieneChecker{WinRM: winrm}

	err := checker.Check(context.Background(), GuestHygieneRequest{
		GuestAccess: GuestAccess{
			Protocol:           guestProtocolWinRM,
			Host:               "127.0.0.1",
			HostPort:           5986,
			User:               "imagebuilder",
			PasswordPath:       "/credentials/password",
			WinRMHTTPS:         true,
			InsecureSkipVerify: true,
		},
		GeneratedUser: "imagebuilder",
		GeneratedPass: true,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	for _, want := range []string{"Autounattend.xml", "AutoAdminLogon", "DefaultPassword", "WSMan:\\localhost\\Service\\Auth\\Basic", "user:"} {
		if !strings.Contains(winrm.script, want) {
			t.Fatalf("windows hygiene script missing %q:\n%s", want, winrm.script)
		}
	}
}

type recordingHygieneSSHRunner struct {
	commands []sshutil.Command
}

func (r *recordingHygieneSSHRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.commands = append(r.commands, cmd)
	return nil
}

type recordingHygieneWinRM struct {
	script string
}

func (r *recordingHygieneWinRM) ExecutePowerShell(_ context.Context, _ winrmexec.Access, script string) error {
	r.script = script
	return nil
}
