package vsphere

import (
	"fmt"
	"path"
	"strings"

	"github.com/vmware/govmomi/vim25/types"
)

// Build-VM safety guard (ADR-009). The vSphere provider creates and destroys
// virtual machines on shared infrastructure, so every VM it touches must be
// provably its own: the name carries a fixed prefix and the VM lives in a
// dedicated folder. Anything else is refused, whatever the caller passes in.
const (
	buildVMNamePrefix = "imagebuilder-"
	buildFolderPrefix = "imagebuilder"
	maxBuildVMNameLen = 80
)

// buildVMName returns the deterministic name of the temporary build VM for an
// image build. It always starts with buildVMNamePrefix and embeds a short
// build identifier so two builds of the same image never collide.
func buildVMName(imageName, buildID string) string {
	suffix := sanitizeName(buildID)
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	base := strings.TrimPrefix(sanitizeName(firstNonEmpty(imageName, "build")), buildVMNamePrefix)
	if base == "" {
		base = "build"
	}
	maxBase := maxBuildVMNameLen - len(buildVMNamePrefix) - len(suffix) - 1
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-_.")
	}
	return buildVMNamePrefix + base + "-" + suffix
}

func checkBuildVMName(name string, protected []string) error {
	if !strings.HasPrefix(name, buildVMNamePrefix) || len(name) == len(buildVMNamePrefix) {
		return fmt.Errorf("vsphere guard: VM %q does not carry the build VM prefix %q", name, buildVMNamePrefix)
	}
	if hit := protectedHit(name, protected); hit != "" {
		return fmt.Errorf("vsphere guard: VM %q matches protected name %q", name, hit)
	}
	return nil
}

func checkBuildFolderPath(folder string, protected []string) error {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return fmt.Errorf("vsphere guard: ProviderConfig extra buildFolder is required for remote builds")
	}
	if !strings.HasPrefix(folder, "/") {
		return fmt.Errorf("vsphere guard: buildFolder %q must be an absolute inventory path", folder)
	}
	trimmed := strings.TrimRight(folder, "/")
	if path.Clean(trimmed) != trimmed {
		return fmt.Errorf("vsphere guard: buildFolder %q must be a clean path", folder)
	}
	if !strings.HasPrefix(path.Base(trimmed), buildFolderPrefix) {
		return fmt.Errorf("vsphere guard: buildFolder %q must be a dedicated folder whose name starts with %q", folder, buildFolderPrefix)
	}
	if hit := protectedHit(trimmed, protected); hit != "" {
		return fmt.Errorf("vsphere guard: buildFolder %q matches protected name %q", folder, hit)
	}
	return nil
}

// checkOwnedBuildVM verifies that a VM may be modified or destroyed by the
// provider: build prefix, not protected, and directly inside the build folder.
func checkOwnedBuildVM(name string, parent *types.ManagedObjectReference, buildFolder types.ManagedObjectReference, protected []string) error {
	if err := checkBuildVMName(name, protected); err != nil {
		return err
	}
	if parent == nil || *parent != buildFolder {
		return fmt.Errorf("vsphere guard: VM %q is not in the dedicated build folder", name)
	}
	return nil
}

// checkProtectedConfig rejects ProviderConfig values that mention a protected
// name, so a misconfigured target can never point the provider at them.
func checkProtectedConfig(values map[string]string, protected []string) error {
	if len(protected) == 0 {
		return nil
	}
	for key, value := range values {
		if hit := protectedHit(value, protected); hit != "" {
			return fmt.Errorf("vsphere guard: ProviderConfig %s %q matches protected name %q", key, value, hit)
		}
	}
	return nil
}

func protectedHit(value string, protected []string) string {
	lower := strings.ToLower(value)
	for _, p := range protected {
		if p != "" && strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

func parseProtectedNames(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
