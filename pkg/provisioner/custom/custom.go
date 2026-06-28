package custom

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/remotecli"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
	"github.com/anwendt/imagebuilder/pkg/provisioner/winrmexec"
)

const name = "custom"

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
	if spec.Inline == "" && len(spec.Args) == 0 {
		return fmt.Errorf("custom provisioner requires inline command or args")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if err := p.Validate(ctx, req.Spec); err != nil {
		return nil, err
	}
	command := req.Spec.Inline
	if command == "" {
		command = remotecli.CommandWithArgs("", req.Spec.Args)
	}
	switch req.Protocol {
	case "ssh":
		if err := remotecli.RunSSH(ctx, p.Runner, req, command); err != nil {
			return nil, err
		}
	case "winrm":
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
		}, command); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("custom provisioner requires ssh or winrm guest access, got %q", req.Protocol)
	}
	return &provisioner.RunResult{Message: "custom command executed"}, nil
}
