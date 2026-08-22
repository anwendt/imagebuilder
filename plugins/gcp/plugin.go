package gcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	cloudauth "cloud.google.com/go/auth"
	gcpcredentials "cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"cloud.google.com/go/storage"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/plugin"
	"github.com/anwendt/imagebuilder/pkg/plugin/platform"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
	"github.com/anwendt/imagebuilder/pkg/security/netguard"
)

const defaultGCSObjectPrefix = "imagebuilder"

const gcsResumeChunkSize int64 = 16 * 1024 * 1024

type gcsResumeSession struct {
	SessionURI string    `json:"sessionUri"`
	Bucket     string    `json:"bucket"`
	Object     string    `json:"object"`
	Size       int64     `json:"size"`
	Offset     int64     `json:"offset"`
	CreatedAt  time.Time `json:"createdAt"`
}

var errGCSResumeSessionExpired = errors.New("GCS resumable session expired or no longer exists")

func init() {
	if err := plugin.RegisterFactory(&Plugin{}, func() platform.Plugin { return &Plugin{} }); err != nil {
		panic(fmt.Sprintf("gcp plugin: %v", err))
	}
}

type Plugin struct {
	log    *slog.Logger
	config config
	client imageClient
}

type config struct {
	providerConfigName string
	project            string
	bucket             string
	objectPrefix       string
	storageLocation    string
	imageName          string
	imageFamily        string
	description        string
	architecture       string
	guestOSFeatures    []string
	licenses           []string
	retainUpload       bool
	extra              map[string]string
}

type imageClient interface {
	UploadObject(ctx context.Context, bucket, object, filePath string) (string, error)
	CreateImageFromObject(ctx context.Context, input createImageInput) (*platform.ImageRef, error)
	CreateImageFromSource(ctx context.Context, input createImageInput) (*platform.ImageRef, error)
	GetImage(ctx context.Context, name string) (*platform.ImageRef, error)
	DeleteImage(ctx context.Context, name string) error
	DeleteObject(ctx context.Context, bucket, object string) error
	HealthCheck(ctx context.Context) error
}

type createImageInput struct {
	Project          string
	Name             string
	Family           string
	Description      string
	Architecture     string
	GuestOSFeatures  []string
	Licenses         []string
	Labels           map[string]string
	StorageLocations []string
	GCSURI           string
	SourceImage      string
	SourceSnapshot   string
	RequestID        string
}

type operationError struct {
	operation string
	codes     []string
	message   string
}

func (e *operationError) Error() string {
	return fmt.Sprintf("GCP operation %s failed: %s", e.operation, e.message)
}
func (e *operationError) ErrorCode() string { return strings.Join(e.codes, ",") }

var _ platform.RemoteBuildCleanupPlugin = (*Plugin)(nil)
var _ platform.ClosePlugin = (*Plugin)(nil)
var _ platform.ResumablePlugin = (*Plugin)(nil)

func (p *Plugin) Name() string    { return "gcp" }
func (p *Plugin) Version() string { return "v0.4.0" }
func (p *Plugin) SupportedFormats() []platform.ImageFormat {
	return []platform.ImageFormat{platform.FormatGCETarball}
}
func (p *Plugin) SupportedOS() []platform.OSFamily {
	return []platform.OSFamily{platform.OSFamilyLinux, platform.OSFamilyWindows}
}
func (p *Plugin) SupportedBuildModes() []string {
	return []string{v1alpha1.BuildModeLocal, v1alpha1.BuildModeRemote}
}

func (p *Plugin) Close() error {
	if client, ok := p.client.(interface{ Close() error }); ok {
		return client.Close()
	}
	return nil
}

func (p *Plugin) Init(ctx context.Context, cfg platform.PluginConfig) error {
	p.log = slog.Default().With(slog.String("plugin", p.Name()))
	p.config = config{
		providerConfigName: cfg.ProviderConfigName,
		project:            firstNonEmpty(strings.TrimSpace(cfg.Extra["project"]), credentialProject(cfg.SecretData)),
		bucket:             strings.TrimSpace(cfg.Extra["gcsBucket"]),
		objectPrefix:       strings.Trim(strings.TrimSpace(firstNonEmpty(cfg.Extra["gcsPrefix"], defaultGCSObjectPrefix)), "/"),
		storageLocation:    strings.TrimSpace(firstNonEmpty(cfg.Extra["storageLocation"], cfg.Region)),
		imageName:          strings.TrimSpace(cfg.Extra["imageName"]),
		imageFamily:        strings.TrimSpace(cfg.Extra["imageFamily"]),
		description:        strings.TrimSpace(cfg.Extra["description"]),
		architecture:       strings.TrimSpace(cfg.Extra["architecture"]),
		guestOSFeatures:    splitCSV(cfg.Extra["guestOsFeatures"]),
		licenses:           splitCSV(cfg.Extra["licenses"]),
		retainUpload:       boolValue(cfg.Extra["retainUpload"]),
		extra:              cloneStringMap(cfg.Extra),
	}
	if p.config.providerConfigName == "" {
		return fmt.Errorf("gcp plugin: provider config name is required")
	}
	if p.config.project == "" {
		return fmt.Errorf("gcp plugin: ProviderConfig extra project or credentials projectId is required")
	}
	endpointOptions := netguard.Options{AllowedPrivateCIDRs: cfg.AllowedPrivateCIDRs, AllowedDNSNames: cfg.AllowedDNSNames}
	for _, endpoint := range []struct{ name, value string }{
		{"endpoint", cfg.Endpoint},
		{"storageEndpoint", cfg.Extra["storageEndpoint"]},
		{"computeEndpoint", cfg.Extra["computeEndpoint"]},
		{"storageUploadEndpoint", cfg.Extra["storageUploadEndpoint"]},
	} {
		if err := netguard.ValidatePublicHTTPSURL(ctx, "gcp "+endpoint.name, endpoint.value, endpointOptions); err != nil {
			return fmt.Errorf("gcp plugin: endpoint rejected: %w", err)
		}
	}
	client, err := newSDKClient(ctx, cfg, p.config)
	if err != nil {
		return fmt.Errorf("gcp plugin: initialise client: %w", classifyError(err))
	}
	p.client = client
	return nil
}

