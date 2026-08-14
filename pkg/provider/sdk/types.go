package sdk

import (
	"context"
	"io"
)

const ProtocolVersion = "v1"

type Capabilities struct {
	ProviderName    string
	ProviderVersion string
	Formats         []string
	OSFamilies      []string
	// BuildModes declares supported execution modes. Use []string{"local"}
	// for upload/register-only providers. Add "remote" only when the provider
	// implements RemoteBuildProvider and can run the full build lifecycle on
	// the target platform.
	BuildModes []string
	// UploadResumeMode is "restart" for idempotent full retransmission or
	// "offset" when the provider durably commits byte ranges.
	UploadResumeMode string
}

type Config struct {
	ProviderConfigName string
	Credentials        map[string][]byte
	Region             string
	Endpoint           string
	Insecure           bool
	Extra              map[string]string
}

type ArtifactInfo struct {
	Format             string
	Checksum           string
	TotalSizeBytes     int64
	OSFamily           string
	Metadata           map[string]string
	ProviderConfigName string
	IdempotencyKey     string
}

type UploadSession struct {
	IdempotencyKey  string
	ResumeToken     string
	CommittedOffset int64
	ResumeMode      string
}

// ResumableProvider is optional. PrepareUpload must reconstruct session state
// from durable provider/backend data and return the authoritative offset.
// UploadArtifactResumable receives only bytes starting at that offset.
type ResumableProvider interface {
	Provider
	PrepareUpload(ctx context.Context, artifact ArtifactInfo, requested UploadSession) (UploadSession, error)
	UploadArtifactResumable(ctx context.Context, artifact ArtifactInfo, session UploadSession, body io.Reader, progress ProgressReporter) (UploadResult, error)
}

type UploadResult struct {
	ProviderRef string
	Metadata    map[string]string
}

type RegisterInput struct {
	ProviderRef        string
	ImageName          string
	Tags               map[string]string
	ProviderConfigName string
	Format             string
	IdempotencyKey     string
}

type ImageRef struct {
	ID       string
	Name     string
	Location string
	Tags     map[string]string
}

type DeleteInput struct {
	ProviderRef        string
	ProviderConfigName string
}

type Progress struct {
	BytesWritten    int64
	TotalBytes      int64
	Phase           string
	Message         string
	SessionToken    string
	CommittedOffset int64
	ResumeMode      string
}

type ProgressReporter interface {
	Report(ctx context.Context, progress Progress) error
}

type Provider interface {
	Capabilities(ctx context.Context) (Capabilities, error)
	ValidateConfig(ctx context.Context, config Config) error
	UploadArtifact(ctx context.Context, artifact ArtifactInfo, body io.Reader, progress ProgressReporter) (UploadResult, error)
	RegisterImage(ctx context.Context, input RegisterInput) (ImageRef, error)
	DeleteArtifact(ctx context.Context, input DeleteInput) (bool, string, error)
	HealthCheck(ctx context.Context) (string, error)
}

type RemoteBuildInput struct {
	// BuildID is stable for the VMImage build and must be used for idempotency.
	BuildID string
	// OperationRef is the opaque provider operation reference returned by a
	// previous ReconcileRemoteBuild call. Empty means the provider should create
	// or discover the remote operation for this BuildID.
	OperationRef   string
	ImageName      string
	Namespace      string
	OSFamily       string
	OSDistribution string
	OSVersion      string
	OSArch         string
	SourceType     string
	SourceURL      string
	// SourceProviderRef is an opaque provider-native source reference such as
	// an AMI ID, vSphere template ID, Glance UUID, or cloud image resource name.
	// Prefer this over overloading SourceURL for remote builds.
	SourceProviderRef  string
	SourceMarketplace  *MarketplaceRef
	SourceChecksum     string
	ProviderConfigName string
	Format             string
	Tags               map[string]string
	Provisioners       []RemoteProvisioner
	GuestAccess        *RemoteGuestAccess
	TimeoutSeconds     int64
}

type MarketplaceRef struct {
	Publisher string
	Offer     string
	SKU       string
	Version   string
}

type RemoteProvisioner struct {
	Type      string
	Image     string
	Inline    string
	Playbook  string
	Args      []string
	ExtraVars map[string]string
	Source    *RemoteProvisionerSource
}

type RemoteProvisionerSource struct {
	Git *RemoteGitProvisionerSource
}

type RemoteGitProvisionerSource struct {
	URL  string
	Ref  string
	Path string
	Auth *RemoteGitProvisionerAuth
}

type RemoteGitProvisionerAuth struct {
	Token    string
	Username string
	Password string
}

type RemoteGuestAccess struct {
	Protocol          string
	User              string
	GuestPort         int32
	GeneratedSSHKey   bool
	GeneratedPassword bool
	InjectionMethod   string
}

type RemoteBuildResult struct {
	// OperationRef is an opaque, non-secret handle for the provider-side remote
	// operation. Return it as soon as one exists so retries can continue safely.
	OperationRef string
	// Phase should be one of: Pending, Booting, Readiness, Provisioning,
	// Sanitizing, Registering, Ready. Unknown values are treated as progress.
	Phase string
	// Message is copied to VMImage status/events. Never include credentials,
	// cloud-init data, scripts, passwords, tokens, or other secret material.
	Message string
	// Done must be true only when final image references are available.
	Done     bool
	Images   []RemoteImageRef
	Artifact *RemoteArtifact
	// Hygiene attests provider-side final image hygiene/sanitization checks.
	// Status should be "passed", "failed", or "unknown". Messages and refs
	// must be non-secret and safe to copy into Kubernetes status/events.
	Hygiene *RemoteHygieneResult
}

type RemoteHygieneResult struct {
	Status    string
	Message   string
	Checks    []string
	ResultRef string
}

type RemoteImageRef struct {
	Provider           string
	ProviderConfigName string
	// ImageRef is the final platform-native image identifier, for example an
	// AMI ID, vSphere template ID, Glance UUID, or cloud image resource name.
	ImageRef  string
	ImageName string
	Location  string
	Format    string
	Checksum  string
	Tags      map[string]string
}

type RemoteArtifact struct {
	Path      string
	Format    string
	Checksum  string
	SizeBytes int64
	OSFamily  string
	Metadata  map[string]string
}

type RemoteBuildProvider interface {
	Provider
	// ReconcileRemoteBuild starts or continues a provider-owned remote build.
	//
	// Implementations must be idempotent for the same BuildID and OperationRef:
	// repeated calls may happen after controller restarts, retries, or watch
	// events. Return Done=false while work is still running. Return Done=true
	// only after Images contains the final registered image reference(s).
	//
	// Do not include secrets in returned messages, operation refs, image refs,
	// artifact metadata, or logs.
	ReconcileRemoteBuild(ctx context.Context, input RemoteBuildInput) (RemoteBuildResult, error)
}

type RemoteBuildCleanupResult struct {
	Cleaned bool
	// Message is copied to logs/events by callers. Never include secrets,
	// scripts, passwords, tokens, or provider credentials.
	Message string
}

type RemoteBuildCleanupProvider interface {
	RemoteBuildProvider
	// CleanupRemoteBuild removes provider-owned resources for a remote build
	// after deletion, timeout, cancellation, or failed reconciliation.
	//
	// Implementations must be idempotent for the same BuildID and OperationRef,
	// and must tolerate already-deleted resources.
	CleanupRemoteBuild(ctx context.Context, input RemoteBuildInput) (RemoteBuildCleanupResult, error)
}
