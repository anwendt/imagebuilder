package ansible_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	"github.com/anwendt/imagebuilder/pkg/provisioner/ansible"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

func TestProvisioner_Run_WritesSSHInventoryAndRunsPlaybook(t *testing.T) {
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	runner := &recordingRunner{}
	p := ansible.Provisioner{Runner: runner}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "imagebuilder",
		SSHPort:      2222,
		SSHKeyPath:   keyPath,
		Spec: v1alpha1.ProvisionerSpec{
			Type:     "ansible",
			Playbook: "site.yml",
			ExtraVars: map[string]string{
				"cis_level": "1",
			},
		},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.command.Name != "ansible-playbook" || !containsString(runner.command.Args, "site.yml") {
		t.Fatalf("command = %#v", runner.command)
	}
	inventory, err := os.ReadFile(filepath.Join(workspace, ".ansible", "inventory.ini"))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if !strings.Contains(string(inventory), "ansible_connection=ssh") ||
		!strings.Contains(string(inventory), "ansible_ssh_private_key_file=") {
		t.Fatalf("inventory = %s", inventory)
	}
}

func TestProvisioner_Run_WritesWinRMInventory(t *testing.T) {
	workspace := t.TempDir()
	passwordPath := filepath.Join(workspace, "password")
	if err := os.WriteFile(passwordPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write password: %v", err)
	}
	runner := &recordingRunner{}
	p := ansible.Provisioner{Runner: runner}

	if _, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir:            workspace,
		Protocol:                "winrm",
		VMAddress:               "127.0.0.1",
		VMUser:                  "Administrator",
		SSHPort:                 55986,
		VMPasswordPath:          passwordPath,
		WinRMHTTPS:              true,
		WinRMInsecureSkipVerify: true,
		Spec:                    v1alpha1.ProvisionerSpec{Type: "ansible", Playbook: "windows.yml"},
	}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	inventory, err := os.ReadFile(filepath.Join(workspace, ".ansible", "inventory.ini"))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if !strings.Contains(string(inventory), "ansible_connection=winrm") ||
		!strings.Contains(string(inventory), "ansible_winrm_scheme=https") ||
		!strings.Contains(string(inventory), "ansible_winrm_server_cert_validation=ignore") {
		t.Fatalf("inventory = %s", inventory)
	}
}

type recordingRunner struct {
	command sshutil.Command
}

func (r *recordingRunner) Run(_ context.Context, cmd sshutil.Command) error {
	r.command = cmd
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
