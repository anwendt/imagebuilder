package builder_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/builder"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func TestQEMUImageBackend_Build_ConvertsCloudImageWithQEMUImg(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.qcow2")
	if err := os.WriteFile(sourcePath, []byte("source image"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runner := &recordingRunner{
		writeOutput: []byte("converted image"),
	}
	backend := builder.NewQEMUImageBackend(builder.QEMUImageBackendOptions{
		Runner:      runner,
		QEMUImgPath: "/usr/bin/qemu-img",
	})

	artifact, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{
			Image:        testImage(v1alpha1.SourceSpec{Type: "cloud-image"}, "vmdk"),
			WorkspaceDir: workspace,
		},
		Source: &builder.SourceArtifact{
			Path:     sourcePath,
			Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Format: platform.FormatVMDK,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if len(runner.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(runner.commands))
	}
	cmd := runner.commands[0]
	wantArgs := []string{"convert", "-p", "-O", "vmdk", sourcePath, filepath.Join(workspace, "artifact.vmdk")}
	if cmd.Name != "/usr/bin/qemu-img" {
		t.Fatalf("command name = %q, want /usr/bin/qemu-img", cmd.Name)
	}
	if !equalStrings(cmd.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", cmd.Args, wantArgs)
	}
	if artifact.Path != filepath.Join(workspace, "artifact.vmdk") {
		t.Fatalf("artifact path = %q", artifact.Path)
	}
	if artifact.Format != platform.FormatVMDK {
		t.Fatalf("format = %q, want vmdk", artifact.Format)
	}
	if artifact.SizeBytes != int64(len(runner.writeOutput)) {
		t.Fatalf("size = %d, want %d", artifact.SizeBytes, len(runner.writeOutput))
	}
}

func TestQEMUImageBackend_Build_CreatesGCEArchive(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.qcow2")
	if err := os.WriteFile(sourcePath, []byte("source image"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	runner := &recordingRunner{writeOutput: []byte("raw disk")}
	backend := builder.NewQEMUImageBackend(builder.QEMUImageBackendOptions{Runner: runner, QEMUImgPath: "/usr/bin/qemu-img"})
	artifact, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{Image: testImage(v1alpha1.SourceSpec{Type: "cloud-image"}, "gcetarball"), WorkspaceDir: workspace},
		Source:       &builder.SourceArtifact{Path: sourcePath},
		Format:       platform.FormatGCETarball,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if artifact.Format != platform.FormatGCETarball || filepath.Ext(artifact.Path) != ".gcetarball" {
		t.Fatalf("artifact = %#v", artifact)
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		t.Fatalf("open artifact: %v", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gzipReader.Close()
	header, err := tar.NewReader(gzipReader).Next()
	if err != nil || header.Name != "disk.raw" {
		t.Fatalf("GCE archive header = %#v error=%v", header, err)
	}
}

func TestQEMUImageBackend_SupportsOnlyCloudImageConvertibleFormats(t *testing.T) {
	backend := builder.NewQEMUImageBackend(builder.QEMUImageBackendOptions{})

	tests := []struct {
		name   string
		source string
		format string
		want   bool
	}{
		{name: "cloud image raw", source: "cloud-image", format: "raw", want: true},
		{name: "cloud image qcow2", source: "cloud-image", format: "qcow2", want: true},
		{name: "cloud image vmdk", source: "cloud-image", format: "vmdk", want: true},
		{name: "cloud image vhd", source: "cloud-image", format: "vhd", want: true},
		{name: "cloud image GCE archive", source: "cloud-image", format: "gcetarball", want: true},
		{name: "iso not yet implemented", source: "iso", format: "qcow2", want: false},
		{name: "provider native ami not qemu", source: "cloud-image", format: "ami", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backend.Supports(builder.BuildRequest{
				Image: testImage(v1alpha1.SourceSpec{Type: tt.source}, tt.format),
			})
			if got != tt.want {
				t.Fatalf("Supports() = %v, want %v", got, tt.want)
			}
		})
	}

	arm := testImage(v1alpha1.SourceSpec{Type: "cloud-image"}, "qcow2")
	arm.Spec.OS.Arch = "arm64"
	if !backend.Supports(builder.BuildRequest{Image: arm}) {
		t.Fatal("Supports() should allow arm64 cloud-image conversion")
	}
	unsupportedArch := testImage(v1alpha1.SourceSpec{Type: "cloud-image"}, "qcow2")
	unsupportedArch.Spec.OS.Arch = "s390x"
	if backend.Supports(builder.BuildRequest{Image: unsupportedArch}) {
		t.Fatal("Supports() should reject unsupported arch")
	}
}

func TestQEMUImageBackend_Build_RemovesPartialArtifactOnFailure(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.qcow2")
	if err := os.WriteFile(sourcePath, []byte("source image"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	backend := builder.NewQEMUImageBackend(builder.QEMUImageBackendOptions{
		Runner: &recordingRunner{
			writeOutput: []byte("partial"),
			err:         errors.New("qemu failed"),
		},
		QEMUImgPath: "/usr/bin/qemu-img",
	})

	_, err := backend.Build(context.Background(), builder.BackendRequest{
		BuildRequest: builder.BuildRequest{
			Image:        testImage(v1alpha1.SourceSpec{Type: "cloud-image"}, "qcow2"),
			WorkspaceDir: workspace,
		},
		Source: &builder.SourceArtifact{Path: sourcePath},
		Format: platform.FormatQCOW2,
	})
	if err == nil {
		t.Fatal("Build should return command failure")
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "artifact.qcow2")); !os.IsNotExist(statErr) {
		t.Fatalf("partial artifact should be removed, stat err = %v", statErr)
	}
}

type recordingRunner struct {
	commands    []builder.Command
	writeOutput []byte
	err         error
	waitErr     error
}

func (r *recordingRunner) Run(ctx context.Context, cmd builder.Command) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	r.commands = append(r.commands, cmd)
	if cmd.Name == "tar" {
		return (builder.ExecRunner{}).Run(ctx, cmd)
	}
	if len(cmd.Args) > 0 && len(r.writeOutput) > 0 && containsString(cmd.Args, "convert") {
		out := cmd.Args[len(cmd.Args)-1]
		if err := os.WriteFile(out, r.writeOutput, 0o600); err != nil {
			return err
		}
	}
	return r.err
}

func (r *recordingRunner) Start(ctx context.Context, cmd builder.Command) (builder.CommandProcess, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	r.commands = append(r.commands, cmd)
	if r.err != nil {
		return nil, r.err
	}
	return &recordingProcess{waitErr: r.waitErr}, nil
}

type recordingProcess struct {
	waitErr error
	killed  bool
}

func (p *recordingProcess) Wait() error {
	return p.waitErr
}

func (p *recordingProcess) Kill() error {
	p.killed = true
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
