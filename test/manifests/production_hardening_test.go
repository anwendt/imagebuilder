package manifests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorRBACIsScopedForProduction(t *testing.T) {
	for _, path := range []string{
		filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "rbac.yaml"),
		filepath.Join(repoRoot, "config", "deploy", "operator.yaml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		if strings.Contains(text, `resources: ["vmimages", "platformproviders", "providerconfigs"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]`) {
			t.Fatalf("%s grants write/delete verbs on CRD specs", path)
		}
		if !strings.Contains(text, `resources: ["secrets"]
    verbs: ["get"]`) {
			t.Fatalf("%s must grant only get on Secrets", path)
		}
	}
}

func TestStaticDeployManifestIsClearlyDevelopmentOnly(t *testing.T) {
	path := filepath.Join(repoRoot, "config", "deploy", "operator.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read operator manifest: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"imagebuilder.io/profile: development",
		`imagebuilder.io/production-ready: "false"`,
		"ghcr.io/anwendt/imagebuilder-operator:dev",
		"--require-provider-mtls=false",
		"--require-provider-digest=false",
		"--require-provider-signature=false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("operator manifest missing development marker %q", want)
		}
	}
}

func TestPrometheusRulesCoverProviderHealthAndStuckBuilds(t *testing.T) {
	for _, path := range []string{
		filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "prometheusrule.yaml"),
		filepath.Join(repoRoot, "config", "deploy", "prometheusrule.yaml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		for _, want := range []string{
			"ImageBuilderProviderUnhealthy",
			"imagebuilder_provider_healthy == 0",
			"ImageBuilderStuckActiveBuilds",
			"imagebuilder_active_builds",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
	}
}

func TestHelmChartRendersNamespaceResourceGuardrails(t *testing.T) {
	valuesPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.yaml")
	values, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	valuesText := string(values)
	for _, want := range []string{
		"namespaceResourceGuardrails:",
		"enabled: true",
		"resourceQuota:",
		"limitRange:",
		"requests.storage: 500Gi",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("values.yaml missing namespace guardrail setting %q", want)
		}
	}

	templatePath := filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "resource-guardrails.yaml")
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read resource guardrails template: %v", err)
	}
	templateText := string(template)
	for _, want := range []string{
		"kind: ResourceQuota",
		"kind: LimitRange",
		".Values.namespaceResourceGuardrails.resourceQuota.hard",
		".Values.namespaceResourceGuardrails.limitRange.defaultRequest",
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("resource guardrails template missing %q", want)
		}
	}

	schemaPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.schema.json")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read Helm values schema: %v", err)
	}
	if !strings.Contains(string(schema), `"namespaceResourceGuardrails"`) ||
		!strings.Contains(string(schema), `"Production installs require ResourceQuota and LimitRange guardrails`) {
		t.Fatalf("values.schema.json must enforce namespace resource guardrails")
	}
}

func TestMetricsCoverUploadThroughput(t *testing.T) {
	path := filepath.Join(repoRoot, "pkg", "observability", "metrics.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics.go: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"imagebuilder_upload_bytes_total",
		"imagebuilder_upload_throughput_bytes_per_second",
		"UploadBytesTotal",
		"UploadThroughputBytesPerSecond",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics.go missing upload throughput metric %q", want)
		}
	}
}
