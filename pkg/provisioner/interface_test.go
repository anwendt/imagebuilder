// pkg/provisioner/interface_test.go
//
// Unit tests for the in-process provisioner registry.
//
// Covered behaviours:
//   - RegisterInProcess registers a provisioner
//   - GetInProcess retrieves a registered provisioner
//   - GetInProcess returns false for unknown types
//   - IsInitContainer returns false for registered in-process types unless the type is built-in init-container
//   - IsInitContainer returns true for unknown types (init-container by default)

package provisioner_test

import (
	"context"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
)

// ---------------------------------------------------------------------------
// Fake in-process provisioner
// ---------------------------------------------------------------------------

type fakeProvisioner struct {
	name string
}

func (p *fakeProvisioner) Name() string                                                 { return p.name }
func (p *fakeProvisioner) ExecutionType() provisioner.Type                              { return provisioner.TypeInProcess }
func (p *fakeProvisioner) Validate(_ context.Context, _ v1alpha1.ProvisionerSpec) error { return nil }
func (p *fakeProvisioner) Run(_ context.Context, _ *provisioner.RunRequest) (*provisioner.RunResult, error) {
	return &provisioner.RunResult{Message: "ok"}, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRegisterInProcess_And_GetInProcess(t *testing.T) {
	p := &fakeProvisioner{name: "test-shell-" + t.Name()}
	provisioner.RegisterInProcess(p)

	got, ok := provisioner.GetInProcess(p.Name())
	if !ok {
		t.Fatalf("GetInProcess(%q) = false, want true", p.Name())
	}
	if got.Name() != p.Name() {
		t.Errorf("GetInProcess returned provisioner with name %q, want %q", got.Name(), p.Name())
	}
}

func TestGetInProcess_UnknownType_ReturnsFalse(t *testing.T) {
	_, ok := provisioner.GetInProcess("definitely-not-registered-xyz")
	if ok {
		t.Error("GetInProcess for unknown type should return false")
	}
}

func TestIsInitContainer_False_ForRegisteredInProcess(t *testing.T) {
	p := &fakeProvisioner{name: "in-process-" + t.Name()}
	provisioner.RegisterInProcess(p)

	if provisioner.IsInitContainer(p.Name()) {
		t.Errorf("IsInitContainer(%q) = true, want false for registered in-process type", p.Name())
	}
}

func TestIsInitContainer_True_ForUnknownType(t *testing.T) {
	if !provisioner.IsInitContainer("completely-unknown-provisioner-xyz") {
		t.Error("IsInitContainer for unknown type should return true (default to init-container)")
	}
}

func TestIsInitContainer_True_ForBuiltInExternalProvisioners(t *testing.T) {
	for _, typeName := range []string{"ansible", "chef", "custom", "puppet", "saltstack"} {
		if !provisioner.IsInitContainer(typeName) {
			t.Errorf("IsInitContainer(%q) = false, want true", typeName)
		}
	}
}

func TestIsInitContainer_False_ForBuiltInInProcessProvisioners(t *testing.T) {
	for _, typeName := range []string{"cloud-init", "file", "powershell", "shell", "sysprep"} {
		if provisioner.IsInitContainer(typeName) {
			t.Errorf("IsInitContainer(%q) = true, want false", typeName)
		}
	}
}

func TestExecutionTypeConstants(t *testing.T) {
	cases := []struct {
		t    provisioner.Type
		want string
	}{
		{provisioner.TypeInProcess, "in-process"},
		{provisioner.TypeInitContainer, "init-container"},
		{provisioner.TypeSidecar, "sidecar"},
	}
	for _, tc := range cases {
		if string(tc.t) != tc.want {
			t.Errorf("Type constant = %q, want %q", tc.t, tc.want)
		}
	}
}

func TestRunResult_HasMessage(t *testing.T) {
	p := &fakeProvisioner{name: "test-run-" + t.Name()}
	provisioner.RegisterInProcess(p)

	got, _ := provisioner.GetInProcess(p.Name())
	result, err := got.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: "/tmp",
		OS:           "linux",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Message == "" {
		t.Error("RunResult.Message should not be empty")
	}
}
