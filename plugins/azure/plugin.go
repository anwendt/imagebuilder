package azure

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/pageblob"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	"golang.org/x/sync/errgroup"
)

const (
	azurePageSize        = int64(pageblob.PageBytes)
	defaultPageChunkSize = int64(4 * 1024 * 1024)
	defaultPageWorkers   = int32(4)
	vhdFooterSize        = 512
	vhdCookie            = "conectix"
	vhdDiskTypeFixed     = 2
)

func init() {
	registerAzureMetrics()
	if err := plugin.Register(&Plugin{}); err != nil {
		panic(fmt.Sprintf("azure plugin: %v", err))
	}
}

type Plugin struct {
	log    *slog.Logger
	config config
	client client
}

var (
	_ platform.RemoteBuildPlugin        = (*Plugin)(nil)
	_ platform.RemoteBuildCleanupPlugin = (*Plugin)(nil)
)

type config struct {
	providerConfigName string
	subscriptionID     string
	tenantID           string
	clientID           string
	clientSecret       string
	location           string
	resourceGroup      string
	storageAccount     string
	storageAccountKey  string
	storageContainer   string
	blobPrefix         string
	storageEndpoint    string
	armEndpoint        string
	armAudience        string
	cloudName          string
	authMode           string
	managedIdentityID  string
	tokenFilePath      string
	authorityHost      string
	imageName          string
	hyperVGeneration   armcompute.HyperVGenerationTypes
	osState            armcompute.OperatingSystemStateTypes
	diskSizeGiB        int32
	storageAccountType armcompute.StorageAccountTypes
	galleryName        string
	galleryImageName   string
	galleryVersion     string
	replicaCount       int32
	targetRegions      []string
	pageUploadWorkers  int32
	pageUploadChunk    int64
	extraConfig        map[string]string
}

type client interface {
	UploadBlob(ctx context.Context, container, blobName, filePath string) (string, error)
	RegisterImage(ctx context.Context, input registerInput) (*platform.ImageRef, error)
	ReconcileRemoteBuild(ctx context.Context, input azureRemoteBuildInput) (*azureRemoteBuildState, error)
	CleanupRemoteBuild(ctx context.Context, input azureRemoteBuildInput) error
	Cleanup(ctx context.Context, metadata map[string]string) error
	HealthCheck(ctx context.Context) error
}

type registerInput struct {
	ResourceGroup          string
	Location               string
	ImageName              string
	BlobURL                string
	Format                 platform.ImageFormat
	OS                     platform.OSFamily
	Checksum               string
	Tags                   map[string]string
	SnapshotID             string
	ManagedDiskID          string
	HyperVGeneration       armcompute.HyperVGenerationTypes
	OSState                armcompute.OperatingSystemStateTypes
	DiskSizeGiB            int32
	StorageAccountType     armcompute.StorageAccountTypes
	GalleryName            string
	GalleryImageName       string
	GalleryVersion         string
	ReplicaCount           int32
	TargetRegions          []string
	SourceVirtualMachineID string
}

func (p *Plugin) Name() string    { return "azure" }
func (p *Plugin) Version() string { return "v0.2.0" }

func (p *Plugin) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{platform.FormatVHD}
}

func (p *Plugin) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{platform.OSFamilyLinux, platform.OSFamilyWindows}
}

func (p *Plugin) SupportedBuildModes() []string {
	return []string{v1alpha1.BuildModeLocal, v1alpha1.BuildModeRemote}
}

func (p *Plugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))
	platform.ApplyProxyEnvironment(cfg.Extra)
	p.config = config{
		providerConfigName: cfg.ProviderConfigName,
		subscriptionID:     firstNonEmpty(secretString(cfg.SecretData, "subscriptionId"), cfg.Extra["subscriptionId"]),
		tenantID:           firstNonEmpty(secretString(cfg.SecretData, "tenantId"), cfg.Extra["tenantId"]),
		clientID:           firstNonEmpty(secretString(cfg.SecretData, "clientId"), cfg.Extra["clientId"]),
		clientSecret:       secretString(cfg.SecretData, "clientSecret"),
		location:           firstNonEmpty(strings.TrimSpace(cfg.Region), strings.TrimSpace(cfg.Extra["location"])),
		resourceGroup:      strings.TrimSpace(cfg.Extra["resourceGroup"]),
		storageAccount:     strings.TrimSpace(cfg.Extra["storageAccount"]),
		storageAccountKey:  secretString(cfg.SecretData, "storageAccountKey"),
		storageContainer:   firstNonEmpty(strings.TrimSpace(cfg.Extra["storageContainer"]), "imagebuilder"),
		blobPrefix:         strings.Trim(strings.TrimSpace(cfg.Extra["blobPrefix"]), "/"),
		cloudName:          strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Extra["cloud"], "public"))),
		storageEndpoint:    strings.TrimSpace(firstNonEmpty(cfg.Extra["storageEndpoint"], defaultStorageEndpoint(cfg.Extra["cloud"]))),
		armEndpoint:        strings.TrimSpace(firstNonEmpty(cfg.Endpoint, cfg.Extra["armEndpoint"])),
		armAudience:        strings.TrimRight(strings.TrimSpace(firstNonEmpty(cfg.Extra["armAudience"], defaultARMAudience(cfg.Extra["cloud"]))), "/"),
		authMode:           strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.Extra["authMode"], "clientSecret"))),
		managedIdentityID:  strings.TrimSpace(cfg.Extra["managedIdentityID"]),
		tokenFilePath:      strings.TrimSpace(cfg.Extra["tokenFilePath"]),
		authorityHost:      strings.TrimRight(strings.TrimSpace(cfg.Extra["authorityHost"]), "/"),
		imageName:          strings.TrimSpace(cfg.Extra["imageName"]),
		hyperVGeneration:   hyperVGenerationFromExtra(cfg.Extra),
		osState:            osStateFromExtra(cfg.Extra),
		diskSizeGiB:        int32FromExtra(cfg.Extra, "diskSizeGiB", 0),
		storageAccountType: storageAccountTypeFromExtra(cfg.Extra),
		galleryName:        strings.TrimSpace(cfg.Extra["galleryName"]),
		galleryImageName:   strings.TrimSpace(cfg.Extra["galleryImageName"]),
		galleryVersion:     strings.TrimSpace(cfg.Extra["galleryVersion"]),
		replicaCount:       int32FromExtra(cfg.Extra, "galleryReplicaCount", 1),
		targetRegions:      splitCSV(firstNonEmpty(cfg.Extra["galleryTargetRegions"], cfg.Extra["targetRegions"])),
		pageUploadWorkers:  int32FromExtra(cfg.Extra, "pageUploadConcurrency", defaultPageWorkers),
		pageUploadChunk:    int64FromExtra(cfg.Extra, "pageUploadChunkMiB", defaultPageChunkSize/(1024*1024)) * 1024 * 1024,
		extraConfig:        cloneStringMap(cfg.Extra),
	}
	if cfg.Insecure {
		return fmt.Errorf("azure plugin: insecure TLS verification is not supported")
	}
	if err := p.config.validate(); err != nil {
		return err
	}
	if p.client == nil {
		c, err := newAzureClient(ctx, p.config)
		if err != nil {
			return fmt.Errorf("azure plugin: initialise client: %w", err)
		}
		p.client = c
	}
	p.log.Info("azure plugin initialised",
		slog.String("subscriptionID", p.config.subscriptionID),
		slog.String("location", p.config.location),
		slog.String("resourceGroup", p.config.resourceGroup),
	)
	return nil
}

