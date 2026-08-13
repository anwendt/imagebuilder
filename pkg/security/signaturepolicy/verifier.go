package signaturepolicy

import (
	"context"
	"fmt"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	managedByLabel   = "app.kubernetes.io/managed-by"
	providerPodLabel = "imagebuilder.io/provider-pod"
)

// Config identifies the fail-closed Kyverno resources that cryptographically
// verify Image Builder pod images before Kubernetes admits them.
type Config struct {
	PolicyName        string
	ProviderNamespace string
}

// Verifier proves that the configured signature policy is both enforcing and
// guarded by a fail-closed admission webhook. It intentionally validates the
// effective cluster resources instead of trusting an opt-in field or pod
// annotation.
type Verifier struct {
	Client client.Client
	Config Config
}

func (v *Verifier) Verify(ctx context.Context, image string) error {
	if v == nil || v.Client == nil {
		return fmt.Errorf("signature verifier is not configured")
	}
	if strings.TrimSpace(v.Config.PolicyName) == "" {
		return fmt.Errorf("signature policy name is required")
	}
	if strings.TrimSpace(image) == "" || !strings.Contains(image, "@sha256:") {
		return fmt.Errorf("provider image must be digest-pinned before signature verification")
	}

	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(schema.GroupVersionKind{Group: "kyverno.io", Version: "v1", Kind: "ClusterPolicy"})
	if err := v.Client.Get(ctx, types.NamespacedName{Name: v.Config.PolicyName}, policy); err != nil {
		return fmt.Errorf("get enforcing signature ClusterPolicy %q: %w", v.Config.PolicyName, err)
	}
	if err := validatePolicy(policy, image); err != nil {
		return fmt.Errorf("signature ClusterPolicy %q is not fail-closed: %w", v.Config.PolicyName, err)
	}

	webhooks := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	if err := v.Client.List(ctx, webhooks); err != nil {
		return fmt.Errorf("list signature validation webhooks: %w", err)
	}
	namespaceLabels, err := v.providerNamespaceLabels(ctx, webhooks)
	if err != nil {
		return err
	}
	podLabels := labels.Set{managedByLabel: "imagebuilder", providerPodLabel: "true"}
	for index := range webhooks.Items {
		webhook := &webhooks.Items[index]
		if isKyvernoWebhook(webhook) && validateWebhook(webhook, namespaceLabels, podLabels) == nil {
			return nil
		}
	}
	return fmt.Errorf("no active Kyverno ValidatingWebhookConfiguration intercepts Pod creation with failurePolicy=Fail")
}

func (v *Verifier) providerNamespaceLabels(ctx context.Context, configurations *admissionregistrationv1.ValidatingWebhookConfigurationList) (labels.Set, error) {
	needsNamespace := false
	for _, configuration := range configurations.Items {
		for _, webhook := range configuration.Webhooks {
			if webhook.NamespaceSelector != nil {
				needsNamespace = true
				break
			}
		}
	}
	if !needsNamespace {
		return labels.Set{}, nil
	}
	if strings.TrimSpace(v.Config.ProviderNamespace) == "" {
		return nil, fmt.Errorf("provider namespace is required to evaluate signature webhook namespace selectors")
	}
	namespace := &corev1.Namespace{}
	if err := v.Client.Get(ctx, types.NamespacedName{Name: v.Config.ProviderNamespace}, namespace); err != nil {
		return nil, fmt.Errorf("get provider namespace %q for signature webhook evaluation: %w", v.Config.ProviderNamespace, err)
	}
	return labels.Set(namespace.Labels), nil
}

func validatePolicy(policy *unstructured.Unstructured, image string) error {
	action, found, err := unstructured.NestedString(policy.Object, "spec", "validationFailureAction")
	if err != nil || !found || !strings.EqualFold(action, "Enforce") {
		return fmt.Errorf("spec.validationFailureAction must be Enforce")
	}
	rules, found, err := unstructured.NestedSlice(policy.Object, "spec", "rules")
	if err != nil || !found {
		return fmt.Errorf("spec.rules is required")
	}
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok || !ruleMatchesManagedPods(rule) || ruleHasExclusions(rule) {
			continue
		}
		verifyImages, found, err := unstructured.NestedSlice(rule, "verifyImages")
		if err != nil || !found {
			continue
		}
		valid := len(verifyImages) > 0
		for _, rawVerification := range verifyImages {
			verification, ok := rawVerification.(map[string]any)
			if !ok || !imageVerificationIsFailClosed(verification, image) {
				valid = false
				break
			}
		}
		if valid {
			return nil
		}
	}
	return fmt.Errorf("no rule verifies imagebuilder-managed Pod images with required=true, verifyDigest=true, image references, and attestors")
}

