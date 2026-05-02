// api/v1alpha1/platformprovider_types.go

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PlatformProviderSpec defines the desired state of PlatformProvider.
// Analog to Crossplane's Provider CRD — the operator pulls the OCI image
// and starts it as a Kubernetes Deployment. The provider pod runs a gRPC
// server implementing the PlatformProvider protobuf service.
type PlatformProviderSpec struct {
	// Package is the OCI image reference for the provider
	// e.g. ghcr.io/yourorg/imagebuilder-provider-aws:v1.2.0
	Package string `json:"package"`

	// PackagePullPolicy controls when the image is pulled
	// +kubebuilder:default=IfNotPresent
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	PackagePullPolicy string `json:"packagePullPolicy,omitempty"`

	// PackagePullSecrets are references to secrets for pulling the provider image
	// +optional
	PackagePullSecrets []string `json:"packagePullSecrets,omitempty"`

	// ControllerConfigRef references a ControllerConfig for resource overrides
	// +optional
	ControllerConfigRef *string `json:"controllerConfigRef,omitempty"`

	// Security controls supply-chain policy for the provider package.
	// +optional
	Security *ProviderPackageSecuritySpec `json:"security,omitempty"`

	// Transport configures operator-to-provider gRPC transport security.
	// Defaults to plaintext TCP inside the namespace-local ClusterIP and
	// NetworkPolicy trust boundary. Use mTLS for cross-boundary endpoints.
	// +optional
	Transport *ProviderTransportSpec `json:"transport,omitempty"`
}

type ProviderTransportSpec struct {
	// TLS configures gRPC TLS. When mode is Mutual, the operator verifies the
	// provider certificate against caSecretRef and presents client credentials
	// from clientCertificateSecretRef.
	// +optional
	TLS *ProviderTransportTLSSpec `json:"tls,omitempty"`
}

type ProviderTransportTLSSpec struct {
	// Mode selects provider gRPC TLS behavior.
	// Disabled keeps plaintext TCP and is valid only inside the local
	// ClusterIP + NetworkPolicy trust boundary. Mutual enables mTLS.
	// +kubebuilder:validation:Enum=Disabled;Mutual
	// +kubebuilder:default=Disabled
	// +optional
	Mode string `json:"mode,omitempty"`

	// ServerName is the expected DNS name in the provider server certificate.
	// If omitted, the provider service DNS name is used.
	// +optional
	ServerName string `json:"serverName,omitempty"`

	// CASecretRef references the CA bundle used to verify the provider server.
	// Required when mode=Mutual.
	// +optional
	CASecretRef *ProviderTLSSecretRef `json:"caSecretRef,omitempty"`

	// ClientCertificateSecretRef references the operator client certificate and
	// private key presented to the provider. Required when mode=Mutual.
	// +optional
	ClientCertificateSecretRef *ProviderTLSSecretRef `json:"clientCertificateSecretRef,omitempty"`

	// ServerCertificateSecretRef references the provider server certificate and
	// private key mounted into the provider pod. Required when mode=Mutual for
	// operator-managed provider Deployments.
	// +optional
	ServerCertificateSecretRef *ProviderTLSSecretRef `json:"serverCertificateSecretRef,omitempty"`
}

type ProviderTLSSecretRef struct {
	// Name is the Secret name.
	Name string `json:"name"`

	// Namespace is the Secret namespace. Required for cluster-scoped
	// PlatformProvider resources.
	Namespace string `json:"namespace"`

	// CAKey is the key containing a PEM CA bundle. Defaults to ca.crt.
	// +optional
	CAKey string `json:"caKey,omitempty"`

	// CertKey is the key containing a PEM certificate. Defaults to tls.crt.
	// +optional
	CertKey string `json:"certKey,omitempty"`

	// KeyKey is the key containing a PEM private key. Defaults to tls.key.
	// +optional
	KeyKey string `json:"keyKey,omitempty"`
}

