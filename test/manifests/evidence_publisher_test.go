package manifests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// ADR-011: the controller evidence publisher is opt-in, digest-pinned and
// scoped to the operator namespace.
func TestHelmChartEvidencePublisherIsOptInAndScoped(t *testing.T) {
	values, err := os.ReadFile(filepath.Join(repoRoot, "charts", "imagebuilder", "values.yaml"))
	if err != nil {
		t.Fatalf("read values: %v", err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(values, &parsed); err != nil {
		t.Fatalf("parse values: %v", err)
	}
	publisher, ok := parsed["evidencePublisher"].(map[string]any)
	if !ok {
		t.Fatalf("evidencePublisher values missing: %#v", parsed["evidencePublisher"])
	}
	if publisher["enabled"] != false {
		t.Fatalf("evidencePublisher.enabled = %#v, want false by default", publisher["enabled"])
	}
	for _, key := range []string{"orasImage", "cosignImage"} {
		image, _ := publisher[key].(string)
		if !strings.Contains(image, "@sha256:") {
			t.Fatalf("evidencePublisher.%s %q must be digest-pinned", key, image)
		}
	}

	template, err := os.ReadFile(filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "evidence-publisher.yaml"))
	if err != nil {
		t.Fatalf("read evidence publisher template: %v", err)
	}
	text := string(template)
	for _, want := range []string{
		"{{- if .Values.evidencePublisher.enabled }}",
		"kind: Role\n",
		"kind: RoleBinding",
		`namespace: {{ include "imagebuilder.namespace" . }}`,
		`resources: ["secrets"]
    verbs: ["get", "list", "create", "update", "delete"]`,
		"kind: NetworkPolicy",
		"app.kubernetes.io/component: evidence-publisher",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("evidence publisher template missing %q", want)
		}
	}
	if strings.Contains(text, "kind: ClusterRole") {
		t.Fatal("evidence publisher RBAC must stay namespaced")
	}

	deployment, err := os.ReadFile(filepath.Join(repoRoot, "charts", "imagebuilder", "templates", "deployment.yaml"))
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	for _, want := range []string{
		"--evidence-publisher-enabled=true",
		`required "evidencePublisher.registryCredentialsSecret is required when evidencePublisher.enabled"`,
		`required "evidencePublisher.cosignKeySecret.name is required when evidencePublisher.enabled"`,
		"name: IMAGEBUILDER_VERSION",
	} {
		if !strings.Contains(string(deployment), want) {
			t.Fatalf("deployment missing %q", want)
		}
	}
}
