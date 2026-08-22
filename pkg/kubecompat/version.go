package kubecompat

import (
	"fmt"
	"strconv"
	"strings"

	versioninfo "k8s.io/apimachinery/pkg/version"
)

const (
	// MinimumKubernetesVersion is the first Kubernetes release where native
	// sidecar containers are enabled by default. Image Builder uses the native
	// representation: initContainers[].restartPolicy=Always.
	MinimumKubernetesVersion = "1.29.0"
	minimumMajor             = 1
	minimumMinor             = 29
)

// ServerVersionGetter is implemented by discovery.DiscoveryInterface.
type ServerVersionGetter interface {
	ServerVersion() (*versioninfo.Info, error)
}

// CheckServer verifies the Kubernetes compatibility boundary before any
// controller creates build Jobs with restartable init containers.
func CheckServer(discovery ServerVersionGetter) error {
	if discovery == nil {
		return fmt.Errorf("kubernetes discovery client is required")
	}
	info, err := discovery.ServerVersion()
	if err != nil {
		return fmt.Errorf("discover Kubernetes server version: %w", err)
	}
	return CheckVersion(info)
}

// CheckVersion accepts vendor-suffixed versions such as 1.29+ and compares the
// numeric major/minor boundary. Patch and vendor suffixes do not affect support.
func CheckVersion(info *versioninfo.Info) error {
	if info == nil {
		return fmt.Errorf("kubernetes server returned no version information")
	}
	major, err := numericPrefix(info.Major)
	if err != nil {
		return fmt.Errorf("parse Kubernetes major version %q: %w", info.Major, err)
	}
	minor, err := numericPrefix(info.Minor)
	if err != nil {
		return fmt.Errorf("parse Kubernetes minor version %q: %w", info.Minor, err)
	}
	if major < minimumMajor || (major == minimumMajor && minor < minimumMinor) {
		return fmt.Errorf("kubernetes %s is unsupported: Image Builder requires Kubernetes %s or newer for restartable init containers (SidecarContainers)", displayVersion(info), MinimumKubernetesVersion)
	}
	return nil
}

func numericPrefix(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	end := 0
	for end < len(raw) && raw[end] >= '0' && raw[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("version component has no numeric prefix")
	}
	return strconv.Atoi(raw[:end])
}

func displayVersion(info *versioninfo.Info) string {
	if strings.TrimSpace(info.GitVersion) != "" {
		return info.GitVersion
	}
	return info.Major + "." + info.Minor
}
