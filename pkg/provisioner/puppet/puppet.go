package puppet

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/remotecli"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

const name = "puppet"

func init() {
	provisioner.RegisterInProcess(Provisioner{})
}

type Provisioner struct {
	Runner sshutil.Runner
}

func (p Provisioner) Name() string { return name }

func (p Provisioner) ExecutionType() provisioner.Type { return provisioner.TypeInProcess }

func (p Provisioner) Validate(_ context.Context, spec v1alpha1.ProvisionerSpec) error {
	if spec.Inline == "" && len(spec.Args) == 0 {
		return fmt.Errorf("puppet provisioner requires inline manifest or args")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if err := p.Validate(ctx, req.Spec); err != nil {
		return nil, err
	}
	if req.Spec.Inline != "" {
		remotePath := "/tmp/imagebuilder-puppet.pp"
		localPath, err := remotecli.UploadInline(ctx, p.Runner, req, req.Spec.Inline, "puppet", remotePath)
		if err != nil {
			return nil, err
		}
		if err := remotecli.RunSSH(ctx, p.Runner, req, remotecli.CommandWithArgs("sudo puppet apply "+remotePath, req.Spec.Args)); err != nil {
			return nil, err
		}
		return &provisioner.RunResult{Message: "puppet manifest applied", Artifacts: []string{localPath}}, nil
	}
	if err := remotecli.RunSSH(ctx, p.Runner, req, remotecli.CommandWithArgs("sudo puppet apply", req.Spec.Args)); err != nil {
		return nil, err
	}
	return &provisioner.RunResult{Message: "puppet apply executed"}, nil
}
