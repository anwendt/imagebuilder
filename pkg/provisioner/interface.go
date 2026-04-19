// pkg/provisioner/interface.go
//
// Provisioner interface — all in-process provisioners implement this.
// Init-container provisioners do NOT implement this interface; they communicate
// via the filesystem contract (/workspace/config.json + /workspace/status.json).
//
// See docs/adr/ADR-003-provisioners-as-init-containers.md for the full init-container contract.

package provisioner

import (
	"context"
	"log/slog"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

// Type classifies how a provisioner is executed.
type Type string

const (
	// TypeInProcess runs inside the build container via Go code.
	TypeInProcess Type = "in-process"

	// TypeInitContainer runs as a Kubernetes init container.
	// The operator assembles the pod spec dynamically based on ProvisionerSpec.
	TypeInitContainer Type = "init-container"

	// TypeSidecar runs as a sidecar container alongside the build container.
	// Use sparingly — only for daemons that must run during the entire build.
	TypeSidecar Type = "sidecar"
)

// Provisioner is implemented by all in-process provisioners.
type Provisioner interface {
	// Name returns the provisioner type name matching ProvisionerSpec.Type
	// e.g. "cloud-init", "shell", "file", "powershell", "sysprep"
	Name() string

	// ExecutionType returns how this provisioner runs.
	// In-process provisioners return TypeInProcess.
	ExecutionType() Type

	// Validate checks the ProvisionerSpec before the build starts.
	Validate(ctx context.Context, spec v1alpha1.ProvisionerSpec) error

	// Run executes the provisioner.
	// For in-process provisioners this runs synchronously inside the build job.
	Run(ctx context.Context, req *RunRequest) (*RunResult, error)
}

// RunRequest carries everything a provisioner needs to run.
type RunRequest struct {
	// WorkspaceDir is the path to the shared workspace volume mountpoint.
	// Provisioners read input from and write output to this directory.
	WorkspaceDir string

	// VMAddress is the IP or hostname of the running build VM.
	// Empty for provisioners that run before VM boot (e.g. cloud-init generator).
	VMAddress string

	// SSHPort defaults to 22 for Linux, 5986 (WinRM) for Windows.
	SSHPort int

	// SSHKeyPath is the path to the ephemeral private key for this build session.
	SSHKeyPath string

	// OS family of the target image.
	OS string

	// Spec is the raw ProvisionerSpec from the VMImage manifest.
	Spec v1alpha1.ProvisionerSpec

	// Logger scoped to this provisioner step.
	Logger *slog.Logger
}

// RunResult is returned by a successful provisioner run.
type RunResult struct {
	// Message is a human-readable summary written to VMImage status conditions.
	Message string

	// Artifacts are paths to files the provisioner placed in WorkspaceDir
	// that subsequent provisioners or the build engine may consume.
	// +optional
	Artifacts []string
}

// ---------------------------------------------------------------------------
// Init-container filesystem contract
// ---------------------------------------------------------------------------
//
// For init-container provisioners the following JSON schemas apply.
// The operator writes ProvisionerInput to /workspace/config.json before
// starting each init container. The init container must write ProvisionerOutput
// to /workspace/status.json before exiting.
//
// Exit code 0  → success, next init container starts.
// Exit code != 0 → failure, the build job fails immediately.

// ProvisionerInput is written by the operator to /workspace/config.json.
// Init-container provisioners read this at startup.
type ProvisionerInput struct {
	// VMAddress is the IP/hostname of the running build VM.
	VMAddress string `json:"vmAddress"`

	// SSHPort is the SSH (or WinRM) port.
	SSHPort int `json:"sshPort"`

	// SSHKeyPath is the path to the ephemeral SSH private key inside the workspace.
	SSHKeyPath string `json:"sshKeyPath"`

	// OS family: "linux" or "windows".
	OS string `json:"os"`

	// Step is the index of this provisioner in the spec.provisioners list.
	Step int `json:"step"`

	// UserConfig is the raw ProvisionerSpec as passed by the user.
	UserConfig v1alpha1.ProvisionerSpec `json:"userConfig"`
}

// ProvisionerOutput is written by the init-container to /workspace/status.json.
type ProvisionerOutput struct {
	// Success indicates whether the provisioner completed successfully.
	Success bool `json:"success"`

	// Message is a human-readable summary.
	// +optional
	Message string `json:"message,omitempty"`

	// Error is a machine-readable error string (only when Success=false).
	// +optional
	Error string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Registry for in-process provisioners (same pattern as platform plugin registry)
// ---------------------------------------------------------------------------

var inProcessRegistry = map[string]Provisioner{}

// RegisterInProcess registers an in-process provisioner.
// Called from provisioner package init() functions.
func RegisterInProcess(p Provisioner) {
	inProcessRegistry[p.Name()] = p
}

// GetInProcess returns the in-process provisioner for the given type name.
func GetInProcess(typeName string) (Provisioner, bool) {
	p, ok := inProcessRegistry[typeName]
	return p, ok
}

// IsInitContainer returns true if the given provisioner type runs as init-container.
func IsInitContainer(typeName string) bool {
	_, inProcess := inProcessRegistry[typeName]
	return !inProcess // if not registered as in-process → it's an init-container
}