func (p *Plugin) Validate(_ context.Context, spec v1alpha1.TargetSpec) error {
	if platform.ImageFormat(spec.Format) != platform.FormatGCETarball {
		return fmt.Errorf("gcp plugin: unsupported format %q; use gcetarball", spec.Format)
	}
	if p.config.bucket == "" {
		return fmt.Errorf("gcp plugin: ProviderConfig extra gcsBucket is required for local GCP uploads")
	}
	return validateTargetLabels(spec.Tags)
}

func (p *Plugin) Upload(ctx context.Context, artifact *platform.BuildArtifact) (*platform.UploadResult, error) {
	if artifact == nil || artifact.Path == "" {
		return nil, fmt.Errorf("gcp plugin: artifact path is required")
	}
	if artifact.Format != platform.FormatGCETarball {
		return nil, fmt.Errorf("gcp plugin: artifact must be a gcetarball, got %q", artifact.Format)
	}
	if p.client == nil {
		return nil, fmt.Errorf("gcp plugin: client is not initialised")
	}
	buildID := strings.TrimSpace(artifact.Metadata["buildID"])
	if buildID == "" {
		return nil, fmt.Errorf("gcp plugin: artifact metadata missing required key buildID")
	}
	object := path.Join(p.config.objectPrefix, sanitizeName(buildID)+"-"+checksumToken(artifact.Checksum)+".tar.gz")
	uri, err := p.client.UploadObject(ctx, p.config.bucket, object, artifact.Path)
	if err != nil {
		return nil, fmt.Errorf("gcp plugin: upload artifact to gs://%s/%s: %w", p.config.bucket, object, classifyError(err))
	}
	imageName := firstNonEmpty(artifact.Metadata["imageName"], artifact.Metadata["vmimage"], p.config.imageName, "imagebuilder-"+sanitizeName(buildID))
	return &platform.UploadResult{ProviderRef: uri, Metadata: map[string]string{
		"providerConfigName": p.config.providerConfigName, "project": p.config.project,
		"bucket": p.config.bucket, "object": object, "gcsURI": uri, "buildID": buildID,
		"imageName": imageName, "imageFamily": p.config.imageFamily, "format": string(artifact.Format),
		"checksum": artifact.Checksum, "os": string(artifact.OS), "arch": artifact.Metadata["arch"],
	}}, nil
}

func (p *Plugin) UploadResumable(ctx context.Context, artifact *platform.BuildArtifact, session platform.UploadSession, checkpoint platform.UploadCheckpoint) (*platform.UploadResult, error) {
	accepted, err := p.PrepareUpload(ctx, artifact, session)
	if err != nil {
		return nil, err
	}
	if checkpoint != nil {
		if err := checkpoint(accepted); err != nil {
			return nil, err
		}
	}
	file, err := os.Open(artifact.Path) // #nosec G304 -- controller-owned workspace artifact.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if _, err := file.Seek(accepted.CommittedOffset, io.SeekStart); err != nil {
		return nil, err
	}
	return p.UploadStreamResumable(ctx, &platform.StreamArtifact{Reader: file, Format: artifact.Format, Checksum: artifact.Checksum, SizeBytes: artifact.SizeBytes, OS: artifact.OS, Metadata: artifact.Metadata}, accepted, checkpoint)
}

func (p *Plugin) PrepareUpload(ctx context.Context, artifact *platform.BuildArtifact, requested platform.UploadSession) (platform.UploadSession, error) {
	if requested.IdempotencyKey == "" || artifact == nil || artifact.SizeBytes <= 0 || artifact.Metadata["buildID"] == "" {
		return platform.UploadSession{}, fmt.Errorf("gcp plugin: valid artifact, build ID, and idempotency key are required")
	}
	client, ok := p.client.(*sdkClient)
	if !ok {
		return restartUploadSession(requested)
	}
	object := path.Join(p.config.objectPrefix, sanitizeName(artifact.Metadata["buildID"])+"-"+checksumToken(artifact.Checksum)+".tar.gz")
	state, err := client.prepareGCSResumeSession(ctx, p.config.bucket, object, artifact.SizeBytes, requested.ResumeToken)
	if err != nil {
		return platform.UploadSession{}, fmt.Errorf("gcp plugin: prepare resumable GCS upload: %w", err)
	}
	token, err := json.Marshal(state)
	if err != nil {
		return platform.UploadSession{}, err
	}
	return platform.UploadSession{IdempotencyKey: requested.IdempotencyKey, ResumeToken: string(token), CommittedOffset: state.Offset, ResumeMode: "offset"}, nil
}

