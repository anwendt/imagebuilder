package manifests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

func TestNetworkPoliciesRestrictProviderGRPCToOperator(t *testing.T) {
	path := filepath.Join(repoRoot, "config", "policy", "networkpolicies.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read network policies: %v", err)
	}
	policies := parseNetworkPolicies(t, string(data))

	defaultDeny := policies["imagebuilder-default-deny"]
	if defaultDeny == nil {
		t.Fatal("imagebuilder-default-deny NetworkPolicy missing")
	}
	if len(defaultDeny.Spec.PodSelector.MatchLabels) != 0 ||
		len(defaultDeny.Spec.PolicyTypes) != 2 {
		t.Fatalf("default deny policy = %#v", defaultDeny.Spec)
	}

	operator := policies["imagebuilder-operator"]
	if operator == nil {
		t.Fatal("imagebuilder-operator NetworkPolicy missing")
	}
	if !egressAllowsProviderGRPC(operator) {
		t.Fatalf("operator policy does not allow egress to provider gRPC on 50051: %#v", operator.Spec.Egress)
	}

	provider := policies["imagebuilder-provider"]
	if provider == nil {
		t.Fatal("imagebuilder-provider NetworkPolicy missing")
	}
	if !selectsProviderPods(provider) {
		t.Fatalf("provider policy does not select provider pods: %#v", provider.Spec.PodSelector)
	}
	if !ingressAllowsOperatorAndUploadGRPC(provider) {
		t.Fatalf("provider policy must allow operator and managed upload Job ingress on TCP/50051: %#v", provider.Spec.Ingress)
	}

	jobs := policies["imagebuilder-build-upload-jobs"]
	if jobs == nil || !egressAllowsProviderGRPC(jobs) {
		t.Fatalf("build/upload policy must allow egress to provider gRPC on 50051: %#v", jobs)
	}
}

