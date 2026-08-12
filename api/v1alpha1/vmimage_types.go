// api/v1alpha1/vmimage_types.go
// +groupName=imagebuilder.io

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase constants for VMImage lifecycle.
const (
	PhasePending      = "Pending"
	PhaseBuilding     = "Building"
	PhaseProvisioning = "Provisioning"
	PhaseUploading    = "Uploading"
	PhaseReady        = "Ready"
	PhaseFailed       = "Failed"
)

const (
	// BuildModeLocal runs the build in a Kubernetes build Job using the local backend.
	BuildModeLocal = "local"
	// BuildModeRemote delegates VM instantiation, provisioning, capture, and registration to a provider.
	BuildModeRemote = "remote"
)

// VMImageSpec defines the desired state of VMImage
type VMImageSpec struct {
	// OS describes the operating system to build
	OS OSSpec `json:"os"`

	// Source describes where to get the base image from
	Source SourceSpec `json:"source"`

	// Provisioners run sequentially after the OS boots.
	// In-process types: cloud-init, shell, file, powershell, sysprep.
	// Built-in init-container types: ansible, chef, puppet, saltstack, custom.
	// Other types run as custom OCI init-container provisioners and require image.
	// +optional
	Provisioners []ProvisionerSpec `json:"provisioners,omitempty"`

	// Targets defines which platforms the image should be uploaded to
	Targets []TargetSpec `json:"targets"`

	// Build controls build-job behaviour
	// +optional
	Build BuildSpec `json:"build,omitempty"`
}

type OSSpec struct {
	// Family is either "linux" or "windows"
	// +kubebuilder:validation:Enum=linux;windows
	Family string `json:"family"`

	// Distribution e.g. ubuntu, debian, rhel, windows-server
	Distribution string `json:"distribution"`

	// Version e.g. "24.04", "2022"
	Version string `json:"version"`

	// Arch defaults to amd64
	// +kubebuilder:default=amd64
	// +kubebuilder:validation:Enum=amd64;arm64
	Arch string `json:"arch,omitempty"`
}

type SourceSpec struct {
	// Type is one of: iso, cloud-image, marketplace, snapshot.
	// snapshot is a provider-native source that registers an image from an
	// existing platform snapshot, for example an AWS EBS snapshot ID in
	// providerRef.
	// +kubebuilder:validation:Enum=iso;cloud-image;marketplace;snapshot
	Type string `json:"type"`

	// URL to download the source image from.
	// Download URLs must use HTTPS — plain HTTP is rejected (AS-015, AS-047, REQ-008).
	// Provider-native remote source identifiers belong in providerRef instead
	// of this URL field.
	// +kubebuilder:validation:Pattern=`^https://`
	// +optional
	URL string `json:"url,omitempty"`

	// ProviderRef is an opaque provider-native source reference for remote
	// builds, for example an AWS AMI ID, vSphere template/VM MoID, Azure image
	// resource ID, GCP image selfLink, or OpenStack Glance image UUID. It is
	// passed through to the selected provider and must not contain credentials
	// or presigned URLs.
	// +optional
	ProviderRef string `json:"providerRef,omitempty"`

	// Checksum in format "algorithm:hex" e.g. "sha256:abc123...".
	// Accepted algorithms: sha256 (64 hex chars), sha512 (128 hex chars). AS-010.
	// +kubebuilder:validation:Pattern=`^(sha256|sha512):[0-9a-f]{64,128}$`
	// +optional
	Checksum string `json:"checksum,omitempty"`

	// BootCommand is a sequence of key strokes sent to the VM over VNC/SPICE
	// immediately after power-on to navigate the boot menu or installer dialog.
	// Only valid when type=iso. The build engine (QEMU) emulates each entry as
	// a key-press event.
	//
	// Special tokens (Packer-compatible):
	//   <enter>   — Enter/Return key
	//   <tab>     — Tab key

	//   <esc>     — Escape key
	//   <up> <down> <left> <right> — cursor keys
	//   <wait>    — wait 1 second before next key
	//   <wait5>   — wait 5 seconds
	//   <wait10>  — wait 10 seconds
	//   <ctrl-x>  — Ctrl+X chord
	//   <f1>…<f12> — function keys
	//
	// Example — Rocky Linux 9 ISO minimal install via kernel args:
	//   - "<tab>"
	//   - " inst.ks=http://{{ .HTTPIP }}:{{ .HTTPPort }}/ks.cfg"
	//   - "<enter><wait10>"
	//
	// +optional
	// +listType=atomic
	BootCommand []string `json:"bootCommand,omitempty"`

	// Installer configures unattended installation media for ISO builds.
	// The builder materializes these files in the workspace and attaches them
	// as read-only seed/answer ISOs next to the source installer ISO.
	// Only valid when type=iso.
	// +optional
	Installer *InstallerMediaSpec `json:"installer,omitempty"`

	// MarketplaceRef references a marketplace image (cloud-specific)
	// +optional
	MarketplaceRef *MarketplaceRef `json:"marketplaceRef,omitempty"`
}

