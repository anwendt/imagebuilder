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

	"github.com/anwendt/imagebuilder/api/v1alpha1"
)

// ImageFormat represents a VM image format.
type ImageFormat string

const (
	FormatAMI       ImageFormat = "ami"
	FormatOVA       ImageFormat = "ova"
	FormatOVF       ImageFormat = "ovf"
	FormatVMDK      ImageFormat = "vmdk"
	FormatQCOW2     ImageFormat = "qcow2"
	FormatVHD       ImageFormat = "vhd"
	FormatRaw       ImageFormat = "raw"
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

	// Init is called once when the provider is registered.
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