func (c config) validate() error {
	if c.subscriptionID == "" {
		return fmt.Errorf("azure plugin: subscriptionId is required in secret or ProviderConfig extra")
	}
	switch c.cloudName {
	case "", "public", "government", "usgovernment", "usgov", "china":
	default:
		return fmt.Errorf("azure plugin: unsupported cloud %q", c.cloudName)
	}
	switch c.authMode {
	case "", "clientsecret":
		if c.tenantID == "" || c.clientID == "" || c.clientSecret == "" {
			return fmt.Errorf("azure plugin: clientSecret auth requires tenantId, clientId, and clientSecret")
		}
	case "workloadidentity":
		if c.tenantID == "" || c.clientID == "" {
			return fmt.Errorf("azure plugin: workloadIdentity auth requires tenantId and clientId")
		}
	case "managedidentity":
	default:
		return fmt.Errorf("azure plugin: unsupported authMode %q", c.authMode)
	}
	if c.location == "" {
		return fmt.Errorf("azure plugin: region or extra location is required")
	}
	if c.resourceGroup == "" {
		return fmt.Errorf("azure plugin: ProviderConfig extra resourceGroup is required")
	}
	if c.storageAccount == "" {
		return fmt.Errorf("azure plugin: storageAccount extra is required")
	}
	if c.storageAccountKey == "" && c.authMode == "clientsecret" {
		return fmt.Errorf("azure plugin: clientSecret auth requires storageAccountKey secret for staging storage")
	}
	if c.storageContainer == "" {
		return fmt.Errorf("azure plugin: storageContainer is required")
	}
	if (c.galleryName == "") != (c.galleryImageName == "") {
		return fmt.Errorf("azure plugin: galleryName and galleryImageName must be set together")
	}
	if c.pageUploadWorkers <= 0 {
		return fmt.Errorf("azure plugin: pageUploadConcurrency must be greater than 0")
	}
	if c.pageUploadChunk <= 0 || c.pageUploadChunk%azurePageSize != 0 {
		return fmt.Errorf("azure plugin: pageUploadChunkMiB must resolve to a positive %d-byte aligned chunk", azurePageSize)
	}
	return nil
}

func (p *Plugin) Validate(_ context.Context, spec v1alpha1.TargetSpec) error {
	if platform.ImageFormat(spec.Format) != platform.FormatVHD {
		return fmt.Errorf("azure plugin: unsupported format %q; use vhd", spec.Format)
	}
	return nil
}

func (p *Plugin) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	if artifact == nil {
		return nil, fmt.Errorf("azure plugin: artifact is required")
	}
	if artifact.Path == "" {
		return nil, fmt.Errorf("azure plugin: artifact path is required")
	}
	if artifact.Format != platform.FormatVHD {
		return nil, fmt.Errorf("azure plugin: unsupported artifact format %q; use vhd", artifact.Format)
	}
	if err := validateFixedVHD(artifact.Path); err != nil {
		return nil, fmt.Errorf("azure plugin: artifact is not an Azure-compatible fixed VHD: %w", err)
	}
	buildID := artifact.Metadata["buildID"]
	if buildID == "" {
		return nil, fmt.Errorf("azure plugin: artifact metadata missing required key 'buildID'")
	}
	client := p.client
	if client == nil {
		return nil, fmt.Errorf("azure plugin: client is not initialised")
	}
	blobName := uploadBlobName(p.config.blobPrefix, buildID, artifact.Metadata["imageName"])
	p.log.Info("uploading artifact to Azure Blob Storage",
		slog.String("path", artifact.Path),
		slog.String("container", p.config.storageContainer),
		slog.String("blob", blobName),
	)
	blobURL, err := client.UploadBlob(ctx, p.config.storageContainer, blobName, artifact.Path)
	if err != nil {
		return nil, fmt.Errorf("azure plugin: upload artifact to blob: %w", err)
	}
	imageName := firstNonEmpty(
		artifact.Metadata["imageName"],
		artifact.Metadata["vmimage"],
		p.config.imageName,
		"imagebuilder-"+sanitizeName(buildID),
	)
	if artifact.Metadata == nil {
		artifact.Metadata = map[string]string{}
	}
	artifact.Metadata["azure.container"] = p.config.storageContainer
	artifact.Metadata["azure.blobName"] = blobName
	artifact.Metadata["azure.blobURL"] = blobURL

	return &platform.UploadResult{
		ProviderRef: blobURL,
		Metadata: map[string]string{
			"buildID":       buildID,
			"imageName":     imageName,
			"format":        string(artifact.Format),
			"checksum":      artifact.Checksum,
			"os":            string(artifact.OS),
			"container":     p.config.storageContainer,
			"blobName":      blobName,
			"blobURL":       blobURL,
			"resourceGroup": p.config.resourceGroup,
			"location":      p.config.location,
		},
	}, nil
}

