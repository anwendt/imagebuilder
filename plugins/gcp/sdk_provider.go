package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"github.com/anwendt/imagebuilder/pkg/provider/sdk"
)

type SDKProvider struct {
	mu      sync.Mutex
	plugins map[string]*Plugin
	configs map[string][sha256.Size]byte
	uploads map[string]*platform.UploadResult
}

var (
	_ sdk.Provider                   = (*SDKProvider)(nil)
	_ sdk.ResumableProvider          = (*SDKProvider)(nil)
	_ sdk.RemoteBuildProvider        = (*SDKProvider)(nil)
	_ sdk.RemoteBuildCleanupProvider = (*SDKProvider)(nil)
)

func NewSDKProvider() *SDKProvider {
	return &SDKProvider{plugins: map[string]*Plugin{}, configs: map[string][sha256.Size]byte{}, uploads: map[string]*platform.UploadResult{}}
}

func (p *SDKProvider) Capabilities(context.Context) (sdk.Capabilities, error) {
	plugin := &Plugin{}
	formats := make([]string, 0, len(plugin.SupportedFormats()))
	for _, value := range plugin.SupportedFormats() {
		formats = append(formats, string(value))
	}
	families := make([]string, 0, len(plugin.SupportedOS()))
	for _, value := range plugin.SupportedOS() {
		families = append(families, string(value))
	}
	return sdk.Capabilities{ProviderName: plugin.Name(), ProviderVersion: plugin.Version(), Formats: formats, OSFamilies: families, BuildModes: plugin.SupportedBuildModes(), UploadResumeMode: "offset"}, nil
}

func (p *SDKProvider) PrepareUpload(ctx context.Context, artifact sdk.ArtifactInfo, requested sdk.UploadSession) (sdk.UploadSession, error) {
	plugin, err := p.pluginForConfig(artifact.ProviderConfigName)
	if err != nil {
		return sdk.UploadSession{}, err
	}
	accepted, err := plugin.PrepareUpload(ctx, gcpPlatformArtifact(artifact), platform.UploadSession{IdempotencyKey: requested.IdempotencyKey, ResumeToken: requested.ResumeToken, CommittedOffset: requested.CommittedOffset, ResumeMode: requested.ResumeMode})
	if err != nil {
		return sdk.UploadSession{}, err
	}
	return sdk.UploadSession{IdempotencyKey: accepted.IdempotencyKey, ResumeToken: accepted.ResumeToken, CommittedOffset: accepted.CommittedOffset, ResumeMode: accepted.ResumeMode}, nil
}

