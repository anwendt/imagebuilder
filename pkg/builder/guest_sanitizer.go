package builder

import (
	"context"
	"fmt"
	"strings"

	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

type GuestCredentialSanitizer interface {
	Sanitize(ctx context.Context, access GuestAccess, creds GeneratedGuestCredentials, workspaceDir string) error
}

type RemoteGuestCredentialSanitizer struct {
	SSHRunner sshutil.Runner
	WinRM     winrmexec.Executor
}

func (s RemoteGuestCredentialSanitizer) Sanitize(ctx context.Context, access GuestAccess, creds GeneratedGuestCredentials, workspaceDir string) error {
	if creds.PublicKey == "" && creds.Password == "" {
		return nil
	}
	switch access.Protocol {
	case guestProtocolSSH:
		return s.sanitizeSSH(ctx, access, workspaceDir)
	case guestProtocolWinRM:
		return s.sanitizeWinRM(ctx, access)
	default:
		return fmt.Errorf("sanitize guest credentials: unsupported protocol %q", access.Protocol)
	}
}

func (s RemoteGuestCredentialSanitizer) sanitizeSSH(ctx context.Context, access GuestAccess, workspaceDir string) error {
	args, err := sshutil.SSHArgs(sshutil.Access{
		WorkspaceDir: workspaceDir,
		Address:      access.Host,
		User:         access.User,
		Port:         int(access.HostPort),
		KeyPath:      access.SSHKeyPath,
	}, linuxSanitizeScript(access.User))
	if err != nil {
		return err
	}
	runner := s.SSHRunner
	if runner == nil {
		runner = sshutil.ExecRunner{}
	}
	if err := runner.Run(ctx, sshutil.Command{Name: "ssh", Args: args, Dir: workspaceDir}); err != nil {
		return fmt.Errorf("sanitize ssh guest credentials: %w", err)
	}
	return nil
}

func (s RemoteGuestCredentialSanitizer) sanitizeWinRM(ctx context.Context, access GuestAccess) error {
	executor := s.WinRM
	if executor == nil {
		executor = winrmexec.Client{}
	}
	if err := executor.ExecutePowerShell(ctx, winrmexec.Access{
		Host:               access.Host,
		Port:               int(access.HostPort),
		User:               access.User,
		PasswordPath:       access.PasswordPath,
		HTTPS:              access.WinRMHTTPS,
		InsecureSkipVerify: access.InsecureSkipVerify,
	}, windowsSanitizeScript(access.User)); err != nil {
		return fmt.Errorf("sanitize winrm guest credentials: %w", err)
	}
	return nil
}

func linuxSanitizeScript(user string) string {
	quotedUser := shellQuote(user)
	return "set -eu; user=" + quotedUser + "; " +
		"if command -v sudo >/dev/null 2>&1; then s='sudo'; else s=''; fi; " +
		"$s sh -c 'u=\"$1\"; home=\"\"; if getent passwd \"$u\" >/dev/null 2>&1; then home=$(getent passwd \"$u\" | cut -d: -f6); passwd -l \"$u\" >/dev/null 2>&1 || true; if [ \"$u\" != \"root\" ]; then userdel -r \"$u\" >/dev/null 2>&1 || true; fi; fi; if [ -n \"$home\" ]; then rm -f \"$home/.ssh/authorized_keys\"; fi' sh \"$user\""
}

func windowsSanitizeScript(user string) string {
	escapedUser := strings.ReplaceAll(user, "'", "''")
	return "$ErrorActionPreference = 'Stop'; " +
		"$user = '" + escapedUser + "'; " +
		"$bytes = New-Object byte[] 32; " +
		"[System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes); " +
		"$rotated = [Convert]::ToBase64String($bytes) + '!aA1'; " +
		"if (Get-Command Set-LocalUser -ErrorAction SilentlyContinue) { " +
		"$secure = ConvertTo-SecureString $rotated -AsPlainText -Force; " +
		"Set-LocalUser -Name $user -Password $secure; " +
		"if ($user -ne 'Administrator') { Disable-LocalUser -Name $user } " +
		"} else { net user $user $rotated | Out-Null }; " +
		"Set-Item -Path WSMan:\\localhost\\Service\\Auth\\Basic -Value $false -ErrorAction SilentlyContinue"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