func (p *Plugin) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	if result == nil {
		return nil, fmt.Errorf("azure plugin: upload result is required")
	}
	client := p.client
	if client == nil {
		return nil, fmt.Errorf("azure plugin: client is not initialised")
	}
	imageName := firstNonEmpty(result.Metadata["imageName"], p.config.imageName)
	if imageName == "" {
		return nil, fmt.Errorf("azure plugin: imageName is required")
	}
	blobURL := firstNonEmpty(result.Metadata["blobURL"], result.ProviderRef)
	if blobURL == "" {
		return nil, fmt.Errorf("azure plugin: blob URL is required")
	}
	input := registerInput{
		ResourceGroup:      firstNonEmpty(result.Metadata["resourceGroup"], p.config.resourceGroup),
		Location:           firstNonEmpty(result.Metadata["location"], p.config.location),
		ImageName:          imageName,
		BlobURL:            blobURL,
		Format:             platform.ImageFormat(firstNonEmpty(result.Metadata["format"], string(platform.FormatVHD))),
		OS:                 platform.OSFamily(result.Metadata["os"]),
		Checksum:           result.Metadata["checksum"],
		Tags:               tagsFromMetadata(result.Metadata),
		HyperVGeneration:   p.config.hyperVGeneration,
		OSState:            p.config.osState,
		DiskSizeGiB:        p.config.diskSizeGiB,
		StorageAccountType: p.config.storageAccountType,
		GalleryName:        p.config.galleryName,
		GalleryImageName:   p.config.galleryImageName,
		GalleryVersion:     p.config.galleryVersion,
		ReplicaCount:       p.config.replicaCount,
		TargetRegions:      p.config.targetRegions,
	}
	if input.GalleryName != "" {
		input.GalleryVersion = galleryVersionName(input)
		result.Metadata["galleryName"] = input.GalleryName
		result.Metadata["galleryImageName"] = input.GalleryImageName
		result.Metadata["galleryVersion"] = input.GalleryVersion
	}
	ref, err := client.RegisterImage(ctx, input)
	if err != nil {
		cleanupErr := client.Cleanup(ctx, result.Metadata)
		if cleanupErr != nil {
			return nil, fmt.Errorf("azure plugin: register image: %w; cleanup failed: %v", err, cleanupErr)
		}
		return nil, fmt.Errorf("azure plugin: register image: %w", err)
	}
	result.Metadata["imageId"] = ref.ID
	return ref, nil
}

func (p *Plugin) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	if artifact == nil || artifact.Metadata == nil || p.client == nil {
		return nil
	}
	return p.client.Cleanup(ctx, artifact.Metadata)
}

func (p *Plugin) HealthCheck(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	return p.client.HealthCheck(ctx)
}

