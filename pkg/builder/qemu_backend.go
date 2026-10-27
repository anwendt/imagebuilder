package builder

import (
	"context"
	"encoding/json"
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
	inputFormat, err := b.validateStandaloneSource(ctx, req.Source.Path, workspaceDir)
	if err != nil {
		return nil, Classify(ReasonArtifactConvertFailed, err)
	}
	if err := b.runner.Run(ctx, Command{
		Name: b.qemuImgPath,
		Args: []string{"convert", "-p", "-f", inputFormat, "-O", qemuFormat, req.Source.Path, conversionPath},
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

// QEMU metadata reports backing files, external data files and format-specific
// extents. A downloaded source is one self-contained file; it must never cause
// conversion to copy another builder file into the resulting guest disk.
func (b *QEMUImageBackend) validateStandaloneSource(ctx context.Context, sourcePath, workspaceDir string) (string, error) {
	runner, ok := b.runner.(OutputCommandRunner)
	if !ok {
		return "", fmt.Errorf("qemu runner cannot inspect source metadata")
	}
	output, err := runner.Output(ctx, Command{Name: b.qemuImgPath, Args: []string{"info", "--output=json", sourcePath}, Dir: workspaceDir})
	if err != nil {
		return "", fmt.Errorf("inspect source image: %w", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(output, &metadata); err != nil {
		return "", fmt.Errorf("decode source image metadata: %w", err)
	}
	format, _ := metadata["format"].(string)
	if format == "" {
		return "", fmt.Errorf("source image metadata has no format")
	}
	if err := validateImageFileReferences(metadata, filepath.Clean(sourcePath)); err != nil {
		return "", err
	}
	return format, nil
}

func validateImageFileReferences(value any, sourcePath string) error {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok && text != "" {
				switch key {
				case "backing-filename", "full-backing-filename", "data-file":
					return fmt.Errorf("source image must be self-contained: external %s is not allowed", key)
				case "filename":
					if filepath.Clean(text) != sourcePath {
						return fmt.Errorf("source image must be self-contained: external file reference is not allowed")
					}
				}
			}
			if err := validateImageFileReferences(child, sourcePath); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := validateImageFileReferences(child, sourcePath); err != nil {
				return err
			}
		}
	}
	return nil
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
