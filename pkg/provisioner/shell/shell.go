package shell

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

const name = "shell"

func init() {
	provisioner.RegisterInProcess(Provisioner{})
}

type Provisioner struct {
	Runner sshutil.Runner
}

func (p Provisioner) Name() string { return name }

func (p Provisioner) ExecutionType() provisioner.Type { return provisioner.TypeInProcess }

func (p Provisioner) Validate(_ context.Context, spec v1alpha1.ProvisionerSpec) error {
	if spec.Inline == "" {
		return fmt.Errorf("shell provisioner requires inline script")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if req.Protocol != "ssh" {
		return nil, fmt.Errorf("shell provisioner requires ssh guest access, got %q", req.Protocol)
	}
	access := sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.VMAddress,
		User:         req.VMUser,
		Port:         req.SSHPort,
		KeyPath:      req.SSHKeyPath,
	}
	args, err := sshutil.SSHArgs(access, req.Spec.Inline)
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
	return &provisioner.RunResult{Message: "shell script executed"}, nil
}