func (p *Plugin) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	if req == nil {
		return nil, fmt.Errorf("azure plugin: remote build request is required")
	}
	if platform.ImageFormat(req.Target.Format) != platform.FormatVHD {
		return nil, fmt.Errorf("azure plugin: remote snapshot source requires target format %q, got %q", platform.FormatVHD, req.Target.Format)
	}
	sourceType := strings.ToLower(strings.TrimSpace(req.SourceType))
	sourceRef := strings.TrimSpace(firstNonEmpty(req.SourceProviderRef, req.SourceURL))
	if sourceType == "marketplace" {
		if err := validateMarketplaceRef(req.SourceMarketplace); err != nil {
			return nil, err
		}
	} else if sourceRef == "" {
		return nil, fmt.Errorf("azure plugin: remote source requires source providerRef")
	}
	if len(req.Provisioners) > 0 || sourceType == "marketplace" {
		if p.client == nil {
			return nil, fmt.Errorf("azure plugin: client is not initialised")
		}
		state, err := p.client.ReconcileRemoteBuild(ctx, azureRemoteBuildInput{
			BuildID:            req.BuildID,
			OperationRef:       req.OperationRef,
			ImageName:          firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeName(req.BuildID)),
			SourceType:         sourceType,
			SourceRef:          sourceRef,
			SourceMarketplace:  req.SourceMarketplace,
			SourceChecksum:     req.SourceChecksum,
			OSFamily:           req.OSFamily,
			Format:             platform.ImageFormat(req.Target.Format),
			Tags:               req.Target.Tags,
			ProviderConfigName: req.Target.ProviderConfigRef.Name,
			Provisioners:       req.Provisioners,
			GuestAccess:        req.GuestAccess,
		})
		if err != nil {
			return nil, err
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
				return nil, fmt.Errorf("azure plugin: completed remote build did not return an image reference")
			}
			result.Images = []platform.RemoteImageRef{{
				Provider:       p.Name(),
				ProviderConfig: req.Target.ProviderConfigRef.Name,
				ImageRef:       *state.Image,
				Format:         platform.ImageFormat(req.Target.Format),
				Checksum:       req.SourceChecksum,
			}}
		}
		return result, nil
	}
	input := registerInput{
		ResourceGroup:      p.config.resourceGroup,
		Location:           p.config.location,
		ImageName:          firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeName(req.BuildID)),
		Format:             platform.ImageFormat(req.Target.Format),
		OS:                 req.OSFamily,
		Tags:               req.Target.Tags,
		HyperVGeneration:   p.config.hyperVGeneration,
		OSState:            p.config.osState,
		DiskSizeGiB:        p.config.diskSizeGiB,
		StorageAccountType: p.config.storageAccountType,
		GalleryName:        p.config.galleryName,
		GalleryImageName:   p.config.galleryImageName,
		GalleryVersion:     p.config.galleryVersion,
		ReplicaCount:       p.config.replicaCount,
		TargetRegions:      p.config.targetRegions,
	}
	switch sourceType {
	case "snapshot":
		input.SnapshotID = sourceRef
	case "managed-disk", "manageddisk", "disk":
		input.ManagedDiskID = sourceRef
	default:
		return nil, fmt.Errorf("azure plugin: unsupported remote source type %q", req.SourceType)
	}
	if p.client == nil {
		return nil, fmt.Errorf("azure plugin: client is not initialised")
	}
	ref, err := p.client.RegisterImage(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("azure plugin: register remote source image: %w", err)
	}
	return &platform.RemoteBuildResult{
		OperationRef: "azure:" + sourceType + ":" + sourceRef,
		Phase:        platform.RemoteBuildPhaseReady,
		Message:      "Azure image registered from existing source",
		Done:         true,
		Images: []platform.RemoteImageRef{{
			Provider:       p.Name(),
			ProviderConfig: req.Target.ProviderConfigRef.Name,
			ImageRef:       *ref,
			Format:         platform.ImageFormat(req.Target.Format),
			Checksum:       req.SourceChecksum,
		}},
		Hygiene: &platform.RemoteHygieneResult{
			Status:    "passed",
			Message:   "Azure source registered without provider-side boot",
			Checks:    []string{"azure-source-reference-present", "azure-managed-image-registered"},
			ResultRef: ref.ID,
		},
	}, nil
}

func (p *Plugin) CleanupRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) error {
	if req == nil || p.client == nil {
		return nil
	}
	sourceType := strings.ToLower(strings.TrimSpace(req.SourceType))
	sourceRef := strings.TrimSpace(firstNonEmpty(req.SourceProviderRef, req.SourceURL))
	if len(req.Provisioners) > 0 || sourceType == "marketplace" || strings.Contains(req.OperationRef, "azure://remote-build/") {
		return p.client.CleanupRemoteBuild(ctx, azureRemoteBuildInput{
			BuildID:           req.BuildID,
			OperationRef:      req.OperationRef,
			ImageName:         firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeName(req.BuildID)),
			SourceType:        sourceType,
			SourceRef:         sourceRef,
			SourceMarketplace: req.SourceMarketplace,
			OSFamily:          req.OSFamily,
			Format:            platform.ImageFormat(req.Target.Format),
			Tags:              req.Target.Tags,
			Provisioners:      req.Provisioners,
			GuestAccess:       req.GuestAccess,
		})
	}
	imageName := firstNonEmpty(req.ImageName, "imagebuilder-"+sanitizeName(req.BuildID))
	return p.client.Cleanup(ctx, map[string]string{
		"resourceGroup":     p.config.resourceGroup,
		"imageName":         imageName,
		"galleryName":       p.config.galleryName,
		"galleryImageName":  p.config.galleryImageName,
		"galleryVersion":    p.config.galleryVersion,
		"azure.cleanupMode": "remote",
	})
}

type sdkClient struct {
	cfg             config
	images          *armcompute.ImagesClient
	galleryVersions *armcompute.GalleryImageVersionsClient
	vms             *armcompute.VirtualMachinesClient
	disks           *armcompute.DisksClient
	blobs           *azblob.Client
	tokenCredential azcore.TokenCredential
	sharedKey       *azblob.SharedKeyCredential
	storageURL      string
}

