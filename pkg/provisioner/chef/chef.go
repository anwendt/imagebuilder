package chef

import (
	"context"
	"fmt"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/remotecli"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

const name = "chef"

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
		return fmt.Errorf("chef provisioner requires inline recipe or args")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if err := p.Validate(ctx, req.Spec); err != nil {
		return nil, err
	}
	if req.Spec.Inline != "" {
		remotePath := "/tmp/imagebuilder-chef-recipe.rb"
		localPath, err := remotecli.UploadInline(ctx, p.Runner, req, req.Spec.Inline, "chef", remotePath)
		if err != nil {
			return nil, err
		}
		if err := remotecli.RunSSH(ctx, p.Runner, req, remotecli.CommandWithArgs("sudo chef-apply "+remotePath, req.Spec.Args)); err != nil {
			return nil, err
		}
		return &provisioner.RunResult{Message: "chef recipe applied", Artifacts: []string{localPath}}, nil
	}
	if err := remotecli.RunSSH(ctx, p.Runner, req, remotecli.CommandWithArgs("sudo chef-client", req.Spec.Args)); err != nil {
		return nil, err
	}
	return &provisioner.RunResult{Message: "chef client executed"}, nil
}
