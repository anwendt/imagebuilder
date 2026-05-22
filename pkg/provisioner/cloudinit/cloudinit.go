package cloudinit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
)

const (
	name         = "cloud-init"
	seedDirName  = "cloud-init"
	userDataName = "user-data"
	metaDataName = "meta-data"
)

func init() {
	provisioner.RegisterInProcess(Provisioner{})
}

type Provisioner struct{}

func (p Provisioner) Name() string { return name }

func (p Provisioner) ExecutionType() provisioner.Type { return provisioner.TypeInProcess }

func (p Provisioner) Validate(_ context.Context, spec v1alpha1.ProvisionerSpec) error {
	if spec.Inline == "" {
		return fmt.Errorf("cloud-init provisioner requires inline user-data")
	}
	return nil
}

func (p Provisioner) Run(_ context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	seedDir := filepath.Join(req.WorkspaceDir, seedDirName)
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		return nil, fmt.Errorf("create cloud-init seed directory: %w", err)
	}
	userDataPath := filepath.Join(seedDir, userDataName)
	userData := req.Spec.Inline
	// #nosec G304 -- Path is scoped to the controller-owned cloud-init seed directory.
	if existing, err := os.ReadFile(userDataPath); err == nil && strings.TrimSpace(string(existing)) != "" {
		userData = mergeCloudInit(string(existing), req.Spec.Inline)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read existing cloud-init user-data: %w", err)
	}
	// #nosec G703 -- Path is scoped to the controller-owned cloud-init seed directory.
	if err := os.WriteFile(userDataPath, []byte(userData), 0o600); err != nil {
		return nil, fmt.Errorf("write cloud-init user-data: %w", err)
	}
	metaDataPath := filepath.Join(seedDir, metaDataName)
	if _, err := os.Stat(metaDataPath); os.IsNotExist(err) {
		if err := os.WriteFile(metaDataPath, []byte("instance-id: imagebuilder\nlocal-hostname: imagebuilder\n"), 0o600); err != nil {
			return nil, fmt.Errorf("write cloud-init meta-data: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat cloud-init meta-data: %w", err)
	}
	return &provisioner.RunResult{
		Message:   "cloud-init seed written",
		Artifacts: []string{userDataPath, metaDataPath},
	}, nil
}

func mergeCloudInit(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	next = strings.TrimPrefix(next, "#cloud-config\n")
	if strings.HasPrefix(existing, "#cloud-config") {
		return existing + "\n" + next + "\n"
	}
	return "#cloud-config\n" + existing + "\n" + next + "\n"
}