func (p *SDKProvider) UploadArtifactResumable(ctx context.Context, artifact sdk.ArtifactInfo, session sdk.UploadSession, body io.Reader, progress sdk.ProgressReporter) (sdk.UploadResult, error) {
	plugin, err := p.pluginForConfig(artifact.ProviderConfigName)
	if err != nil {
		return sdk.UploadResult{}, err
	}
	latestToken := session.ResumeToken
	result, err := plugin.UploadStreamResumable(ctx, &platform.StreamArtifact{Reader: body, Format: platform.ImageFormat(artifact.Format), Checksum: artifact.Checksum, SizeBytes: artifact.TotalSizeBytes, OS: platform.OSFamily(artifact.OSFamily), Metadata: cloneStringMap(artifact.Metadata)}, platform.UploadSession{IdempotencyKey: session.IdempotencyKey, ResumeToken: session.ResumeToken, CommittedOffset: session.CommittedOffset, ResumeMode: session.ResumeMode}, func(updated platform.UploadSession) error {
		latestToken = updated.ResumeToken
		if progress == nil {
			return nil
		}
		return progress.Report(ctx, sdk.Progress{BytesWritten: updated.CommittedOffset, TotalBytes: artifact.TotalSizeBytes, Phase: "uploading", Message: "resuming GCS upload", SessionToken: updated.ResumeToken, CommittedOffset: updated.CommittedOffset, ResumeMode: updated.ResumeMode})
	})
	if err != nil {
		return sdk.UploadResult{}, err
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["providerConfigName"], result.Metadata["format"], result.Metadata["upload.sessionToken"] = artifact.ProviderConfigName, artifact.Format, latestToken
	p.mu.Lock()
	p.uploads[result.ProviderRef] = result
	p.mu.Unlock()
	return sdk.UploadResult{ProviderRef: result.ProviderRef, Metadata: cloneStringMap(result.Metadata)}, nil
}

func gcpPlatformArtifact(artifact sdk.ArtifactInfo) *platform.BuildArtifact {
	return &platform.BuildArtifact{Format: platform.ImageFormat(artifact.Format), Checksum: artifact.Checksum, SizeBytes: artifact.TotalSizeBytes, OS: platform.OSFamily(artifact.OSFamily), Metadata: cloneStringMap(artifact.Metadata)}
}

func (p *SDKProvider) ValidateConfig(ctx context.Context, cfg sdk.Config) error {
	if cfg.ProviderConfigName == "" {
		return fmt.Errorf("provider config name is required")
	}
	fingerprint := sdkConfigFingerprint(cfg)
	if cfg.Region == "" && len(cfg.Credentials) == 0 && len(cfg.Extra) == 0 {
		p.mu.Lock()
		existing := p.plugins[cfg.ProviderConfigName]
		p.mu.Unlock()
		if existing != nil {
			return nil
		}
		return fmt.Errorf("provider config %q has not been initialised", cfg.ProviderConfigName)
	}
	p.mu.Lock()
	if existing := p.plugins[cfg.ProviderConfigName]; existing != nil && p.configs[cfg.ProviderConfigName] == fingerprint {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	plugin := &Plugin{}
	if err := plugin.Init(ctx, platform.PluginConfig{ProviderConfigName: cfg.ProviderConfigName, SecretData: cfg.Credentials, Region: cfg.Region, Endpoint: cfg.Endpoint, Insecure: cfg.Insecure, Extra: cfg.Extra}); err != nil {
		return err
	}
	p.mu.Lock()
	previous := p.plugins[cfg.ProviderConfigName]
	p.plugins[cfg.ProviderConfigName] = plugin
	p.configs[cfg.ProviderConfigName] = fingerprint
	p.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

func sdkConfigFingerprint(cfg sdk.Config) [sha256.Size]byte {
	raw, _ := json.Marshal(cfg)
	return sha256.Sum256(raw)
}

func (p *SDKProvider) UploadArtifact(ctx context.Context, artifact sdk.ArtifactInfo, body io.Reader, progress sdk.ProgressReporter) (sdk.UploadResult, error) {
	plugin, err := p.pluginForConfig(artifact.ProviderConfigName)
	if err != nil {
		return sdk.UploadResult{}, err
	}
	tmp, err := os.CreateTemp("", "imagebuilder-gcp-upload-*.tar.gz")
	if err != nil {
		return sdk.UploadResult{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	written, err := io.Copy(tmp, body)
	if err != nil {
		_ = tmp.Close()
		return sdk.UploadResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return sdk.UploadResult{}, err
	}
	if progress != nil {
		if err := progress.Report(ctx, sdk.Progress{BytesWritten: written, TotalBytes: artifact.TotalSizeBytes, Phase: "uploading", Message: "artifact received by gcp provider"}); err != nil {
			return sdk.UploadResult{}, err
		}
	}
	metadata := cloneStringMap(artifact.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["providerConfigName"], metadata["format"] = artifact.ProviderConfigName, artifact.Format
	result, err := plugin.Upload(ctx, &platform.BuildArtifact{Path: tmpPath, Format: platform.ImageFormat(artifact.Format), Checksum: artifact.Checksum, SizeBytes: artifact.TotalSizeBytes, OS: platform.OSFamily(artifact.OSFamily), Metadata: metadata})
	if err != nil {
		return sdk.UploadResult{}, err
	}
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
	p.mu.Lock()
	delete(p.uploads, input.ProviderRef)
	p.mu.Unlock()
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
	p.mu.Lock()
	delete(p.uploads, input.ProviderRef)
	p.mu.Unlock()
	return true, "deleted", nil
}

func (p *SDKProvider) HealthCheck(ctx context.Context) (string, error) {
	p.mu.Lock()
	plugins := make([]*Plugin, 0, len(p.plugins))
	for _, value := range p.plugins {
		plugins = append(plugins, value)
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
	result, err := plugin.ReconcileRemoteBuild(ctx, platformRemoteBuildRequest(input))
	if err != nil {
		return sdk.RemoteBuildResult{}, err
	}
	return sdkRemoteBuildResult(result), nil
}
func (p *SDKProvider) CleanupRemoteBuild(ctx context.Context, input sdk.RemoteBuildInput) (sdk.RemoteBuildCleanupResult, error) {
	plugin, err := p.pluginForConfig(input.ProviderConfigName)
	if err != nil {
		return sdk.RemoteBuildCleanupResult{}, err
	}
	if err := plugin.CleanupRemoteBuild(ctx, platformRemoteBuildRequest(input)); err != nil {
		return sdk.RemoteBuildCleanupResult{}, err
	}
	return sdk.RemoteBuildCleanupResult{Cleaned: true, Message: "cleaned"}, nil
}
func (p *SDKProvider) pluginForConfig(name string) (*Plugin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value := p.plugins[name]
	if value == nil {
		return nil, fmt.Errorf("provider config %q has not been initialised", name)
	}
	return value, nil
}
func (p *SDKProvider) uploadResult(input sdk.RegisterInput) *platform.UploadResult {
	p.mu.Lock()
	cached := p.uploads[input.ProviderRef]
	p.mu.Unlock()
	if cached != nil {
		result := &platform.UploadResult{ProviderRef: cached.ProviderRef, Metadata: cloneStringMap(cached.Metadata)}
		mergeRegisterMetadata(result.Metadata, input)
		return result
	}
	metadata := map[string]string{"providerConfigName": input.ProviderConfigName, "providerRef": input.ProviderRef, "gcsURI": input.ProviderRef, "format": input.Format, "imageName": input.ImageName}
	if bucket, object, ok := parseGCSImportURL(input.ProviderRef); ok {
		metadata["bucket"], metadata["object"] = bucket, object
	}
	mergeRegisterMetadata(metadata, input)
	return &platform.UploadResult{ProviderRef: input.ProviderRef, Metadata: metadata}
}

func parseGCSImportURL(value string) (string, string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "storage.googleapis.com" {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	bucket, bucketErr := url.PathUnescape(parts[0])
	object, objectErr := url.PathUnescape(parts[1])
	if bucketErr != nil || objectErr != nil || bucket == "" || object == "" {
		return "", "", false
	}
	return bucket, object, true
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
	if input.IdempotencyKey != "" {
		metadata["register.idempotencyKey"] = input.IdempotencyKey
	}
	for key, value := range input.Tags {
		metadata["target.tag."+key] = value
	}
}

func platformRemoteBuildRequest(input sdk.RemoteBuildInput) *platform.RemoteBuildRequest {
	req := &platform.RemoteBuildRequest{BuildID: input.BuildID, OperationRef: input.OperationRef, ImageName: input.ImageName, Namespace: input.Namespace, OSFamily: platform.OSFamily(input.OSFamily), OSDistribution: input.OSDistribution, OSVersion: input.OSVersion, OSArch: input.OSArch, SourceType: input.SourceType, SourceURL: input.SourceURL, SourceProviderRef: input.SourceProviderRef, SourceMarketplace: marketplaceRef(input.SourceMarketplace), SourceChecksum: input.SourceChecksum, Target: v1alpha1.TargetSpec{ProviderConfigRef: v1alpha1.ProviderConfigRef{Name: input.ProviderConfigName}, Format: input.Format, Tags: cloneStringMap(input.Tags)}, Timeout: time.Duration(input.TimeoutSeconds) * time.Second}
	for _, item := range input.Provisioners {
		req.Provisioners = append(req.Provisioners, v1alpha1.ProvisionerSpec{Type: item.Type, Image: item.Image, Inline: item.Inline, Playbook: item.Playbook, Args: append([]string(nil), item.Args...), ExtraVars: cloneStringMap(item.ExtraVars)})
	}
	if input.GuestAccess != nil {
		req.GuestAccess = &v1alpha1.GuestAccessSpec{Protocol: input.GuestAccess.Protocol, User: input.GuestAccess.User, GuestPort: input.GuestAccess.GuestPort}
	}
	return req
}
func marketplaceRef(input *sdk.MarketplaceRef) *v1alpha1.MarketplaceRef {
	if input == nil {
		return nil
	}
	return &v1alpha1.MarketplaceRef{Publisher: input.Publisher, Offer: input.Offer, SKU: input.SKU, Version: input.Version}
}
func sdkRemoteBuildResult(result *platform.RemoteBuildResult) sdk.RemoteBuildResult {
	if result == nil {
		return sdk.RemoteBuildResult{}
	}
	output := sdk.RemoteBuildResult{OperationRef: result.OperationRef, Phase: string(result.Phase), Message: result.Message, Done: result.Done}
	if result.Hygiene != nil {
		output.Hygiene = &sdk.RemoteHygieneResult{Status: result.Hygiene.Status, Message: result.Hygiene.Message, Checks: append([]string(nil), result.Hygiene.Checks...), ResultRef: result.Hygiene.ResultRef}
	}
	for _, image := range result.Images {
		output.Images = append(output.Images, sdk.RemoteImageRef{Provider: image.Provider, ProviderConfigName: image.ProviderConfig, ImageRef: image.ImageRef.ID, ImageName: image.ImageRef.Name, Location: image.ImageRef.Location, Format: string(image.Format), Checksum: image.Checksum, Tags: cloneStringMap(image.ImageRef.Tags)})
	}
	return output
}
