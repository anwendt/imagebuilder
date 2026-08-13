package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

const defaultQEMUImgPath = "/usr/bin/qemu-img"

// QEMUImageBackendOptions configures the qemu-img based image backend.
type QEMUImageBackendOptions struct {
	Runner      CommandRunner
	QEMUImgPath string
}

// QEMUImageBackend uses qemu-img as an external process to convert verified
// cloud images into provider-ready disk formats.
type QEMUImageBackend struct {
	runner      CommandRunner
	qemuImgPath string
}

func NewQEMUImageBackend(opts QEMUImageBackendOptions) *QEMUImageBackend {
	runner := opts.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	qemuImgPath := opts.QEMUImgPath
	if qemuImgPath == "" {
		qemuImgPath = defaultQEMUImgPath
	}
	return &QEMUImageBackend{
		runner:      runner,
		qemuImgPath: qemuImgPath,
	}
}

func (b *QEMUImageBackend) Name() string { return "qemu-img" }

func (b *QEMUImageBackend) Supports(req BuildRequest) bool {
	if req.Image == nil || strings.ToLower(req.Image.Spec.Source.Type) != "cloud-image" {
		return false
	}
	if !localBackendSupportsArch(req.Image.Spec.OS.Arch) {
		return false
	}
	if len(req.Image.Spec.Targets) == 0 {
		return false
	}
	_, ok := qemuOutputFormat(platform.ImageFormat(req.Image.Spec.Targets[0].Format))
	return ok
}

func localBackendSupportsArch(arch string) bool {
	switch strings.ToLower(arch) {
	case "", "amd64", "arm64":
		return true
	default:
		return false
	}
}

func (b *QEMUImageBackend) Build(ctx context.Context, req BackendRequest) (*platform.BuildArtifact, error) {
	if req.Source == nil {
		return nil, fmt.Errorf("source artifact is required")
	}
	qemuFormat, ok := qemuOutputFormat(req.Format)
	if !ok {
		return nil, fmt.Errorf("qemu backend does not support output format %q", req.Format)
	}
	if req.WorkspaceDir == "" {
		return nil, fmt.Errorf("workspace directory is required")
	}
	workspaceDir, err := cleanWorkspace(req.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	artifactPath := filepath.Join(workspaceDir, fmt.Sprintf("%s.%s", defaultArtifactFileName, req.Format))
	conversionPath := artifactPath
	if req.Format == platform.FormatGCETarball {
		conversionPath = filepath.Join(workspaceDir, defaultArtifactFileName+".raw")
	}
	if err := b.runner.Run(ctx, Command{
		Name: b.qemuImgPath,
		Args: []string{"convert", "-p", "-O", qemuFormat, req.Source.Path, conversionPath},
		Dir:  workspaceDir,
	}); err != nil {
		_ = os.Remove(artifactPath)
		return nil, Classify(ReasonArtifactConvertFailed, fmt.Errorf("qemu-img convert: %w", err))
	}
	if req.Format == platform.FormatGCETarball {
		defer os.Remove(conversionPath)
		if err := createGCEArchive(ctx, b.runner, conversionPath, artifactPath); err != nil {
			return nil, Classify(ReasonArtifactConvertFailed, err)
		}
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("stat qemu artifact: %w", err)
	}
	checksum, err := fileChecksum(artifactPath, "sha256")
	if err != nil {
		return nil, fmt.Errorf("checksum qemu artifact: %w", err)
	}
	return &platform.BuildArtifact{
		Path:      artifactPath,
		Format:    req.Format,
		Checksum:  checksum,
		SizeBytes: info.Size(),
		OS:        platform.OSFamily(req.Image.Spec.OS.Family),
		Metadata:  buildMetadata(req.BuildRequest, b.Name()),
	}, nil
}

func qemuOutputFormat(format platform.ImageFormat) (string, bool) {
	switch format {
	case platform.FormatRaw:
		return "raw", true
	case platform.FormatQCOW2:
		return "qcow2", true
	case platform.FormatVMDK:
		return "vmdk", true
	case platform.FormatVHD:
		return "vpc", true
	case platform.FormatGCETarball:
		return "raw", true
	default:
		return "", false
	}
}
