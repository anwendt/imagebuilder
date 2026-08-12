package openstack

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
)

func init() {
	if err := plugin.RegisterFactory(&Plugin{}, func() platform.Plugin { return &Plugin{} }); err != nil {
		panic(fmt.Sprintf("openstack plugin: %v", err))
	}
}

type Plugin struct {
	log    *slog.Logger
	config openStackConfig
	client openStackClient
}

var (
	_ platform.RemoteBuildPlugin        = (*Plugin)(nil)
	_ platform.RemoteBuildCleanupPlugin = (*Plugin)(nil)
)

type openStackConfig struct {
	providerConfigName string
	region             string
	authURL            string
	insecure           bool
	username           string
	userID             string
	password           string
	tokenID            string
	projectID          string
	projectName        string
	domainID           string
	domainName         string
	appCredID          string
	appCredName        string
	appCredSecret      string
	remotePrivateKey   string
	extraConfig        map[string]string
}

type openStackClient interface {
	UploadImage(ctx context.Context, input openStackUploadInput) (*platform.ImageRef, error)
	GetImage(ctx context.Context, id string) (*platform.ImageRef, error)
	DeleteImage(ctx context.Context, id string) error
	ReconcileRemoteBuild(ctx context.Context, input openStackRemoteBuildInput) (*openStackRemoteBuildState, error)
	CleanupRemoteBuild(ctx context.Context, input openStackRemoteBuildInput) error
	HealthCheck(ctx context.Context) error
}

type openStackUploadInput struct {
	Path            string
	ImageName       string
	Format          platform.ImageFormat
	Checksum        string
	SizeBytes       int64
	OS              platform.OSFamily
	Tags            map[string]string
	Properties      map[string]string
	DiskFormat      string
	ContainerFormat string
	Visibility      string
	Protected       bool
	MinDiskGB       int
	MinRAMMB        int
}

func (p *Plugin) Name() string    { return "openstack" }
func (p *Plugin) Version() string { return "v0.3.0" }

func (p *Plugin) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{
		platform.FormatQCOW2,
		platform.FormatRaw,
		platform.FormatVMDK,
		platform.FormatVHD,
	}
}

func (p *Plugin) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{
		platform.OSFamilyLinux,
		platform.OSFamilyWindows,
	}
}

func (p *Plugin) SupportedBuildModes() []string {
	return []string{
		v1alpha1.BuildModeLocal,
		v1alpha1.BuildModeRemote,
	}
}

func (p *Plugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))
	p.config = openStackConfig{
		providerConfigName: cfg.ProviderConfigName,
		region:             cfg.Region,
		authURL:            firstNonEmpty(cfg.Endpoint, string(cfg.SecretData["authURL"]), string(cfg.SecretData["authUrl"])),
		insecure:           cfg.Insecure,
		username:           string(cfg.SecretData["username"]),
		userID:             string(cfg.SecretData["userID"]),
		password:           string(cfg.SecretData["password"]),
		tokenID:            string(cfg.SecretData["token"]),
		projectID:          firstNonEmpty(string(cfg.SecretData["projectID"]), string(cfg.SecretData["tenantID"])),
		projectName:        firstNonEmpty(string(cfg.SecretData["projectName"]), string(cfg.SecretData["tenantName"])),
		domainID:           string(cfg.SecretData["domainID"]),
		domainName:         firstNonEmpty(string(cfg.SecretData["domainName"]), "Default"),
		appCredID:          string(cfg.SecretData["applicationCredentialID"]),
		appCredName:        string(cfg.SecretData["applicationCredentialName"]),
		appCredSecret:      string(cfg.SecretData["applicationCredentialSecret"]),
		remotePrivateKey:   firstNonEmpty(string(cfg.SecretData["remotePrivateKey"]), string(cfg.SecretData["sshPrivateKey"])),
		extraConfig:        cfg.Extra,
	}
	if p.config.authURL == "" {
		return fmt.Errorf("openstack plugin: endpoint/authURL is required")
	}
	if p.config.insecure {
		return fmt.Errorf("openstack plugin: insecure TLS verification is not supported")
	}
	if p.client == nil {
		client, err := newGophercloudClient(ctx, p.config)
		if err != nil {
			return fmt.Errorf("openstack plugin: initialise client: %w", err)
		}
		p.client = client
	}
	p.log.Info("openstack plugin initialised", slog.String("region", p.config.region))
	return nil
}

