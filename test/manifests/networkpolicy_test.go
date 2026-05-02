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
	if !ingressOnlyAllowsOperatorGRPC(provider) {
		t.Fatalf("provider policy must allow only operator ingress on TCP/50051: %#v", provider.Spec.Ingress)
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

	schemaPath := filepath.Join(repoRoot, "charts", "imagebuilder", "values.schema.json")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read Helm values schema: %v", err)
	}
	if !strings.Contains(string(schema), `"networkPolicy"`) || !strings.Contains(string(schema), `"enum": [true]`) {
		t.Fatalf("values.schema.json must enforce networkPolicy.enabled=true")
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
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("network policy template missing %q", want)
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

func ingressOnlyAllowsOperatorGRPC(policy *networkingv1.NetworkPolicy) bool {
	if len(policy.Spec.Ingress) != 1 {
		return false
	}
	ingress := policy.Spec.Ingress[0]
	if len(ingress.From) != 1 || ingress.From[0].PodSelector == nil {
		return false
	}
	if ingress.From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "imagebuilder-operator" {
		return false
	}
	return hasSingleTCPPort(ingress.Ports, 50051)
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