func (p *Plugin) UploadStreamResumable(ctx context.Context, artifact *platform.StreamArtifact, session platform.UploadSession, checkpoint platform.UploadCheckpoint) (*platform.UploadResult, error) {
	client, ok := p.client.(*sdkClient)
	if !ok {
		return nil, fmt.Errorf("gcp plugin: configured client does not support GCS session resume")
	}
	var state gcsResumeSession
	if err := json.Unmarshal([]byte(session.ResumeToken), &state); err != nil {
		return nil, fmt.Errorf("gcp plugin: decode resumable session: %w", err)
	}
	if err := client.uploadGCSResume(ctx, artifact.Reader, &state, func(updated gcsResumeSession) error {
		token, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		if checkpoint != nil {
			return checkpoint(platform.UploadSession{IdempotencyKey: session.IdempotencyKey, ResumeToken: string(token), CommittedOffset: updated.Offset, ResumeMode: "offset"})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	uri := gcsImportURL(state.Bucket, state.Object)
	buildID := artifact.Metadata["buildID"]
	imageName := firstNonEmpty(artifact.Metadata["imageName"], artifact.Metadata["vmimage"], p.config.imageName, "imagebuilder-"+sanitizeName(buildID))
	return &platform.UploadResult{ProviderRef: uri, Metadata: map[string]string{
		"providerConfigName": p.config.providerConfigName, "project": p.config.project,
		"bucket": state.Bucket, "object": state.Object, "gcsURI": uri, "buildID": buildID,
		"imageName": imageName, "imageFamily": p.config.imageFamily, "format": string(artifact.Format),
		"checksum": artifact.Checksum, "os": string(artifact.OS), "arch": artifact.Metadata["arch"],
	}}, nil
}

func (p *Plugin) Register(ctx context.Context, result *platform.UploadResult) (*platform.ImageRef, error) {
	if result == nil || result.ProviderRef == "" {
		return nil, fmt.Errorf("gcp plugin: upload result and provider reference are required")
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	if p.client == nil {
		return nil, fmt.Errorf("gcp plugin: client is not initialised")
	}
	metadata := cloneStringMap(result.Metadata)
	if metadata["buildID"] == "" {
		metadata["buildID"] = uploadObjectBuildID(metadata["object"])
	}
	metadata["sourceIdentity"] = result.ProviderRef
	input := p.imageInput(metadata)
	input.GCSURI = firstNonEmpty(result.Metadata["gcsURI"], result.ProviderRef)
	ref, err := p.client.CreateImageFromObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("gcp plugin: create custom image: %w", classifyError(err))
	}
	if ref == nil || ref.ID == "" {
		return nil, fmt.Errorf("gcp plugin: create custom image returned an empty image reference")
	}
	if !p.config.retainUpload {
		if err := p.client.DeleteObject(ctx, result.Metadata["bucket"], result.Metadata["object"]); err != nil {
			p.log.Warn("delete GCP staging object after image creation", slog.Any("error", err))
		}
	}
	result.Metadata["imageRef"] = ref.ID
	return ref, nil
}

func (p *Plugin) Cleanup(ctx context.Context, artifact *platform.BuildArtifact) error {
	if artifact == nil || artifact.Metadata == nil || p.client == nil {
		return nil
	}
	var errs []error
	if imageRef := firstNonEmpty(artifact.Metadata["gcp.imageRef"], artifact.Metadata["imageRef"]); imageRef != "" {
		if err := p.client.DeleteImage(ctx, resourceName(imageRef)); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete GCP image: %w", classifyError(err)))
		}
	}
	bucket := firstNonEmpty(artifact.Metadata["gcp.bucket"], artifact.Metadata["bucket"])
	object := firstNonEmpty(artifact.Metadata["gcp.object"], artifact.Metadata["object"])
	if bucket != "" && object != "" {
		if err := p.client.DeleteObject(ctx, bucket, object); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete GCP staging object: %w", classifyError(err)))
		}
	}
	return errors.Join(errs...)
}

func (p *Plugin) HealthCheck(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("gcp plugin: client is not initialised")
	}
	return classifyError(p.client.HealthCheck(ctx))
}

