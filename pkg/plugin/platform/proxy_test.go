package platform

import (
	"os"
	"testing"
)

func TestApplyProxyEnvironmentSetsStandardProxyVariables(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	ApplyProxyEnvironment(map[string]string{
		"httpProxy":  "http://proxy.example.com:8080",
		"httpsProxy": "http://proxy.example.com:8443",
		"noProxy":    "localhost,127.0.0.1",
	})

	assertEnv(t, "HTTP_PROXY", "http://proxy.example.com:8080")
	assertEnv(t, "http_proxy", "http://proxy.example.com:8080")
	assertEnv(t, "HTTPS_PROXY", "http://proxy.example.com:8443")
	assertEnv(t, "https_proxy", "http://proxy.example.com:8443")
	assertEnv(t, "NO_PROXY", "localhost,127.0.0.1")
	assertEnv(t, "no_proxy", "localhost,127.0.0.1")
}

func assertEnv(t *testing.T, key, want string) {
	t.Helper()
	if got := os.Getenv(key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
