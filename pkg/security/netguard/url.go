// Package netguard contains network safety checks shared by admission and
// runtime code paths.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Resolver resolves DNS names for URL validation.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Options controls URL validation behavior.
type Options struct {
	// AllowUnresolved keeps admission permissive for clusters with custom DNS.
	// Runtime callers should leave this false so DNS failures fail closed.
	AllowUnresolved     bool
	Resolver            Resolver
	AllowedPrivateCIDRs []string
	AllowedDNSNames     []string
}

type defaultResolver struct{}

func (defaultResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// ValidatePublicHTTPSURL rejects URLs that could target internal or metadata
// endpoints. It permits DNS names only, then checks all resolved addresses
// against the blocked ranges.
func ValidatePublicHTTPSURL(ctx context.Context, fieldPath, rawURL string, opts Options) error {
	if rawURL == "" {
		return nil
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s: invalid URL: %w", fieldPath, err)
	}

	if strings.ToLower(parsedURL.Scheme) != "https" {
		return fmt.Errorf("%s: URL must use https, got %q", fieldPath, parsedURL.Scheme)
	}
	if parsedURL.User != nil {
		return fmt.Errorf("%s: URL must not embed credentials; use Secret references for authentication", fieldPath)
	}

	host := parsedURL.Hostname()
	if host == "" {
		return fmt.Errorf("%s: URL has no host", fieldPath)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("%s: URL host must be a DNS name, not a raw IP address (%s)", fieldPath, host)
	}
	if len(opts.AllowedDNSNames) > 0 && !dnsNameAllowed(host, opts.AllowedDNSNames) {
		return fmt.Errorf("%s: URL host %q is not in the configured DNS allowlist", fieldPath, host)
	}
	allowedPrivate, err := parseCIDRs(opts.AllowedPrivateCIDRs)
	if err != nil {
		return fmt.Errorf("%s: invalid private endpoint allowlist: %w", fieldPath, err)
	}

	resolver := opts.Resolver
	if resolver == nil {
		resolver = defaultResolver{}
	}
	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		if opts.AllowUnresolved {
			return nil
		}
		return fmt.Errorf("%s: URL host %q could not be resolved: %w", fieldPath, host, err)
	}
	if len(addrs) == 0 {
		if opts.AllowUnresolved {
			return nil
		}
		return fmt.Errorf("%s: URL host %q did not resolve to any address", fieldPath, host)
	}

	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		for _, blocked := range permanentlyBlockedCIDRs {
			if blocked.Contains(ip) {
				return fmt.Errorf(
					"%s: URL host %q resolves to %s which is in blocked range %s (SSRF protection, AS-047)",
					fieldPath, host, addr, blocked,
				)
			}
		}
		for _, blocked := range privateCIDRs {
			if blocked.Contains(ip) && !containedByAny(ip, allowedPrivate) {
				return fmt.Errorf("%s: URL host %q resolves to private address %s outside the configured CIDR allowlist", fieldPath, host, addr)
			}
		}
	}
	return nil
}

// blockedCIDRs contains the SSRF-relevant address ranges (AS-047, AS-049).
var permanentlyBlockedCIDRs = mustCIDRs([]string{
	"0.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4",
	"::/128", "::1/128", "fe80::/10", "ff00::/8",
})

var privateCIDRs = mustCIDRs([]string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
})

func mustCIDRs(raw []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(raw))
	for _, cidr := range raw {
		_, n, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}

func parseCIDRs(raw []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(raw))
	for _, value := range raw {
		_, network, err := net.ParseCIDR(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR", value)
		}
		for _, permanent := range permanentlyBlockedCIDRs {
			if permanent.Contains(network.IP) || network.Contains(permanent.IP) {
				return nil, fmt.Errorf("%q overlaps permanently blocked range %s", value, permanent)
			}
		}
		nets = append(nets, network)
	}
	return nets, nil
}

func containedByAny(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func dnsNameAllowed(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
		if pattern == host {
			return true
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}
