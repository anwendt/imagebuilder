package openstack

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

type SDKProvider struct {
	log     *slog.Logger
	mu      sync.Mutex
	plugins map[string]*Plugin
	uploads map[string]*platform.UploadResult
}

var (
	_ sdk.Provider                   = (*SDKProvider)(nil)
	_ sdk.RemoteBuildProvider        = (*SDKProvider)(nil)
	_ sdk.RemoteBuildCleanupProvider = (*SDKProvider)(nil)
)

func NewSDKProvider() *SDKProvider {
	return &SDKProvider{
		log:     slog.Default().With(slog.String("provider", "openstack")),
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
	metadata := cloneStringMap(artifact.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["providerConfigName"] = artifact.ProviderConfigName
	metadata["format"] = artifact.Format
	reader, err := sdk.NewValidatingReader(body, artifact.TotalSizeBytes, artifact.Checksum, func(bytesRead int64) error {
		if progress == nil {
			return nil
		}
		return progress.Report(ctx, sdk.Progress{BytesWritten: bytesRead, TotalBytes: artifact.TotalSizeBytes, Phase: "uploading", Message: "streaming artifact directly to Glance"})
	})
	if err != nil {
		return sdk.UploadResult{}, err
	}
	result, err := plugin.UploadStream(ctx, &platform.StreamArtifact{
		Reader:    reader,
		Format:    platform.ImageFormat(artifact.Format),
		Checksum:  artifact.Checksum,
		SizeBytes: artifact.TotalSizeBytes,
		OS:        platform.OSFamily(artifact.OSFamily),
		Metadata:  metadata,
	})
	if err != nil {
		return sdk.UploadResult{}, err
	}
	if err := reader.Verify(); err != nil {
		if result != nil {
			_ = plugin.Cleanup(ctx, &platform.BuildArtifact{Metadata: cloneStringMap(result.Metadata)})
		}
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
	p.cleanupUpload(input.ProviderRef)
	return sdk.ImageRef{ID: ref.ID, Name: ref.Name, Location: ref.Location, Tags: cloneStringMap(ref.Tags)}, nil
}

func (p *SDKProvider) DeleteArtifact(ctx context.Context, input sdk.DeleteInput) (bool, string, error) {
	plugin, err := p.pluginForConfig(input.ProviderConfigName)
	if err != nil {
		return false, "", err
	}
	result := p.uploadResult(sdk.RegisterInput{ProviderRef: input.ProviderRef, ProviderConfigName: input.ProviderConfigName})
	if err := plugin.Cleanup(ctx, &platform.BuildArtifact{Metadata: cloneStringMap(result.Metadata)}); err != nil {
		return false, "", err
	}
	p.cleanupUpload(input.ProviderRef)
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

func (p *SDKProvider) ReconcileRemoteBuild(ctx context.Context, input sdk.RemoteBuildInput) (sdk.RemoteBuildResult, error) {
	plugin, err := p.pluginForConfig(input.ProviderConfigName)
	if err != nil {
		return sdk.RemoteBuildResult{}, err
	}
	result, err := plugin.ReconcileRemoteBuild(ctx, openStackPlatformRemoteBuildRequest(input))
	if err != nil {
		return sdk.RemoteBuildResult{}, err
	}
	return openStackSDKRemoteBuildResult(result), nil
}

func (p *SDKProvider) CleanupRemoteBuild(ctx context.Context, input sdk.RemoteBuildInput) (sdk.RemoteBuildCleanupResult, error) {
	plugin, err := p.pluginForConfig(input.ProviderConfigName)
	if err != nil {
		return sdk.RemoteBuildCleanupResult{}, err
	}
	if err := plugin.CleanupRemoteBuild(ctx, openStackPlatformRemoteBuildRequest(input)); err != nil {
		return sdk.RemoteBuildCleanupResult{}, err
	}
	return sdk.RemoteBuildCleanupResult{Cleaned: true, Message: "cleaned"}, nil
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
		result := &platform.UploadResult{ProviderRef: cached.ProviderRef, Metadata: cloneStringMap(cached.Metadata)}
		mergeOpenStackRegisterMetadata(result.Metadata, input)
		return result
	}
	metadata := map[string]string{
		"providerConfigName": input.ProviderConfigName,
		"providerRef":        input.ProviderRef,
		"imageID":            input.ProviderRef,
		"format":             input.Format,
		"imageName":          input.ImageName,
	}
	mergeOpenStackRegisterMetadata(metadata, input)
	return &platform.UploadResult{ProviderRef: input.ProviderRef, Metadata: metadata}
}

func (p *SDKProvider) cleanupUpload(providerRef string) {
	p.mu.Lock()
	delete(p.uploads, providerRef)
	p.mu.Unlock()
}

func mergeOpenStackRegisterMetadata(metadata map[string]string, input sdk.RegisterInput) {
	if metadata == nil {
		return
	}
	if input.ImageName != "" {
		metadata["imageName"] = input.ImageName
	}
	if input.Format != "" {
		metadata["format"] = input.Format
	}
	if input.IdempotencyKey != "" {
		metadata["register.idempotencyKey"] = input.IdempotencyKey
	}
	for key, value := range input.Tags {
		metadata["tag."+key] = value
	}
}

func openStackPlatformRemoteBuildRequest(input sdk.RemoteBuildInput) *platform.RemoteBuildRequest {
	req := &platform.RemoteBuildRequest{
		BuildID:           input.BuildID,
		OperationRef:      input.OperationRef,
		ImageName:         input.ImageName,
		Namespace:         input.Namespace,
		OSFamily:          platform.OSFamily(input.OSFamily),
		OSDistribution:    input.OSDistribution,
		OSVersion:         input.OSVersion,
		OSArch:            input.OSArch,
		SourceType:        input.SourceType,
		SourceURL:         input.SourceURL,
		SourceProviderRef: input.SourceProviderRef,
		SourceMarketplace: v1alpha1MarketplaceRef(input.SourceMarketplace),
		SourceChecksum:    input.SourceChecksum,
		Target: v1alpha1.TargetSpec{
			ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: input.ProviderConfigName},
			Format:            input.Format,
			Tags:              cloneStringMap(input.Tags),
		},
		Timeout: time.Duration(input.TimeoutSeconds) * time.Second,
	}
	for _, provisioner := range input.Provisioners {
		req.Provisioners = append(req.Provisioners, v1alpha1.ProvisionerSpec{
			Type:      provisioner.Type,
			Image:     provisioner.Image,
			Inline:    provisioner.Inline,
			Playbook:  provisioner.Playbook,
			Args:      append([]string(nil), provisioner.Args...),
			ExtraVars: cloneStringMap(provisioner.ExtraVars),
			Source:    openStackSDKProvisionerSource(provisioner.Source),
		})
	}
	if input.GuestAccess != nil {
		req.GuestAccess = &v1alpha1.GuestAccessSpec{
			Protocol:  input.GuestAccess.Protocol,
			User:      input.GuestAccess.User,
			GuestPort: input.GuestAccess.GuestPort,
		}
	}
	return req
}

func v1alpha1MarketplaceRef(ref *sdk.MarketplaceRef) *v1alpha1.MarketplaceRef {
	if ref == nil {
		return nil
	}
	return &v1alpha1.MarketplaceRef{
		Publisher: ref.Publisher,
		Offer:     ref.Offer,
		SKU:       ref.SKU,
		Version:   ref.Version,
	}
}

func openStackSDKProvisionerSource(source *sdk.RemoteProvisionerSource) *v1alpha1.ProvisionerSourceSpec {
	if source == nil {
		return nil
	}
	out := &v1alpha1.ProvisionerSourceSpec{}
	if source.Git != nil {
		out.Git = &v1alpha1.GitProvisionerSourceSpec{URL: source.Git.URL, Ref: source.Git.Ref, Path: source.Git.Path}
		if source.Git.Auth != nil {
			out.Git.Auth = &v1alpha1.GitProvisionerAuthSpec{
				RuntimeToken:    source.Git.Auth.Token,
				RuntimeUsername: source.Git.Auth.Username,
				RuntimePassword: source.Git.Auth.Password,
			}
		}
	}
	return out
}

func openStackSDKRemoteBuildResult(result *platform.RemoteBuildResult) sdk.RemoteBuildResult {
	if result == nil {
		return sdk.RemoteBuildResult{}
	}
	out := sdk.RemoteBuildResult{
		OperationRef: result.OperationRef,
		Phase:        string(result.Phase),
		Message:      result.Message,
		Done:         result.Done,
	}
	if result.Hygiene != nil {
		out.Hygiene = &sdk.RemoteHygieneResult{
			Status:    result.Hygiene.Status,
			Message:   result.Hygiene.Message,
			Checks:    append([]string(nil), result.Hygiene.Checks...),
			ResultRef: result.Hygiene.ResultRef,
		}
	}
	for _, image := range result.Images {
		out.Images = append(out.Images, sdk.RemoteImageRef{
			Provider:           image.Provider,
			ProviderConfigName: image.ProviderConfig,
			ImageRef:           image.ImageRef.ID,
			ImageName:          image.ImageRef.Name,
			Location:           image.ImageRef.Location,
			Format:             string(image.Format),
			Checksum:           image.Checksum,
			Tags:               cloneStringMap(image.ImageRef.Tags),
		})
	}
	return out
}