func (p *Plugin) Validate(_ context.Context, spec v1alpha1.TargetSpec) error {
	switch platform.ImageFormat(spec.Format) {
	case platform.FormatQCOW2, platform.FormatRaw, platform.FormatVMDK, platform.FormatVHD:
		return nil
	default:
		return fmt.Errorf("openstack plugin: unsupported format %q; use qcow2, raw, vmdk, or vhd", spec.Format)
	}
}

func (p *Plugin) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	if artifact == nil {
		return nil, fmt.Errorf("openstack plugin: artifact is required")
	}
	if artifact.Path == "" {
		return nil, fmt.Errorf("openstack plugin: artifact path is required")
	}
	client := p.client
	if client == nil {
		return nil, fmt.Errorf("openstack plugin: client is not initialised")
	}
	buildID := artifact.Metadata["buildID"]
	imageName := firstNonEmpty(
		artifact.Metadata["imageName"],
		artifact.Metadata["vmimage"],
		p.config.extraConfig["imageName"],
		"imagebuilder-"+sanitizeOpenStackName(buildID),
	)
	settings := openStackImageSettingsFromExtra(p.config.extraConfig, artifact.Format)
	ref, err := client.UploadImage(ctx, openStackUploadInput{
		Path:            artifact.Path,
		ImageName:       imageName,
		Format:          artifact.Format,
		Checksum:        artifact.Checksum,
		SizeBytes:       artifact.SizeBytes,
		OS:              artifact.OS,
		Tags:            openStackTags(artifact.Metadata, buildID),
		Properties:      openStackImageProperties(artifact, p.config.extraConfig),
		DiskFormat:      settings.DiskFormat,
		ContainerFormat: settings.ContainerFormat,
		Visibility:      settings.Visibility,
		Protected:       settings.Protected,
		MinDiskGB:       settings.MinDiskGB,
		MinRAMMB:        settings.MinRAMMB,
	})
	if err != nil {
		return nil, fmt.Errorf("openstack plugin: upload image to Glance: %w", err)
	}
	return &platform.UploadResult{
		ProviderRef: ref.ID,
		Metadata: map[string]string{
			"imageID":            ref.ID,
			"imageName":          ref.Name,
			"location":           ref.Location,
			"providerConfigName": p.config.providerConfigName,
			"format":             string(artifact.Format),
			"checksum":           artifact.Checksum,
		},
	}, nil
}

func (p *Plugin) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	if result == nil {
		return nil, fmt.Errorf("openstack plugin: upload result is required")
	}
	imageID := firstNonEmpty(result.Metadata["imageID"], result.ProviderRef)
	if imageID == "" {
		return nil, fmt.Errorf("openstack plugin: upload result missing image ID")
	}
	client := p.client
	if client == nil {
		return nil, fmt.Errorf("openstack plugin: client is not initialised")
	}
	ref, err := client.GetImage(ctx, imageID)
	if err != nil {
		return nil, fmt.Errorf("openstack plugin: read Glance image %q: %w", imageID, err)
	}
	return ref, nil
}

func (p *Plugin) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	if artifact == nil || artifact.Metadata == nil {
		return nil
	}
	imageID := firstNonEmpty(artifact.Metadata["imageID"], artifact.Metadata["providerRef"])
	if imageID == "" || p.client == nil {
		return nil
	}
	if err := p.client.DeleteImage(ctx, imageID); err != nil {
		return fmt.Errorf("openstack plugin: cleanup image %q: %w", imageID, err)
	}
	return nil
}

func (p *Plugin) HealthCheck(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("openstack plugin: client is not initialised")
	}
	return p.client.HealthCheck(ctx)
}

