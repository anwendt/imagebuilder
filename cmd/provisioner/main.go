package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/anwendt/imagebuilder/pkg/provisioner"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/ansible"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/chef"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/custom"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/puppet"
	_ "github.com/anwendt/imagebuilder/pkg/provisioner/saltstack"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}))
	if err := run(ctx, logger); err != nil {
		logger.Error("provisioner failed", slog.String("error", err.Error()))
		if writeErr := writeStatus(false, "", err.Error()); writeErr != nil {
			logger.Error("write failure status", slog.String("error", writeErr.Error()))
		}
		os.Exit(1)
	}
	if strings.EqualFold(os.Getenv("IMAGEBUILDER_PROVISIONER_EXIT_AFTER_STATUS"), "true") {
		return
	}
	logger.Info("provisioner completed; waiting for pod termination")
	waitForever(ctx)
}

func run(ctx context.Context, logger *slog.Logger) error {
	input, err := waitForInput(ctx, configPath())
	if err != nil {
		return err
	}
	spec := input.UserConfig
	if spec.Type == "" {
		spec.Type = strings.TrimSpace(os.Getenv("IMAGEBUILDER_PROVISIONER_TYPE"))
	}
	p, ok := provisioner.GetInProcess(spec.Type)
	if !ok {
		return fmt.Errorf("provisioner type %q is not available in this image", spec.Type)
	}
	req := &provisioner.RunRequest{
		WorkspaceDir:            workspaceDir(input),
		VMAddress:               input.VMAddress,
		VMUser:                  input.VMUser,
		Protocol:                input.Protocol,
		SSHPort:                 input.SSHPort,
		SSHKeyPath:              input.SSHKeyPath,
		VMPasswordPath:          input.VMPasswordPath,
		WinRMHTTPS:              input.WinRMHTTPS,
		WinRMInsecureSkipVerify: input.WinRMInsecureSkipVerify,
		OS:                      input.OS,
		Spec:                    spec,
		Logger:                  logger.With("provisioner", spec.Type, "step", input.Step),
	}
	if err := p.Validate(ctx, spec); err != nil {
		return fmt.Errorf("validate provisioner %q: %w", spec.Type, err)
	}
	result, err := p.Run(ctx, req)
	if err != nil {
		return fmt.Errorf("run provisioner %q: %w", spec.Type, err)
	}
	message := "provisioner completed"
	if result != nil && result.Message != "" {
		message = result.Message
	}
	return writeStatus(true, message, "")
}

func configPath() string {
	if path := strings.TrimSpace(os.Getenv("PROVISIONER_CONFIG_PATH")); path != "" {
		return path
	}
	step := strings.TrimSpace(os.Getenv("PROVISIONER_STEP"))
	if step == "" {
		step = "0"
	}
	return filepath.Join("/workspace", "provisioners", "step-"+step, "config.json")
}

func statusPath() string {
	if path := strings.TrimSpace(os.Getenv("PROVISIONER_STATUS_PATH")); path != "" {
		return path
	}
	step := strings.TrimSpace(os.Getenv("PROVISIONER_STEP"))
	if step == "" {
		step = "0"
	}
	return filepath.Join("/workspace", "provisioners", "step-"+step, "status.json")
}

func waitForInput(ctx context.Context, path string) (provisioner.ProvisionerInput, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var input provisioner.ProvisionerInput
			if err := json.Unmarshal(data, &input); err != nil {
				return provisioner.ProvisionerInput{}, fmt.Errorf("parse provisioner config %s: %w", path, err)
			}
			return input, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return provisioner.ProvisionerInput{}, fmt.Errorf("read provisioner config %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return provisioner.ProvisionerInput{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func writeStatus(success bool, message, detail string) error {
	output := provisioner.ProvisionerOutput{Success: success, Message: message, Error: detail}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	path := statusPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create status directory: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func workspaceDir(input provisioner.ProvisionerInput) string {
	if path := strings.TrimSpace(os.Getenv("WORKSPACE_DIR")); path != "" {
		return path
	}
	if input.UserConfig.Source != nil {
		return filepath.Dir(filepath.Dir(filepath.Dir(configPath())))
	}
	return "/workspace"
}

func waitForever(ctx context.Context) {
	<-ctx.Done()
}
