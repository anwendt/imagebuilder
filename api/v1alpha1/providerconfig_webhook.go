// api/v1alpha1/providerconfig_webhook.go
//
// Validating Admission Webhook for ProviderConfig (AS-026, AS-049, REQ-008).
//
// Enforces:
//   - spec.endpoint must be HTTPS and must not resolve to a private IP (SSRF, AS-049)
//   - Issues a warning when spec.insecure=true to discourage TLS bypass (AS-036)
//
// Registration:
//   if err := (&v1alpha1.ProviderConfig{}).SetupWebhookWithManager(mgr); err != nil { ... }
//
// +kubebuilder:webhook:path=/validate-imagebuilder-io-v1alpha1-providerconfig,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,sideEffects=None,groups=imagebuilder.io,resources=providerconfigs,verbs=create;update,versions=v1alpha1,name=vproviderconfig.kb.io,admissionReviewVersions=v1,timeoutSeconds=10,serviceName=imagebuilder-webhook-service,serviceNamespace=imagebuilder-system

package v1alpha1

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the ProviderConfig validating webhook.
func (r *ProviderConfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).WithValidator(providerConfigValidator{}).Complete()
}

var _ admission.Validator[*ProviderConfig] = providerConfigValidator{}

type providerConfigValidator struct{}

func (providerConfigValidator) ValidateCreate(_ context.Context, obj *ProviderConfig) (admission.Warnings, error) {
	return obj.validateProviderConfig()
}

func (providerConfigValidator) ValidateUpdate(_ context.Context, _ *ProviderConfig, newObj *ProviderConfig) (admission.Warnings, error) {
	return newObj.validateProviderConfig()
}

func (providerConfigValidator) ValidateDelete(_ context.Context, _ *ProviderConfig) (admission.Warnings, error) {
	return nil, nil
}

func (r *ProviderConfig) ValidateCreate() (admission.Warnings, error) {
	return r.validateProviderConfig()
}

func (r *ProviderConfig) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return r.validateProviderConfig()
}

func (r *ProviderConfig) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (r *ProviderConfig) validateProviderConfig() (admission.Warnings, error) {
	var errs []error
	var warnings admission.Warnings

	// AS-049: SSRF check — endpoint must not resolve to a private / metadata IP.
	if err := validateProviderEndpoint("spec.endpoint", r.Spec.Endpoint, r.Spec.NetworkAccess); err != nil {
		errs = append(errs, err)
	}
	if strings.EqualFold(strings.TrimSpace(r.Spec.Provider), "gcp") {
		for _, key := range []string{"storageEndpoint", "computeEndpoint", "storageUploadEndpoint"} {
			if err := validateProviderEndpoint("spec.extra."+key, r.Spec.Extra[key], r.Spec.NetworkAccess); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// AS-036: warn (non-blocking) if TLS verification is disabled.
	if r.Spec.Insecure && r.Spec.Endpoint != "" {
		warnings = append(warnings,
			"spec.insecure is true: TLS certificate verification disabled for the provider endpoint. "+
				"This must not be used in production environments (AS-036).")
	}

	if len(errs) > 0 {
		return warnings, joinErrors(errs)
	}
	return warnings, nil
}
