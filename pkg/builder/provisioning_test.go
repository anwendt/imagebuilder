package builder_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
)

func TestSequentialProvisionerRunner_RunsInProcessProvisionersInOrder(t *testing.T) {
	var calls []string
	runner := builder.SequentialProvisionerRunner{
		Lookup: func(typeName string) (provisioner.Provisioner, bool) {
			return &fakeRuntimeProvisioner{name: typeName, calls: &calls}, true
		},
	}
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{
		{Type: "cloud-init", Inline: "#cloud-config"},
		{Type: "shell", Inline: "echo ok"},
	}

	workspace := t.TempDir()
	err := runner.Run(context.Background(), builder.ProvisioningRequest{
		Image:        img,
		WorkspaceDir: workspace,
		GuestAccess: builder.GuestAccess{
			Protocol:   "ssh",
			Host:       "127.0.0.1",
			HostPort:   2222,
			User:       "imagebuilder",
			SSHKeyPath: "/workspace/id_ed25519",
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{
		"validate:cloud-init",
		"run:cloud-init:127.0.0.1:2222:imagebuilder:/workspace/id_ed25519:ssh",
		"validate:shell",
		"run:shell:127.0.0.1:2222:imagebuilder:/workspace/id_ed25519:ssh",
	}
	if !equalStrings(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "provisioners-result.json"))
	if err != nil {
		t.Fatalf("read provisioner result: %v", err)
	}
	var statuses []builder.ProvisionerStepStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		t.Fatalf("decode statuses: %v", err)
	}
	if len(statuses) != 2 || !statuses[0].Success || statuses[1].Type != "shell" {
		t.Fatalf("statuses = %#v", statuses)
	}
	if len(statuses[1].Artifacts) != 1 || statuses[1].Artifacts[0] != "/workspace/artifact.txt" {
		t.Fatalf("artifacts = %#v", statuses[1].Artifacts)
	}
	stepConfig := filepath.Join(workspace, "provisioners", "step-1", "config.json")
	if info, err := os.Stat(stepConfig); err != nil {
		t.Fatalf("stat step config: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("step config mode = %v, want 0600", info.Mode().Perm())
	}
	var stepOutput provisioner.ProvisionerOutput
	data, err = os.ReadFile(filepath.Join(workspace, "provisioners", "step-1", "status.json"))
	if err != nil {
		t.Fatalf("read step status: %v", err)
	}
	if err := json.Unmarshal(data, &stepOutput); err != nil {
		t.Fatalf("decode step status: %v", err)
	}
	if !stepOutput.Success || stepOutput.Message != "ok" {
		t.Fatalf("step output = %#v", stepOutput)
	}
}

func TestSequentialProvisionerRunner_RequiresGuestAccessForRemoteProvisioner(t *testing.T) {
	runner := builder.SequentialProvisionerRunner{
		Lookup: func(typeName string) (provisioner.Provisioner, bool) {
			return &fakeRuntimeProvisioner{name: typeName}, true
		},
	}
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}}

	if err := runner.Run(context.Background(), builder.ProvisioningRequest{Image: img, WorkspaceDir: "/workspace"}); err == nil {
		t.Fatal("Run should require guest access for shell provisioner")
	}
}