func TestHelmChartDefaultsNetworkPolicyOnAndRendersProviderBoundary(t *testing.T) {
	valuesPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.yaml")
	values, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(values, &parsed); err != nil {
		t.Fatalf("parse Helm values: %v", err)
	}
	networkPolicy, ok := parsed["networkPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("networkPolicy values missing or malformed: %#v", parsed["networkPolicy"])
	}
	if networkPolicy["enabled"] != true {
		t.Fatalf("networkPolicy.enabled = %#v, want true", networkPolicy["enabled"])
	}
	if _, ok := networkPolicy["workloadNamespaces"].([]any); !ok {
		t.Fatalf("networkPolicy.workloadNamespaces missing or malformed: %#v", networkPolicy["workloadNamespaces"])
	}
	providerSecurity, ok := parsed["providerSecurity"].(map[string]any)
	if !ok {
		t.Fatalf("providerSecurity values missing or malformed: %#v", parsed["providerSecurity"])
	}
	if providerSecurity["requireMTLS"] != true {
		t.Fatalf("providerSecurity.requireMTLS = %#v, want true", providerSecurity["requireMTLS"])
	}
	if providerSecurity["requireDigest"] != true {
		t.Fatalf("providerSecurity.requireDigest = %#v, want true", providerSecurity["requireDigest"])
	}
	if providerSecurity["requireSignature"] != true {
		t.Fatalf("providerSecurity.requireSignature = %#v, want true", providerSecurity["requireSignature"])
	}
	if _, ok := providerSecurity["allowedRegistries"].([]any); !ok {
		t.Fatalf("providerSecurity.allowedRegistries missing or malformed: %#v", providerSecurity["allowedRegistries"])
	}
	provisionerImages, ok := parsed["provisionerImages"].(map[string]any)
	if !ok {
		t.Fatalf("provisionerImages values missing or malformed: %#v", parsed["provisionerImages"])
	}
	for _, name := range []string{"ansible", "chef", "puppet", "saltstack"} {
		if _, ok := provisionerImages[name].(map[string]any); !ok {
			t.Fatalf("provisionerImages.%s missing or malformed: %#v", name, provisionerImages[name])
		}
	}
	imageSignaturePolicy, ok := parsed["imageSignaturePolicy"].(map[string]any)
	if !ok {
		t.Fatalf("imageSignaturePolicy values missing or malformed: %#v", parsed["imageSignaturePolicy"])
	}
	if imageSignaturePolicy["enabled"] != true {
		t.Fatalf("imageSignaturePolicy.enabled = %#v, want true by default", imageSignaturePolicy["enabled"])
	}

	schemaPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.schema.json")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read Helm values schema: %v", err)
	}
	if !strings.Contains(string(schema), `"networkPolicy"`) ||
		!strings.Contains(string(schema), `Production installs should keep this true; local development profiles may disable it.`) {
		t.Fatalf("values.schema.json must document production networkPolicy default and development override")
	}
	if !strings.Contains(string(schema), `"workloadNamespaces"`) || !strings.Contains(string(schema), `"uniqueItems": true`) {
		t.Fatalf("values.schema.json must define unique networkPolicy.workloadNamespaces")
	}
	if !strings.Contains(string(schema), `"providerSecurity"`) ||
		!strings.Contains(string(schema), `"requireMTLS"`) ||
		!strings.Contains(string(schema), `"requireDigest"`) ||
		!strings.Contains(string(schema), `"requireSignature"`) ||
		!strings.Contains(string(schema), `Production installs should keep this true.`) {
		t.Fatalf("values.schema.json must document production providerSecurity defaults")
	}
	if !strings.Contains(string(schema), `"provisionerImages"`) ||
		!strings.Contains(string(schema), `"ansible"`) ||
		!strings.Contains(string(schema), `"saltstack"`) {
		t.Fatalf("values.schema.json must define provisioner image references")
	}
	if !strings.Contains(string(schema), `"imageSignaturePolicy"`) ||
		!strings.Contains(string(schema), `"keyless"`) ||
		!strings.Contains(string(schema), `Must remain enabled whenever providerSecurity.requireSignature is true.`) {
		t.Fatalf("values.schema.json must enforce fail-closed image signature policy rendering")
	}

	devValuesPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values-development.yaml")
	devValues, err := os.ReadFile(devValuesPath)
	if err != nil {
		t.Fatalf("read development Helm values: %v", err)
	}
	var devParsed map[string]any
	if err := yaml.Unmarshal(devValues, &devParsed); err != nil {
		t.Fatalf("parse development Helm values: %v", err)
	}
	devNetworkPolicy, ok := devParsed["networkPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("development networkPolicy values missing or malformed: %#v", devParsed["networkPolicy"])
	}
	if devNetworkPolicy["enabled"] != false {
		t.Fatalf("development networkPolicy.enabled = %#v, want false", devNetworkPolicy["enabled"])
	}
	devProviderSecurity, ok := devParsed["providerSecurity"].(map[string]any)
	if !ok {
		t.Fatalf("development providerSecurity values missing or malformed: %#v", devParsed["providerSecurity"])
	}
	for _, key := range []string{"requireMTLS", "requireDigest", "requireSignature"} {
		if devProviderSecurity[key] != false {
			t.Fatalf("development providerSecurity.%s = %#v, want false", key, devProviderSecurity[key])
		}
	}
	devImageSignaturePolicy, ok := devParsed["imageSignaturePolicy"].(map[string]any)
	if !ok || devImageSignaturePolicy["enabled"] != false {
		t.Fatalf("development imageSignaturePolicy = %#v, want disabled", devParsed["imageSignaturePolicy"])
	}

	templatePath := filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "networkpolicies.yaml")
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read Helm network policy template: %v", err)
	}
	templateText := string(template)
	for _, want := range []string{
		"{{- if .Values.networkPolicy.enabled }}",
		"operator: Exists",
		"imagebuilder.io/provider-name",
		"port: 50051",
		"imagebuilder.fullname",
		"build-upload-jobs",
		".Values.networkPolicy.workloadNamespaces",
		`if ne . $operatorNamespace`,
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("network policy template missing %q", want)
		}
	}
}

func TestHelmChartRendersImageSignaturePolicy(t *testing.T) {
	templatePath := filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "image-signature-policy.yaml")
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read Helm image signature policy template: %v", err)
	}
	templateText := string(template)
	for _, want := range []string{
		"kind: ClusterPolicy",
		"verifyImages:",
		"required: true",
		"verifyDigest: true",
		"keyless:",
		".Values.imageSignaturePolicy.imageReferences",
		"validationFailureAction: Enforce",
		`imagebuilder.io/provider-pod: "true"`,
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("image signature policy template missing %q", want)
		}
	}
}

