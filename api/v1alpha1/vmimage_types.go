// api/v1alpha1/vmimage_types.go
// +groupName=imagebuilder.io

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VMImageSpec defines the desired state of VMImage
type VMImageSpec struct {
	// OS describes the operating system to build
	OS OSSpec `json:"os"`

	// Source describes where to get the base image from
	Source SourceSpec `json:"source"`

	// Provisioners run sequentially after the OS boots
	// In-process types: cloud-init, shell, file, powershell, sysprep
	// Init-container types: ansible, chef, puppet, saltstack, custom
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
	// Type is one of: iso, cloud-image, marketplace
	// +kubebuilder:validation:Enum=iso;cloud-image;marketplace
	Type string `json:"type"`

	// URL to download the source image from.
	// Must use HTTPS — plain HTTP is rejected (AS-015, AS-047, REQ-008).
	// +kubebuilder:validation:Pattern=`^https://`
	// +optional
	URL string `json:"url,omitempty"`

	// Checksum in format "algorithm:hex" e.g. "sha256:abc123...".
	// Accepted algorithms: sha256 (64 hex chars), sha512 (128 hex chars). AS-010.
	// +kubebuilder:validation:Pattern=`^(sha256|sha512):[0-9a-f]{64,128}$`
	// +optional
	Checksum string `json:"checksum,omitempty"`

	// MarketplaceRef references a marketplace image (cloud-specific)
	// +optional
	MarketplaceRef *MarketplaceRef `json:"marketplaceRef,omitempty"`
}

type MarketplaceRef struct {
	Publisher string `json:"publisher"`
	Offer     string `json:"offer"`
	SKU       string `json:"sku"`
	Version   string `json:"version"`
}

type ProvisionerSpec struct {
	// Type determines how the provisioner runs.
	// In-process: cloud-init, shell, file, powershell, sysprep
	// Init-container: ansible, chef, puppet, saltstack, custom
	// Unknown values are rejected by the admission webhook (AS-026, AS-027, REQ-008).
	// +kubebuilder:validation:Enum=cloud-init;shell;file;powershell;sysprep;ansible;chef;puppet;saltstack;custom
	Type string `json:"type"`

	// Image is the OCI image for init-container provisioners
	// If omitted, the operator uses its default image for known types
	// +optional
	Image string `json:"image,omitempty"`

	// Inline script or cloud-init config
	// +optional
	Inline string `json:"inline,omitempty"`

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
	// Timeout for the entire build including upload. Default: 2h
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// NodeSelector for the build job pods
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Resources for the build job container
	// +optional
	Resources *ResourceRequirements `json:"resources,omitempty"`

	// CacheRef references a PVC used to cache source images
	// +optional
	CacheRef *string `json:"cacheRef,omitempty"`
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
	// Phase is the current lifecycle phase
	// +kubebuilder:validation:Enum=Pending;Building;Provisioning;Uploading;Ready;Failed
	Phase string `json:"phase,omitempty"`

	// Images contains references to the registered images per platform
	// +optional
	Images []ImageStatus `json:"images,omitempty"`

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
	SchemeBuilder.Register(&VMImage{}, &VMImageList{})
}
