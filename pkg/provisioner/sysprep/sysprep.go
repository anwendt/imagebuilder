package sysprep

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/powershell"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

const name = "sysprep"

func init() {
	provisioner.RegisterInProcess(Provisioner{})
}

type Provisioner struct {
	Runner      sshutil.Runner
	WinRM       winrmexec.Executor
	EndpointURL string
}

func (p Provisioner) Name() string { return name }

func (p Provisioner) ExecutionType() provisioner.Type { return provisioner.TypeInProcess }

func (p Provisioner) Validate(_ context.Context, _ v1alpha1.ProvisionerSpec) error {
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	config := sysprepConfig(req.Spec)
	if req.Protocol == "winrm" {
		executor := p.WinRM
		if executor == nil {
			executor = winrmexec.Client{}
		}
		if err := executor.ExecutePowerShell(ctx, winrmexec.Access{
			EndpointURL:        p.EndpointURL,
			Host:               req.VMAddress,
			Port:               req.SSHPort,
			User:               req.VMUser,
			PasswordPath:       req.VMPasswordPath,
			HTTPS:              req.WinRMHTTPS,
			InsecureSkipVerify: req.WinRMInsecureSkipVerify,
		}, script(config)); err != nil {
			return nil, err
		}
		return &provisioner.RunResult{Message: "sysprep started via winrm"}, nil
	}
	if req.Protocol != "ssh" {
		return nil, fmt.Errorf("sysprep provisioner requires ssh or winrm guest access, got %q", req.Protocol)
	}
	access := sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.VMAddress,
		User:         req.VMUser,
		Port:         req.SSHPort,
		KeyPath:      req.SSHKeyPath,
	}
	args, err := sshutil.SSHArgs(access, powershell.EncodedCommand(script(config)))
	if err != nil {
		return nil, err
	}
	runner := p.Runner
	if runner == nil {
		runner = sshutil.ExecRunner{}
	}
	if err := runner.Run(ctx, sshutil.Command{Name: "ssh", Args: args, Dir: req.WorkspaceDir}); err != nil {
		return nil, err
	}
	return &provisioner.RunResult{Message: "sysprep started"}, nil
}

func sysprepConfig(spec v1alpha1.ProvisionerSpec) v1alpha1.SysprepConfig {
	if spec.WindowsConfig != nil && spec.WindowsConfig.Sysprep != nil {
		return *spec.WindowsConfig.Sysprep
	}
	return v1alpha1.SysprepConfig{Generalize: true, Shutdown: true}
}

func script(config v1alpha1.SysprepConfig) string {
	args := "@('/oobe'"
	if config.Generalize {
		args += ", '/generalize'"
	}
	if config.Shutdown {
		args += ", '/shutdown'"
	} else {
		args += ", '/quit'"
	}
	args += ")"
	return "$sysprep = Join-Path $env:SystemRoot 'System32\\Sysprep\\Sysprep.exe'; " +
		"$args = " + args + "; " +
		"Start-Process -FilePath $sysprep -ArgumentList $args -Wait -NoNewWindow"
}