func newAzureClient(ctx context.Context, cfg config) (client, error) {
	credential, err := tokenCredential(cfg)
	if err != nil {
		return nil, err
	}
	armOptions, err := armClientOptions(cfg)
	if err != nil {
		return nil, err
	}
	images, err := armcompute.NewImagesClient(cfg.subscriptionID, credential, armOptions)
	if err != nil {
		return nil, fmt.Errorf("create Azure images client: %w", err)
	}
	galleryVersions, err := armcompute.NewGalleryImageVersionsClient(cfg.subscriptionID, credential, armOptions)
	if err != nil {
		return nil, fmt.Errorf("create Azure gallery image versions client: %w", err)
	}
	vms, err := armcompute.NewVirtualMachinesClient(cfg.subscriptionID, credential, armOptions)
	if err != nil {
		return nil, fmt.Errorf("create Azure virtual machines client: %w", err)
	}
	disks, err := armcompute.NewDisksClient(cfg.subscriptionID, credential, armOptions)
	if err != nil {
		return nil, fmt.Errorf("create Azure disks client: %w", err)
	}
	blobs, sharedKey, storageURL, err := blobClient(cfg, credential)
	if err != nil {
		return nil, err
	}
	client := &sdkClient{
		cfg:             cfg,
		images:          images,
		galleryVersions: galleryVersions,
		vms:             vms,
		disks:           disks,
		blobs:           blobs,
		tokenCredential: credential,
		sharedKey:       sharedKey,
		storageURL:      storageURL,
	}
	if err := client.HealthCheck(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func tokenCredential(cfg config) (azcore.TokenCredential, error) {
	options := credentialClientOptions(cfg)
	switch cfg.authMode {
	case "", "clientsecret":
		credential, err := azidentity.NewClientSecretCredential(cfg.tenantID, cfg.clientID, cfg.clientSecret, options)
		if err != nil {
			return nil, fmt.Errorf("create Azure service principal credential: %w", err)
		}
		return credential, nil
	case "workloadidentity":
		credential, err := azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{
			ClientOptions: options.ClientOptions,
			TenantID:      cfg.tenantID,
			ClientID:      cfg.clientID,
			TokenFilePath: cfg.tokenFilePath,
		})
		if err != nil {
			return nil, fmt.Errorf("create Azure workload identity credential: %w", err)
		}
		return credential, nil
	case "managedidentity":
		miOptions := &azidentity.ManagedIdentityCredentialOptions{ClientOptions: options.ClientOptions}
		if cfg.managedIdentityID != "" {
			miOptions.ID = azidentity.ClientID(cfg.managedIdentityID)
		}
		credential, err := azidentity.NewManagedIdentityCredential(miOptions)
		if err != nil {
			return nil, fmt.Errorf("create Azure managed identity credential: %w", err)
		}
		return credential, nil
	default:
		return nil, fmt.Errorf("unsupported Azure authMode %q", cfg.authMode)
	}
}

func credentialClientOptions(cfg config) *azidentity.ClientSecretCredentialOptions {
	options := &azidentity.ClientSecretCredentialOptions{}
	authorityHost := firstNonEmpty(cfg.authorityHost, defaultAuthorityHost(cfg.cloudName))
	if authorityHost != "" {
		options.Cloud.ActiveDirectoryAuthorityHost = ensureTrailingSlash(authorityHost)
	}
	return options
}

func armClientOptions(cfg config) (*arm.ClientOptions, error) {
	endpoint := firstNonEmpty(cfg.armEndpoint, defaultARMEndpoint(cfg.cloudName))
	if endpoint == "" {
		return nil, nil
	}
	audience := ensureTrailingSlash(firstNonEmpty(cfg.armAudience, defaultARMAudience(cfg.cloudName), "https://management.azure.com"))
	authorityHost := ensureTrailingSlash(firstNonEmpty(cfg.authorityHost, defaultAuthorityHost(cfg.cloudName), cloud.AzurePublic.ActiveDirectoryAuthorityHost))
	if strings.HasPrefix(strings.ToLower(endpoint), "http://") {
		return nil, fmt.Errorf("insecure ARM endpoint is not supported")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Cloud: cloud.Configuration{
				ActiveDirectoryAuthorityHost: authorityHost,
				Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					cloud.ResourceManager: {
						Audience: audience,
						Endpoint: endpoint,
					},
				},
			},
		},
	}, nil
}

func blobClient(cfg config, tokenCredential azcore.TokenCredential) (*azblob.Client, *azblob.SharedKeyCredential, string, error) {
	endpoint := cfg.storageEndpoint
	if strings.HasPrefix(strings.ToLower(endpoint), "http://") {
		return nil, nil, "", fmt.Errorf("insecure Azure Storage endpoint is not supported")
	}
	if !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		endpoint = "https://" + cfg.storageAccount + "." + strings.TrimPrefix(endpoint, ".")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if cfg.storageAccountKey != "" {
		credential, err := azblob.NewSharedKeyCredential(cfg.storageAccount, cfg.storageAccountKey)
		if err != nil {
			return nil, nil, "", fmt.Errorf("create Azure Storage shared key credential: %w", err)
		}
		client, err := azblob.NewClientWithSharedKeyCredential(endpoint, credential, nil)
		if err != nil {
			return nil, nil, "", fmt.Errorf("create Azure Blob client: %w", err)
		}
		return client, credential, endpoint, nil
	}
	client, err := azblob.NewClient(endpoint, tokenCredential, nil)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create Azure Blob client: %w", err)
	}
	return client, nil, endpoint, nil
}

func (c *sdkClient) UploadBlob(ctx context.Context, container, blobName, filePath string) (blobURL string, retErr error) {
	start := time.Now()
	defer func() { observeAzureOperation("upload_blob", start, retErr) }()
	if container != c.cfg.storageContainer {
		return "", fmt.Errorf("configured container mismatch: got %q want %q", container, c.cfg.storageContainer)
	}
	if _, err := c.blobs.CreateContainer(ctx, container, nil); err != nil {
		if !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
			return "", fmt.Errorf("ensure container exists: %w", err)
		}
	}
	file, err := os.Open(filePath) // #nosec G304 -- Artifact path is supplied by the controller-owned build result.
	if err != nil {
		return "", fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat artifact: %w", err)
	}
	if err := c.uploadPageBlob(ctx, container, blobName, file, info.Size()); err != nil {
		return "", err
	}
	return c.blobURL(container, blobName), nil
}