type ProviderPackageSecuritySpec struct {
	// AllowedRegistries restricts package references to these registry prefixes,
	// for example ghcr.io/yourorg or registry.example.com/platform.
	// +optional
	AllowedRegistries []string `json:"allowedRegistries,omitempty"`

	// RequireDigest rejects mutable tag-only image references.
	// +optional
	RequireDigest bool `json:"requireDigest,omitempty"`

	// VerifySignature marks this provider as requiring signature verification.
	// The core enforces immutable digest references and records the policy in
	// the Deployment; cluster admission can bind this to cosign/Sigstore.
	// +optional
	VerifySignature bool `json:"verifySignature,omitempty"`
}

type PlatformProviderStatus struct {
	// Phase of the provider lifecycle
	// +kubebuilder:validation:Enum=Installing;Healthy;Unhealthy;Unknown
	Phase string `json:"phase,omitempty"`

	// CurrentRevision is the resolved image digest currently running
	// +optional
	CurrentRevision string `json:"currentRevision,omitempty"`

	// Capabilities reported by the provider after successful handshake
	// +optional
	Capabilities *ProviderCapabilities `json:"capabilities,omitempty"`

	// Conditions follow the standard Kubernetes condition pattern
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type ProviderCapabilities struct {
	ProviderName    string   `json:"providerName"`
	ProviderVersion string   `json:"providerVersion"`
	Formats         []string `json:"formats"`
	OSFamilies      []string `json:"osFamilies"`
	// BuildModes lists supported build execution modes. Providers that omit
	// this field are treated as supporting only local upload/register flows.
	// +optional
	BuildModes      []string `json:"buildModes,omitempty"`
	ProtocolVersion string   `json:"protocolVersion"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.spec.package`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PlatformProvider installs a platform plugin as a Kubernetes Deployment.
// The operator pulls the specified OCI image and starts it as a pod running
// a gRPC server. Once healthy, the provider is available for VMImage targets.
type PlatformProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PlatformProviderSpec   `json:"spec,omitempty"`
	Status PlatformProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type PlatformProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlatformProvider `json:"items"`
}

// ---------------------------------------------------------------------------

// ProviderConfigSpec holds credentials and endpoint configuration for one
// instance of a platform provider. Multiple ProviderConfigs can exist for
// the same provider (e.g. aws-eu-west-1 and aws-us-east-1).
type ProviderConfigSpec struct {
	// Provider is the name of the PlatformProvider this config belongs to
	// e.g. "aws", "vsphere", "openstack"
	Provider string `json:"provider"`

	// Credentials reference for the platform
	Credentials CredentialsSpec `json:"credentials"`

	// Region for AWS/Azure/GCP
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint for on-prem platforms (vSphere, OpenStack)
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Insecure disables TLS verification (development only)
	// +optional
	Insecure bool `json:"insecure,omitempty"`

	// Extra holds provider-specific configuration as raw JSON
	// +optional
	Extra map[string]string `json:"extra,omitempty"`
}

type CredentialsSpec struct {
	// SecretRef references a Kubernetes Secret containing the credentials.
	// The secret format is provider-specific and documented in each provider.
	SecretRef SecretRef `json:"secretRef"`
}

type SecretRef struct {
	Name string `json:"name"`
	// Key is the data key within the Secret. If omitted, the whole Secret is used.
	// +optional
	Key string `json:"key,omitempty"`
	// Namespace is intentionally omitted: the Secret must reside in the same namespace
	// as the ProviderConfig to prevent cross-namespace credential access (AS-005, SR-005).
}

type ProviderConfigStatus struct {
	// Conditions follow the standard Kubernetes condition pattern
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ProviderConfig holds credentials and endpoint config for a platform provider instance.
// Intentionally namespace-scoped (no scope=Cluster): a VMImage may only reference a
// ProviderConfig in its own namespace, preventing cross-namespace credential access.
// See AS-005 (REQ-008) and SR-005 (REQ-004).
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderConfigSpec   `json:"spec,omitempty"`
	Status ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&PlatformProvider{}, &PlatformProviderList{},
		&ProviderConfig{}, &ProviderConfigList{},
	)
}
