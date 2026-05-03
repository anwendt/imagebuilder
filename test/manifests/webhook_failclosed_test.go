package manifests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

const repoRoot = "../.."

func TestWebhookManifestIsFailClosedAndTargetsProductionService(t *testing.T) {
	path := filepath.Join(repoRoot, "config", "webhook", "manifests.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read webhook manifest: %v", err)
	}
	docs := splitYAMLDocuments(string(data))
	if len(docs) != 1 {
		t.Fatalf("webhook manifest documents = %d, want 1", len(docs))
	}

	var cfg admissionv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal([]byte(docs[0]), &cfg); err != nil {
		t.Fatalf("parse webhook manifest: %v", err)
	}
	if cfg.Name != "imagebuilder-validating-webhook-configuration" {
		t.Fatalf("webhook configuration name = %q", cfg.Name)
	}
	if got := cfg.Annotations["cert-manager.io/inject-ca-from"]; got != "imagebuilder-system/imagebuilder-webhook-serving-cert" {
		t.Fatalf("inject-ca-from = %q, want imagebuilder-system/imagebuilder-webhook-serving-cert", got)
	}
	if len(cfg.Webhooks) != 3 {
		t.Fatalf("webhooks = %d, want 3", len(cfg.Webhooks))
	}
	for _, webhook := range cfg.Webhooks {
		if webhook.FailurePolicy == nil || *webhook.FailurePolicy != admissionv1.Fail {
			t.Fatalf("%s failurePolicy = %#v, want Fail", webhook.Name, webhook.FailurePolicy)
		}
		if webhook.ClientConfig.Service == nil {
			t.Fatalf("%s missing service client config", webhook.Name)
		}
		if webhook.ClientConfig.Service.Name != "imagebuilder-webhook-service" {
			t.Fatalf("%s service name = %q", webhook.Name, webhook.ClientConfig.Service.Name)
		}
		if webhook.ClientConfig.Service.Namespace != "imagebuilder-system" {
			t.Fatalf("%s service namespace = %q", webhook.Name, webhook.ClientConfig.Service.Namespace)
		}
		if webhook.TimeoutSeconds == nil || *webhook.TimeoutSeconds != 10 {
			t.Fatalf("%s timeoutSeconds = %#v, want 10", webhook.Name, webhook.TimeoutSeconds)
		}
		if webhook.MatchPolicy == nil || *webhook.MatchPolicy != admissionv1.Equivalent {
			t.Fatalf("%s matchPolicy = %#v, want Equivalent", webhook.Name, webhook.MatchPolicy)
		}
	}
}

func TestHelmChartDefaultsAdmissionFailClosed(t *testing.T) {
	valuesPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.yaml")
	values, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(values, &parsed); err != nil {
		t.Fatalf("parse Helm values: %v", err)
	}
	webhook, ok := parsed["webhook"].(map[string]any)
	if !ok {
		t.Fatalf("webhook values missing or malformed: %#v", parsed["webhook"])
	}
	if webhook["enabled"] != true {
		t.Fatalf("webhook.enabled = %#v, want true", webhook["enabled"])
	}
	if webhook["failurePolicy"] != "Fail" {
		t.Fatalf("webhook.failurePolicy = %#v, want Fail", webhook["failurePolicy"])
	}
	if webhook["serviceName"] != "imagebuilder-webhook-service" {
		t.Fatalf("webhook.serviceName = %#v, want imagebuilder-webhook-service", webhook["serviceName"])
	}

	schemaPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.schema.json")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read Helm values schema: %v", err)
	}
	schemaText := string(schema)
	for _, want := range []string{`"enum": [true]`, `"enum": ["Fail"]`} {
		if !strings.Contains(schemaText, want) {
			t.Fatalf("values.schema.json missing %s", want)
		}
	}

	webhookTemplatePath := filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "webhook.yaml")
	webhookTemplate, err := os.ReadFile(webhookTemplatePath)
	if err != nil {
		t.Fatalf("read Helm webhook template: %v", err)
	}
	templateText := string(webhookTemplate)
	for _, want := range []string{
		"failurePolicy: {{ .Values.webhook.failurePolicy }}",
		"cert-manager.io/inject-ca-from:",
		"path: /validate-imagebuilder-io-v1alpha1-platformprovider",
		"path: /validate-imagebuilder-io-v1alpha1-vmimage",
		"path: /validate-imagebuilder-io-v1alpha1-providerconfig",
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("webhook template missing %q", want)
		}
	}
}

func TestHelmChartIncludesCRDs(t *testing.T) {
	crdDir := filepath.Join(repoRoot, "charts", "imagebuilder", "crds")
	for _, name := range []string{
		"imagebuilder.io_vmimages.yaml",
		"imagebuilder.io_platformproviders.yaml",
		"imagebuilder.io_providerconfigs.yaml",
	} {
		if _, err := os.Stat(filepath.Join(crdDir, name)); err != nil {
			t.Fatalf("expected Helm CRD %s: %v", name, err)
		}
	}
}

func splitYAMLDocuments(data string) []string {
	parts := strings.Split(data, "\n---")
	docs := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		docs = append(docs, part)
	}
	return docs
}
