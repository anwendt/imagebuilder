package builder

import (
	"context"
	"fmt"
	"strings"

	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

type GuestHygieneRequest struct {
	OSFamily        string
	GuestAccess     GuestAccess
	WorkspaceDir    string
	GeneratedUser   string
	GeneratedSSHKey bool
	GeneratedPass   bool
}

type GuestHygieneChecker interface {
	Check(ctx context.Context, req GuestHygieneRequest) error
}

type RemoteGuestHygieneChecker struct {
	SSHRunner sshutil.Runner
	WinRM     winrmexec.Executor
}

func (c RemoteGuestHygieneChecker) Check(ctx context.Context, req GuestHygieneRequest) error {
	switch req.GuestAccess.Protocol {
	case guestProtocolSSH:
		return c.checkLinux(ctx, req)
	case guestProtocolWinRM:
		return c.checkWindows(ctx, req)
	default:
		return fmt.Errorf("check guest hygiene: unsupported protocol %q", req.GuestAccess.Protocol)
	}
}

func (c RemoteGuestHygieneChecker) checkLinux(ctx context.Context, req GuestHygieneRequest) error {
	args, err := sshutil.SSHArgs(sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.GuestAccess.Host,
		User:         req.GuestAccess.User,
		Port:         int(req.GuestAccess.HostPort),
		KeyPath:      req.GuestAccess.SSHKeyPath,
	}, linuxHygieneScript(req.GeneratedUser, req.GeneratedSSHKey || req.GeneratedPass))
	if err != nil {
		return err
	}
	runner := c.SSHRunner
	if runner == nil {
		runner = sshutil.ExecRunner{}
	}
	if err := runner.Run(ctx, sshutil.Command{Name: "ssh", Args: args, Dir: req.WorkspaceDir}); err != nil {
		return fmt.Errorf("check linux guest hygiene: %w", err)
	}
	return nil
}

func (c RemoteGuestHygieneChecker) checkWindows(ctx context.Context, req GuestHygieneRequest) error {
	executor := c.WinRM
	if executor == nil {
		executor = winrmexec.Client{}
	}
	if err := executor.ExecutePowerShell(ctx, winrmexec.Access{
		Host:               req.GuestAccess.Host,
		Port:               int(req.GuestAccess.HostPort),
		User:               req.GuestAccess.User,
		PasswordPath:       req.GuestAccess.PasswordPath,
		HTTPS:              req.GuestAccess.WinRMHTTPS,
		InsecureSkipVerify: req.GuestAccess.InsecureSkipVerify,
	}, windowsHygieneScript(req.GeneratedUser, req.GeneratedPass)); err != nil {
		return fmt.Errorf("check windows guest hygiene: %w", err)
	}
	return nil
}

func linuxHygieneScript(user string, checkGeneratedUser bool) string {
	quotedUser := shellQuote(user)
	checkUser := "false"
	if checkGeneratedUser && user != "" {
		checkUser = "true"
	}
	return "set -eu; user=" + quotedUser + "; check_user=" + checkUser + "; " +
		"failures=''; " +
		"for p in /var/lib/cloud/seed/nocloud /var/lib/cloud/seed/nocloud-net; do " +
		"if [ -e \"$p\" ]; then failures=\"$failures $p\"; fi; done; " +
		"if [ \"$check_user\" = true ] && getent passwd \"$user\" >/dev/null 2>&1; then failures=\"$failures user:$user\"; fi; " +
		"if [ -n \"$failures\" ]; then echo \"image hygiene failures:$failures\" >&2; exit 42; fi"
}

func windowsHygieneScript(user string, checkGeneratedUser bool) string {
	escapedUser := strings.ReplaceAll(user, "'", "''")
	checkUser := "$false"
	if checkGeneratedUser && user != "" {
		checkUser = "$true"
	}
	return "$ErrorActionPreference = 'Stop'; " +
		"$failures = @(); " +
		"$paths = @('C:\\Autounattend.xml','C:\\Windows\\Panther\\Autounattend.xml','C:\\Windows\\Panther\\Unattend.xml'); " +
		"foreach ($p in $paths) { if (Test-Path -LiteralPath $p) { $failures += $p } }; " +
		"$winlogon = 'HKLM:\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Winlogon'; " +
		"$props = Get-ItemProperty -Path $winlogon -ErrorAction SilentlyContinue; " +
		"if ($props) { " +
		"if ($props.AutoAdminLogon -eq '1') { $failures += 'autologon:AutoAdminLogon' }; " +
		"if ($null -ne $props.DefaultPassword -and $props.DefaultPassword -ne '') { $failures += 'autologon:DefaultPassword' } " +
		"}; " +
		"$basic = Get-Item -LiteralPath WSMan:\\localhost\\Service\\Auth\\Basic -ErrorAction SilentlyContinue; " +
		"if ($basic -and [string]$basic.Value -eq 'true') { $failures += 'winrm:basic-auth' }; " +
		"$checkUser = " + checkUser + "; $user = '" + escapedUser + "'; " +
		"if ($checkUser -and (Get-Command Get-LocalUser -ErrorAction SilentlyContinue)) { " +
		"$localUser = Get-LocalUser -Name $user -ErrorAction SilentlyContinue; " +
		"if ($localUser -and $localUser.Enabled) { $failures += ('user:' + $user) } " +
		"}; " +
		"if ($failures.Count -gt 0) { throw ('image hygiene failures: ' + ($failures -join ', ')) }"
}
