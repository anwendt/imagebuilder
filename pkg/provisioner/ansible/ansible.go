package ansible

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

const name = "ansible"

func init() {
	provisioner.RegisterInProcess(Provisioner{})
}

type Provisioner struct {
	Runner sshutil.Runner
}

func (p Provisioner) Name() string { return name }

func (p Provisioner) ExecutionType() provisioner.Type { return provisioner.TypeInProcess }

func (p Provisioner) Validate(_ context.Context, spec v1alpha1.ProvisionerSpec) error {
	if spec.Playbook == "" {
		return fmt.Errorf("ansible provisioner requires playbook")
	}
	return nil
}

func (p Provisioner) Run(ctx context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if req.Protocol != "ssh" && req.Protocol != "winrm" {
		return nil, fmt.Errorf("ansible provisioner requires ssh or winrm guest access, got %q", req.Protocol)
	}
	if err := p.Validate(ctx, req.Spec); err != nil {
		return nil, err
	}
	inventoryPath, err := writeInventory(req)
	if err != nil {
		return nil, err
	}
	args := []string{"-i", inventoryPath, req.Spec.Playbook}
	if len(req.Spec.ExtraVars) > 0 {
		extraVarsPath, err := writeExtraVars(req.WorkspaceDir, req.Spec.ExtraVars)
		if err != nil {
			return nil, err
		}
		args = append(args, "--extra-vars", "@"+extraVarsPath)
	}
	args = append(args, req.Spec.Args...)
	runner := p.Runner
	if runner == nil {
		runner = sshutil.ExecRunner{}
	}
	if err := runner.Run(ctx, sshutil.Command{Name: "ansible-playbook", Args: args, Dir: req.WorkspaceDir}); err != nil {
		return nil, err
	}
	return &provisioner.RunResult{Message: "ansible playbook executed", Artifacts: []string{inventoryPath}}, nil
}

func writeInventory(req *provisioner.RunRequest) (string, error) {
	if req.VMAddress == "" || req.SSHPort <= 0 {
		return "", fmt.Errorf("ansible provisioner requires guest address and port")
	}
	if req.VMUser == "" {
		return "", fmt.Errorf("ansible provisioner requires guest user")
	}
	dir := filepath.Join(req.WorkspaceDir, ".ansible")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ansible workspace: %w", err)
	}
	path := filepath.Join(dir, "inventory.ini")
	line := "target ansible_host=" + req.VMAddress +
		" ansible_port=" + fmt.Sprintf("%d", req.SSHPort) +
		" ansible_user=" + shellQuoteInventory(req.VMUser)
	switch req.Protocol {
	case "ssh":
		if req.SSHKeyPath == "" {
			return "", fmt.Errorf("ansible ssh requires ssh key path")
		}
		if err := sshutil.ValidatePrivateKeyPath(req.SSHKeyPath); err != nil {
			return "", err
		}
		line += " ansible_connection=ssh ansible_ssh_private_key_file=" + shellQuoteInventory(req.SSHKeyPath) +
			" ansible_ssh_common_args='-o StrictHostKeyChecking=accept-new -o PasswordAuthentication=no -o IdentitiesOnly=yes'"
	case "winrm":
		if req.VMPasswordPath == "" {
			return "", fmt.Errorf("ansible winrm requires password path")
		}
		password, err := readSecretFile(req.VMPasswordPath)
		if err != nil {
			return "", err
		}
		scheme := "http"
		validation := "validate"
		if req.WinRMHTTPS {
			scheme = "https"
		}
		if req.WinRMInsecureSkipVerify {
			validation = "ignore"
		}
		line += " ansible_connection=winrm ansible_password=" + shellQuoteInventory(password) +
			" ansible_winrm_scheme=" + scheme +
			" ansible_winrm_server_cert_validation=" + validation
	}
	data := []byte("[imagebuilder]\n" + line + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write ansible inventory: %w", err)
	}
	return path, nil
}

func writeExtraVars(workspaceDir string, values map[string]string) (string, error) {
	dir := filepath.Join(workspaceDir, ".ansible")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ansible workspace: %w", err)
	}
	path := filepath.Join(dir, "extra-vars.json")
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal ansible extra vars: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write ansible extra vars: %w", err)
	}
	return path, nil
}

func readSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat secret file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("secret path must be a file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret file permissions must not grant group or other access")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- Path was validated as a private regular secret file just above.
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	return string(bytesTrimRightNewline(data)), nil
}

func bytesTrimRightNewline(data []byte) []byte {
	for len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r') {
		data = data[:len(data)-1]
	}
	return data
}

func shellQuoteInventory(value string) string {
	escaped := "'"
	for _, r := range value {
		if r == '\'' {
			escaped += `'\''`
			continue
		}
		escaped += string(r)
	}
	return escaped + "'"
}
