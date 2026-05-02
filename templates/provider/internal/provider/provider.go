package provider

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

type Provider struct {
	log *slog.Logger
}

var _ sdk.Provider = (*Provider)(nil)
var _ sdk.RemoteBuildProvider = (*Provider)(nil)
var _ sdk.RemoteBuildCleanupProvider = (*Provider)(nil)

func New() *Provider {
	return &Provider{log: slog.Default().With(slog.String("provider", "example"))}
}

func (p *Provider) Capabilities(context.Context) (sdk.Capabilities, error) {
	return sdk.Capabilities{
		ProviderName:    "example",
		ProviderVersion: "v0.1.0",
		Formats:         []string{"qcow2", "raw"},
		OSFamilies:      []string{"linux", "windows"},
		// Add "remote" after ReconcileRemoteBuild is implemented for your platform.
		BuildModes: []string{"local"},
	}, nil
}

func (p *Provider) ValidateConfig(_ context.Context, config sdk.Config) error {
	if config.ProviderConfigName == "" {
		return fmt.Errorf("provider config name is required")
	}
	if config.Region == "" {
		return fmt.Errorf("spec.region is required")
	}
	if len(config.Credentials) == 0 {
		return fmt.Errorf("credentials are required")
	}
	return nil
}

func (p *Provider) UploadArtifact(ctx context.Context, artifact sdk.ArtifactInfo, body io.Reader, progress sdk.ProgressReporter) (sdk.UploadResult, error) {
	if artifact.ProviderConfigName == "" {
		return sdk.UploadResult{}, fmt.Errorf("provider config name is required")
	}
	if artifact.Format == "" {
		return sdk.UploadResult{}, fmt.Errorf("artifact format is required")
	}
	dir := filepath.Join(os.TempDir(), "imagebuilder-provider-example", artifact.ProviderConfigName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return sdk.UploadResult{}, fmt.Errorf("create upload dir: %w", err)
	}
	path := filepath.Join(dir, "artifact."+artifact.Format)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		out, err = os.OpenFile(path, os.O_TRUNC|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return sdk.UploadResult{}, fmt.Errorf("open upload target: %w", err)
	}
	defer out.Close()

	written, err := io.Copy(out, body)
	if err != nil {
		return sdk.UploadResult{}, fmt.Errorf("write upload target: %w", err)
	}
	if err := progress.Report(ctx, sdk.Progress{
		BytesWritten: written,
		TotalBytes:   artifact.TotalSizeBytes,
		Phase:        "verifying",
		Message:      "artifact received",
	}); err != nil {
		return sdk.UploadResult{}, err
	}
	p.log.Info("artifact uploaded", slog.String("path", path), slog.Int64("bytes", written))
	return sdk.UploadResult{
		ProviderRef: path,
		Metadata:    map[string]string{"path": path},
	}, nil
}

func (p *Provider) RegisterImage(_ context.Context, input sdk.RegisterInput) (sdk.ImageRef, error) {
	if input.ProviderRef == "" {
		return sdk.ImageRef{}, fmt.Errorf("provider ref is required")
	}
	name := input.ImageName
	if name == "" {
		name = filepath.Base(input.ProviderRef)
	}
	return sdk.ImageRef{
		ID:       "example://" + input.ProviderRef,
		Name:     name,
		Location: input.ProviderConfigName,
		Tags:     input.Tags,
	}, nil
}

func (p *Provider) DeleteArtifact(_ context.Context, input sdk.DeleteInput) (bool, string, error) {
	if input.ProviderRef == "" {
		return false, "no provider ref supplied", nil
	}
	if err := os.Remove(input.ProviderRef); err != nil {
		if os.IsNotExist(err) {
			return false, "already deleted", nil
		}
		return false, "", fmt.Errorf("delete artifact: %w", err)
	}
	return true, "deleted", nil
}

func (p *Provider) HealthCheck(context.Context) (string, error) {
	return "ok", nil
}

func (p *Provider) ReconcileRemoteBuild(_ context.Context, input sdk.RemoteBuildInput) (sdk.RemoteBuildResult, error) {
	if input.BuildID == "" {
		return sdk.RemoteBuildResult{}, fmt.Errorf("build id is required")
	}
	if input.ProviderConfigName == "" {
		return sdk.RemoteBuildResult{}, fmt.Errorf("provider config name is required")
	}
	// Remote build implementations must be idempotent for input.BuildID and
	// input.OperationRef. Return an OperationRef as soon as a platform-side VM,
	// instance, task, or workflow exists, and return Done=true only after the
	// final image reference is available. Never include credentials or bootstrap
	// material in OperationRef, Message, image refs, metadata, or logs.
	// Use input.SourceProviderRef for provider-native source identifiers such
	// as AMI IDs, template IDs, Glance UUIDs, or cloud image resource names;
	// input.SourceURL is only for downloadable HTTPS sources.
	// Completed builds should return Hygiene with Status passed, failed, or
	// unknown. Use failed when temporary users, bootstrap credentials, seed
	// media, autologon data, or cloud-init/unattend residues remain.
	return sdk.RemoteBuildResult{}, fmt.Errorf("remote build is not implemented by the example provider")
}

func (p *Provider) CleanupRemoteBuild(_ context.Context, input sdk.RemoteBuildInput) (sdk.RemoteBuildCleanupResult, error) {
	if input.BuildID == "" && input.OperationRef == "" {
		return sdk.RemoteBuildCleanupResult{Cleaned: false, Message: "no remote operation supplied"}, nil
	}
	// Cleanup must be idempotent. It should remove temporary instances, disks,
	// snapshots, uploads, locks, and partial images associated with BuildID or
	// OperationRef. Treat already-deleted resources as success and never include
	// credentials or bootstrap material in the returned message or logs.
	return sdk.RemoteBuildCleanupResult{}, fmt.Errorf("remote build cleanup is not implemented by the example provider")
}
