package builder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// Command is an external process invocation used by build backends.
type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

// CommandRunner executes external tools such as qemu-img.
type CommandRunner interface {
	Run(ctx context.Context, cmd Command) error
}

// OutputCommandRunner captures bounded metadata from a tool before execution.
type OutputCommandRunner interface {
	CommandRunner
	Output(ctx context.Context, cmd Command) ([]byte, error)
}

func (r ExecRunner) Output(ctx context.Context, cmd Command) ([]byte, error) {
	if cmd.Name == "" {
		return nil, fmt.Errorf("command name is required")
	}
	execCmd := exec.CommandContext(ctx, cmd.Name, cmd.Args...) // #nosec G204 -- Typed backend supplies an allowlisted internal tool.
	execCmd.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		execCmd.Env = cmd.Env
	}
	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := execCmd.Start(); err != nil {
		return nil, err
	}
	const maxMetadataBytes = 1 << 20
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxMetadataBytes+1))
	if readErr != nil || len(output) > maxMetadataBytes {
		_ = execCmd.Process.Kill()
		_ = execCmd.Wait()
		if readErr != nil {
			return nil, fmt.Errorf("read tool metadata: %w", readErr)
		}
		return nil, fmt.Errorf("tool metadata exceeds %d bytes", maxMetadataBytes)
	}
	if err := execCmd.Wait(); err != nil {
		return nil, fmt.Errorf("inspect image metadata: %w", err)
	}
	return output, nil
}

// CommandProcess is a started external process.
type CommandProcess interface {
	Wait() error
	Kill() error
}

// ManagedCommandRunner can start long-running external processes.
type ManagedCommandRunner interface {
	CommandRunner
	Start(ctx context.Context, cmd Command) (CommandProcess, error)
}

// ExecRunner is the production CommandRunner implementation.
type ExecRunner struct{}

func (r ExecRunner) Run(ctx context.Context, cmd Command) error {
	if cmd.Name == "" {
		return fmt.Errorf("command name is required")
	}
	execCmd := exec.CommandContext(ctx, cmd.Name, cmd.Args...) // #nosec G204 -- Builder executes allowlisted internal tool commands assembled by typed backends.
	execCmd.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		execCmd.Env = cmd.Env
	}
	output, err := execCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run %s: %w: %s", cmd.Name, err, string(output))
	}
	return nil
}

func (r ExecRunner) Start(ctx context.Context, cmd Command) (CommandProcess, error) {
	if cmd.Name == "" {
		return nil, fmt.Errorf("command name is required")
	}
	execCmd := exec.CommandContext(ctx, cmd.Name, cmd.Args...) // #nosec G204 -- Builder executes allowlisted internal tool commands assembled by typed backends.
	execCmd.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		execCmd.Env = cmd.Env
	}
	process := &execProcess{cmd: execCmd}
	execCmd.Stdout = &process.stdout
	execCmd.Stderr = &process.stderr
	if err := execCmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Name, err)
	}
	return process, nil
}

type execProcess struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func (p *execProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("wait %s: %w: stdout=%q stderr=%q", p.cmd.Path, err, p.stdout.String(), p.stderr.String())
	}
	return nil
}

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