func (c *sdkClient) uploadPageBlob(ctx context.Context, container, blobName string, file *os.File, size int64) error {
	if size <= 0 || size%azurePageSize != 0 {
		return fmt.Errorf("fixed VHD size must be a positive multiple of %d bytes, got %d", azurePageSize, size)
	}
	client, err := c.pageBlobClient(container, blobName)
	if err != nil {
		return err
	}
	if _, err := client.Create(ctx, size, nil); err != nil {
		if !bloberror.HasCode(err, bloberror.BlobAlreadyExists) {
			return fmt.Errorf("create page blob: %w", err)
		}
		if _, deleteErr := c.blobs.DeleteBlob(ctx, container, blobName, &azblob.DeleteBlobOptions{
			DeleteSnapshots: to.Ptr(azblob.DeleteSnapshotsOptionTypeInclude),
		}); deleteErr != nil && !bloberror.HasCode(deleteErr, bloberror.BlobNotFound) {
			return fmt.Errorf("replace existing page blob: %w", deleteErr)
		}
		if _, err := client.Create(ctx, size, nil); err != nil {
			return fmt.Errorf("recreate page blob: %w", err)
		}
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(int(c.cfg.pageUploadWorkers))
	for offset := int64(0); offset < size; offset += c.cfg.pageUploadChunk {
		offset := offset
		count := minInt64(c.cfg.pageUploadChunk, size-offset)
		group.Go(func() error {
			reader := &sectionReadSeekCloser{SectionReader: io.NewSectionReader(file, offset, count)}
			if _, err := client.UploadPages(groupCtx, reader, blob.HTTPRange{Offset: offset, Count: count}, nil); err != nil {
				azurePageUploadRanges.WithLabelValues(container, "false").Inc()
				return fmt.Errorf("upload page range offset=%d count=%d: %w", offset, count, err)
			}
			azurePageUploadBytes.WithLabelValues(container).Add(float64(count))
			azurePageUploadRanges.WithLabelValues(container, "true").Inc()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return nil
}

func (c *sdkClient) pageBlobClient(container, blobName string) (*pageblob.Client, error) {
	blobURL := c.blobURL(container, blobName)
	if c.sharedKey != nil {
		client, err := pageblob.NewClientWithSharedKeyCredential(blobURL, c.sharedKey, nil)
		if err != nil {
			return nil, fmt.Errorf("create page blob client: %w", err)
		}
		return client, nil
	}
	client, err := pageblob.NewClient(blobURL, c.tokenCredential, nil)
	if err != nil {
		return nil, fmt.Errorf("create page blob client: %w", err)
	}
	return client, nil
}

func (c *sdkClient) blobURL(container, blobName string) string {
	return c.storageURL + "/" + url.PathEscape(container) + "/" + url.PathEscape(blobName)
}

func (c *sdkClient) RegisterImage(ctx context.Context, input registerInput) (ref *platform.ImageRef, retErr error) {
	start := time.Now()
	defer func() { observeAzureOperation("register_image", start, retErr) }()
	image := armcompute.Image{
		Location: to.Ptr(input.Location),
		Tags:     azureTags(input.Tags),
		Properties: &armcompute.ImageProperties{
			StorageProfile: &armcompute.ImageStorageProfile{
				OSDisk: &armcompute.ImageOSDisk{
					OSType:             to.Ptr(azureOSType(input.OS)),
					OSState:            to.Ptr(input.OSState),
					BlobURI:            to.Ptr(input.BlobURL),
					Caching:            to.Ptr(armcompute.CachingTypesReadWrite),
					StorageAccountType: to.Ptr(input.StorageAccountType),
				},
			},
			HyperVGeneration:     to.Ptr(input.HyperVGeneration),
			SourceVirtualMachine: sourceVirtualMachine(input.SourceVirtualMachineID),
		},
	}
	if input.SourceVirtualMachineID != "" {
		image.Properties.StorageProfile = nil
	}
	switch {
	case input.SnapshotID != "":
		image.Properties.StorageProfile.OSDisk.BlobURI = nil
		image.Properties.StorageProfile.OSDisk.Snapshot = &armcompute.SubResource{ID: to.Ptr(input.SnapshotID)}
	case input.ManagedDiskID != "":
		image.Properties.StorageProfile.OSDisk.BlobURI = nil
		image.Properties.StorageProfile.OSDisk.ManagedDisk = &armcompute.SubResource{ID: to.Ptr(input.ManagedDiskID)}
	}
	if input.DiskSizeGiB > 0 {
		image.Properties.StorageProfile.OSDisk.DiskSizeGB = to.Ptr(input.DiskSizeGiB)
	}
	poller, err := c.images.BeginCreateOrUpdate(ctx, input.ResourceGroup, input.ImageName, image, nil)
	if err != nil {
		return nil, fmt.Errorf("create managed image %q: %w", input.ImageName, err)
	}
	created, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("wait for managed image %q: %w", input.ImageName, err)
	}
	imageID := firstNonEmpty(value(created.ID), managedImageID(c.cfg.subscriptionID, input.ResourceGroup, input.ImageName))
	ref = &platform.ImageRef{
		ID:       imageID,
		Name:     input.ImageName,
		Location: input.Location,
		Tags:     input.Tags,
	}
	if input.GalleryName != "" {
		galleryRef, err := c.publishGalleryVersion(ctx, input, imageID)
		if err != nil {
			return nil, err
		}
		ref = galleryRef
	}
	return ref, nil
}

func (c *sdkClient) publishGalleryVersion(ctx context.Context, input registerInput, imageID string) (*platform.ImageRef, error) {
	version := galleryVersionName(input)
	targetRegions := []*armcompute.TargetRegion{{Name: to.Ptr(input.Location)}}
	if len(input.TargetRegions) > 0 {
		targetRegions = make([]*armcompute.TargetRegion, 0, len(input.TargetRegions))
		for _, region := range input.TargetRegions {
			targetRegions = append(targetRegions, &armcompute.TargetRegion{Name: to.Ptr(region)})
		}
	}
	galleryVersion := armcompute.GalleryImageVersion{
		Location: to.Ptr(input.Location),
		Tags:     azureTags(input.Tags),
		Properties: &armcompute.GalleryImageVersionProperties{
			StorageProfile: &armcompute.GalleryImageVersionStorageProfile{
				Source: &armcompute.GalleryArtifactVersionSource{ID: to.Ptr(imageID)},
			},
			PublishingProfile: &armcompute.GalleryImageVersionPublishingProfile{
				TargetRegions: targetRegions,
				ReplicaCount:  to.Ptr(input.ReplicaCount),
			},
		},
	}
	poller, err := c.galleryVersions.BeginCreateOrUpdate(ctx, input.ResourceGroup, input.GalleryName, input.GalleryImageName, version, galleryVersion, nil)
	if err != nil {
		return nil, fmt.Errorf("create compute gallery image version %q/%q/%q: %w", input.GalleryName, input.GalleryImageName, version, err)
	}
	created, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("wait for compute gallery image version %q/%q/%q: %w", input.GalleryName, input.GalleryImageName, version, err)
	}
	return &platform.ImageRef{
		ID:       firstNonEmpty(value(created.ID), galleryImageVersionID(c.cfg.subscriptionID, input.ResourceGroup, input.GalleryName, input.GalleryImageName, version)),
		Name:     input.GalleryImageName + "/" + version,
		Location: input.Location,
		Tags:     input.Tags,
	}, nil
}

func (c *sdkClient) Cleanup(ctx context.Context, metadata map[string]string) (retErr error) {
	start := time.Now()
	defer func() { observeAzureOperation("cleanup", start, retErr) }()
	if metadata == nil {
		return nil
	}
	var errs []error
	if galleryName := firstNonEmpty(metadata["galleryName"], metadata["azure.galleryName"]); galleryName != "" {
		galleryImageName := firstNonEmpty(metadata["galleryImageName"], metadata["azure.galleryImageName"])
		galleryVersion := firstNonEmpty(metadata["galleryVersion"], metadata["azure.galleryVersion"])
		if galleryImageName != "" && galleryVersion != "" {
			poller, err := c.galleryVersions.BeginDelete(ctx, firstNonEmpty(metadata["resourceGroup"], c.cfg.resourceGroup), galleryName, galleryImageName, galleryVersion, nil)
			if err != nil {
				if !isHTTPStatus(err, http.StatusNotFound) {
					errs = append(errs, fmt.Errorf("delete compute gallery image version %q/%q/%q: %w", galleryName, galleryImageName, galleryVersion, err))
				}
			} else if _, err := poller.PollUntilDone(ctx, nil); err != nil {
				if !isHTTPStatus(err, http.StatusNotFound) {
					errs = append(errs, fmt.Errorf("wait for compute gallery image version delete %q/%q/%q: %w", galleryName, galleryImageName, galleryVersion, err))
				}
			}
		}
	}
	if imageName := firstNonEmpty(metadata["imageName"], metadata["azure.imageName"]); imageName != "" {
		poller, err := c.images.BeginDelete(ctx, firstNonEmpty(metadata["resourceGroup"], c.cfg.resourceGroup), imageName, nil)
		if err != nil {
			if !isHTTPStatus(err, http.StatusNotFound) {
				errs = append(errs, fmt.Errorf("delete managed image %q: %w", imageName, err))
			}
		} else if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			if !isHTTPStatus(err, http.StatusNotFound) {
				errs = append(errs, fmt.Errorf("wait for managed image delete %q: %w", imageName, err))
			}
		}
	}
	if blobName := firstNonEmpty(metadata["blobName"], metadata["azure.blobName"]); blobName != "" {
		if _, err := c.blobs.DeleteBlob(ctx, c.cfg.storageContainer, blobName, &azblob.DeleteBlobOptions{
			DeleteSnapshots: to.Ptr(azblob.DeleteSnapshotsOptionTypeInclude),
		}); err != nil {
			if !bloberror.HasCode(err, bloberror.BlobNotFound) {
				errs = append(errs, fmt.Errorf("delete blob %q: %w", blobName, err))
			}
		}
	}
	retErr = errors.Join(errs...)
	return retErr
}

func (c *sdkClient) HealthCheck(ctx context.Context) (retErr error) {
	start := time.Now()
	defer func() { observeAzureOperation("health_check", start, retErr) }()
	_, err := c.images.Get(ctx, c.cfg.resourceGroup, "__imagebuilder-healthcheck__", nil)
	if err == nil {
		return nil
	}
	if isHTTPStatus(err, http.StatusNotFound) {
		return nil
	}
	return fmt.Errorf("azure compute health check: %w", err)
}

func azureOSType(os platform.OSFamily) armcompute.OperatingSystemTypes {
	if os == platform.OSFamilyWindows {
		return armcompute.OperatingSystemTypesWindows
	}
	return armcompute.OperatingSystemTypesLinux
}

func sourceVirtualMachine(id string) *armcompute.SubResource {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return &armcompute.SubResource{ID: to.Ptr(strings.TrimSpace(id))}
}

func galleryVersionName(input registerInput) string {
	return firstNonEmpty(input.GalleryVersion, "1.0."+strconv.FormatInt(time.Now().Unix(), 10))
}

func uploadBlobName(prefix, buildID, imageName string) string {
	name := firstNonEmpty(imageName, "imagebuilder-"+sanitizeName(buildID))
	fileName := sanitizeName(name) + ".vhd"
	if prefix == "" {
		return path.Join(sanitizeName(buildID), fileName)
	}
	return path.Join(prefix, sanitizeName(buildID), fileName)
}

func tagsFromMetadata(metadata map[string]string) map[string]string {
	tags := map[string]string{}
	for key, value := range metadata {
		if strings.HasPrefix(key, "tag.") {
			tags[strings.TrimPrefix(key, "tag.")] = value
		}
	}
	return tags
}

func azureTags(tags map[string]string) map[string]*string {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]*string, len(tags))
	for key, value := range tags {
		v := value
		out[key] = &v
	}
	return out
}