// ReconcileRemoteBuild performs idempotent custom-image creation from existing
// GCP images or snapshots. Guest provisioners remain a local-build concern.
func (p *Plugin) ReconcileRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) (*platform.RemoteBuildResult, error) {
	if req == nil || req.BuildID == "" {
		return nil, fmt.Errorf("gcp plugin: remote build request and build ID are required")
	}
	if platform.ImageFormat(req.Target.Format) != platform.FormatGCETarball {
		return nil, fmt.Errorf("gcp plugin: remote build requires target format gcetarball")
	}
	if len(req.Provisioners) > 0 || req.GuestAccess != nil {
		return nil, fmt.Errorf("gcp plugin: remote provisioners and guest access are not supported; use a local build")
	}
	if err := validateTargetLabels(req.Target.Tags); err != nil {
		return nil, err
	}
	if p.client == nil {
		return nil, fmt.Errorf("gcp plugin: client is not initialised")
	}
	if req.OperationRef != "" {
		ref, err := p.client.GetImage(ctx, resourceName(req.OperationRef))
		if err != nil {
			return nil, fmt.Errorf("gcp plugin: read remote image: %w", classifyError(err))
		}
		if ref == nil || ref.ID == "" {
			return nil, fmt.Errorf("gcp plugin: read remote image returned an empty image reference")
		}
		return remoteReady(req, ref), nil
	}
	input := p.imageInput(map[string]string{"buildID": req.BuildID, "imageName": req.ImageName, "arch": req.OSArch})
	input.RequestID = requestID(req.BuildID)
	source := firstNonEmpty(req.SourceProviderRef, req.SourceURL)
	switch strings.ToLower(strings.TrimSpace(req.SourceType)) {
	case "snapshot":
		if source == "" {
			return nil, fmt.Errorf("gcp plugin: snapshot sourceProviderRef is required")
		}
		input.SourceSnapshot = source
	case "cloud-image", "marketplace":
		if source == "" {
			if req.SourceMarketplace == nil {
				return nil, fmt.Errorf("gcp plugin: image sourceProviderRef is required")
			}
			marketplaceImage, marketplaceErr := marketplaceSource(req.SourceMarketplace)
			if marketplaceErr != nil {
				return nil, marketplaceErr
			}
			source = marketplaceImage
		}
		input.SourceImage = source
	default:
		return nil, fmt.Errorf("gcp plugin: unsupported remote source type %q", req.SourceType)
	}
	input.Labels["source-id"] = identityLabel(source)
	ref, err := p.client.CreateImageFromSource(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("gcp plugin: create remote image: %w", classifyError(err))
	}
	if ref == nil || ref.ID == "" {
		return nil, fmt.Errorf("gcp plugin: create remote image returned an empty image reference")
	}
	return remoteReady(req, ref), nil
}

func (p *Plugin) CleanupRemoteBuild(ctx context.Context, req *platform.RemoteBuildRequest) error {
	if req == nil || p.client == nil || req.OperationRef == "" {
		return nil
	}
	if err := p.client.DeleteImage(ctx, resourceName(req.OperationRef)); err != nil && !isNotFound(err) {
		return fmt.Errorf("gcp plugin: cleanup remote image: %w", classifyError(err))
	}
	return nil
}

func (p *Plugin) imageInput(metadata map[string]string) createImageInput {
	name := firstNonEmpty(metadata["imageName"], p.config.imageName, "imagebuilder-"+sanitizeName(metadata["buildID"]))
	labels := labelsFromMetadata(metadata)
	if buildID := strings.TrimSpace(metadata["buildID"]); buildID != "" {
		labels["build-id"] = identityLabel(buildID)
	}
	if source := strings.TrimSpace(metadata["sourceIdentity"]); source != "" {
		labels["source-id"] = identityLabel(source)
	}
	if key := strings.TrimSpace(metadata["register.idempotencyKey"]); key != "" {
		labels["registration-id"] = identityLabel(key)
	}
	return createImageInput{
		Project: p.config.project, Name: sanitizeName(name),
		Family:      sanitizeOptionalName(firstNonEmpty(metadata["imageFamily"], p.config.imageFamily)),
		Description: p.config.description, Architecture: gcpArchitecture(firstNonEmpty(metadata["arch"], p.config.architecture)),
		GuestOSFeatures: append([]string(nil), p.config.guestOSFeatures...), Licenses: append([]string(nil), p.config.licenses...),
		Labels: labels, StorageLocations: nonEmptyStrings(p.config.storageLocation),
	}
}

func remoteReady(req *platform.RemoteBuildRequest, ref *platform.ImageRef) *platform.RemoteBuildResult {
	return &platform.RemoteBuildResult{
		OperationRef: ref.ID, Phase: platform.RemoteBuildPhaseReady, Message: "GCP custom image is ready", Done: true,
		Images:  []platform.RemoteImageRef{{Provider: "gcp", ProviderConfig: req.Target.ProviderConfigRef.Name, ImageRef: *ref, Format: platform.FormatGCETarball, Checksum: req.SourceChecksum}},
		Hygiene: &platform.RemoteHygieneResult{Status: "passed", Message: "GCP source image copied without provider-side guest boot", Checks: []string{"gcp-source-reference-present", "gcp-custom-image-ready"}, ResultRef: ref.ID},
	}
}

type sdkClient struct {
	project        string
	storage        *storage.Client
	compute        *compute.Service
	httpClient     *http.Client
	uploadEndpoint string
}

func (c *sdkClient) Close() error {
	if c == nil || c.storage == nil {
		return nil
	}
	return c.storage.Close()
}

