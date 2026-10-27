package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeRepoPathConfinesSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []bool{false, true} {
		repo := t.TempDir()
		target, selected := filepath.Join(outside, "secret"), "payload"
		if directory {
			target, selected = outside, "payload/secret"
		}
		if err := os.Symlink(target, filepath.Join(repo, "payload")); err != nil {
			t.Fatal(err)
		}
		if path, err := safeRepoPath(repo, selected); err == nil {
			t.Fatalf("escaping symlink accepted: %s", path)
		}
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "script"), []byte("echo safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("script", filepath.Join(repo, "alias")); err != nil {
		t.Fatal(err)
	}
	path, err := safeRepoPath(repo, "alias")
	if err != nil {
		t.Fatalf("in-repository symlink: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "echo safe" {
		t.Fatalf("legitimate content = %q, %v", content, err)
	}
}

func TestReadGitProvisionerFileConfinesOpenAndBoundsContent(t *testing.T) {
	repo, outside := t.TempDir(), t.TempDir()
	outsideFile := filepath.Join(outside, "secret")
	if err := os.WriteFile(outsideFile, []byte("outside-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, "payload")
	if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	validated, err := safeRepoPath(repo, "payload")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, file); err != nil {
		t.Fatal(err)
	}
	if content, err := readGitProvisionerFile(repo, validated); err == nil {
		t.Fatalf("symlink replaced after validation read: %q", content)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("x", maxGitProvisionerFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGitProvisionerFile(repo, validated); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := readGitProvisionerFile(repo, validated); err != nil || string(content) != "safe" {
		t.Fatalf("legitimate read: %q, %v", content, err)
	}
	if _, err := readGitProvisionerFile(repo, repo); err == nil {
		t.Fatal("directory accepted as file")
	}
}
