package builder

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

type GuestFinalizationRequest struct {
	OSFamily        string
	GuestAccess     GuestAccess
	WorkspaceDir    string
	SysprepShutdown bool
}

type GuestFinalizer interface {
	Finalize(ctx context.Context, req GuestFinalizationRequest) error
}

type RemoteGuestFinalizer struct {
	SSHRunner sshutil.Runner
	WinRM     winrmexec.Executor
}

func (f RemoteGuestFinalizer) Finalize(ctx context.Context, req GuestFinalizationRequest) error {
	switch req.GuestAccess.Protocol {
	case guestProtocolSSH:
		return f.shutdownLinux(ctx, req)
	case guestProtocolWinRM:
		if req.SysprepShutdown {
			return nil
		}
		return f.shutdownWindows(ctx, req)
	default:
		return fmt.Errorf("finalize guest: unsupported protocol %q", req.GuestAccess.Protocol)
	}
}

func (f RemoteGuestFinalizer) shutdownLinux(ctx context.Context, req GuestFinalizationRequest) error {
	args, err := sshutil.SSHArgs(sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.GuestAccess.Host,
		User:         req.GuestAccess.User,
		Port:         int(req.GuestAccess.HostPort),
		KeyPath:      req.GuestAccess.SSHKeyPath,
	}, linuxShutdownScript())
	if err != nil {
		return err
	}
	runner := f.SSHRunner
	if runner == nil {
		runner = sshutil.ExecRunner{}
	}
	if err := runner.Run(ctx, sshutil.Command{Name: "ssh", Args: args, Dir: req.WorkspaceDir}); err != nil {
		return fmt.Errorf("shutdown linux guest: %w", err)
	}
	return nil
}

func (f RemoteGuestFinalizer) shutdownWindows(ctx context.Context, req GuestFinalizationRequest) error {
	executor := f.WinRM
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
	}, windowsShutdownScript()); err != nil {
		return fmt.Errorf("shutdown windows guest: %w", err)
	}
	return nil
}

func linuxShutdownScript() string {
	return "set -eu; if command -v sudo >/dev/null 2>&1; then sudo -n shutdown -P now; else shutdown -P now; fi"
}

func windowsShutdownScript() string {
	return "$ErrorActionPreference = 'Stop'; " +
		"Start-Process -FilePath shutdown.exe -ArgumentList @('/s','/t','0','/f') -WindowStyle Hidden"
}
