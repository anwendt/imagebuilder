package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	provisionersource "github.com/anwendt/imagebuilder/pkg/provisioner/source"
)

type ProvisionerRunner interface {
	Run(ctx context.Context, req ProvisioningRequest) error
}

type ProvisioningRequest struct {
	Image        *v1alpha1.VMImage
	WorkspaceDir string
	GuestAccess  GuestAccess
	Logger       *slog.Logger
}

type ProvisionerLookup func(typeName string) (provisioner.Provisioner, bool)

type SequentialProvisionerRunner struct {
	Lookup          ProvisionerLookup
	IsInitContainer func(typeName string) bool
	Logger          *slog.Logger
	PollInterval    time.Duration
}

type ProvisionerStepStatus struct {
	Step      int      `json:"step"`
	Type      string   `json:"type"`
	Success   bool     `json:"success"`
	Message   string   `json:"message,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	Error     string   `json:"error,omitempty"`
	Duration  float64  `json:"durationSeconds,omitempty"`
}

func NewSequentialProvisionerRunner() SequentialProvisionerRunner {
	return SequentialProvisionerRunner{Lookup: provisioner.GetInProcess}
}

func (r SequentialProvisionerRunner) Run(ctx context.Context, req ProvisioningRequest) error {
	if req.Image == nil {
		return fmt.Errorf("provisioning image is required")
	}
	if len(req.Image.Spec.Provisioners) == 0 {
		return nil
	}
	lookup := r.Lookup
	if lookup == nil {
		lookup = provisioner.GetInProcess
	}
	logger := req.Logger
	if logger == nil {
		logger = r.Logger
	}
	if logger == nil {
		logger = slog.Default()
	}

	provisioners, err := provisionersource.ExpandProvisioners(ctx, req.WorkspaceDir, req.Image.Spec.Provisioners)
	if err != nil {
		return err
	}
	statuses := make([]ProvisionerStepStatus, 0, len(provisioners))
	externalStep := 0
	for step, spec := range provisioners {
		status := ProvisionerStepStatus{Step: step, Type: spec.Type}
		runReq := &provisioner.RunRequest{
			WorkspaceDir:            req.WorkspaceDir,
			VMAddress:               req.GuestAccess.Host,
			VMUser:                  req.GuestAccess.User,
			Protocol:                req.GuestAccess.Protocol,
			SSHPort:                 int(req.GuestAccess.HostPort),
			SSHKeyPath:              req.GuestAccess.SSHKeyPath,
			VMPasswordPath:          req.GuestAccess.PasswordPath,
			WinRMHTTPS:              req.GuestAccess.WinRMHTTPS,
			WinRMInsecureSkipVerify: req.GuestAccess.InsecureSkipVerify,
			OS:                      req.Image.Spec.OS.Family,
			Spec:                    spec,
		}

		if requiresGuestAccess(spec.Type) && req.GuestAccess.Host == "" {
			err := fmt.Errorf("provisioner step %d type %q requires spec.build.guestAccess", step, spec.Type)
			status.Error = sanitizeProvisionerDetail(err.Error(), req)
			statuses = append(statuses, status)
			_ = writeProvisionerStepOutput(req.WorkspaceDir, step, status)
			_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
			return err
		}

		if r.isInitContainerType(spec.Type) {
			configStep := externalStep
			externalStep++
			if err := writeProvisionerInput(req.WorkspaceDir, configStep, runReq); err != nil {
				status.Error = sanitizeProvisionerDetail(err.Error(), req)
				statuses = append(statuses, status)
				_ = writeProvisionerStepOutput(req.WorkspaceDir, configStep, status)
				_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
				return err
			}
			stepLogger := logger.With("provisioner", spec.Type, "step", step, "externalStep", configStep)
			stepLogger.Info("waiting for init-container provisioner")
			start := time.Now()
			output, err := r.waitForInitContainerOutput(ctx, req.WorkspaceDir, configStep)
			status.Duration = time.Since(start).Seconds()
			if err != nil {
				wrapped := fmt.Errorf("wait for provisioner step %d type %q: %w", step, spec.Type, err)
				status.Error = sanitizeProvisionerDetail(wrapped.Error(), req)
				statuses = append(statuses, status)
				_ = writeProvisionerStepOutput(req.WorkspaceDir, configStep, status)
				_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
				return wrapped
			}
			if !output.Success {
				detail := output.Error
				if detail == "" {
					detail = output.Message
				}
				err := fmt.Errorf("provisioner step %d type %q failed: %s", step, spec.Type, detail)
				status.Error = sanitizeProvisionerDetail(err.Error(), req)
				statuses = append(statuses, status)
				_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
				return err
			}
			status.Success = true
			status.Message = output.Message
			statuses = append(statuses, status)
			if err := writeProvisionerStatuses(req.WorkspaceDir, statuses); err != nil {
				return err
			}
			if status.Message != "" {
				stepLogger.Info("init-container provisioner completed", slog.String("message", status.Message))
			} else {
				stepLogger.Info("init-container provisioner completed")
			}
			continue
		}

		if err := writeProvisionerInput(req.WorkspaceDir, step, runReq); err != nil {
			status.Error = sanitizeProvisionerDetail(err.Error(), req)
			statuses = append(statuses, status)
			_ = writeProvisionerStepOutput(req.WorkspaceDir, step, status)
			_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
			return err
		}
		p, ok := lookup(spec.Type)
		if !ok {
			err := fmt.Errorf("provisioner step %d type %q is not available in the builder runtime", step, spec.Type)
			status.Error = sanitizeProvisionerDetail(err.Error(), req)
			statuses = append(statuses, status)
			_ = writeProvisionerStepOutput(req.WorkspaceDir, step, status)
			_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
			return err
		}
		if p.ExecutionType() != provisioner.TypeInProcess {
			err := fmt.Errorf("provisioner step %d type %q has unsupported execution type %q in builder runtime", step, spec.Type, p.ExecutionType())
			status.Error = sanitizeProvisionerDetail(err.Error(), req)
			statuses = append(statuses, status)
			_ = writeProvisionerStepOutput(req.WorkspaceDir, step, status)
			_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
			return err
		}
		if err := p.Validate(ctx, spec); err != nil {
			wrapped := fmt.Errorf("validate provisioner step %d type %q: %w", step, spec.Type, err)
			status.Error = sanitizeProvisionerDetail(wrapped.Error(), req)
			statuses = append(statuses, status)
			_ = writeProvisionerStepOutput(req.WorkspaceDir, step, status)
			_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
			return wrapped
		}
		stepLogger := logger.With("provisioner", spec.Type, "step", step)
		runReq.Logger = stepLogger
		start := time.Now()
		result, err := p.Run(ctx, runReq)
		status.Duration = time.Since(start).Seconds()
		if err != nil {
			wrapped := fmt.Errorf("run provisioner step %d type %q: %w", step, spec.Type, err)
			status.Error = sanitizeProvisionerDetail(wrapped.Error(), req)
			statuses = append(statuses, status)
			_ = writeProvisionerStepOutput(req.WorkspaceDir, step, status)
			_ = writeProvisionerStatuses(req.WorkspaceDir, statuses)
			return wrapped
		}
		if result != nil {
			status.Message = result.Message
			status.Artifacts = result.Artifacts
		}
		if status.Message != "" {
			stepLogger.Info("provisioner completed", slog.String("message", result.Message))
		} else {
			stepLogger.Info("provisioner completed")
		}
		status.Success = true
		statuses = append(statuses, status)
		if err := writeProvisionerStepOutput(req.WorkspaceDir, step, status); err != nil {
			return err
		}
		if err := writeProvisionerStatuses(req.WorkspaceDir, statuses); err != nil {
			return err
		}
	}
	return nil
}

func (r SequentialProvisionerRunner) isInitContainerType(typeName string) bool {
	if r.IsInitContainer != nil {
		return r.IsInitContainer(typeName)
	}
	return provisioner.IsInitContainer(typeName)
}

func (r SequentialProvisionerRunner) waitForInitContainerOutput(ctx context.Context, workspaceDir string, step int) (provisioner.ProvisionerOutput, error) {
	if workspaceDir == "" {
		return provisioner.ProvisionerOutput{}, fmt.Errorf("workspace directory is required for init-container provisioner")
	}
	interval := r.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	statusPath := filepath.Join(provisionerStepDir(workspaceDir, step), "status.json")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		output, ok, err := readProvisionerOutput(statusPath)
		if err != nil {
			return provisioner.ProvisionerOutput{}, err
		}
		if ok {
			return output, nil
		}
		select {
		case <-ctx.Done():
			return provisioner.ProvisionerOutput{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func readProvisionerOutput(path string) (provisioner.ProvisionerOutput, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return provisioner.ProvisionerOutput{}, false, nil
		}
		return provisioner.ProvisionerOutput{}, false, fmt.Errorf("read provisioner output %s: %w", path, err)
	}
	if len(data) == 0 {
		return provisioner.ProvisionerOutput{}, false, nil
	}
	var output provisioner.ProvisionerOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return provisioner.ProvisionerOutput{}, false, nil
	}
	return output, true, nil
}

func writeProvisionerInput(workspaceDir string, step int, req *provisioner.RunRequest) error {
	if workspaceDir == "" {
		return nil
	}
	input := provisioner.ProvisionerInput{
		VMAddress:               req.VMAddress,
		VMUser:                  req.VMUser,
		Protocol:                req.Protocol,
		SSHPort:                 req.SSHPort,
		SSHKeyPath:              req.SSHKeyPath,
		VMPasswordPath:          req.VMPasswordPath,
		WinRMHTTPS:              req.WinRMHTTPS,
		WinRMInsecureSkipVerify: req.WinRMInsecureSkipVerify,
		OS:                      req.OS,
		Step:                    step,
		UserConfig:              req.Spec,
	}
	return writeProvisionerJSON(filepath.Join(provisionerStepDir(workspaceDir, step), "config.json"), input)
}

func writeProvisionerStepOutput(workspaceDir string, step int, status ProvisionerStepStatus) error {
	if workspaceDir == "" {
		return nil
	}
	output := provisioner.ProvisionerOutput{
		Success: status.Success,
		Message: status.Message,
		Error:   status.Error,
	}
	return writeProvisionerJSON(filepath.Join(provisionerStepDir(workspaceDir, step), "status.json"), output)
}

func writeProvisionerStatuses(workspaceDir string, statuses []ProvisionerStepStatus) error {
	if workspaceDir == "" {
		return nil
	}
	data, err := json.MarshalIndent(statuses, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal provisioner statuses: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(workspaceDir, "provisioners-result.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write provisioner statuses: %w", err)
	}
	return nil
}

func writeProvisionerJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create provisioner status directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal provisioner json: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write provisioner json %s: %w", path, err)
	}
	return nil
}

func provisionerStepDir(workspaceDir string, step int) string {
	return filepath.Join(workspaceDir, "provisioners", fmt.Sprintf("step-%d", step))
}

func sanitizeProvisionerDetail(detail string, req ProvisioningRequest) string {
	if detail == "" {
		return ""
	}
	replacements := []string{
		req.GuestAccess.SSHKeyPath, "[ssh-key]",
		req.GuestAccess.PasswordPath, "[guest-password]",
		req.WorkspaceDir, "[workspace]",
	}
	detail = strings.NewReplacer(replacements...).Replace(detail)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(password|passwd|token|secret|private[_-]?key)(\s*[:=]\s*)\S+`),
		regexp.MustCompile(`(?i)(Authorization:\s*(Basic|Bearer)\s+)\S+`),
	}
	for _, pattern := range patterns {
		detail = pattern.ReplaceAllString(detail, `$1$2[redacted]`)
	}
	return detail
}

func requiresGuestAccess(typeName string) bool {
	switch typeName {
	case "cloud-init":
		return false
	default:
		return true
	}
}

func provisionersRequireGuestAccess(specs []v1alpha1.ProvisionerSpec) bool {
	for _, spec := range specs {
		if requiresGuestAccess(spec.Type) {
			return true
		}
	}
	return false
}