func newSDKClient(ctx context.Context, cfg platform.PluginConfig, parsed config) (*sdkClient, error) {
	var commonOpts []option.ClientOption
	credentialJSON := credentialsJSON(cfg.SecretData)
	authOptions := &gcpcredentials.DetectOptions{Scopes: []string{compute.CloudPlatformScope}}
	var authCredentialsErr error
	var authCredentials *cloudauth.Credentials
	if len(credentialJSON) > 0 {
		authCredentials, authCredentialsErr = gcpcredentials.NewCredentialsFromJSON(gcpcredentials.ServiceAccount, credentialJSON, authOptions)
	} else {
		authCredentials, authCredentialsErr = gcpcredentials.DetectDefault(authOptions)
	}
	if authCredentialsErr != nil {
		return nil, fmt.Errorf("load GCP credentials: %w", authCredentialsErr)
	}
	commonOpts = append(commonOpts, option.WithAuthCredentials(authCredentials))
	authenticatedHTTP, err := httptransport.NewClient(&httptransport.Options{Credentials: authCredentials})
	if err != nil {
		return nil, fmt.Errorf("create authenticated GCP HTTP client: %w", err)
	}
	storageOpts := append([]option.ClientOption(nil), commonOpts...)
	if endpoint := strings.TrimSpace(cfg.Extra["storageEndpoint"]); endpoint != "" {
		storageOpts = append(storageOpts, option.WithEndpoint(strings.TrimRight(endpoint, "/")+"/"))
	}
	storageClient, err := storage.NewClient(ctx, storageOpts...)
	if err != nil {
		return nil, err
	}
	computeOpts := append([]option.ClientOption(nil), commonOpts...)
	if endpoint := firstNonEmpty(cfg.Endpoint, cfg.Extra["computeEndpoint"]); endpoint != "" {
		computeOpts = append(computeOpts, option.WithEndpoint(strings.TrimRight(endpoint, "/")+"/"))
	}
	computeService, err := compute.NewService(ctx, computeOpts...)
	if err != nil {
		_ = storageClient.Close()
		return nil, err
	}
	uploadEndpoint := strings.TrimRight(firstNonEmpty(cfg.Extra["storageUploadEndpoint"], "https://storage.googleapis.com/upload/storage/v1"), "/")
	return &sdkClient{project: parsed.project, storage: storageClient, compute: computeService, httpClient: authenticatedHTTP, uploadEndpoint: uploadEndpoint}, nil
}

func (c *sdkClient) UploadObject(ctx context.Context, bucket, object, filePath string) (string, error) {
	file, err := os.Open(filePath) // #nosec G304 -- Path is an operator-owned workspace or SDK spool file, not provider user input.
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	crc, err := fileCRC32C(file)
	if err != nil {
		return "", err
	}
	objectHandle := c.storage.Bucket(bucket).Object(object)
	uploadCtx, cancelUpload := context.WithCancel(ctx)
	defer cancelUpload()
	writer := objectHandle.If(storage.Conditions{DoesNotExist: true}).NewWriter(uploadCtx)
	writer.ContentType, writer.ChunkSize = "application/gzip", 16*1024*1024
	writer.CRC32C, writer.SendCRC32C = crc, true
	if _, err := io.Copy(writer, file); err != nil {
		cancelUpload()
		_ = writer.Close()
		if isPreconditionFailed(err) {
			if matchErr := existingObjectMatches(ctx, objectHandle, info.Size(), crc); matchErr != nil {
				return "", matchErr
			}
			return gcsImportURL(bucket, object), nil
		}
		return "", err
	}
	if err := writer.Close(); err != nil {
		if isPreconditionFailed(err) {
			if matchErr := existingObjectMatches(ctx, objectHandle, info.Size(), crc); matchErr != nil {
				return "", matchErr
			}
			return gcsImportURL(bucket, object), nil
		}
		return "", err
	}
	return gcsImportURL(bucket, object), nil
}

