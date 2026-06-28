package remotecli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

func SSHAccess(req *provisioner.RunRequest) sshutil.Access {
	return sshutil.Access{
		WorkspaceDir: req.WorkspaceDir,
		Address:      req.VMAddress,
		User:         req.VMUser,
		Port:         req.SSHPort,
		KeyPath:      req.SSHKeyPath,
	}
}

func RunnerOrDefault(runner sshutil.Runner) sshutil.Runner {
	if runner != nil {
		return runner
	}
	return sshutil.ExecRunner{}
}

func RunSSH(ctx context.Context, runner sshutil.Runner, req *provisioner.RunRequest, command string) error {
	if req.Protocol != "ssh" {
		return fmt.Errorf("provisioner requires ssh guest access, got %q", req.Protocol)
	}
	args, err := sshutil.SSHArgs(SSHAccess(req), command)
	if err != nil {
		return err
	}
	return RunnerOrDefault(runner).Run(ctx, sshutil.Command{Name: "ssh", Args: args, Dir: req.WorkspaceDir})
}

func UploadInline(ctx context.Context, runner sshutil.Runner, req *provisioner.RunRequest, content, prefix, remotePath string) (string, error) {
	if req.Protocol != "ssh" {
		return "", fmt.Errorf("provisioner requires ssh guest access, got %q", req.Protocol)
	}
	localPath, err := WritePayload(req.WorkspaceDir, content, prefix)
	if err != nil {
		return "", err
	}
	args, err := sshutil.SCPArgs(SSHAccess(req), localPath, remotePath)
	if err != nil {
		return "", err
	}
	if err := RunnerOrDefault(runner).Run(ctx, sshutil.Command{Name: "scp", Args: args, Dir: req.WorkspaceDir}); err != nil {
		return "", err
	}
	return localPath, nil
}

func WritePayload(workspaceDir, content, prefix string) (string, error) {
	dir := filepath.Join(workspaceDir, ".provisioner-payloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create provisioner payload workspace: %w", err)
	}
	file, err := os.CreateTemp(dir, prefix+"-*")
	if err != nil {
		return "", fmt.Errorf("create provisioner payload: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write provisioner payload: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close provisioner payload: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("chmod provisioner payload: %w", err)
	}
	return path, nil
}

func CommandWithArgs(base string, args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	if strings.TrimSpace(base) == "" {
		return strings.Join(quoted, " ")
	}
	if len(args) == 0 {
		return base
	}
	return base + " " + strings.Join(quoted, " ")
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}
