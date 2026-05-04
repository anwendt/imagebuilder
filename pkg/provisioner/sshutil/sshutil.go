package sshutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Command struct {
	Name string
	Args []string
	Dir  string
}

type Runner interface {
	Run(ctx context.Context, cmd Command) error
}

type ExecRunner struct{}

func (r ExecRunner) Run(ctx context.Context, cmd Command) error {
	execCmd := exec.CommandContext(ctx, cmd.Name, cmd.Args...) // #nosec G204 -- SSH/SCP command lines are built by provisioner code from validated guest access settings.
	execCmd.Dir = cmd.Dir
	output, err := execCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run %s: %w: %s", cmd.Name, err, SanitizeOutput(output))
	}
	return nil
}

type Access struct {
	WorkspaceDir string
	Address      string
	User         string
	Port         int
	KeyPath      string
}

func ValidateAccess(access Access) error {
	if access.Address == "" || access.Port <= 0 {
		return fmt.Errorf("ssh access requires guest address and port")
	}
	if access.User == "" {
		return fmt.Errorf("ssh access requires guest user")
	}
	if access.KeyPath == "" {
		return fmt.Errorf("ssh access requires key path")
	}
	return ValidatePrivateKeyPath(access.KeyPath)
}

func ValidatePrivateKeyPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("ssh key path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat ssh key path: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("ssh key path must be a file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("ssh key file permissions must not grant group or other access")
	}
	return nil
}

func KnownHostsFile(workspaceDir string) (string, error) {
	dir := filepath.Join(workspaceDir, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ssh workspace: %w", err)
	}
	return filepath.Join(dir, "known_hosts"), nil
}

func SSHArgs(access Access, remoteCommand string) ([]string, error) {
	if err := ValidateAccess(access); err != nil {
		return nil, err
	}
	knownHostsPath, err := KnownHostsFile(access.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	return []string{
		"-i", access.KeyPath,
		"-p", fmt.Sprintf("%d", access.Port),
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "ConnectTimeout=10",
		fmt.Sprintf("%s@%s", access.User, access.Address),
		"--",
		remoteCommand,
	}, nil
}

func SCPArgs(access Access, sourcePath, destinationPath string) ([]string, error) {
	if err := ValidateAccess(access); err != nil {
		return nil, err
	}
	knownHostsPath, err := KnownHostsFile(access.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	return []string{
		"-i", access.KeyPath,
		"-P", fmt.Sprintf("%d", access.Port),
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "ConnectTimeout=10",
		sourcePath,
		fmt.Sprintf("%s@%s:%s", access.User, access.Address, destinationPath),
	}, nil
}

func SanitizeOutput(output []byte) string {
	const limit = 4096
	output = bytes.TrimSpace(output)
	if len(output) > limit {
		output = output[:limit]
	}
	return string(output)
}