type InstallerMediaSpec struct {
	// Type selects the unattended installer media format.
	// Linux: nocloud, autoinstall, kickstart, preseed.
	// Windows: autounattend.
	// +kubebuilder:validation:Enum=nocloud;autoinstall;kickstart;preseed;autounattend
	Type string `json:"type"`

	// UserData is cloud-init user-data for nocloud/autoinstall media.
	// +optional
	UserData string `json:"userData,omitempty"`

	// MetaData is cloud-init meta-data. If omitted, the builder writes a stable
	// imagebuilder instance-id and hostname.
	// +optional
	MetaData string `json:"metaData,omitempty"`

	// NetworkConfig is optional cloud-init network-config.
	// +optional
	NetworkConfig string `json:"networkConfig,omitempty"`

	// Kickstart is the ks.cfg content for RHEL-compatible installers.
	// +optional
	Kickstart string `json:"kickstart,omitempty"`

	// Preseed is the preseed.cfg content for Debian-compatible installers.
	// +optional
	Preseed string `json:"preseed,omitempty"`

	// Autounattend is Windows Autounattend.xml content. If omitted, the builder
	// can generate one when guest credentials are generated for WinRM.
	// +optional
	Autounattend string `json:"autounattend,omitempty"`

	// Windows contains optional Windows installer media hooks.
	// +optional
	Windows *WindowsInstallerMediaSpec `json:"windows,omitempty"`
}

type WindowsInstallerMediaSpec struct {
	// VirtioDriverPath is a path visible to Windows Setup, for example
	// E:\viostor\2k22\amd64, used in the windowsPE driver search path.
	// The actual driver media must be attached by the runtime environment.
	// +optional
	VirtioDriverPath string `json:"virtioDriverPath,omitempty"`

	// CloudbaseInitMSI is a path visible inside Windows, for example
	// E:\CloudbaseInitSetup.msi, installed during first logon.
	// The actual MSI media must be attached by the runtime environment.
	// +optional
	CloudbaseInitMSI string `json:"cloudbaseInitMsi,omitempty"`

	// CloudbaseInit configures the Cloudbase-Init service after installation.
	// When omitted and cloudbaseInitMsi is set, secure defaults are written.
	// +optional
	CloudbaseInit *CloudbaseInitSpec `json:"cloudbaseInit,omitempty"`
}

type CloudbaseInitSpec struct {
	// MetadataServices is the ordered Cloudbase-Init metadata service list.
	// Defaults to config-drive and NoCloud config-drive services.
	// +optional
	MetadataServices []string `json:"metadataServices,omitempty"`

	// Plugins is the ordered Cloudbase-Init plugin list.
	// Defaults to hostname, networking, user/password, SSH public keys and local scripts.
	// +optional
	Plugins []string `json:"plugins,omitempty"`

	// LocalScriptsPath is the Windows path where Cloudbase-Init local scripts run from.
	// Defaults to C:\Program Files\Cloudbase Solutions\Cloudbase-Init\LocalScripts.
	// +optional
	LocalScriptsPath string `json:"localScriptsPath,omitempty"`

	// AddUserToLocalGroups controls local group membership for Cloudbase-created users.
	// Defaults to Administrators.
	// +optional
	AddUserToLocalGroups []string `json:"addUserToLocalGroups,omitempty"`
}

