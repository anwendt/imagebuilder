package manifests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestArgoCDResourceExamplesAreValidVMImages(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot, "examples", "argocd", "*-resources", "*.yaml"))
	if err != nil {
		t.Fatalf("glob ArgoCD resource examples: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no ArgoCD resource example manifests found")
	}

	seenVMImages := 0
	for _, file := range files {
		docs := readYAMLDocuments(t, file)
		for _, doc := range docs {
			meta := manifestMetadata{}
			if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
				t.Fatalf("parse metadata in %s: %v", file, err)
			}
			if meta.Kind != "VMImage" {
				continue
			}

			var img v1alpha1.VMImage
			if err := yaml.Unmarshal([]byte(doc), &img); err != nil {
				t.Fatalf("parse VMImage in %s: %v", file, err)
			}
			if _, err := img.ValidateCreate(); err != nil {
				t.Fatalf("validate VMImage example %s/%s in %s: %v", img.Namespace, img.Name, file, err)
			}
			seenVMImages++
		}
	}
	if seenVMImages != 7 {
		t.Fatalf("validated VMImage examples = %d, want 7", seenVMImages)
	}
}

func TestArgoCDPlainYAMLExamplesParse(t *testing.T) {
	patterns := []string{
		filepath.Join(repoRoot, "examples", "argocd", "*-resources", "*.yaml"),
		filepath.Join(repoRoot, "examples", "argocd", "*-application.yaml"),
		filepath.Join(repoRoot, "examples", "argocd", "*-tomcat-chart", "Chart.yaml"),
		filepath.Join(repoRoot, "examples", "argocd", "*-tomcat-chart", "values.yaml"),
	}

	seen := 0
	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		for _, file := range files {
			for _, doc := range readYAMLDocuments(t, file) {
				var parsed map[string]any
				if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
					t.Fatalf("parse YAML example %s: %v", file, err)
				}
				seen++
			}
		}
	}
	if seen == 0 {
		t.Fatal("no ArgoCD YAML examples parsed")
	}
}

type manifestMetadata struct {
	Kind string `json:"kind"`
}

func readYAMLDocuments(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var docs []string
	for _, doc := range splitYAMLDocuments(string(data)) {
		if strings.TrimSpace(doc) != "" {
			docs = append(docs, doc)
		}
	}
	return docs
}
