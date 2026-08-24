package shell

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

const name = "shell"

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	for _, variable := range spec.Env {
		if !environmentNamePattern.MatchString(variable.Name) {
			return fmt.Errorf("shell provisioner environment name %q is invalid", variable.Name)
		}
		if variable.ValueFrom != nil {
			return fmt.Errorf("shell provisioner environment %q must use a literal non-secret value", variable.Name)
		}
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
	remoteScript, err := shellEnvironment(req.Spec)
	if err != nil {
		return nil, err
	}
	args, err := sshutil.SSHArgs(access, remoteScript)
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

func shellEnvironment(spec v1alpha1.ProvisionerSpec) (string, error) {
	if err := (Provisioner{}).Validate(context.Background(), spec); err != nil {
		return "", err
	}
	if len(spec.Env) == 0 {
		return spec.Inline, nil
	}
	var script strings.Builder
	for _, variable := range spec.Env {
		script.WriteString("export ")
		script.WriteString(variable.Name)
		script.WriteByte('=')
		script.WriteString(singleQuote(variable.Value))
		script.WriteByte('\n')
	}
	script.WriteString(spec.Inline)
	return script.String(), nil
}

func singleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
