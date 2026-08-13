package signaturepolicy

import (
	"context"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testProviderImage = "ghcr.io/anwendt/provider@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestVerifierAcceptsEnforcingPolicyAndFailClosedWebhook(t *testing.T) {
	policy := validPolicy("imagebuilder-signatures")
	webhook := validWebhook("kyverno-resource-validating-webhook-cfg")
	verifier := testVerifier(t, policy, webhook)
	if err := verifier.Verify(context.Background(), testProviderImage); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifierRejectsAuditPolicy(t *testing.T) {
	policy := validPolicy("imagebuilder-signatures")
	policy.Object["spec"].(map[string]any)["validationFailureAction"] = "Audit"
	verifier := testVerifier(t, policy, validWebhook("kyverno-resource-validating-webhook-cfg"))
	if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
		t.Fatal("Audit policy should be rejected")
	}
}

func TestVerifierRejectsNonVerifyingPolicy(t *testing.T) {
	policy := validPolicy("imagebuilder-signatures")
	rule := policy.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	verification := rule["verifyImages"].([]any)[0].(map[string]any)
	verification["required"] = false
	verifier := testVerifier(t, policy, validWebhook("kyverno-resource-validating-webhook-cfg"))
	if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
		t.Fatal("required=false should be rejected")
	}
}

func TestVerifierRejectsExcludedOrPartiallyEnforcingRules(t *testing.T) {
	t.Run("exclude", func(t *testing.T) {
		policy := validPolicy("imagebuilder-signatures")
		rule := policy.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
		rule["exclude"] = map[string]any{"any": []any{map[string]any{"resources": map[string]any{"names": []any{"provider-*"}}}}}
		if err := testVerifier(t, policy, validWebhook("kyverno-resource-validating-webhook-cfg")).Verify(context.Background(), testProviderImage); err == nil {
			t.Fatal("rule exclusions should be rejected")
		}
	})
	t.Run("one fail-open verification", func(t *testing.T) {
		policy := validPolicy("imagebuilder-signatures")
		rule := policy.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
		rule["verifyImages"] = append(rule["verifyImages"].([]any), map[string]any{
			"imageReferences": []any{"example.invalid/*"},
			"required":        false,
			"verifyDigest":    true,
			"attestors":       []any{map[string]any{"entries": []any{map[string]any{"keyless": map[string]any{"issuer": "issuer", "subject": "subject"}}}}},
		})
		if err := testVerifier(t, policy, validWebhook("kyverno-resource-validating-webhook-cfg")).Verify(context.Background(), testProviderImage); err == nil {
			t.Fatal("partially fail-open verifyImages should be rejected")
		}
	})
}

func TestVerifierRejectsIgnoreWebhook(t *testing.T) {
	webhook := validWebhook("kyverno-resource-validating-webhook-cfg")
	ignore := admissionregistrationv1.Ignore
	webhook.Webhooks[0].FailurePolicy = &ignore
	verifier := testVerifier(t, validPolicy("imagebuilder-signatures"), webhook)
	if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
		t.Fatal("failurePolicy=Ignore should be rejected")
	}
}

func TestVerifierEvaluatesWebhookSelectorsForProviderPod(t *testing.T) {
	webhook := validWebhook("kyverno-resource-validating-webhook-cfg")
	webhook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"policy": "enabled"}}
	webhook.Webhooks[0].ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{providerPodLabel: "true"}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "imagebuilder-system", Labels: map[string]string{"policy": "enabled"}}}
	verifier := testVerifier(t, validPolicy("imagebuilder-signatures"), webhook, namespace)
	verifier.Config.ProviderNamespace = namespace.Name
	if err := verifier.Verify(context.Background(), testProviderImage); err != nil {
		t.Fatalf("matching selectors rejected: %v", err)
	}

	webhook.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"policy": "other"}}
	verifier = testVerifier(t, validPolicy("imagebuilder-signatures"), webhook, namespace)
	verifier.Config.ProviderNamespace = namespace.Name
	if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
		t.Fatal("non-matching namespace selector should be rejected")
	}
}

func TestVerifierRejectsMissingResourcesOrConfiguration(t *testing.T) {
	t.Run("missing policy", func(t *testing.T) {
		verifier := testVerifier(t, validWebhook("kyverno-resource-validating-webhook-cfg"))
		if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
			t.Fatal("missing policy should be rejected")
		}
	})
	t.Run("missing webhook", func(t *testing.T) {
		verifier := testVerifier(t, validPolicy("imagebuilder-signatures"))
		if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
			t.Fatal("missing webhook should be rejected")
		}
	})
	t.Run("empty names", func(t *testing.T) {
		verifier := testVerifier(t)
		verifier.Config = Config{}
		if err := verifier.Verify(context.Background(), testProviderImage); err == nil {
			t.Fatal("empty resource names should be rejected")
		}
	})
}

func TestVerifierRejectsPolicyThatDoesNotCoverProviderImage(t *testing.T) {
	policy := validPolicy("imagebuilder-signatures")
	rule := policy.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	verification := rule["verifyImages"].([]any)[0].(map[string]any)
	verification["imageReferences"] = []any{"registry.example.com/other/*"}
	if err := testVerifier(t, policy, validWebhook("kyverno-resource-validating-webhook-cfg")).Verify(context.Background(), testProviderImage); err == nil {
		t.Fatal("policy that does not cover the provider image should be rejected")
	}
}

func testVerifier(t *testing.T, objects ...runtime.Object) *Verifier {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := admissionregistrationv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add admission scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return &Verifier{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build(),
		Config: Config{PolicyName: "imagebuilder-signatures"},
	}
}

func validPolicy(name string) *unstructured.Unstructured {
	return PolicyObject(name, map[string]any{
		"apiVersion": "kyverno.io/v1",
		"kind":       "ClusterPolicy",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"validationFailureAction": "Enforce",
			"rules": []any{map[string]any{
				"name": "verify-images",
				"match": map[string]any{"any": []any{map[string]any{"resources": map[string]any{
					"kinds":      []any{"Pod"},
					"operations": []any{"CREATE"},
					"selector":   map[string]any{"matchLabels": map[string]any{managedByLabel: "imagebuilder", providerPodLabel: "true"}},
				}}}},
				"verifyImages": []any{map[string]any{
					"imageReferences": []any{"ghcr.io/anwendt/*"},
					"required":        true,
					"verifyDigest":    true,
					"attestors":       []any{map[string]any{"entries": []any{map[string]any{"keyless": map[string]any{"issuer": "issuer", "subject": "subject"}}}}},
				}},
			}},
		},
	})
}

func validWebhook(name string) *admissionregistrationv1.ValidatingWebhookConfiguration {
	fail := admissionregistrationv1.Fail
	path := "/validate"
	port := int32(443)
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name:          "validate.kyverno.svc",
			FailurePolicy: &fail,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{Service: &admissionregistrationv1.ServiceReference{
				Name: "kyverno-svc", Namespace: "kyverno", Path: &path, Port: &port,
			}},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
			}},
		}},
	}
}
