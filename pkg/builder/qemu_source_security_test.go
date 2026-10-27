package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestImageFileReferencesRejectExternalStorage(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"backing-filename": "/credentials/token"},
		{"format-specific": map[string]any{"data": map[string]any{"data-file": "/credentials/token"}}},
		{"format-specific": map[string]any{"data": map[string]any{"extents": []any{map[string]any{"filename": "/credentials/token"}}}}},
		{"children": []any{map[string]any{"info": map[string]any{"filename": "/credentials/token"}}}},
	} {
		if err := validateImageFileReferences(metadata, "/workspace/source.img"); err == nil {
			t.Fatalf("external reference accepted: %#v", metadata)
		}
	}
	if err := validateImageFileReferences(map[string]any{"filename": "/workspace/source.img", "children": []any{map[string]any{"info": map[string]any{"filename": "/workspace/source.img"}}}}, "/workspace/source.img"); err != nil {
		t.Fatal(err)
	}
}

// These cases exercise the actual QEMU metadata schema, not a hand-written
// metadata mock. CI installs qemu-utils; developers without it skip this test.
func TestQEMUImageBackendStandaloneSourceSecurity(t *testing.T) {
	binary, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img is not installed")
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("qemu fixture: %v: %s", err, out)
		}
	}
	for _, kind := range []string{"raw", "qcow2", "vmdk", "vpc", "vhdx", "vdi", "qed", "backing", "data-file", "split-vmdk"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			source := filepath.Join(workspace, "source.img")
			external := filepath.Join(t.TempDir(), "external.raw")
			if err := os.WriteFile(external, []byte("NON_SECRET_TEST_MARKER"), 0o600); err != nil {
				t.Fatal(err)
			}
			unsafe := false
			switch kind {
			case "raw":
				if err := os.WriteFile(source, []byte("standalone raw"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "qcow2":
				run("create", "-f", "qcow2", source, "1M")
			case "vmdk":
				run("create", "-f", "vmdk", source, "1M")
			case "vpc", "vhdx", "vdi", "qed":
				run("create", "-f", kind, source, "1M")
			case "backing":
				unsafe = true
				run("create", "-f", "qcow2", "-F", "raw", "-b", external, source)
			case "data-file":
				unsafe = true
				run("create", "-f", "qcow2", "-o", "data_file="+external+",data_file_raw=on", source, "1M")
			case "split-vmdk":
				unsafe = true
				run("create", "-f", "vmdk", "-o", "subformat=twoGbMaxExtentSparse", source, "1M")
			}
			backend := NewQEMUImageBackend(QEMUImageBackendOptions{QEMUImgPath: binary})
			_, err := backend.Build(context.Background(), BackendRequest{
				BuildRequest: BuildRequest{Image: &v1alpha1.VMImage{Spec: v1alpha1.VMImageSpec{OS: v1alpha1.OSSpec{Family: "linux"}}}, WorkspaceDir: workspace},
				Source:       &SourceArtifact{Path: source}, Format: platform.FormatRaw,
			})
			if unsafe {
				if err == nil || !strings.Contains(err.Error(), "self-contained") {
					t.Fatalf("unsafe image was not rejected: %v", err)
				}
				if _, err := os.Stat(filepath.Join(workspace, "artifact.raw")); !os.IsNotExist(err) {
					t.Fatalf("unsafe image produced output: %v", err)
				}
			} else if err != nil {
				t.Fatalf("standalone image rejected: %v", err)
			}
		})
	}
}