func (c *sdkClient) prepareGCSResumeSession(ctx context.Context, bucket, object string, size int64, token string) (gcsResumeSession, error) {
	if token != "" {
		var state gcsResumeSession
		if err := json.Unmarshal([]byte(token), &state); err != nil {
			return gcsResumeSession{}, fmt.Errorf("decode existing session: %w", err)
		}
		if state.SessionURI == "" || state.Bucket != bucket || state.Object != object || state.Size != size {
			return gcsResumeSession{}, fmt.Errorf("existing GCS session does not match artifact")
		}
		if err := validateGCSResumeOrigin(state.SessionURI, c.uploadEndpoint); err != nil {
			return gcsResumeSession{}, err
		}
		// Google documents resumable sessions as expiring after one week of
		// inactivity. Rotate proactively before that boundary.
		if !state.CreatedAt.IsZero() && time.Since(state.CreatedAt) >= 6*24*time.Hour {
			token = ""
		} else {
			offset, complete, err := c.queryGCSResumeOffset(ctx, state.SessionURI, size)
			if err != nil && !errors.Is(err, errGCSResumeSessionExpired) {
				return gcsResumeSession{}, err
			}
			if err == nil {
				if complete {
					offset = size
				}
				state.Offset = offset
				return state, nil
			}
			token = ""
		}
	}
	endpoint := fmt.Sprintf("%s/b/%s/o?uploadType=resumable&name=%s", c.uploadEndpoint, url.PathEscape(bucket), url.QueryEscape(object))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"contentType":"application/gzip"}`))
	if err != nil {
		return gcsResumeSession{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "application/gzip")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	resp, err := c.doGCSResumeRequest(req)
	if err != nil {
		return gcsResumeSession{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return gcsResumeSession{}, fmt.Errorf("start resumable upload: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	sessionURI := resp.Header.Get("Location")
	if sessionURI == "" {
		return gcsResumeSession{}, fmt.Errorf("start resumable upload: response has no Location header")
	}
	if err := validateGCSResumeOrigin(sessionURI, c.uploadEndpoint); err != nil {
		return gcsResumeSession{}, err
	}
	return gcsResumeSession{SessionURI: sessionURI, Bucket: bucket, Object: object, Size: size, CreatedAt: time.Now().UTC()}, nil
}

func (c *sdkClient) queryGCSResumeOffset(ctx context.Context, sessionURI string, size int64) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURI, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Length", "0")
	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
	resp, err := c.doGCSResumeRequest(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return size, true, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return 0, false, errGCSResumeSessionExpired
	}
	if resp.StatusCode != http.StatusPermanentRedirect {
		return 0, false, fmt.Errorf("query resumable upload: HTTP %d", resp.StatusCode)
	}
	return gcsRangeOffset(resp.Header.Get("Range")), false, nil
}

func (c *sdkClient) uploadGCSResume(ctx context.Context, body io.Reader, state *gcsResumeSession, checkpoint func(gcsResumeSession) error) error {
	buffer := make([]byte, gcsResumeChunkSize)
	for state.Offset < state.Size {
		count := min(gcsResumeChunkSize, state.Size-state.Offset)
		n, err := io.ReadFull(body, buffer[:count])
		if err != nil {
			return fmt.Errorf("read GCS chunk at %d: %w", state.Offset, err)
		}
		start, end := state.Offset, state.Offset+int64(n)-1
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, state.SessionURI, bytes.NewReader(buffer[:n]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/gzip")
		req.Header.Set("Content-Length", strconv.Itoa(n))
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, state.Size))
		resp, err := c.doGCSResumeRequest(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusPermanentRedirect:
			state.Offset = gcsRangeOffset(resp.Header.Get("Range"))
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			state.Offset = state.Size
		default:
			return fmt.Errorf("upload GCS chunk at %d: HTTP %d", start, resp.StatusCode)
		}
		if state.Offset <= start {
			return fmt.Errorf("GCS did not commit chunk at offset %d", start)
		}
		if err := checkpoint(*state); err != nil {
			return err
		}
	}
	return nil
}

func validateGCSResumeOrigin(sessionURI, uploadEndpoint string) error {
	session, err := url.Parse(sessionURI)
	if err != nil || session.Scheme == "" || session.Host == "" {
		return fmt.Errorf("GCS resumable session URI is invalid")
	}
	endpoint, err := url.Parse(uploadEndpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return fmt.Errorf("GCS upload endpoint is invalid")
	}
	if !strings.EqualFold(session.Scheme, endpoint.Scheme) || !strings.EqualFold(session.Host, endpoint.Host) {
		return fmt.Errorf("GCS resumable session origin %q does not match configured upload origin %q", session.Scheme+"://"+session.Host, endpoint.Scheme+"://"+endpoint.Host)
	}
	if session.Scheme != "https" && !isLoopbackHost(session.Hostname()) {
		return fmt.Errorf("GCS resumable session must use https outside loopback development endpoints")
	}
	return nil
}

func (c *sdkClient) doGCSResumeRequest(req *http.Request) (*http.Response, error) {
	if err := validateGCSResumeOrigin(req.URL.String(), c.uploadEndpoint); err != nil {
		return nil, err
	}
	client := *c.httpClient
	client.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		return validateGCSResumeOrigin(next.URL.String(), c.uploadEndpoint)
	}
	return client.Do(req)
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1"
}

func gcsRangeOffset(header string) int64 {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 2 {
		return 0
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < 0 {
		return 0
	}
	return end + 1
}

func restartUploadSession(requested platform.UploadSession) (platform.UploadSession, error) {
	if requested.IdempotencyKey == "" {
		return platform.UploadSession{}, fmt.Errorf("upload idempotency key is required")
	}
	requested.ResumeToken, requested.CommittedOffset, requested.ResumeMode = requested.IdempotencyKey, 0, "restart"
	return requested, nil
}
func existingObjectMatches(ctx context.Context, object *storage.ObjectHandle, size int64, crc uint32) error {
	attrs, err := object.Attrs(ctx)
	if err != nil {
		return err
	}
	if attrs.Size != size || attrs.CRC32C != crc {
		return fmt.Errorf("existing GCS object gs://%s/%s does not match artifact size and CRC32C", attrs.Bucket, attrs.Name)
	}
	return nil
}
func (c *sdkClient) CreateImageFromObject(ctx context.Context, input createImageInput) (*platform.ImageRef, error) {
	return c.createImage(ctx, input, true)
}
func (c *sdkClient) CreateImageFromSource(ctx context.Context, input createImageInput) (*platform.ImageRef, error) {
	return c.createImage(ctx, input, false)
}
func (c *sdkClient) createImage(ctx context.Context, input createImageInput, rawDisk bool) (*platform.ImageRef, error) {
	image := &compute.Image{Name: input.Name, Family: input.Family, Description: input.Description, Architecture: input.Architecture, Labels: input.Labels, Licenses: input.Licenses, StorageLocations: input.StorageLocations}
	for _, feature := range input.GuestOSFeatures {
		image.GuestOsFeatures = append(image.GuestOsFeatures, &compute.GuestOsFeature{Type: feature})
	}
	if rawDisk {
		image.RawDisk = &compute.ImageRawDisk{Source: input.GCSURI, ContainerType: "TAR"}
	} else if input.SourceSnapshot != "" {
		image.SourceSnapshot = input.SourceSnapshot
	} else {
		image.SourceImage = input.SourceImage
	}
	call := c.compute.Images.Insert(input.Project, image)
	if input.RequestID != "" {
		call = call.RequestId(input.RequestID)
	}
	op, err := call.Context(ctx).Do()
	if err != nil {
		if isAlreadyExists(err) {
			existing, getErr := c.GetImage(ctx, input.Name)
			if getErr != nil {
				return nil, getErr
			}
			if !matchesIdentityLabels(existing.Tags, input.Labels) {
				return nil, fmt.Errorf("GCP image %q already exists but is not owned by this build artifact", input.Name)
			}
			return existing, nil
		}
		return nil, err
	}
	if err := c.waitGlobalOperation(ctx, input.Project, op.Name); err != nil {
		return nil, err
	}
	return c.GetImage(ctx, input.Name)
}
func (c *sdkClient) GetImage(ctx context.Context, name string) (*platform.ImageRef, error) {
	pollInterval := 2 * time.Second
	for {
		image, err := c.compute.Images.Get(c.project, resourceName(name)).Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		switch status := strings.ToUpper(strings.TrimSpace(image.Status)); status {
		case "READY":
			return &platform.ImageRef{ID: image.SelfLink, Name: image.Name, Location: "global", Tags: cloneStringMap(image.Labels)}, nil
		case "FAILED":
			return nil, fmt.Errorf("GCP image %q entered FAILED status", image.Name)
		case "PENDING", "CREATING":
		default:
			return nil, fmt.Errorf("GCP image %q returned unknown status %q", image.Name, status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
		pollInterval = min(pollInterval*2, 15*time.Second)
	}
}
func (c *sdkClient) DeleteImage(ctx context.Context, name string) error {
	op, err := c.compute.Images.Delete(c.project, resourceName(name)).RequestId(requestID("delete-" + name)).Context(ctx).Do()
	if err != nil {
		return err
	}
	return c.waitGlobalOperation(ctx, c.project, op.Name)
}
func (c *sdkClient) DeleteObject(ctx context.Context, bucket, object string) error {
	if bucket == "" || object == "" {
		return nil
	}
	return c.storage.Bucket(bucket).Object(object).Delete(ctx)
}
func (c *sdkClient) HealthCheck(ctx context.Context) error {
	_, err := c.compute.Projects.Get(c.project).Context(ctx).Do()
	return err
}
func (c *sdkClient) waitGlobalOperation(ctx context.Context, project, operation string) error {
	pollInterval := 2 * time.Second
	for operation != "" {
		op, err := c.compute.GlobalOperations.Get(project, operation).Context(ctx).Do()
		if err != nil {
			return err
		}
		if op.Status == "DONE" {
			if op.Error != nil && len(op.Error.Errors) > 0 {
				parts := make([]string, 0, len(op.Error.Errors))
				codes := make([]string, 0, len(op.Error.Errors))
				for _, item := range op.Error.Errors {
					parts = append(parts, item.Code+": "+item.Message)
					codes = append(codes, item.Code)
				}
				return &operationError{operation: operation, codes: codes, message: strings.Join(parts, "; ")}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		pollInterval = min(pollInterval*2, 15*time.Second)
	}
	return nil
}

func classifyError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && (apiErr.Code == 408 || apiErr.Code == 429 || apiErr.Code >= 500) {
		return providererrors.Transient(err, retryAfter(apiErr.Header.Get("Retry-After")))
	}
	return providererrors.Classify(err)
}
func isNotFound(err error) bool { var e *googleapi.Error; return errors.As(err, &e) && e.Code == 404 }
func isAlreadyExists(err error) bool {
	var e *googleapi.Error
	return errors.As(err, &e) && e.Code == 409
}
func isPreconditionFailed(err error) bool {
	var e *googleapi.Error
	return errors.As(err, &e) && e.Code == 412
}

var (
	gcpNamePattern            = regexp.MustCompile(`[^a-z0-9-]+`)
	checksumTokenPattern      = regexp.MustCompile(`[^a-z0-9]+`)
	marketplaceSegmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*$`)
)

func sanitizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = gcpNamePattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		value = "image-" + value
	}
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}
func sanitizeOptionalName(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return sanitizeName(value)
}
func resourceName(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if i := strings.LastIndex(value, "/"); i >= 0 {
		return value[i+1:]
	}
	return value
}
func gcsImportURL(bucket, object string) string {
	parts := strings.Split(object, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return "https://storage.googleapis.com/" + url.PathEscape(bucket) + "/" + strings.Join(parts, "/")
}
func checksumToken(checksum string) string {
	checksum = strings.TrimSpace(checksum)
	if index := strings.Index(checksum, ":"); index >= 0 {
		checksum = checksum[index+1:]
	}
	checksum = strings.ToLower(strings.TrimSpace(checksum))
	checksum = checksumTokenPattern.ReplaceAllString(checksum, "-")
	checksum = strings.Trim(checksum, "-")
	if checksum == "" {
		return "unverified"
	}
	if len(checksum) > 128 {
		return identityLabel(checksum)
	}
	return checksum
}
func uploadObjectBuildID(object string) string {
	base := strings.TrimSuffix(path.Base(strings.TrimSpace(object)), ".tar.gz")
	if index := strings.LastIndex(base, "-"); index > 0 {
		return base[:index]
	}
	return ""
}
func marketplaceSource(ref *v1alpha1.MarketplaceRef) (string, error) {
	project := strings.TrimSpace(ref.Publisher)
	image := firstNonEmpty(ref.Version, ref.SKU, ref.Offer)
	if !marketplaceSegmentPattern.MatchString(project) {
		return "", fmt.Errorf("gcp plugin: marketplace publisher must be an exact GCP project ID")
	}
	if !marketplaceSegmentPattern.MatchString(image) {
		return "", fmt.Errorf("gcp plugin: marketplace version, SKU, or offer must be an exact GCP image name")
	}
	return fmt.Sprintf("projects/%s/global/images/%s", project, image), nil
}
func requestID(value string) string {
	digest := sha256.Sum256([]byte(value))
	// Set RFC 4122 version/variant bits; Compute only requires a valid UUID.
	digest[6] = (digest[6] & 0x0f) | 0x50
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}
func fileCRC32C(file *os.File) (uint32, error) {
	h := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	if _, err := io.Copy(h, file); err != nil {
		return 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	return h.Sum32(), nil
}
func labelsFromMetadata(metadata map[string]string) map[string]string {
	labels := map[string]string{"managed-by": "imagebuilder"}
	for k, v := range metadata {
		if strings.HasPrefix(k, "target.tag.") {
			key := sanitizeLabelKey(k[len("target.tag."):])
			value := sanitizeLabel(v)
			if key != "" && value != "" && !reservedLabelKey(key) {
				labels[key] = value
			}
		}
	}
	return labels
}
func sanitizeLabelKey(value string) string {
	value = sanitizeLabel(value)
	if value != "" && (value[0] < 'a' || value[0] > 'z') {
		value = "label-" + value
		if len(value) > 63 {
			value = strings.TrimRight(value[:63], "-")
		}
	}
	return value
}
func validateTargetLabels(labels map[string]string) error {
	if len(labels) > 61 {
		return fmt.Errorf("gcp plugin: at most 61 target tags are supported; three labels are reserved for provider identity")
	}
	for key := range labels {
		if reservedLabelKey(sanitizeLabelKey(key)) {
			return fmt.Errorf("gcp plugin: target tag %q conflicts with a provider-reserved label", key)
		}
	}
	return nil
}
func reservedLabelKey(key string) bool {
	return key == "managed-by" || key == "build-id" || key == "source-id" || key == "registration-id"
}
func sanitizeLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = gcpNamePattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}
func identityLabel(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return fmt.Sprintf("%x", digest[:12])
}
func matchesIdentityLabels(actual, expected map[string]string) bool {
	if actual["managed-by"] != "imagebuilder" {
		return false
	}
	for _, key := range []string{"build-id", "source-id", "registration-id"} {
		if expected[key] != "" && actual[key] != expected[key] {
			return false
		}
	}
	return true
}
func retryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}
func gcpArchitecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "arm64", "arm":
		return "ARM64"
	case "amd64", "x86_64", "":
		return "X86_64"
	default:
		return strings.ToUpper(value)
	}
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func firstNonEmptyBytes(values map[string][]byte, keys ...string) []byte {
	for _, k := range keys {
		if len(values[k]) > 0 {
			return values[k]
		}
	}
	return nil
}
func credentialsJSON(values map[string][]byte) []byte {
	if raw := firstNonEmptyBytes(values, "serviceAccountJSON", "credentials", "serviceAccountKey", "service-account.json"); len(raw) > 0 {
		return raw
	}
	if len(values["private_key"]) == 0 || len(values["client_email"]) == 0 {
		return nil
	}
	decoded := make(map[string]string, len(values))
	for key, value := range values {
		decoded[key] = string(value)
	}
	raw, err := json.Marshal(decoded)
	if err != nil {
		return nil
	}
	return raw
}
func credentialProject(values map[string][]byte) string {
	if project := firstNonEmpty(string(values["projectId"]), string(values["projectID"]), string(values["project_id"])); project != "" {
		return project
	}
	raw := credentialsJSON(values)
	if len(raw) == 0 {
		return ""
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return ""
	}
	for _, key := range []string{"project_id", "projectId", "projectID"} {
		var project string
		if value := document[key]; len(value) > 0 && json.Unmarshal(value, &project) == nil && strings.TrimSpace(project) != "" {
			return strings.TrimSpace(project)
		}
	}
	return ""
}
func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
func boolValue(value string) bool {
	parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
	return parsed
}
func nonEmptyStrings(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}
func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
