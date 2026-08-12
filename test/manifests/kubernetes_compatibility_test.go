package manifests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type chartCompatibility struct {
	KubeVersion string `json:"kubeVersion"`
}

func TestOperatorStartupChecksKubernetesCompatibility(t *testing.T) {
	path := filepath.Join(repoRoot, "cmd", "operator", "main.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read operator main: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"discovery.NewDiscoveryClientForConfig(restConfig)",
		"kubecompat.CheckServer(discoveryClient)",
		"os.Exit(1)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("operator startup compatibility guard missing %q", required)
		}
	}
}

func TestHelmChartDeclaresRestartableInitContainerBoundary(t *testing.T) {
	path := filepath.Join(repoRoot, "charts", "imagebuilder", "Chart.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart chartCompatibility
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatalf("parse Chart.yaml: %v", err)
	}
	if chart.KubeVersion != ">=1.29.0-0" {
		t.Fatalf("kubeVersion = %q, want >=1.29.0-0", chart.KubeVersion)
	}
}
