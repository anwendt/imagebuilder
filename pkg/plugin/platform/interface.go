// pkg/plugin/platform/interface.go
//
// This file defines the stable Plugin interface. It is the contract between
// the core operator and all platform providers (built-in and external).
//
// IMPORTANT: Do not add breaking changes to this interface.
// Additive changes are fine. Removing or changing signatures requires a new
// interface version (e.g. platform/v2).

package platform

import (
	"context"
	"io"
	"time"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

// ImageFormat represents a VM image format.
type ImageFormat string

const (
	FormatAMI        ImageFormat = "ami"
	FormatOVA        ImageFormat = "ova"
	FormatOVF        ImageFormat = "ovf"
	FormatVMDK       ImageFormat = "vmdk"
	FormatQCOW2      ImageFormat = "qcow2"
	FormatVHD        ImageFormat = "vhd"
	FormatRaw        ImageFormat = "raw"
	FormatGCETarball ImageFormat = "gcetarball"
)

// OSFamily represents a family of operating systems.
type OSFamily string

const (
	OSFamilyLinux   OSFamily = "linux"
	OSFamilyWindows OSFamily = "windows"
)

// Plugin is the interface every platform provider must implement.
// Built-in providers compile this interface directly. External providers
// implement it via the gRPC adapter (see pkg/plugin/grpc/adapter.go).
type Plugin interface {
	// Identity — these must be stable and match the PlatformProvider name.
	Name() string    // e.g. "aws", "vsphere", "openstack"
	Version() string // semver e.g. "v1.2.0"

	// Capabilities — called once during provider registration.
	SupportedFormats() []ImageFormat
	SupportedOS() []OSFamily

	// Lifecycle

	// Init is called exactly once for an operation instance.
	// cfg contains the resolved ProviderConfig (credentials already loaded from Secret).
	Init(ctx context.Context, cfg PluginConfig) error

	// Validate checks the TargetSpec before a build starts.
	// Return a descriptive error if the config is invalid.
	Validate(ctx context.Context, spec v1alpha1.TargetSpec) error

	// Core operations — called per VMImage target.

	// Upload streams the build artifact to the provider.
	// The provider is responsible for format conversion if needed.
	Upload(ctx context.Context, artifact *BuildArtifact) (*UploadResult, error)

	// Register makes the uploaded artifact available as a platform image.
	// For AWS this registers an AMI, for vSphere it marks a VM template, etc.
	Register(ctx context.Context, result *UploadResult) (*ImageRef, error)

	// Cleanup removes temporary artifacts on failure.
	// Must be idempotent — may be called even if Upload never completed.
	Cleanup(ctx context.Context, artifact *BuildArtifact) error

	// Health — called periodically by the operator.
	HealthCheck(ctx context.Context) error
}

// ClosePlugin is an optional lifecycle capability for providers that own
// transports or other resources which must be released after an operation.
type ClosePlugin interface {
	Plugin
	Close() error
}

// RemoteBuildPlugin is an optional additive provider capability. Providers
// implement this interface when they can execute the full build lifecycle on
// the target platform without a local QEMU build Job.
type RemoteBuildPlugin interface {
	Plugin

	// SupportedBuildModes returns supported execution modes. Providers that
	// implement RemoteBuildPlugin should include "remote".
	SupportedBuildModes() []string

	// ReconcileRemoteBuild starts or continues a provider-owned remote build.
	// The method must be idempotent for the same VMImage UID and OperationRef.
	ReconcileRemoteBuild(ctx context.Context, req *RemoteBuildRequest) (*RemoteBuildResult, error)
}

// RemoteBuildCleanupPlugin is an optional additive provider capability for
// providers that can clean up provider-owned remote build resources after
// timeout, cancellation, deletion, or failed reconciliation.
type RemoteBuildCleanupPlugin interface {
	RemoteBuildPlugin

	// CleanupRemoteBuild removes temporary provider resources for a remote build.
	// It must be idempotent and tolerate already-removed resources.
	CleanupRemoteBuild(ctx context.Context, req *RemoteBuildRequest) error
}

// PluginConfig holds resolved configuration passed to Init().
type PluginConfig struct {
	// ProviderConfigName is the name of the ProviderConfig CR
	ProviderConfigName string

	// SecretData contains the decoded secret referenced by ProviderConfig.credentials
	SecretData map[string][]byte

	// Region / Endpoint from ProviderConfigSpec
	Region   string
	Endpoint string
	Insecure bool

	// Extra holds provider-specific key-value config from ProviderConfigSpec.extra
	Extra map[string]string

	// Endpoint allowlists are resolved from ProviderConfig.networkAccess and
	// must be applied to every provider-specific endpoint override.
	AllowedPrivateCIDRs []string
	AllowedDNSNames     []string
}

// BuildArtifact is the result of a successful build step, ready for upload.
type BuildArtifact struct {
	// Path is the local filesystem path to the artifact file
	Path string

	// Format of the artifact
	Format ImageFormat

	// Checksum in format "algorithm:hex"
	Checksum string

	// SizeBytes is the artifact size
	SizeBytes int64

	// OS of the built image
	OS OSFamily

	// Metadata carries additional info (distro, version, build-id, ...)
	Metadata map[string]string
}

// StreamArtifact is the sequential representation used by external provider
// SDK servers. The caller owns Reader and keeps it valid until UploadStream
// returns. Providers must not retain it or assume seek/random-access support.
type StreamArtifact struct {
	Reader     io.Reader
	Format     ImageFormat
	Checksum   string
	SizeBytes  int64
	OS         OSFamily
	Metadata   map[string]string
	OnProgress func(bytesRead int64) error
}

// StreamingPlugin is an optional additive capability for providers that can
// forward a gRPC artifact stream directly to the target platform. Providers
// requiring random access or archive inspection may omit it and retain their
// bounded spool path.
type StreamingPlugin interface {
	Plugin
	UploadStream(ctx context.Context, artifact *StreamArtifact) (*UploadResult, error)
}

// UploadSession is a durable, non-secret checkpoint for retrying one target
// upload. ResumeToken is opaque provider state. CommittedOffset is the first
// byte that still needs to be sent. ResumeMode is "restart" or "offset".
type UploadSession struct {
	IdempotencyKey  string
	ResumeToken     string
	CommittedOffset int64
	ResumeMode      string
}

// UploadCheckpoint is called only for provider-acknowledged session state.
// Callers must persist the checkpoint before considering it resumable.
type UploadCheckpoint func(UploadSession) error

// ResumablePlugin is an optional additive capability. Implementations must use
// IdempotencyKey to identify the same logical upload across process retries.
type ResumablePlugin interface {
	Plugin
	UploadResumable(ctx context.Context, artifact *BuildArtifact, session UploadSession, checkpoint UploadCheckpoint) (*UploadResult, error)
}

// ResumableStreamingPlugin is implemented when provider-native durable state
// can resume a sequential stream at CommittedOffset. PrepareUpload must
// reconstruct backend state without relying on process memory.
type ResumableStreamingPlugin interface {
	StreamingPlugin
	PrepareUpload(ctx context.Context, artifact *BuildArtifact, requested UploadSession) (UploadSession, error)
	UploadStreamResumable(ctx context.Context, artifact *StreamArtifact, session UploadSession, checkpoint UploadCheckpoint) (*UploadResult, error)
}

// UploadResult is returned by Upload() and passed to Register().
type UploadResult struct {
	// ProviderRef is a provider-specific handle to the uploaded artifact
	// e.g. an S3 key, a Glance UUID, a vSphere datastore path
	ProviderRef string

	// Metadata carries provider-specific intermediate state
	Metadata map[string]string
}

// ImageRef is the final reference to the registered image in the target platform.
type ImageRef struct {
	// ID is the platform-specific image identifier
	// AWS: ami-0abc123  vSphere: vm-123 (MOID)  OpenStack: uuid  GCP: projects/.../global/images/name
	ID string

	// Name is the human-readable image name
	Name string

	// Location is the region, datacenter, or project where the image was registered
	Location string

	// Tags applied to the image in the target platform
	Tags map[string]string
}

type RemoteBuildRequest struct {
	BuildID           string
	OperationRef      string
	ImageName         string
	Namespace         string
	OSFamily          OSFamily
	OSDistribution    string
	OSVersion         string
	OSArch            string
	SourceType        string
	SourceURL         string
	SourceProviderRef string
	SourceMarketplace *v1alpha1.MarketplaceRef
	SourceChecksum    string
	Target            v1alpha1.TargetSpec
	Provisioners      []v1alpha1.ProvisionerSpec
	GuestAccess       *v1alpha1.GuestAccessSpec
	Evidence          *v1alpha1.EvidenceSpec
	Timeout           time.Duration
}

type RemoteBuildPhase string

const (
	RemoteBuildPhasePending      RemoteBuildPhase = "Pending"
	RemoteBuildPhaseBooting      RemoteBuildPhase = "Booting"
	RemoteBuildPhaseReadiness    RemoteBuildPhase = "Readiness"
	RemoteBuildPhaseProvisioning RemoteBuildPhase = "Provisioning"
	RemoteBuildPhaseSanitizing   RemoteBuildPhase = "Sanitizing"
	RemoteBuildPhaseRegistering  RemoteBuildPhase = "Registering"
	RemoteBuildPhaseReady        RemoteBuildPhase = "Ready"
)

type RemoteBuildResult struct {
	OperationRef string
	Phase        RemoteBuildPhase
	Message      string
	Images       []RemoteImageRef
	Artifact     *BuildArtifact
	Hygiene      *RemoteHygieneResult
	Evidence     *RemoteEvidenceResult
	Done         bool
}

type RemoteHygieneResult struct {
	Status    string
	Message   string
	Checks    []string
	ResultRef string
}

type RemoteEvidenceResult struct {
	Status                 string `json:"status"`
	Message                string `json:"message,omitempty"`
	SBOMRef                string `json:"sbomRef"`
	VulnerabilityReportRef string `json:"vulnerabilityReportRef"`
	ProvenanceRef          string `json:"provenanceRef"`
	SignatureRef           string `json:"signatureRef"`
}

type RemoteImageRef struct {
	Provider       string
	ProviderConfig string
	ImageRef       ImageRef
	Format         ImageFormat
	Checksum       string
}
