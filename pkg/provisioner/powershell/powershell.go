package powershell

import (
	"context"
	"encoding/base64"
	"fmt"
	"unicode/utf16"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

const name = "powershell"

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

func (p Provisioner) Validate(_ context.Context, spec v1alpha1.ProvisionerSpec) error {
	if spec.Inline == "" {
		return fmt.Errorf("powershell provisioner requires inline script")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if err := p.Validate(ctx, req.Spec); err != nil {
		return nil, err
	}
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
		}, req.Spec.Inline); err != nil {
			return nil, err
		}
		return &provisioner.RunResult{Message: "powershell script executed via winrm"}, nil
	}
	if req.Protocol != "ssh" {
		return nil, fmt.Errorf("powershell provisioner requires ssh or winrm guest access, got %q", req.Protocol)
	}
	access := sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.VMAddress,
		User:         req.VMUser,
		Port:         req.SSHPort,
		KeyPath:      req.SSHKeyPath,
	}
	args, err := sshutil.SSHArgs(access, EncodedCommand(req.Spec.Inline))
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
	return &provisioner.RunResult{Message: "powershell script executed"}, nil
}

func EncodedCommand(script string) string {
	encoded := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(encoded)*2)
	for _, value := range encoded {
		raw = append(raw, byte(value), byte(value>>8))
	}
	return "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
}
