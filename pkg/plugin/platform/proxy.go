package platform

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// HTTPClient returns a ProviderConfig-scoped HTTP client. It never mutates
// process environment, so concurrent provider instances cannot overwrite each
// other's proxy routing.
func HTTPClient(extra map[string]string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	config := httpproxy.Config{
		HTTPProxy:  extraValue(extra, "httpProxy"),
		HTTPSProxy: extraValue(extra, "httpsProxy"),
		NoProxy:    extraValue(extra, "noProxy"),
	}
	proxy := config.ProxyFunc()
	transport.Proxy = func(req *http.Request) (*url.URL, error) { return proxy(req.URL) }
	return &http.Client{Transport: transport}, nil
}

func extraValue(extra map[string]string, key string) string {
	if extra == nil {
		return ""
	}
	return strings.TrimSpace(extra[key])
}