func ruleMatchesManagedPods(rule map[string]any) bool {
	anyMatches, found, _ := unstructured.NestedSlice(rule, "match", "any")
	if !found {
		return false
	}
	for _, rawMatch := range anyMatches {
		match, ok := rawMatch.(map[string]any)
		if !ok {
			continue
		}
		kinds, found, _ := unstructured.NestedStringSlice(match, "resources", "kinds")
		if !found || !containsResource(kinds, "Pod") {
			continue
		}
		labels, found, _ := unstructured.NestedStringMap(match, "resources", "selector", "matchLabels")
		operations, operationsFound, _ := unstructured.NestedStringSlice(match, "resources", "operations")
		if operationsFound && !containsResource(operations, "CREATE") && !containsResource(operations, "*") {
			continue
		}
		if found && labels[managedByLabel] == "imagebuilder" && labels[providerPodLabel] == "true" {
			return true
		}
	}
	return false
}

func ruleHasExclusions(rule map[string]any) bool {
	_, found, _ := unstructured.NestedFieldNoCopy(rule, "exclude")
	return found
}

func imageVerificationIsFailClosed(verification map[string]any, image string) bool {
	required, requiredFound, _ := unstructured.NestedBool(verification, "required")
	verifyDigest, digestFound, _ := unstructured.NestedBool(verification, "verifyDigest")
	imageReferences, referencesFound, _ := unstructured.NestedStringSlice(verification, "imageReferences")
	attestors, attestorsFound, _ := unstructured.NestedSlice(verification, "attestors")
	return requiredFound && required && digestFound && verifyDigest && referencesFound && imageReferenceMatches(image, imageReferences) && attestorsFound && len(attestors) > 0
}

func imageReferenceMatches(image string, values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, pattern := range values {
		pattern = strings.TrimSpace(pattern)
		if pattern == image {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(image, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

func validateWebhook(configuration *admissionregistrationv1.ValidatingWebhookConfiguration, namespaceLabels, podLabels labels.Set) error {
	for _, webhook := range configuration.Webhooks {
		if webhook.FailurePolicy == nil || *webhook.FailurePolicy != admissionregistrationv1.Fail {
			continue
		}
		if webhook.ClientConfig.Service == nil && webhook.ClientConfig.URL == nil {
			continue
		}
		if !selectorMatches(webhook.NamespaceSelector, namespaceLabels) || !selectorMatches(webhook.ObjectSelector, podLabels) {
			continue
		}
		for _, rule := range webhook.Rules {
			if ruleMatchesPods(rule) {
				return nil
			}
		}
	}
	return fmt.Errorf("no failurePolicy=Fail webhook rule intercepts Pod admission")
}

func selectorMatches(selector *metav1.LabelSelector, values labels.Set) bool {
	if selector == nil {
		return true
	}
	compiled, err := metav1.LabelSelectorAsSelector(selector)
	return err == nil && compiled.Matches(values)
}

func isKyvernoWebhook(configuration *admissionregistrationv1.ValidatingWebhookConfiguration) bool {
	if strings.Contains(strings.ToLower(configuration.Name), "kyverno") {
		return true
	}
	for _, webhook := range configuration.Webhooks {
		if strings.Contains(strings.ToLower(webhook.Name), "kyverno") {
			return true
		}
		if service := webhook.ClientConfig.Service; service != nil &&
			(strings.Contains(strings.ToLower(service.Name), "kyverno") || strings.Contains(strings.ToLower(service.Namespace), "kyverno")) {
			return true
		}
	}
	return false
}

func ruleMatchesPods(rule admissionregistrationv1.RuleWithOperations) bool {
	if !containsOperation(rule.Operations, admissionregistrationv1.Create) && !containsOperation(rule.Operations, admissionregistrationv1.OperationAll) {
		return false
	}
	return containsResource(rule.Resources, "pods") || containsResource(rule.Resources, "pods/*") || containsResource(rule.Resources, "*") || containsResource(rule.Resources, "*/*")
}

func containsOperation(values []admissionregistrationv1.OperationType, want admissionregistrationv1.OperationType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsResource(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// PolicyObject returns the GVK used by tests and callers that need to seed an
// unstructured Kyverno policy without importing Kyverno Go types.
func PolicyObject(name string, object map[string]any) *unstructured.Unstructured {
	policy := &unstructured.Unstructured{Object: object}
	policy.SetGroupVersionKind(schema.GroupVersionKind{Group: "kyverno.io", Version: "v1", Kind: "ClusterPolicy"})
	policy.SetName(name)
	policy.SetCreationTimestamp(metav1.Time{})
	return policy
}