func TestSequentialProvisionerRunner_WaitsForInitContainerProvisioner(t *testing.T) {
	runner := builder.SequentialProvisionerRunner{
		Lookup: func(string) (provisioner.Provisioner, bool) {
			t.Fatal("init-container provisioner must not be resolved through in-process lookup")
			return nil, false
		},
		PollInterval: 10 * time.Millisecond,
	}
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "ansible", Playbook: "site.yml"}}
	workspace := t.TempDir()

	done := make(chan error, 1)
	go func() {
		configPath := filepath.Join(workspace, "provisioners", "step-0", "config.json")
		deadline := time.After(5 * time.Second)
		for {
			data, err := os.ReadFile(configPath)
			if err == nil {
				var input provisioner.ProvisionerInput
				if err := json.Unmarshal(data, &input); err != nil {
					done <- err
					return
				}
				if input.UserConfig.Type != "ansible" || input.UserConfig.Playbook != "site.yml" {
					done <- fmt.Errorf("input = %#v", input)
					return
				}
				statusPath := filepath.Join(workspace, "provisioners", "step-0", "status.json")
				done <- os.WriteFile(statusPath, []byte(`{"success":true,"message":"ansible ok"}`), 0o600)
				return
			}
			if !os.IsNotExist(err) {
				done <- err
				return
			}
			select {
			case <-deadline:
				done <- fmt.Errorf("timed out waiting for config")
				return
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	err := runner.Run(context.Background(), builder.ProvisioningRequest{
		Image:        img,
		WorkspaceDir: workspace,
		GuestAccess: builder.GuestAccess{
			Protocol:   "ssh",
			Host:       "127.0.0.1",
			HostPort:   2222,
			User:       "imagebuilder",
			SSHKeyPath: "/workspace/id_ed25519",
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("external provisioner simulation: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "provisioners-result.json"))
	if err != nil {
		t.Fatalf("read provisioner result: %v", err)
	}
	var statuses []builder.ProvisionerStepStatus
	if err := json.Unmarshal(data, &statuses); err != nil {
		t.Fatalf("decode statuses: %v", err)
	}
	if len(statuses) != 1 || !statuses[0].Success || statuses[0].Message != "ansible ok" {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestSequentialProvisionerRunner_UnknownProvisionerReturnsError(t *testing.T) {
	runner := builder.SequentialProvisionerRunner{
		Lookup: func(string) (provisioner.Provisioner, bool) {
			return nil, false
		},
	}
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell"}}

	if err := runner.Run(context.Background(), builder.ProvisioningRequest{Image: img, WorkspaceDir: "/workspace"}); err == nil {
		t.Fatal("Run should reject unavailable provisioner")
	}
}

func TestSequentialProvisionerRunner_RedactsSensitiveFailureDetails(t *testing.T) {
	runner := builder.SequentialProvisionerRunner{
		Lookup: func(typeName string) (provisioner.Provisioner, bool) {
			return &fakeRuntimeProvisioner{name: typeName, runErr: fmt.Errorf("password=supersecret key /workspace/id_ed25519")}, true
		},
	}
	img := testImage(v1alpha1.SourceSpec{Type: "iso"}, "qcow2")
	img.Spec.Provisioners = []v1alpha1.ProvisionerSpec{{Type: "shell", Inline: "echo ok"}}
	workspace := t.TempDir()

	err := runner.Run(context.Background(), builder.ProvisioningRequest{
		Image:        img,
		WorkspaceDir: workspace,
		GuestAccess: builder.GuestAccess{
			Protocol:   "ssh",
			Host:       "127.0.0.1",
			HostPort:   2222,
			User:       "imagebuilder",
			SSHKeyPath: "/workspace/id_ed25519",
		},
	})
	if err == nil {
		t.Fatal("Run should fail")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "provisioners-result.json"))
	if err != nil {
		t.Fatalf("read provisioner result: %v", err)
	}
	if string(data) == "" || strings.Contains(string(data), "supersecret") || strings.Contains(string(data), "/workspace/id_ed25519") {
		t.Fatalf("status leaked sensitive data: %s", data)
	}
}

type fakeRuntimeProvisioner struct {
	name   string
	calls  *[]string
	runErr error
}

func (p *fakeRuntimeProvisioner) Name() string { return p.name }

func (p *fakeRuntimeProvisioner) ExecutionType() provisioner.Type {
	return provisioner.TypeInProcess
}

func (p *fakeRuntimeProvisioner) Validate(_ context.Context, spec v1alpha1.ProvisionerSpec) error {
	if p.calls != nil {
		*p.calls = append(*p.calls, "validate:"+spec.Type)
	}
	return nil
}

func (p *fakeRuntimeProvisioner) Run(_ context.Context, req *provisioner.RunRequest) (*provisioner.RunResult, error) {
	if p.runErr != nil {
		return nil, p.runErr
	}
	if p.calls != nil {
		*p.calls = append(*p.calls, fmt.Sprintf("run:%s:%s:%d:%s:%s:%s",
			req.Spec.Type, req.VMAddress, req.SSHPort, req.VMUser, req.SSHKeyPath, req.Protocol))
	}
	return &provisioner.RunResult{Message: "ok", Artifacts: []string{"/workspace/artifact.txt"}}, nil
}