type MarketplaceRef struct {
	Publisher string `json:"publisher"`
	Offer     string `json:"offer"`
	SKU       string `json:"sku"`
	Version   string `json:"version"`
}

type ProvisionerSpec struct {
	// Type determines how the provisioner runs.
	// In-process: cloud-init, shell, file, powershell, sysprep.
	// Built-in init-container: ansible, chef, puppet, saltstack, custom.
	// Other values are treated as OCI init-container provisioners and require
	// spec.provisioners[].image to select the runtime image.
	Type string `json:"type"`

	// Image is the OCI image for init-container provisioners
	// If omitted, the operator uses its default image for known types
	// +optional
	Image string `json:"image,omitempty"`

	// Inline script or cloud-init config
	// +optional
	Inline string `json:"inline,omitempty"`

	// Source loads provisioner content from an external source. When source.git.path
	// points to a directory, regular files are loaded in lexicographic order and
	// expanded into separate provisioner steps of the same type. Directory
	// expansion is supported only for in-process provisioners; init-container
	// provisioners require a single resolved file per provisioner step.
	// +optional
	Source *ProvisionerSourceSpec `json:"source,omitempty"`

	// Playbook path for ansible (s3://, http://, or local path in workspace)
	// +optional
	Playbook string `json:"playbook,omitempty"`

	// Args passed to the init-container
	// +optional
	Args []string `json:"args,omitempty"`

	// Env variables for init-container provisioners
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// ExtraVars for ansible provisioner
	// +optional
	ExtraVars map[string]string `json:"extraVars,omitempty"`

	// WindowsConfig holds Windows-specific provisioner settings
	// +optional
	WindowsConfig *WindowsProvisionerConfig `json:"windowsConfig,omitempty"`
}

type ProvisionerSourceSpec struct {
	// Git loads provisioner content from a Git repository.
	// +optional
	Git *GitProvisionerSourceSpec `json:"git,omitempty"`
}

type GitProvisionerSourceSpec struct {
	// URL is the HTTPS Git repository URL. Raw IP hosts and private/link-local
	// resolved addresses are rejected by admission and runtime SSRF checks.
	URL string `json:"url"`

	// Ref is the branch, tag, or commit to check out. Production manifests should
	// use an immutable commit SHA.
	Ref string `json:"ref"`

	// Path is a file or directory within the repository. For in-process provisioners,
	// directories are expanded into regular files sorted by relative path, for
	// example 01-base.sh before 02-hardening.sh. Init-container provisioners require
	// this path to resolve to a single file.
	Path string `json:"path"`

	// Auth references credentials for private Git repositories.
	// +optional
	Auth *GitProvisionerAuthSpec `json:"auth,omitempty"`
}

type GitProvisionerAuthSpec struct {
	// SecretRef references a Secret in the VMImage namespace. Use tokenKey for
	// bearer/personal-access tokens, or usernameKey/passwordKey for basic auth.
	// +optional
	SecretRef *GitProvisionerAuthSecretRef `json:"secretRef,omitempty"`

	// TokenPath is a runtime-mounted file containing a bearer or personal access token.
	// +optional
	TokenPath string `json:"tokenPath,omitempty"`

	// UsernamePath is a runtime-mounted file containing the basic auth username.
	// +optional
	UsernamePath string `json:"usernamePath,omitempty"`

	// PasswordPath is a runtime-mounted file containing the basic auth password/token.
	// +optional
	PasswordPath string `json:"passwordPath,omitempty"`

	// RuntimeToken is populated by controllers for provider calls and is never serialized.
	RuntimeToken string `json:"-"`

	// RuntimeUsername is populated by controllers for provider calls and is never serialized.
	RuntimeUsername string `json:"-"`

	// RuntimePassword is populated by controllers for provider calls and is never serialized.
	RuntimePassword string `json:"-"`
}

