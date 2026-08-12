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
		for _, want := range []string{
			`resources: ["vmimages", "platformproviders"]
    verbs: ["get", "list", "watch", "update"]`,
			`resources: ["providerconfigs"]
    verbs: ["get", "list", "watch"]`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing least-privilege controller rule %q", path, want)
			}
		}
		if !strings.Contains(text, `resources: ["secrets"]
    verbs: ["get", "create", "update"]`) {
			t.Fatalf("%s must grant get/create/update on Secrets for VMImage-owned upload mTLS bundles", path)
		}
	}
}

func TestOperatorDelegatesNodePlacementToKubeScheduler(t *testing.T) {
	for _, path := range []string{
		filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "rbac.yaml"),
		filepath.Join(repoRoot, "config", "deploy", "operator.yaml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), `resources: ["nodes"]`) {
			t.Fatalf("%s must not grant Node access for custom placement", path)
		}
	}

	for _, path := range []string{
		filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "deployment.yaml"),
		filepath.Join(repoRoot, "config", "deploy", "operator.yaml"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), "--max-concurrent-builds-per-node") {
			t.Fatalf("%s still configures the removed custom per-node scheduler", path)
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
		!strings.Contains(string(schema), `Production installs should keep this true; local development profiles may disable it.`) {
		t.Fatalf("values.schema.json must document production namespace guardrail default and development override")
	}

	devValuesPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values-development.yaml")
	devValues, err := os.ReadFile(devValuesPath)
	if err != nil {
		t.Fatalf("read development Helm values: %v", err)
	}
	if !strings.Contains(string(devValues), "namespaceResourceGuardrails:") ||
		!strings.Contains(string(devValues), "enabled: false") {
		t.Fatalf("development values must disable namespace resource guardrails")
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