func TestHelmChartImagesAreDigestConfigurable(t *testing.T) {
	valuesPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.yaml")
	values, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("read Helm values: %v", err)
	}
	valuesText := string(values)
	for _, want := range []string{
		"builderImage:",
		"uploaderImage:",
		"  digest: \"\"",
	} {
		if !strings.Contains(valuesText, want) {
			t.Fatalf("values.yaml missing %q", want)
		}
	}

	helpersPath := filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "_helpers.tpl")
	helpers, err := os.ReadFile(helpersPath)
	if err != nil {
		t.Fatalf("read Helm helpers: %v", err)
	}
	helpersText := string(helpers)
	if !strings.Contains(helpersText, `printf "%s@%s" .repository .digest`) {
		t.Fatal("image helper must render digest-pinned image references")
	}

	deploymentPath := filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "deployment.yaml")
	deployment, err := os.ReadFile(deploymentPath)
	if err != nil {
		t.Fatalf("read Helm deployment: %v", err)
	}
	deploymentText := string(deployment)
	for _, want := range []string{
		`image: "{{ include "imagebuilder.imageRef" .Values.image }}"`,
		"name: BUILDER_IMAGE",
		"name: UPLOADER_IMAGE",
		"--require-provider-mtls={{ .Values.providerSecurity.requireMTLS }}",
		"--require-provider-digest={{ .Values.providerSecurity.requireDigest }}",
		"--require-provider-signature={{ .Values.providerSecurity.requireSignature }}",
		"--provider-signature-policy={{ include \"imagebuilder.fullname\" . }}-require-signed-digests",
		"--allowed-provider-registries={{ join \",\" .Values.providerSecurity.allowedRegistries }}",
		"name: PROVISIONER_ANSIBLE_IMAGE",
		"name: PROVISIONER_CHEF_IMAGE",
		"name: PROVISIONER_CUSTOM_IMAGE",
		"name: PROVISIONER_PUPPET_IMAGE",
		"name: PROVISIONER_SALTSTACK_IMAGE",
		`{{ include "imagebuilder.imageRef" .Values.builderImage | quote }}`,
		`{{ include "imagebuilder.imageRef" .Values.uploaderImage | quote }}`,
		`{{ include "imagebuilder.imageRef" .Values.provisionerImages.ansible | quote }}`,
	} {
		if !strings.Contains(deploymentText, want) {
			t.Fatalf("deployment template missing %q", want)
		}
	}
}

func parseNetworkPolicies(t *testing.T, raw string) map[string]*networkingv1.NetworkPolicy {
	t.Helper()
	out := map[string]*networkingv1.NetworkPolicy{}
	for _, doc := range splitYAMLDocuments(raw) {
		var policy networkingv1.NetworkPolicy
		if err := yaml.Unmarshal([]byte(doc), &policy); err != nil {
			t.Fatalf("parse NetworkPolicy: %v\n%s", err, doc)
		}
		if policy.Kind != "NetworkPolicy" {
			continue
		}
		out[policy.Name] = &policy
	}
	return out
}

func selectsProviderPods(policy *networkingv1.NetworkPolicy) bool {
	if policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/managed-by"] != "imagebuilder" {
		return false
	}
	for _, expr := range policy.Spec.PodSelector.MatchExpressions {
		if expr.Key == "imagebuilder.io/provider-name" && expr.Operator == "Exists" {
			return true
		}
	}
	return false
}

func ingressAllowsOperatorAndUploadGRPC(policy *networkingv1.NetworkPolicy) bool {
	if len(policy.Spec.Ingress) != 2 {
		return false
	}
	operatorAllowed := false
	uploadAllowed := false
	for _, ingress := range policy.Spec.Ingress {
		if len(ingress.From) != 1 || ingress.From[0].PodSelector == nil || !hasSingleTCPPort(ingress.Ports, 50051) {
			continue
		}
		labels := ingress.From[0].PodSelector.MatchLabels
		if labels["app.kubernetes.io/name"] == "imagebuilder-operator" {
			operatorAllowed = true
		}
		if labels["app.kubernetes.io/managed-by"] == "imagebuilder" && labels["imagebuilder.io/job-kind"] == "upload" {
			uploadAllowed = true
		}
	}
	return operatorAllowed && uploadAllowed
}

func egressAllowsProviderGRPC(policy *networkingv1.NetworkPolicy) bool {
	for _, egress := range policy.Spec.Egress {
		if !hasSingleTCPPort(egress.Ports, 50051) {
			continue
		}
		for _, peer := range egress.To {
			if peer.PodSelector == nil {
				continue
			}
			for _, expr := range peer.PodSelector.MatchExpressions {
				if expr.Key == "imagebuilder.io/provider-name" && expr.Operator == "Exists" {
					return true
				}
			}
		}
	}
	return false
}

func hasSingleTCPPort(ports []networkingv1.NetworkPolicyPort, port int) bool {
	if len(ports) != 1 || ports[0].Port == nil {
		return false
	}
	if ports[0].Protocol == nil || *ports[0].Protocol != "TCP" {
		return false
	}
	return ports[0].Port.IntVal == int32(port)
}
