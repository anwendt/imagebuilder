package manifests_test

import (
	"os"
	"os/exec"
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
		for _, want := range []string{
			`apiGroups: ["kyverno.io"]`,
			`resources: ["clusterpolicies"]`,
			`apiGroups: ["admissionregistration.k8s.io"]`,
			`resources: ["validatingwebhookconfigurations"]`,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing signature-policy verification RBAC %q", path, want)
			}
		}
		if !strings.Contains(text, `resources: ["validatingwebhookconfigurations"]
    verbs: ["get", "list"]`) {
			t.Fatalf("%s must grant get/list on validating webhook configurations", path)
		}
		if !strings.Contains(text, `resources: ["namespaces"]
    verbs: ["get"]`) {
			t.Fatalf("%s must grant get on Namespaces for webhook selector evaluation", path)
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

func TestCoreReleaseChartUsesFreshImageDigests(t *testing.T) {
	path := filepath.Join(repoRoot, ".github", "workflows", "core-images-release.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read core release workflow: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"OPERATOR_DIGEST: ${{ steps.operator.outputs.digest }}",
		"BUILDER_DIGEST: ${{ steps.builder.outputs.digest }}",
		"UPLOADER_DIGEST: ${{ steps.uploader.outputs.digest }}",
		"PROVISIONER_ANSIBLE_DIGEST: ${{ steps.provisioner_ansible.outputs.digest }}",
		"PROVISIONER_CHEF_DIGEST: ${{ steps.provisioner_chef.outputs.digest }}",
		"PROVISIONER_CUSTOM_DIGEST: ${{ steps.provisioner_custom.outputs.digest }}",
		"PROVISIONER_PUPPET_DIGEST: ${{ steps.provisioner_puppet.outputs.digest }}",
		"PROVISIONER_SALTSTACK_DIGEST: ${{ steps.provisioner_saltstack.outputs.digest }}",
		"python3 hack/update-release-chart-digests.py",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("core release workflow does not pin packaged chart digest %q", want)
		}
	}

	sourceValues := filepath.Join(repoRoot, "charts", "imagebuilder", "values.yaml")
	targetValues := filepath.Join(t.TempDir(), "values.yaml")
	values, err := os.ReadFile(sourceValues)
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	if err := os.WriteFile(targetValues, values, 0o600); err != nil {
		t.Fatalf("write temporary Helm values: %v", err)
	}

	digest := "sha256:" + strings.Repeat("a", 64)
	command := exec.Command(
		"python3",
		filepath.Join(repoRoot, "hack", "update-release-chart-digests.py"),
		"--values",
		targetValues,
	)
	command.Env = append(os.Environ(),
		"OPERATOR_DIGEST="+digest,
		"BUILDER_DIGEST="+digest,
		"UPLOADER_DIGEST="+digest,
		"PROVISIONER_ANSIBLE_DIGEST="+digest,
		"PROVISIONER_CHEF_DIGEST="+digest,
		"PROVISIONER_CUSTOM_DIGEST="+digest,
		"PROVISIONER_PUPPET_DIGEST="+digest,
		"PROVISIONER_SALTSTACK_DIGEST="+digest,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("update release chart digests: %v\n%s", err, output)
	}
	updatedValues, err := os.ReadFile(targetValues)
	if err != nil {
		t.Fatalf("read updated Helm values: %v", err)
	}
	if count := strings.Count(string(updatedValues), `digest: "`+digest+`"`); count != 8 {
		t.Fatalf("updated Helm values contain %d release digests, want 8", count)
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
