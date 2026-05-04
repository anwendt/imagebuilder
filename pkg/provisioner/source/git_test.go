package source

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func TestProvisionerSourceFiles_DirectorySortedByName(t *testing.T) {
	dir := t.TempDir()
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"02-second.sh", "01-first.sh"} {
		if err := os.WriteFile(filepath.Join(scriptsDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	files, err := provisionerSourceFiles(scriptsDir)
	if err != nil {
		t.Fatalf("provisionerSourceFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %#v, want 2 files", files)
	}
	if filepath.Base(files[0]) != "01-first.sh" || filepath.Base(files[1]) != "02-second.sh" {
		t.Fatalf("files order = %#v", files)
	}
}

func TestSafeRepoPathRejectsTraversal(t *testing.T) {
	if _, err := safeRepoPath(t.TempDir(), "../script.sh"); err == nil {
		t.Fatal("safeRepoPath should reject path traversal")
	}
}

func TestHasSources(t *testing.T) {
	if HasSources(nil) {
		t.Fatal("HasSources(nil) should be false")
	}
	if !HasSources([]v1alpha1.ProvisionerSpec{{Source: &v1alpha1.ProvisionerSourceSpec{}}}) {
		t.Fatal("HasSources should detect provisioner source")
	}
}

func TestGitAuthUsesRuntimeToken(t *testing.T) {
	auth := gitAuth(&v1alpha1.GitProvisionerSourceSpec{
		Auth: &v1alpha1.GitProvisionerAuthSpec{RuntimeToken: "secret-token"},
	})

	tokenAuth, ok := auth.(*githttp.TokenAuth)
	if !ok {
		t.Fatalf("gitAuth type = %T, want *http.TokenAuth", auth)
	}
	if tokenAuth.Token != "secret-token" {
		t.Fatalf("token = %q, want runtime token", tokenAuth.Token)
	}
}

func TestGitAuthUsesTokenPath(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	auth := gitAuth(&v1alpha1.GitProvisionerSourceSpec{
		Auth: &v1alpha1.GitProvisionerAuthSpec{TokenPath: tokenPath},
	})

	tokenAuth, ok := auth.(*githttp.TokenAuth)
	if !ok {
		t.Fatalf("gitAuth type = %T, want *http.TokenAuth", auth)
	}
	if tokenAuth.Token != "file-token" {
		t.Fatalf("token = %q, want trimmed file token", tokenAuth.Token)
	}
}

func TestGitAuthUsesBasicCredentials(t *testing.T) {
	auth := gitAuth(&v1alpha1.GitProvisionerSourceSpec{
		Auth: &v1alpha1.GitProvisionerAuthSpec{
			RuntimeUsername: "git-user",
			RuntimePassword: "git-pass",
		},
	})

	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("gitAuth type = %T, want *http.BasicAuth", auth)
	}
	if basicAuth.Username != "git-user" || basicAuth.Password != "git-pass" {
		t.Fatalf("basic auth = %#v, want runtime credentials", basicAuth)
	}
}
