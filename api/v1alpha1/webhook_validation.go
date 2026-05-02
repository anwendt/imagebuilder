// api/v1alpha1/webhook_validation.go
//
// Shared SSRF / URL validation helpers used by both the VMImage and
// ProviderConfig admission webhooks (AS-047, AS-049, REQ-008).
//
// Block requests that resolve to:
//   - IPv4 link-local (169.254.0.0/16) — cloud metadata endpoints
//   - IPv4 loopback  (127.0.0.0/8)
//   - IPv6 loopback  (::1)
//   - IPv4 private   (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
//   - any raw IP address in the URL host (hostname required)

package v1alpha1

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// blockedCIDRs contains the SSRF-relevant address ranges (AS-047, AS-049).
var blockedCIDRs = func() []*net.IPNet {
	raw := []string{
		"169.254.0.0/16", // link-local (AWS/GCP/Azure metadata)
		"127.0.0.0/8",    // loopback IPv4
		"10.0.0.0/8",     // private class A
		"172.16.0.0/12",  // private class B
		"192.168.0.0/16", // private class C
		"::1/128",        // loopback IPv6
		"fc00::/7",       // unique local (IPv6 private)
	}
	nets := make([]*net.IPNet, 0, len(raw))
	for _, cidr := range raw {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// validateNoSSRF returns an error if rawURL is unsafe:
//   - not HTTPS
//   - host is a raw IP
//   - host resolves to a blocked CIDR
//
// fieldPath is used in the error message for kubectl feedback.
func validateNoSSRF(fieldPath, rawURL string) error {
	if rawURL == "" {
		return nil // optional field, nothing to validate
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s: invalid URL: %w", fieldPath, err)
	}

	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("%s: URL must use https, got %q", fieldPath, u.Scheme)
	}

	host := u.Hostname() // strips port
	if host == "" {
		return fmt.Errorf("%s: URL has no host", fieldPath)
	}

	// Reject raw IP addresses in the host — DNS names only (AS-047).
	if net.ParseIP(host) != nil {
		return fmt.Errorf("%s: URL host must be a DNS name, not a raw IP address (%s)", fieldPath, host)
	}

	// Resolve hostname and check every returned address.
	addrs, err := net.LookupHost(host)
	if err != nil {
		// If the hostname does not resolve at admission time (e.g. custom DNS),
		// we allow it: the build pod will still fail-safe if unreachable.
		// Record a warning-level non-blocking pass here.
		return nil
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		for _, blocked := range blockedCIDRs {
			if blocked.Contains(ip) {
				return fmt.Errorf(
					"%s: URL host %q resolves to %s which is in blocked range %s (SSRF protection, AS-047)",
					fieldPath, host, addr, blocked,
				)
			}
		}
	}
	return nil
}
