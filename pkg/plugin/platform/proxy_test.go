package platform

import (
	"net/http"
	"net/url"
	"os"
	"testing"
)

func TestHTTPClientUsesProviderScopedProxyWithoutMutatingEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://process-proxy.example.com:9443")
	client, err := HTTPClient(map[string]string{
		"httpProxy":  "http://proxy.example.com:8080",
		"httpsProxy": "http://proxy.example.com:8443",
		"noProxy":    "localhost,127.0.0.1,.svc,10.0.0.0/8",
	})
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	transport := client.Transport.(*http.Transport)

	assertProxy(t, transport, "http://api.example.com", "http://proxy.example.com:8080")
	assertProxy(t, transport, "https://api.example.com", "http://proxy.example.com:8443")
	assertProxy(t, transport, "https://provider.imagebuilder.svc", "")
	assertProxy(t, transport, "https://10.2.3.4", "")
	if got := os.Getenv("HTTPS_PROXY"); got != "http://process-proxy.example.com:9443" {
		t.Fatalf("HTTPS_PROXY was mutated to %q", got)
	}
}

func TestHTTPClientInstancesKeepDifferentProxyConfiguration(t *testing.T) {
	first, err := HTTPClient(map[string]string{"httpsProxy": "http://first.example.com:8443"})
	if err != nil {
		t.Fatalf("first HTTPClient: %v", err)
	}
	second, err := HTTPClient(map[string]string{"httpsProxy": "http://second.example.com:8443"})
	if err != nil {
		t.Fatalf("second HTTPClient: %v", err)
	}
	assertProxy(t, first.Transport.(*http.Transport), "https://api.example.com", "http://first.example.com:8443")
	assertProxy(t, second.Transport.(*http.Transport), "https://api.example.com", "http://second.example.com:8443")
}

func assertProxy(t *testing.T, transport *http.Transport, rawURL, want string) {
	t.Helper()
	target, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	proxy, err := transport.Proxy(&http.Request{URL: target})
	if err != nil {
		t.Fatalf("proxy for %s: %v", rawURL, err)
	}
	got := ""
	if proxy != nil {
		got = proxy.String()
	}
	if got != want {
		t.Fatalf("proxy for %s = %q, want %q", rawURL, got, want)
	}
}