func hyperVGenerationFromExtra(extra map[string]string) armcompute.HyperVGenerationTypes {
	switch strings.ToUpper(strings.TrimSpace(extra["hyperVGeneration"])) {
	case "V1":
		return armcompute.HyperVGenerationTypesV1
	case "V2":
		return armcompute.HyperVGenerationTypesV2
	default:
		return armcompute.HyperVGenerationTypesV2
	}
}

func osStateFromExtra(extra map[string]string) armcompute.OperatingSystemStateTypes {
	if strings.EqualFold(strings.TrimSpace(extra["osState"]), "specialized") {
		return armcompute.OperatingSystemStateTypesSpecialized
	}
	return armcompute.OperatingSystemStateTypesGeneralized
}

func storageAccountTypeFromExtra(extra map[string]string) armcompute.StorageAccountTypes {
	switch strings.ToLower(strings.TrimSpace(extra["storageAccountType"])) {
	case "premium_lrs", "premiumlrs":
		return armcompute.StorageAccountTypesPremiumLRS
	case "standardssd_lrs", "standardssdlrs":
		return armcompute.StorageAccountTypesStandardSSDLRS
	case "ultrassd_lrs", "ultrassdlrs":
		return armcompute.StorageAccountTypesUltraSSDLRS
	default:
		return armcompute.StorageAccountTypesStandardLRS
	}
}

