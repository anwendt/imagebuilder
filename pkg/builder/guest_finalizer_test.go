package builder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

func TestRemoteGuestFinalizer_ShutsDownLinuxOverSSH(t *testing.T) {
	runner := &recordingFinalizerSSHRunner{}
	finalizer := RemoteGuestFinalizer{SSHRunner: runner}

	err := finalizer.Finalize(context.Background(), GuestFinalizationRequest{
		WorkspaceDir: t.TempDir(),
		GuestAccess: GuestAccess{
			Protocol:   "ssh",
			Host:       "127.0.0.1",
			HostPort:   2222,
			User:       "imagebuilder",
			SSHKeyPath: writeTestKey(t),
		},
	})
	if err != nil {
		t.Fatalf("Finalize returned error: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if !testContainsString(runner.commands[0].Args, linuxShutdownScript()) {
		t.Fatalf("ssh args should contain linux shutdown script: %#v", runner.commands[0].Args)
	}
}

func TestRemoteGuestFinalizer_ShutsDownWindowsOverWinRM(t *testing.T) {
	winrm := &recordingFinalizerWinRM{}
	finalizer := RemoteGuestFinalizer{WinRM: winrm}

	err := finalizer.Finalize(context.Background(), GuestFinalizationRequest{
		GuestAccess: GuestAccess{
			Protocol:     "winrm",
			Host:         "127.0.0.1",
			HostPort:     55986,
			User:         "Administrator",
			PasswordPath: "/workspace/password",
			WinRMHTTPS:   true,
		},
	})
	if err != nil {
		t.Fatalf("Finalize returned error: %v", err)
	}
	if len(winrm.scripts) != 1 || !strings.Contains(winrm.scripts[0], "shutdown.exe") {
		t.Fatalf("winrm scripts = %#v", winrm.scripts)
	}
}

func TestRemoteGuestFinalizer_SkipsWindowsShutdownWhenSysprepAlreadyShutsDown(t *testing.T) {
	winrm := &recordingFinalizerWinRM{}
	finalizer := RemoteGuestFinalizer{WinRM: winrm}

	err := finalizer.Finalize(context.Background(), GuestFinalizationRequest{
		SysprepShutdown: true,
		GuestAccess: GuestAccess{
			Protocol: "winrm",
		},
	})
	if err != nil {
		t.Fatalf("Finalize returned error: %v", err)
	}
	if len(winrm.scripts) != 0 {
		t.Fatalf("winrm should not be called when sysprep already shuts down: %#v", winrm.scripts)
	}
}

type recordingFinalizerSSHRunner struct {
	commands []sshutil.Command
}

func (r *recordingFinalizerSSHRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.commands = append(r.commands, cmd)
	return nil
}

type recordingFinalizerWinRM struct {
	scripts []string
}

func (r *recordingFinalizerWinRM) ExecutePowerShell(_ context.Context, _ winrmexec.Access, script string) error {
	r.scripts = append(r.scripts, script)
	return nil
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, []byte("test-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func testContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
