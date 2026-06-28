package platform

import (
	"os"
	"strings"
)

// ApplyProxyEnvironment applies provider-level proxy settings from
// ProviderConfig.spec.extra to the current provider process.
//
// Provider implementations use the standard Go HTTP transport stacks of their
// SDKs. Those stacks consult HTTP_PROXY, HTTPS_PROXY, and NO_PROXY, so setting
// both upper- and lower-case variants makes ProviderConfig proxy examples work
// without provider-specific transport plumbing.
func ApplyProxyEnvironment(extra map[string]string) {
	setProxyEnv("HTTP_PROXY", "http_proxy", extraValue(extra, "httpProxy"))
	setProxyEnv("HTTPS_PROXY", "https_proxy", extraValue(extra, "httpsProxy"))
	setProxyEnv("NO_PROXY", "no_proxy", extraValue(extra, "noProxy"))
}

func setProxyEnv(upper, lower, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	_ = os.Setenv(upper, value)
	_ = os.Setenv(lower, value)
}

func extraValue(extra map[string]string, key string) string {
	if extra == nil {
		return ""
	}
	return strings.TrimSpace(extra[key])
}
