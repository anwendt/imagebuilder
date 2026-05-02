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

func TestRemoteGuestCredentialSanitizer_SSHLocksAndDeletesGeneratedUser(t *testing.T) {
	runner := &recordingSSHRunner{}
	sanitizer := RemoteGuestCredentialSanitizer{SSHRunner: runner}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	err := sanitizer.Sanitize(context.Background(), GuestAccess{
		Protocol:   "ssh",
		Host:       "127.0.0.1",
		HostPort:   2222,
		User:       "imagebuilder",
		SSHKeyPath: keyPath,
	}, GeneratedGuestCredentials{PublicKey: "ssh-ed25519 test"}, t.TempDir())
	if err != nil {
		t.Fatalf("Sanitize returned error: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	command := strings.Join(runner.commands[0].Args, " ")
	for _, want := range []string{"passwd -l", "userdel -r", "authorized_keys"} {
		if !strings.Contains(command, want) {
			t.Fatalf("sanitize command missing %q: %s", want, command)
		}
	}
}

func TestRemoteGuestCredentialSanitizer_WinRMRotatesPasswordAndDisablesBasic(t *testing.T) {
	executor := &recordingWinRMExecutor{}
	sanitizer := RemoteGuestCredentialSanitizer{WinRM: executor}

	err := sanitizer.Sanitize(context.Background(), GuestAccess{
		Protocol:     "winrm",
		Host:         "127.0.0.1",
		HostPort:     55986,
		User:         "Administrator",
		PasswordPath: "/credentials/generated/guest-credentials/password",
		WinRMHTTPS:   true,
	}, GeneratedGuestCredentials{Password: "do-not-log-this-password"}, "/workspace")
	if err != nil {
		t.Fatalf("Sanitize returned error: %v", err)
	}
	if len(executor.scripts) != 1 {
		t.Fatalf("scripts = %#v", executor.scripts)
	}
	script := executor.scripts[0]
	for _, want := range []string{"Set-LocalUser", "RandomNumberGenerator", "Basic -Value $false"} {
		if !strings.Contains(script, want) {
			t.Fatalf("sanitize script missing %q: %s", want, script)
		}
	}
	if strings.Contains(script, "do-not-log-this-password") {
		t.Fatal("sanitize script must not contain the old generated password")
	}
}

type recordingSSHRunner struct {
	commands []sshutil.Command
}

func (r *recordingSSHRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.commands = append(r.commands, cmd)
	return nil
}

type recordingWinRMExecutor struct {
	scripts []string
}

func (e *recordingWinRMExecutor) ExecutePowerShell(_ context.Context, _ winrmexec.Access, script string) error {
	e.scripts = append(e.scripts, script)
	return nil
}
