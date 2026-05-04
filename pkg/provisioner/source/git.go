package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/anwendt/imagebuilder/api/v1alpha1"
	"github.com/anwendt/imagebuilder/pkg/security/netguard"
)

const (
	maxGitProvisionerFileBytes  = 1 << 20
	maxGitProvisionerTotalBytes = 10 << 20
)

func ExpandProvisioners(ctx context.Context, workspaceDir string, specs []v1alpha1.ProvisionerSpec) ([]v1alpha1.ProvisionerSpec, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	expanded := make([]v1alpha1.ProvisionerSpec, 0, len(specs))
	for i, spec := range specs {
		if spec.Source == nil || spec.Source.Git == nil {
			expanded = append(expanded, spec)
			continue
		}
		steps, err := expandGitProvisioner(ctx, workspaceDir, i, spec)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, steps...)
	}
	return expanded, nil
}

func HasSources(specs []v1alpha1.ProvisionerSpec) bool {
	for _, spec := range specs {
		if spec.Source != nil {
			return true
		}
	}
	return false
}

func expandGitProvisioner(ctx context.Context, workspaceDir string, step int, spec v1alpha1.ProvisionerSpec) ([]v1alpha1.ProvisionerSpec, error) {
	gitSpec := spec.Source.Git
	if err := validateGitProvisionerSource(ctx, step, gitSpec); err != nil {
		return nil, err
	}
	repoDir := filepath.Join(workspaceDir, "provisioner-sources", fmt.Sprintf("%03d", step))
	if err := os.RemoveAll(repoDir); err != nil {
		return nil, fmt.Errorf("clean git provisioner workspace: %w", err)
	}
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return nil, fmt.Errorf("create git provisioner workspace: %w", err)
	}
	repo, err := git.PlainCloneContext(ctx, repoDir, false, &git.CloneOptions{
		URL:  gitSpec.URL,
		Auth: gitAuth(gitSpec),
	})
	if err != nil {
		return nil, fmt.Errorf("clone provisioner git source %q: %w", gitSpec.URL, err)
	}
	if err := checkoutGitRef(repo, gitSpec.Ref); err != nil {
		return nil, fmt.Errorf("checkout provisioner git ref %q: %w", gitSpec.Ref, err)
	}
	sourcePath, err := safeRepoPath(repoDir, gitSpec.Path)
	if err != nil {
		return nil, err
	}
	files, err := provisionerSourceFiles(sourcePath)
	if err != nil {
		return nil, err
	}
	steps := make([]v1alpha1.ProvisionerSpec, 0, len(files))
	var total int64
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return nil, fmt.Errorf("stat git provisioner file %q: %w", file, err)
		}
		if info.Size() > maxGitProvisionerFileBytes {
			return nil, fmt.Errorf("git provisioner file %q exceeds %d bytes", file, maxGitProvisionerFileBytes)
		}
		total += info.Size()
		if total > maxGitProvisionerTotalBytes {
			return nil, fmt.Errorf("git provisioner source exceeds %d bytes total", maxGitProvisionerTotalBytes)
		}
		// #nosec G304 -- file path was resolved under the cloned repository workspace.
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read git provisioner file %q: %w", file, err)
		}
		next := spec
		next.Source = nil
		next.Inline = string(content)
		steps = append(steps, next)
	}
	return steps, nil
}

func gitAuth(gitSpec *v1alpha1.GitProvisionerSourceSpec) transport.AuthMethod {
	if gitSpec == nil || gitSpec.Auth == nil {
		return nil
	}
	token := firstNonEmpty(gitSpec.Auth.RuntimeToken, readSecretFile(gitSpec.Auth.TokenPath))
	if token != "" {
		return &githttp.TokenAuth{Token: token}
	}
	username := firstNonEmpty(gitSpec.Auth.RuntimeUsername, readSecretFile(gitSpec.Auth.UsernamePath))
	password := firstNonEmpty(gitSpec.Auth.RuntimePassword, readSecretFile(gitSpec.Auth.PasswordPath))
	if username != "" || password != "" {
		return &githttp.BasicAuth{Username: username, Password: password}
	}
	return nil
}

func readSecretFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	// #nosec G304 -- Path is sourced from controller-managed Secret mounts or explicit runtime config.
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validateGitProvisionerSource(ctx context.Context, step int, gitSpec *v1alpha1.GitProvisionerSourceSpec) error {
	fieldPath := fmt.Sprintf("spec.provisioners[%d].source.git", step)
	if gitSpec == nil {
		return fmt.Errorf("%s is required", fieldPath)
	}
	if err := netguard.ValidatePublicHTTPSURL(ctx, fieldPath+".url", gitSpec.URL, netguard.Options{}); err != nil {
		return err
	}
	if strings.TrimSpace(gitSpec.Ref) == "" {
		return fmt.Errorf("%s.ref is required", fieldPath)
	}
	if strings.TrimSpace(gitSpec.Path) == "" {
		return fmt.Errorf("%s.path is required", fieldPath)
	}
	return nil
}

func checkoutGitRef(repo *git.Repository, ref string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}
	if hash := plumbing.NewHash(ref); !hash.IsZero() {
		if err := worktree.Checkout(&git.CheckoutOptions{Hash: hash}); err == nil {
			return nil
		}
	}
	revision, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return err
	}
	return worktree.Checkout(&git.CheckoutOptions{Hash: *revision})
}

func safeRepoPath(repoDir, rel string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(rel))
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("git provisioner path must be relative and stay within the repository")
	}
	full := filepath.Join(repoDir, clean)
	relToRepo, err := filepath.Rel(repoDir, full)
	if err != nil {
		return "", fmt.Errorf("resolve git provisioner path: %w", err)
	}
	if relToRepo == ".." || strings.HasPrefix(relToRepo, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("git provisioner path must stay within the repository")
	}
	return full, nil
}

func provisionerSourceFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat git provisioner path %q: %w", path, err)
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, current)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk git provisioner directory %q: %w", path, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("git provisioner directory %q does not contain regular files", path)
	}
	return files, nil
}
