package file_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/provisioner"
	fileprov "github.com/anwendt/imagebuilder/pkg/provisioner/file"
	"github.com/anwendt/imagebuilder/pkg/provisioner/sshutil"
)

func TestProvisioner_Run_UploadsInlineContentWithSCP(t *testing.T) {
	workspace := t.TempDir()
	keyPath := filepath.Join(workspace, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	runner := &recordingRunner{}
	p := fileprov.Provisioner{Runner: runner}

	result, err := p.Run(context.Background(), &provisioner.RunRequest{
		WorkspaceDir: workspace,
		Protocol:     "ssh",
		VMAddress:    "127.0.0.1",
		VMUser:       "imagebuilder",
		SSHPort:      2222,
		SSHKeyPath:   keyPath,
		Spec: v1alpha1.ProvisionerSpec{
			Type:   "file",
			Inline: "managed content\n",
			Args:   []string{"/etc/example.conf"},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.command.Name != "scp" {
		t.Fatalf("command = %#v", runner.command)
	}
	if !containsString(runner.command.Args, "imagebuilder@127.0.0.1:/etc/example.conf") {
		t.Fatalf("scp args = %#v", runner.command.Args)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	got, err := os.ReadFile(result.Artifacts[0])
	if err != nil {
		t.Fatalf("read local payload: %v", err)
	}
	if string(got) != "managed content\n" {
		t.Fatalf("payload = %q", got)
	}
}

func TestProvisioner_Validate_RequiresDestination(t *testing.T) {
	p := fileprov.Provisioner{}
	if err := p.Validate(context.Background(), v1alpha1.ProvisionerSpec{Type: "file", Inline: "x"}); err == nil {
		t.Fatal("Validate should require destination arg")
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
