package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

const name = "file"

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
		return fmt.Errorf("file provisioner requires inline content")
	}
	if len(spec.Args) != 1 || spec.Args[0] == "" {
		return fmt.Errorf("file provisioner requires exactly one destination path in args")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if req.Protocol != "ssh" {
		return nil, fmt.Errorf("file provisioner requires ssh guest access, got %q", req.Protocol)
	}
	if err := p.Validate(ctx, req.Spec); err != nil {
		return nil, err
	}
	localPath, err := writeLocalFile(req.WorkspaceDir, req.Spec.Inline)
	if err != nil {
		return nil, err
	}
	access := sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.VMAddress,
		User:         req.VMUser,
		Port:         req.SSHPort,
		KeyPath:      req.SSHKeyPath,
	}
	args, err := sshutil.SCPArgs(access, localPath, req.Spec.Args[0])
	if err != nil {
		return nil, err
	}
	runner := p.Runner
	if runner == nil {
		runner = sshutil.ExecRunner{}
	}
	if err := runner.Run(ctx, sshutil.Command{Name: "scp", Args: args, Dir: req.WorkspaceDir}); err != nil {
		return nil, err
	}
	return &provisioner.RunResult{
		Message:   "file uploaded",
		Artifacts: []string{localPath},
	}, nil
}

func writeLocalFile(workspaceDir, content string) (string, error) {
	dir := filepath.Join(workspaceDir, ".provisioner-files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create file provisioner workspace: %w", err)
	}
	file, err := os.CreateTemp(dir, "file-*")
	if err != nil {
		return "", fmt.Errorf("create local file payload: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write local file payload: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close local file payload: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("chmod local file payload: %w", err)
	}
	return path, nil
}