type GitProvisionerAuthSecretRef struct {
	// Name is the Secret name in the VMImage namespace.
	Name string `json:"name"`

	// TokenKey is the Secret key containing a bearer or personal access token.
	// Defaults to token.
	// +optional
	TokenKey string `json:"tokenKey,omitempty"`

	// UsernameKey is the Secret key containing the basic auth username.
	// Defaults to username.
	// +optional
	UsernameKey string `json:"usernameKey,omitempty"`

	// PasswordKey is the Secret key containing the basic auth password/token.
	// Defaults to password.
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
}

type WindowsProvisionerConfig struct {
	// Autounattend XML content or reference
	// +optional
	Autounattend string `json:"autounattend,omitempty"`

	// VirtioDrivers installs VirtIO drivers (needed for KVM/vSphere)
	// +optional
	VirtioDrivers bool `json:"virtioDrivers,omitempty"`

	// CloudbaseInit installs cloudbase-init (Windows cloud-init equivalent)
	// +optional
	CloudbaseInit bool `json:"cloudbaseInit,omitempty"`

	// Sysprep generalizes the image after provisioning
	// +optional
	Sysprep *SysprepConfig `json:"sysprep,omitempty"`
}

type SysprepConfig struct {
	Generalize bool `json:"generalize"`
	Shutdown   bool `json:"shutdown"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	// +optional
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	SecretKeyRef *SecretKeyRef `json:"secretKeyRef,omitempty"`
}

type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type TargetSpec struct {
	// ProviderConfigRef references a ProviderConfig that holds credentials
	ProviderConfigRef ProviderConfigRef `json:"providerConfigRef"`

	// Format is the image format: ami, ova, ovf, vmdk, qcow2, vhd, raw
	// +kubebuilder:validation:Enum=ami;ova;ovf;vmdk;qcow2;vhd;raw;gcetarball
	Format string `json:"format"`

	// Tags to apply to the registered image in the target platform
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
}

type ProviderConfigRef struct {
	Name string `json:"name"`
}

type BuildSpec struct {
	// Revision is an opaque, user-controlled rebuild token. Change it after a
	// VMImage reaches Ready or Failed to start a new build for the updated spec.
	// Spec changes while a build is active are rejected, and terminal spec
	// changes must include a revision change.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Revision string `json:"revision,omitempty"`

	// Mode selects where the build is executed. local uses the Kubernetes build
	// Job and local backend. remote delegates the build lifecycle to a provider
	// that advertises remote build support. Defaults to local.
	// +kubebuilder:default=local
	// +kubebuilder:validation:Enum=local;remote
	// +optional
	Mode string `json:"mode,omitempty"`

	// Timeout for the entire build including upload. Default: 2h
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// NodeSelector for the build job pods
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Resources for the build job container
	// +optional
	Resources *ResourceRequirements `json:"resources,omitempty"`

	// CacheRef is the legacy shorthand for cache.ref and references a PVC used
	// to cache source images. Prefer cache.ref for new manifests.
	// +optional
	CacheRef *string `json:"cacheRef,omitempty"`

	// Cache configures checksum-addressed source image caching for local builds.
	// +optional
	Cache *SourceCacheSpec `json:"cache,omitempty"`

	// ArtifactStorage controls where build artifacts and workspace files are stored.
	// Defaults to a per-build PVC. Local builds require PVC storage because the
	// build and upload Jobs exchange the artifact through this workspace.
	// +kubebuilder:default:={type:pvc}
	// +optional
	ArtifactStorage *ArtifactStorageSpec `json:"artifactStorage,omitempty"`

	// Upload controls the upload/register Job.
	// +optional
	Upload *UploadSpec `json:"upload,omitempty"`

	// GuestAccess configures how the build engine reaches the temporary build VM
	// for post-install readiness checks and provisioner execution.
	// When omitted, the backend does not expose or wait for a guest management port.
	// +optional
	GuestAccess *GuestAccessSpec `json:"guestAccess,omitempty"`

	// Security configures build-container security exceptions. Defaults are
	// hardened and do not expose host devices.
	// +optional
	Security *BuildSecuritySpec `json:"security,omitempty"`
}

type SourceCacheSpec struct {
	// Ref references the PVC mounted as the source cache.
	// +optional
	Ref string `json:"ref,omitempty"`

	// TTL invalidates a cache entry when the cached file's mtime is older than
	// the configured duration. Omitted means entries do not expire by age.
	// +optional
	TTL *metav1.Duration `json:"ttl,omitempty"`

	// RetainPolicy controls whether verified cache entries remain after a build.
	// Always keeps entries for reuse. Never removes the matching entry after a
	// cache hit and does not persist freshly downloaded entries.
	// +kubebuilder:default=Always
	// +kubebuilder:validation:Enum=Always;Never
	// +optional
	RetainPolicy string `json:"retainPolicy,omitempty"`
}

type UploadSpec struct {
	// Image is the uploader container image. Defaults to the operator's built-in image.
	// +optional
	Image string `json:"image,omitempty"`
}

type BuildSecuritySpec struct {
	// EnableKVM mounts /dev/kvm into the build container for hardware-assisted
	// QEMU acceleration. It is disabled by default and must only be enabled on
	// trusted, dedicated build nodes.
	// +optional
	EnableKVM bool `json:"enableKVM,omitempty"`

	// ProvisionerImages controls supply-chain policy for user-supplied OCI
	// images referenced by spec.provisioners[].image.
	// +optional
	ProvisionerImages *ImagePolicySpec `json:"provisionerImages,omitempty"`
}

type ImagePolicySpec struct {
	// AllowedRegistries restricts image references to these registry prefixes,
	// for example ghcr.io/yourorg or registry.example.com/provisioners.
	// +optional
	AllowedRegistries []string `json:"allowedRegistries,omitempty"`

	// RequireDigest rejects mutable tag-only image references.
	// +optional
	RequireDigest bool `json:"requireDigest,omitempty"`

	// VerifySignature requires an immutable digest reference and is intended to
	// be enforced by cluster admission policy such as Sigstore Policy Controller,
	// Kyverno, or Gatekeeper.
	// +optional
	VerifySignature bool `json:"verifySignature,omitempty"`
}

type GuestAccessSpec struct {
	// Protocol selects the guest management protocol.
	// +kubebuilder:validation:Enum=ssh;winrm
	Protocol string `json:"protocol"`

	// Host is the address the builder should connect to. For QEMU user networking
	// this should remain 127.0.0.1 so the forwarded port is not exposed outside
	// the build pod.
	// +kubebuilder:default:="127.0.0.1"
	// +optional
	Host string `json:"host,omitempty"`

	// HostPort is the local port exposed inside the build container.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	HostPort int32 `json:"hostPort"`

	// User is the guest account used by SSH/WinRM provisioners.
	// +optional
	User string `json:"user,omitempty"`

	// SSHKeyPath is the private key path inside the build container.
	// Prefer mounting a Kubernetes Secret read-only into the build pod and
	// referencing that path here.
	// +optional
	SSHKeyPath string `json:"sshKeyPath,omitempty"`

	// PasswordPath is a file path inside the build container containing the
	// WinRM password. Prefer mounting a Kubernetes Secret read-only and
	// referencing that path here.
	// +optional
	PasswordPath string `json:"passwordPath,omitempty"`

	// Credentials references a Kubernetes Secret mounted read-only into the
	// build container. Explicit sshKeyPath/passwordPath values take precedence.
	// +optional
	Credentials *GuestCredentialsSpec `json:"credentials,omitempty"`

	// GuestPort is the port inside the VM. Defaults are 22 for SSH and 5986 for WinRM.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	GuestPort int32 `json:"guestPort,omitempty"`

	// Timeout bounds how long the build engine waits until the guest is reachable.
	// Defaults to 10 minutes.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// WinRM contains WinRM-specific readiness settings.
	// +optional
	WinRM *WinRMAccessSpec `json:"winrm,omitempty"`
}

type WinRMAccessSpec struct {
	// HTTPS controls whether WinRM readiness uses HTTPS. Defaults to true.
	// +kubebuilder:default=true
	// +optional
	HTTPS *bool `json:"https,omitempty"`

	// InsecureSkipVerify disables TLS certificate verification for HTTPS WinRM.
	// Keep this false for production certificates; set true only for short-lived
	// self-signed installer certificates in isolated build environments.
	// +kubebuilder:default=false
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

type GuestCredentialsSpec struct {
	// SecretRef references a Secret containing guest access credentials.
	// Use this when the installer image already knows the account/password/key
	// through a separately managed unattended install or cloud-init flow.
	// +optional
	SecretRef *GuestCredentialsSecretRef `json:"secretRef,omitempty"`

	// Generate requests short-lived credentials for this build. Generated
	// credentials are written only to the build workspace and are intended to be
	// injected into the temporary guest through cloud-init or autounattend.
	// Mutually exclusive with secretRef.
	// +optional
	Generate *GuestGeneratedCredentialsSpec `json:"generate,omitempty"`

	// Injection controls how generated credentials are injected into the guest.
	// When omitted the builder chooses cloud-init for Linux and autounattend for
	// Windows.
	// +optional
	Injection *GuestCredentialInjectionSpec `json:"injection,omitempty"`
}

type GuestCredentialsSecretRef struct {
	// Name is the Secret name in the VMImage namespace.
	Name string `json:"name"`

	// SSHPrivateKeyKey is the Secret key containing the SSH private key.
	// Defaults to id_ed25519 when omitted.
	// +optional
	SSHPrivateKeyKey string `json:"sshPrivateKeyKey,omitempty"`

	// PasswordKey is the Secret key containing the WinRM password.
	// Defaults to password when omitted.
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
}

type GuestGeneratedCredentialsSpec struct {
	// SSHKey generates an ephemeral Ed25519 SSH key pair for this build.
	// The private key is written to the build pod's generated-credentials
	// emptyDir. The public key is injected into the guest when injection is
	// enabled.
	// +optional
	SSHKey bool `json:"sshKey,omitempty"`

	// Password generates an ephemeral guest password for WinRM or SSH password
	// bootstrap flows. The password is written to the build pod's
	// generated-credentials emptyDir.
	// +optional
	Password bool `json:"password,omitempty"`

	// PasswordLength controls the generated password length. Defaults to 32.
	// +kubebuilder:validation:Minimum=20
	// +kubebuilder:validation:Maximum=128
	// +optional
	PasswordLength int32 `json:"passwordLength,omitempty"`
}

type GuestCredentialInjectionSpec struct {
	// Method selects the guest bootstrap mechanism for generated credentials.
	// none writes credentials for externally handled injection; cloud-init writes
	// a NoCloud seed; autounattend writes Windows unattended-install material.
	// +kubebuilder:validation:Enum=none;cloud-init;autounattend
	// +optional
	Method string `json:"method,omitempty"`
}

type ArtifactStorageSpec struct {
	// Type is the workspace storage strategy.
	// +kubebuilder:validation:Enum=emptyDir;pvc
	// +kubebuilder:default=pvc
	// +optional
	Type string `json:"type,omitempty"`

	// PVC configures persistent workspace storage.
	// +optional
	PVC *ArtifactPVCSpec `json:"pvc,omitempty"`
}

type ArtifactPVCSpec struct {
	// ClaimName uses an existing PVC instead of creating a per-build PVC.
	// This is intended for explicitly managed shared storage.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// StorageClassName is used when the operator creates the PVC.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Size is the requested PVC size. Defaults to spec.build.resources.storage
	// when set, otherwise 20Gi.
	// +optional
	Size string `json:"size,omitempty"`

	// AccessMode selects shared vs block-style semantics.
	// ReadWriteMany is suitable for shared storage; ReadWriteOnce and
	// ReadWriteOncePod are suitable for block storage.
	// +kubebuilder:validation:Enum=ReadWriteOnce;ReadWriteMany;ReadWriteOncePod
	// +kubebuilder:default=ReadWriteOnce
	// +optional
	AccessMode string `json:"accessMode,omitempty"`

	// RetainPolicy controls cleanup of operator-created PVCs.
	// Never retains no PVC after success or failure; OnFailure keeps it only
	// when the VMImage fails; Always keeps it.
	// +kubebuilder:validation:Enum=Never;OnFailure;Always
	// +kubebuilder:default=Never
	// +optional
	RetainPolicy string `json:"retainPolicy,omitempty"`
}

type ResourceRequirements struct {
	// +optional
	CPU string `json:"cpu,omitempty"`
	// +optional
	Memory string `json:"memory,omitempty"`
	// +optional
	Storage string `json:"storage,omitempty"`
}

// VMImageStatus defines the observed state of VMImage
type VMImageStatus struct {
	// ObservedGeneration is the metadata generation for which this status was
	// produced. A lower value means the current spec has not been accepted by
	// the controller yet.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedRevision is the spec.build.revision represented by this status.
	// Change spec.build.revision on a terminal VMImage to request a rebuild.
	// +optional
	ObservedRevision string `json:"observedRevision,omitempty"`

	// Phase is the current lifecycle phase
	// +kubebuilder:validation:Enum=Pending;Building;Provisioning;Uploading;Ready;Failed
	Phase string `json:"phase,omitempty"`

	// Images contains references to the registered images per platform
	// +optional
	Images []ImageStatus `json:"images,omitempty"`

	// BuildArtifact describes the artifact produced by the build Job.
	// +optional
	BuildArtifact *ArtifactStatus `json:"buildArtifact,omitempty"`

	// Steps describes high-level pipeline progress. Messages are intentionally
	// non-sensitive and must not contain credentials, scripts, or environment values.
	// +optional
	// +listType=map
	// +listMapKey=name
	Steps []PipelineStepStatus `json:"steps,omitempty"`

	// ProvisionerResultRef references the non-secret provisioner result file
	// written by the builder in the workspace.
	// +optional
	ProvisionerResultRef string `json:"provisionerResultRef,omitempty"`

	// HygieneResult describes provider-neutral final image hygiene checks.
	// Remote build providers use this to attest sanitization without exposing
	// secrets or detailed logs in Kubernetes status.
	// +optional
	HygieneResult *HygieneResultStatus `json:"hygieneResult,omitempty"`

	// Conditions follow the standard Kubernetes condition pattern
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// StartTime of the build
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime of the build
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// BuildJobRef references the Kubernetes Job running the build
	// +optional
	BuildJobRef *string `json:"buildJobRef,omitempty"`

	// UploadJobRef references the Kubernetes Job running provider upload/register.
	// +optional
	UploadJobRef *string `json:"uploadJobRef,omitempty"`

	// UploadOperations tracks provider-neutral upload/register progress per target.
	// OperationRef and ImageRef are opaque provider identifiers and must not
	// contain credentials, presigned URLs, or other secret material.
	// +optional
	// +listType=map
	// +listMapKey=providerConfig
	// +listMapKey=format
	UploadOperations []UploadOperationStatus `json:"uploadOperations,omitempty"`

	// RemoteBuildRef is the provider-specific operation reference for a remote build.
	// It is intentionally opaque and must not contain secrets.
	// +optional
	RemoteBuildRef *string `json:"remoteBuildRef,omitempty"`

	// RemoteRetryCount is the number of consecutive transient provider errors
	// for the current remote build. It resets after a successful provider call.
	// +optional
	// +kubebuilder:validation:Minimum=0
	RemoteRetryCount int32 `json:"remoteRetryCount,omitempty"`

	// NextRemoteRetryTime is the earliest time at which the controller will call
	// the remote provider again after a transient error.
	// +optional
	NextRemoteRetryTime *metav1.Time `json:"nextRemoteRetryTime,omitempty"`

	// BuildLeaseRefs references scheduler leases held while the build job is active.
	// Values are namespace/name strings.
	// +optional
	BuildLeaseRefs []string `json:"buildLeaseRefs,omitempty"`

	// ScheduledNodeName is retained for backward compatibility with older
	// controller versions that performed direct node binding. New controllers
	// leave it empty and delegate final placement to kube-scheduler.
	// Deprecated: do not use for placement decisions.
	// +optional
	ScheduledNodeName string `json:"scheduledNodeName,omitempty"`
}

type PipelineStepStatus struct {
	// Name is a stable pipeline step name.
	// +kubebuilder:validation:Enum=Build;Boot;Readiness;Provisioning;Sanitization;Upload;Cleanup
	Name string `json:"name"`

	// Status is the current step status.
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Skipped
	Status string `json:"status"`

	// Reason is a machine-readable short reason.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message is a human-readable, non-sensitive summary.
	// +optional
	Message string `json:"message,omitempty"`

	// LastTransitionTime follows the Kubernetes condition timestamp pattern.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`

	// ResultRef references a non-secret file produced by this step.
	// +optional
	ResultRef string `json:"resultRef,omitempty"`
}