func value(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func isHTTPStatus(err error, status int) bool {
	var responseErr *azcore.ResponseError
	return errors.As(err, &responseErr) && responseErr.StatusCode == status
}

type sectionReadSeekCloser struct {
	*io.SectionReader
}

func (s *sectionReadSeekCloser) Close() error {
	return nil
}

func validateFixedVHD(filePath string) error {
	file, err := os.Open(filePath) // #nosec G304 -- Artifact path is supplied by the controller-owned build result.
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat artifact: %w", err)
	}
	size := info.Size()
	if size < vhdFooterSize {
		return fmt.Errorf("file is too small: %d bytes", size)
	}
	if size%azurePageSize != 0 {
		return fmt.Errorf("file size %d is not aligned to %d-byte Azure page boundaries", size, azurePageSize)
	}
	footer := make([]byte, vhdFooterSize)
	if _, err := file.ReadAt(footer, size-vhdFooterSize); err != nil {
		return fmt.Errorf("read VHD footer: %w", err)
	}
	if string(footer[0:8]) != vhdCookie {
		return fmt.Errorf("missing VHD footer cookie")
	}
	if diskType := binary.BigEndian.Uint32(footer[60:64]); diskType != vhdDiskTypeFixed {
		return fmt.Errorf("unsupported VHD disk type %d; fixed VHD is required", diskType)
	}
	currentSize := binary.BigEndian.Uint64(footer[48:56])
	if currentSize == 0 {
		return fmt.Errorf("VHD footer current size is empty")
	}
	sizeWithoutFooter := uint64(size - vhdFooterSize) // #nosec G115 -- size is positive and checked above.
	if currentSize != sizeWithoutFooter {
		return fmt.Errorf("fixed VHD current size %d does not match file size %d", currentSize, size)
	}
	if err := validateVHDFooterChecksum(footer); err != nil {
		return err
	}
	return nil
}

func validateVHDFooterChecksum(footer []byte) error {
	got := binary.BigEndian.Uint32(footer[64:68])
	copyFooter := append([]byte(nil), footer...)
	for i := 64; i < 68; i++ {
		copyFooter[i] = 0
	}
	var sum uint32
	for _, b := range copyFooter {
		sum += uint32(b)
	}
	want := ^sum
	if got != want {
		return fmt.Errorf("invalid VHD footer checksum")
	}
	return nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func int32FromExtra(extra map[string]string, key string, defaultValue int32) int32 {
	value := strings.TrimSpace(extra[key])
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 0 {
		return defaultValue
	}
	return int32(parsed)
}

func int64FromExtra(extra map[string]string, key string, defaultValue int64) int64 {
	value := strings.TrimSpace(extra[key])
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return defaultValue
	}
	return parsed
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func defaultStorageEndpoint(cloudName string) string {
	switch strings.ToLower(strings.TrimSpace(cloudName)) {
	case "government", "usgovernment", "usgov":
		return "blob.core.usgovcloudapi.net"
	case "china":
		return "blob.core.chinacloudapi.cn"
	default:
		return "blob.core.windows.net"
	}
}

func defaultARMEndpoint(cloudName string) string {
	switch strings.ToLower(strings.TrimSpace(cloudName)) {
	case "government", "usgovernment", "usgov":
		return "https://management.usgovcloudapi.net"
	case "china":
		return "https://management.chinacloudapi.cn"
	default:
		return ""
	}
}

func defaultARMAudience(cloudName string) string {
	switch strings.ToLower(strings.TrimSpace(cloudName)) {
	case "government", "usgovernment", "usgov":
		return "https://management.usgovcloudapi.net"
	case "china":
		return "https://management.chinacloudapi.cn"
	default:
		return ""
	}
}

func defaultAuthorityHost(cloudName string) string {
	switch strings.ToLower(strings.TrimSpace(cloudName)) {
	case "government", "usgovernment", "usgov":
		return "https://login.microsoftonline.us"
	case "china":
		return "https://login.chinacloudapi.cn"
	default:
		return ""
	}
}

func ensureTrailingSlash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasSuffix(value, "/") {
		return value
	}
	return value + "/"
}

func secretString(secret map[string][]byte, key string) string {
	if secret == nil {
		return ""
	}
	return strings.TrimSpace(string(secret[key]))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sanitizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "image"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func managedImageID(subscriptionID, resourceGroup, imageName string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/images/%s", subscriptionID, resourceGroup, imageName)
}

func galleryImageVersionID(subscriptionID, resourceGroup, galleryName, galleryImageName, version string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/galleries/%s/images/%s/versions/%s", subscriptionID, resourceGroup, galleryName, galleryImageName, version)
}
