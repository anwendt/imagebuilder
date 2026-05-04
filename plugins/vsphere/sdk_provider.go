package vsphere

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

type SDKProvider struct {
	log     *slog.Logger
	mu      sync.Mutex
	plugins map[string]*Plugin
	uploads map[string]*platform.UploadResult
}

var _ sdk.Provider = (*SDKProvider)(nil)

func NewSDKProvider() *SDKProvider {
	return &SDKProvider{
		log:     slog.Default().With(slog.String("provider", "vsphere")),
		plugins: map[string]*Plugin{},
		uploads: map[string]*platform.UploadResult{},
	}
}

func (p *SDKProvider) Capabilities(context.Context) (sdk.Capabilities, error) {
	plugin := &Plugin{}
	formats := make([]string, 0, len(plugin.SupportedFormats()))
	for _, format := range plugin.SupportedFormats() {
		formats = append(formats, string(format))
	}
	families := make([]string, 0, len(plugin.SupportedOS()))
	for _, family := range plugin.SupportedOS() {
		families = append(families, string(family))
	}
	return sdk.Capabilities{
		ProviderName:    plugin.Name(),
		ProviderVersion: plugin.Version(),
		Formats:         formats,
		OSFamilies:      families,
		BuildModes:      plugin.SupportedBuildModes(),
	}, nil
}

func (p *SDKProvider) ValidateConfig(ctx context.Context, config sdk.Config) error {
	if config.ProviderConfigName == "" {
		return fmt.Errorf("provider config name is required")
	}
	if config.Endpoint == "" && len(config.Credentials) == 0 && len(config.Extra) == 0 {
		p.mu.Lock()
		_, ok := p.plugins[config.ProviderConfigName]
		p.mu.Unlock()
		if ok {
			return nil
		}
		return fmt.Errorf("provider config %q has not been initialised", config.ProviderConfigName)
	}
	plugin := &Plugin{}
	if err := plugin.Init(ctx, platform.PluginConfig{
		ProviderConfigName: config.ProviderConfigName,
		SecretData:         config.Credentials,
		Region:             config.Region,
		Endpoint:           config.Endpoint,
		Insecure:           config.Insecure,
		Extra:              config.Extra,
	}); err != nil {
		return err
	}
	p.mu.Lock()
	p.plugins[config.ProviderConfigName] = plugin
	p.mu.Unlock()
	return nil
}

func (p *SDKProvider) UploadArtifact(ctx context.Context, artifact sdk.ArtifactInfo, body io.Reader, progress sdk.ProgressReporter) (sdk.UploadResult, error) {
	plugin, err := p.pluginForConfig(artifact.ProviderConfigName)
	if err != nil {
		return sdk.UploadResult{}, err
	}
	tmp, err := os.CreateTemp("", "imagebuilder-vsphere-upload-*."+artifact.Format)
	if err != nil {
		return sdk.UploadResult{}, fmt.Errorf("create temporary artifact file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmp, body)
	if err != nil {
		_ = tmp.Close()
		return sdk.UploadResult{}, fmt.Errorf("spool artifact stream: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return sdk.UploadResult{}, fmt.Errorf("close temporary artifact file: %w", err)
	}
	if progress != nil {
		if err := progress.Report(ctx, sdk.Progress{
			BytesWritten: written,
			TotalBytes:   artifact.TotalSizeBytes,
			Phase:        "uploading",
			Message:      "artifact received by vsphere provider",
		}); err != nil {
			return sdk.UploadResult{}, err
		}
	}

	metadata := cloneStringMap(artifact.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["providerConfigName"] = artifact.ProviderConfigName
	metadata["format"] = artifact.Format
	buildArtifact := &platform.BuildArtifact{
		Path:      tmpPath,
		Format:    platform.ImageFormat(artifact.Format),
		Checksum:  artifact.Checksum,
		SizeBytes: artifact.TotalSizeBytes,
		OS:        platform.OSFamily(artifact.OSFamily),
		Metadata:  metadata,
	}
	result, err := plugin.Upload(ctx, buildArtifact)
	if err != nil {
		return sdk.UploadResult{}, err
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["providerConfigName"] = artifact.ProviderConfigName
	result.Metadata["format"] = artifact.Format

	p.mu.Lock()
	p.uploads[result.ProviderRef] = result
	p.mu.Unlock()
	return sdk.UploadResult{ProviderRef: result.ProviderRef, Metadata: cloneStringMap(result.Metadata)}, nil
}

func (p *SDKProvider) RegisterImage(ctx context.Context, input sdk.RegisterInput) (sdk.ImageRef, error) {
	plugin, err := p.pluginForConfig(input.ProviderConfigName)
	if err != nil {
		return sdk.ImageRef{}, err
	}
	result := p.uploadResult(input)
	ref, err := plugin.Register(ctx, result)
	if err != nil {
		return sdk.ImageRef{}, err
	}
	return sdk.ImageRef{
		ID:       ref.ID,
		Name:     ref.Name,
		Location: ref.Location,
		Tags:     cloneStringMap(ref.Tags),
	}, nil
}

func (p *SDKProvider) DeleteArtifact(ctx context.Context, input sdk.DeleteInput) (bool, string, error) {
	plugin, err := p.pluginForConfig(input.ProviderConfigName)
	if err != nil {
		return false, "", err
	}
	result := p.uploadResult(sdk.RegisterInput{
		ProviderRef:        input.ProviderRef,
		ProviderConfigName: input.ProviderConfigName,
	})
	if err := plugin.Cleanup(ctx, &platform.BuildArtifact{Metadata: cloneStringMap(result.Metadata)}); err != nil {
		return false, "", err
	}
	return true, "deleted", nil
}

func (p *SDKProvider) HealthCheck(ctx context.Context) (string, error) {
	p.mu.Lock()
	plugins := make([]*Plugin, 0, len(p.plugins))
	for _, plugin := range p.plugins {
		plugins = append(plugins, plugin)
	}
	p.mu.Unlock()
	for _, plugin := range plugins {
		if err := plugin.HealthCheck(ctx); err != nil {
			return "", err
		}
	}
	return "ok", nil
}

func (p *SDKProvider) pluginForConfig(providerConfigName string) (*Plugin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	plugin := p.plugins[providerConfigName]
	if plugin == nil {
		return nil, fmt.Errorf("provider config %q has not been initialised", providerConfigName)
	}
	return plugin, nil
}

func (p *SDKProvider) uploadResult(input sdk.RegisterInput) *platform.UploadResult {
	p.mu.Lock()
	cached := p.uploads[input.ProviderRef]
	p.mu.Unlock()
	if cached != nil {
		result := &platform.UploadResult{
			ProviderRef: cached.ProviderRef,
			Metadata:    cloneStringMap(cached.Metadata),
		}
		mergeRegisterMetadata(result.Metadata, input)
		return result
	}
	metadata := map[string]string{
		"providerConfigName": input.ProviderConfigName,
		"providerRef":        input.ProviderRef,
		"datastorePath":      input.ProviderRef,
		"format":             input.Format,
		"imageName":          input.ImageName,
	}
	mergeRegisterMetadata(metadata, input)
	return &platform.UploadResult{ProviderRef: input.ProviderRef, Metadata: metadata}
}

func mergeRegisterMetadata(metadata map[string]string, input sdk.RegisterInput) {
	if metadata == nil {
		return
	}
	if input.ImageName != "" {
		metadata["imageName"] = input.ImageName
	}
	if input.Format != "" {
		metadata["format"] = input.Format
	}
	for key, value := range input.Tags {
		metadata[key] = value
	}
}