type ArtifactStatus struct {
	// Path is the artifact path as reported by the build container.
	Path string `json:"path"`

	// Format of the produced artifact.
	Format string `json:"format"`

	// Checksum in algorithm:hex format.
	Checksum string `json:"checksum"`

	// SizeBytes is the artifact size.
	SizeBytes int64 `json:"sizeBytes"`

	// OS is the operating system family of the artifact.
	OS string `json:"os"`

	// Metadata carries build-backend specific metadata.
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`
}

type HygieneResultStatus struct {
	// Status is provider-neutral: passed, failed, or unknown.
	// +kubebuilder:validation:Enum=passed;failed;unknown
	Status string `json:"status"`

	// Message is a short non-sensitive summary.
	// +optional
	Message string `json:"message,omitempty"`

	// Checks contains non-secret check identifiers.
	// +optional
	Checks []string `json:"checks,omitempty"`

	// ResultRef optionally references a non-secret detailed report.
	// +optional
	ResultRef string `json:"resultRef,omitempty"`
}

type ImageStatus struct {
	// Provider name e.g. "aws", "vsphere"
	Provider string `json:"provider"`

	// ProviderConfig name used for this target
	ProviderConfig string `json:"providerConfig"`

	// ImageRef is the platform-specific image identifier (AMI-ID, UUID, MOID...)
	ImageRef string `json:"imageRef"`

	// Location is the region, datacenter, or project
	// +optional
	Location string `json:"location,omitempty"`

	// Format of the registered image
	Format string `json:"format"`

	// Checksum of the artifact that was uploaded
	// +optional
	Checksum string `json:"checksum,omitempty"`
}

type UploadOperationStatus struct {
	// Provider name e.g. "aws", "vsphere".
	Provider string `json:"provider"`

	// ProviderConfig name used for this target.
	ProviderConfig string `json:"providerConfig"`

	// Format requested for this target.
	Format string `json:"format"`

	// Phase is the provider-neutral upload/register operation phase.
	// +kubebuilder:validation:Enum=Pending;Uploading;Registering;Succeeded;Failed
	Phase string `json:"phase"`

	// OperationRef is an opaque, non-secret provider operation reference.
	// +optional
	OperationRef string `json:"operationRef,omitempty"`

	// ImageRef is the final registered image reference when known.
	// +optional
	ImageRef string `json:"imageRef,omitempty"`

	// Message is a short non-sensitive progress or error summary.
	// +optional
	Message string `json:"message,omitempty"`

	// LastTransitionTime follows the Kubernetes condition timestamp pattern.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`

	// UploadMilliseconds records provider upload duration when reported by the upload job.
	// +optional
	UploadMilliseconds int64 `json:"uploadMilliseconds,omitempty"`

	// UploadBytes records the uploaded artifact size when reported by the upload job.
	// +optional
	// +kubebuilder:validation:Minimum=0
	UploadBytes int64 `json:"uploadBytes,omitempty"`

	// RegisterMilliseconds records provider registration duration when reported by the upload job.
	// +optional
	RegisterMilliseconds int64 `json:"registerMilliseconds,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="OS",type=string,JSONPath=`.spec.os.distribution`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.os.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// VMImage is the Schema for the vmimages API
type VMImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VMImageSpec   `json:"spec,omitempty"`
	Status VMImageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VMImageList contains a list of VMImage
type VMImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VMImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(addKnownTypes(&VMImage{}, &VMImageList{}))
}
