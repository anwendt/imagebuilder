// api/v1alpha1/platformprovider_webhook.go
//
// Validating Admission Webhook for PlatformProvider production policies.
//
// +kubebuilder:webhook:path=/validate-imagebuilder-io-v1alpha1-platformprovider,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,groups=imagebuilder.io,resources=platformproviders,verbs=create;update,versions=v1alpha1,name=vplatformprovider.kb.io,admissionReviewVersions=v1,timeoutSeconds=10,serviceName=imagebuilder-webhook-service,serviceNamespace=imagebuilder-system

package v1alpha1

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// PlatformProviderAdmissionPolicy is configured by the operator process and
// lets production installs fail closed at admission time.
type PlatformProviderAdmissionPolicy struct {
	RequireMTLS       bool
	RequireDigest     bool
	RequireSignature  bool
	AllowedRegistries []string
	ProviderNamespace string
}

var platformProviderAdmissionPolicy = struct {
	sync.RWMutex
	value PlatformProviderAdmissionPolicy
}{}

// SetPlatformProviderAdmissionPolicy configures PlatformProvider admission.
func SetPlatformProviderAdmissionPolicy(policy PlatformProviderAdmissionPolicy) {
	platformProviderAdmissionPolicy.Lock()
	defer platformProviderAdmissionPolicy.Unlock()
	platformProviderAdmissionPolicy.value = policy
}

func currentPlatformProviderAdmissionPolicy() PlatformProviderAdmissionPolicy {
	platformProviderAdmissionPolicy.RLock()
	defer platformProviderAdmissionPolicy.RUnlock()
	policy := platformProviderAdmissionPolicy.value
	policy.AllowedRegistries = append([]string(nil), policy.AllowedRegistries...)
	return policy
}

// SetupWebhookWithManager registers the PlatformProvider validating webhook.
func (r *PlatformProvider) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).WithValidator(platformProviderValidator{}).Complete()
}

var _ admission.Validator[*PlatformProvider] = platformProviderValidator{}

type platformProviderValidator struct{}

func (platformProviderValidator) ValidateCreate(_ context.Context, obj *PlatformProvider) (admission.Warnings, error) {
	return obj.validatePlatformProvider()
}

func (platformProviderValidator) ValidateUpdate(_ context.Context, _ *PlatformProvider, newObj *PlatformProvider) (admission.Warnings, error) {
	return newObj.validatePlatformProvider()
}

func (platformProviderValidator) ValidateDelete(_ context.Context, _ *PlatformProvider) (admission.Warnings, error) {
	return nil, nil
}

func (r *PlatformProvider) ValidateCreate() (admission.Warnings, error) {
	return r.validatePlatformProvider()
}

func (r *PlatformProvider) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return r.validatePlatformProvider()
}

func (r *PlatformProvider) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (r *PlatformProvider) validatePlatformProvider() (admission.Warnings, error) {
	var errs []error
	policy := currentPlatformProviderAdmissionPolicy()

	if strings.TrimSpace(r.Spec.Package) == "" {
		errs = append(errs, fmt.Errorf("spec.package is required"))
	}
	if err := validatePlatformProviderPackagePolicy(r, policy); err != nil {
		errs = append(errs, err)
	}
	if err := validatePlatformProviderTransportPolicy(r, policy); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return nil, joinErrors(errs)
	}
	return nil, nil
}

func validatePlatformProviderPackagePolicy(pp *PlatformProvider, policy PlatformProviderAdmissionPolicy) error {
	image := pp.Spec.Package
	if policy.RequireDigest && !strings.Contains(image, "@sha256:") {
		return fmt.Errorf("spec.package must be pinned by digest by operator policy")
	}
	if policy.RequireSignature && (pp.Spec.Security == nil || !pp.Spec.Security.VerifySignature) {
		return fmt.Errorf("spec.security.verifySignature is required by operator policy")
	}
	if len(policy.AllowedRegistries) > 0 && !imageAllowedByPrefixes(image, policy.AllowedRegistries) {
		return fmt.Errorf("spec.package registry is not allowed by operator policy")
	}

	security := pp.Spec.Security
	if security == nil {
		return nil
	}
	if security.RequireDigest || security.VerifySignature {
		if !strings.Contains(image, "@sha256:") {
			return fmt.Errorf("spec.package must be pinned by digest when requireDigest or verifySignature is enabled")
		}
	}
	if len(security.AllowedRegistries) > 0 && !imageAllowedByPrefixes(image, security.AllowedRegistries) {
		return fmt.Errorf("spec.package registry is not in spec.security.allowedRegistries")
	}
	return nil
}

func validatePlatformProviderTransportPolicy(pp *PlatformProvider, policy PlatformProviderAdmissionPolicy) error {
	tlsSpec := platformProviderMutualTLS(pp)
	if tlsSpec == nil {
		if pp.Spec.Transport != nil && pp.Spec.Transport.TLS != nil &&
			pp.Spec.Transport.TLS.Mode != "" && pp.Spec.Transport.TLS.Mode != "Disabled" {
			return fmt.Errorf("spec.transport.tls.mode must be Disabled or Mutual")
		}
		if policy.RequireMTLS {
			return fmt.Errorf("provider mTLS is required by operator policy; set spec.transport.tls.mode=Mutual")
		}
		return nil
	}

	if tlsSpec.CASecretRef == nil {
		return fmt.Errorf("spec.transport.tls.caSecretRef is required when mode=Mutual")
	}
	if tlsSpec.ClientCertificateSecretRef == nil {
		return fmt.Errorf("spec.transport.tls.clientCertificateSecretRef is required when mode=Mutual")
	}
	if tlsSpec.ServerCertificateSecretRef == nil {
		return fmt.Errorf("spec.transport.tls.serverCertificateSecretRef is required when mode=Mutual")
	}
	providerNamespace := policy.ProviderNamespace
	if providerNamespace == "" {
		providerNamespace = pp.Namespace
	}
	if providerNamespace != "" {
		for field, ref := range map[string]*ProviderTLSSecretRef{
			"caSecretRef":                tlsSpec.CASecretRef,
			"clientCertificateSecretRef": tlsSpec.ClientCertificateSecretRef,
			"serverCertificateSecretRef": tlsSpec.ServerCertificateSecretRef,
		} {
			if ref.Name == "" {
				return fmt.Errorf("spec.transport.tls.%s.name is required", field)
			}
			if ref.Namespace == "" {
				return fmt.Errorf("spec.transport.tls.%s.namespace is required", field)
			}
			if ref.Namespace != providerNamespace {
				return fmt.Errorf("spec.transport.tls.%s.namespace must match provider namespace %q", field, providerNamespace)
			}
		}
	}
	return nil
}

func platformProviderMutualTLS(pp *PlatformProvider) *ProviderTransportTLSSpec {
	if pp.Spec.Transport == nil || pp.Spec.Transport.TLS == nil {
		return nil
	}
	tlsSpec := pp.Spec.Transport.TLS
	if tlsSpec.Mode != "Mutual" {
		return nil
	}
	return tlsSpec
}

func imageAllowedByPrefixes(image string, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.TrimSuffix(prefix, "/")
		if image == prefix || strings.HasPrefix(image, prefix+"/") {
			return true
		}
	}
	return false
}
