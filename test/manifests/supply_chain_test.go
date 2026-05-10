package manifests_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDockerfileBaseImagesAreDigestPinned(t *testing.T) {
	for _, name := range []string{
		"Dockerfile",
		"Dockerfile.builder",
		"Dockerfile.uploader",
		"Dockerfile.provider-aws",
		"Dockerfile.provider-azure",
		"Dockerfile.provider-openstack",
		"Dockerfile.provider-vsphere",
	} {
		path := filepath.Join(repoRoot, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "FROM ") {
				continue
			}
			if !strings.Contains(line, "@sha256:") {
				t.Fatalf("%s has unpinned base image line: %s", name, line)
			}
		}
	}
}

func TestGitHubActionsUseCommitSHAs(t *testing.T) {
	workflowDir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	actionRef := regexp.MustCompile(`uses:\s+[-A-Za-z0-9_.]+/[-A-Za-z0-9_.]+(?:/[-A-Za-z0-9_.]+)?@([^\s]+)`)
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		path := filepath.Join(workflowDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range actionRef.FindAllStringSubmatch(string(data), -1) {
			if !sha.MatchString(match[1]) {
				t.Fatalf("%s uses mutable action ref %q", entry.Name(), match[0])
			}
		}
	}
}

func TestCIVerifiesGoModuleIntegrity(t *testing.T) {
	path := filepath.Join(repoRoot, ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ci workflow: %v", err)
	}
	if !strings.Contains(string(data), "make verify-deps") {
		t.Fatalf("ci.yml must run make verify-deps")
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if !strings.Contains(string(makefile), "verify-deps:") ||
		!strings.Contains(string(makefile), "$(GO) mod verify") {
		t.Fatalf("Makefile must define verify-deps using go mod verify")
	}
}