func (p *Plugin) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	if req == nil {
		return nil, fmt.Errorf("openstack plugin: remote build request is required")
	}
	if req.BuildID == "" {
		return nil, fmt.Errorf("openstack plugin: remote build request missing build ID")
	}
	if req.Target.Format != string(platform.FormatQCOW2) && req.Target.Format != string(platform.FormatRaw) {
		return nil, fmt.Errorf("openstack plugin: remote build requires target format qcow2 or raw, got %q", req.Target.Format)
	}
	if req.SourceProviderRef == "" && req.SourceMarketplace == nil {
		return nil, fmt.Errorf("openstack plugin: remote build source providerRef is required")
	}
	if p.client == nil {
		return nil, fmt.Errorf("openstack plugin: client is not initialised")
	}
	state, err := p.client.ReconcileRemoteBuild(ctx, openStackRemoteBuildInput{
		BuildID:            req.BuildID,
		OperationRef:       req.OperationRef,
		ImageName:          firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeOpenStackName(req.BuildID)),
		SourceType:         req.SourceType,
		SourceRef:          req.SourceProviderRef,
		SourceMarketplace:  req.SourceMarketplace,
		SourceChecksum:     req.SourceChecksum,
		OSFamily:           req.OSFamily,
		OSArch:             req.OSArch,
		Format:             platform.ImageFormat(req.Target.Format),
		Tags:               map[string]string{"imagebuilder.io/build-id": req.BuildID},
		ProviderConfigName: p.config.providerConfigName,
		Provisioners:       req.Provisioners,
		GuestAccess:        req.GuestAccess,
	})
	if err != nil {
		return nil, fmt.Errorf("openstack plugin: reconcile remote build: %w", err)
	}
	if state == nil {
		return nil, fmt.Errorf("openstack plugin: remote build client returned nil state")
	}
	result := &platform.RemoteBuildResult{
		OperationRef: state.OperationRef,
		Phase:        state.Phase,
		Message:      state.Message,
		Done:         state.Done,
		Hygiene:      state.Hygiene,
	}
	if state.Done {
		if state.Image == nil || state.Image.ID == "" {
			return nil, fmt.Errorf("openstack plugin: completed remote build missing image ID")
		}
		result.Images = []platform.RemoteImageRef{{
			Provider:       p.Name(),
			ProviderConfig: p.config.providerConfigName,
			ImageRef:       *state.Image,
			Format:         platform.ImageFormat(req.Target.Format),
			Checksum:       req.SourceChecksum,
		}}
	}
	return result, nil
}

func (p *Plugin) CleanupRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) error {
	if req == nil || p.client == nil {
		return nil
	}
	return p.client.CleanupRemoteBuild(ctx, openStackRemoteBuildInput{
		BuildID:      req.BuildID,
		OperationRef: req.OperationRef,
		ImageName:    req.ImageName,
	})
}

func openStackImageSettingsFromExtra(extra map[string]string, format platform.ImageFormat) openStackImageSettings {
	return openStackImageSettings{
		DiskFormat:      firstNonEmpty(extra["image.diskFormat"], extra["diskFormat"], string(format)),
		ContainerFormat: firstNonEmpty(extra["image.containerFormat"], extra["containerFormat"], "bare"),
		Visibility:      firstNonEmpty(extra["image.visibility"], extra["visibility"], "private"),
		Protected:       parseBoolDefault(extra["image.protected"], false),
		MinDiskGB:       parseIntDefault(firstNonEmpty(extra["image.minDiskGB"], extra["minDiskGB"]), 0),
		MinRAMMB:        parseIntDefault(firstNonEmpty(extra["image.minRAMMB"], extra["minRAMMB"]), 0),
	}
}

type openStackImageSettings struct {
	DiskFormat      string
	ContainerFormat string
	Visibility      string
	Protected       bool
	MinDiskGB       int
	MinRAMMB        int
}

func openStackImageProperties(artifact *platform.BuildArtifact, extra map[string]string) map[string]string {
	properties := map[string]string{
		"hw_architecture":       firstNonEmpty(artifact.Metadata["osArch"], "x86_64"),
		"imagebuilder_build_id": artifact.Metadata["buildID"],
	}
	if artifact.OS != "" {
		properties["os_type"] = string(artifact.OS)
	}
	for key, value := range extra {
		if strings.HasPrefix(key, "image.property.") {
			properties[strings.TrimPrefix(key, "image.property.")] = value
		}
	}
	return properties
}

func openStackTags(metadata map[string]string, buildID string) map[string]string {
	tags := map[string]string{
		"imagebuilder.io/managed": "true",
	}
	if buildID != "" {
		tags["imagebuilder.io/build-id"] = buildID
	}
	if value := metadata["vmimage"]; value != "" {
		tags["imagebuilder.io/vmimage"] = value
	}
	return tags
}

func parseBoolDefault(value string, fallback bool) bool {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseIntDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
