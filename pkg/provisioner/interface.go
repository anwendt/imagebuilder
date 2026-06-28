// pkg/provisioner/interface.go
//
// Provisioner interface — in-process provisioners implement this.
// Init-container provisioners communicate
// via the filesystem contract under /workspace/provisioners/step-N/.
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

	// VMUser is the guest user used for SSH/WinRM based provisioners.
	VMUser string

	// Protocol is the guest access protocol, e.g. "ssh" or "winrm".
	Protocol string

	// SSHPort defaults to 22 for Linux, 5986 (WinRM) for Windows.
	SSHPort int

	// SSHKeyPath is the path to the ephemeral private key for this build session.
	SSHKeyPath string

	// VMPasswordPath is a file containing the guest password for WinRM.
	VMPasswordPath string

	// WinRMHTTPS controls whether WinRM provisioners use HTTPS.
	WinRMHTTPS bool

	// WinRMInsecureSkipVerify disables TLS certificate verification for WinRM.
	WinRMInsecureSkipVerify bool

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
// The builder writes ProvisionerInput to /workspace/provisioners/step-N/config.json.
// The restartable init container waits for that file, performs its work, and
// writes ProvisionerOutput to /workspace/provisioners/step-N/status.json.
//
// Restartable init containers should remain alive after writing status or be
// idempotent on restart. Kubernetes starts them before the main build container
// and stops them with the pod when the build container exits.

// ProvisionerInput is written by the builder to the step config path.
// Init-container provisioners read it after startup.
type ProvisionerInput struct {
	// VMAddress is the IP/hostname of the running build VM.
	VMAddress string `json:"vmAddress"`

	// VMUser is the guest user used for SSH/WinRM based provisioners.
	VMUser string `json:"vmUser,omitempty"`

	// Protocol is the guest access protocol, e.g. "ssh" or "winrm".
	Protocol string `json:"protocol,omitempty"`

	// SSHPort is the SSH (or WinRM) port.
	SSHPort int `json:"sshPort"`

	// SSHKeyPath is the path to the ephemeral SSH private key inside the workspace.
	SSHKeyPath string `json:"sshKeyPath"`

	// VMPasswordPath is a file containing the guest password for WinRM.
	VMPasswordPath string `json:"vmPasswordPath,omitempty"`

	// WinRMHTTPS controls whether WinRM provisioners connect with HTTPS.
	WinRMHTTPS bool `json:"winRMHTTPS,omitempty"`

	// WinRMInsecureSkipVerify disables TLS certificate verification for WinRM.
	WinRMInsecureSkipVerify bool `json:"winRMInsecureSkipVerify,omitempty"`

	// OS family: "linux" or "windows".
	OS string `json:"os"`

	// Step is the step file index used under /workspace/provisioners/step-N.
	Step int `json:"step"`

	// UserConfig is the raw ProvisionerSpec as passed by the user.
	UserConfig v1alpha1.ProvisionerSpec `json:"userConfig"`
}

// ProvisionerOutput is written by the init-container to the step status path.
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

var builtInInitContainerTypes = map[string]struct{}{
	"ansible":   {},
	"chef":      {},
	"custom":    {},
	"puppet":    {},
	"saltstack": {},
}

var builtInInProcessTypes = map[string]struct{}{
	"cloud-init": {},
	"file":       {},
	"powershell": {},
	"shell":      {},
	"sysprep":    {},
}

// IsBuiltInInitContainer returns true for the built-in provisioner types that
// are executed through the ADR-003 init-container contract.
func IsBuiltInInitContainer(typeName string) bool {
	_, ok := builtInInitContainerTypes[typeName]
	return ok
}

// IsBuiltInInProcess returns true for the built-in provisioner types that are
// executed directly inside the builder process.
func IsBuiltInInProcess(typeName string) bool {
	_, ok := builtInInProcessTypes[typeName]
	return ok
}

// IsInitContainer returns true if the given provisioner type runs as an
// init-container. Unknown types default to init-container execution so custom
// externally supplied images can still be assembled by the controller.
func IsInitContainer(typeName string) bool {
	if IsBuiltInInitContainer(typeName) {
		return true
	}
	if IsBuiltInInProcess(typeName) {
		return false
	}
	_, inProcess := inProcessRegistry[typeName]
	return !inProcess
}
