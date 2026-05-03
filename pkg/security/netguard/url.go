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
	AllowUnresolved bool
	Resolver        Resolver
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

	host := parsedURL.Hostname()
	if host == "" {
		return fmt.Errorf("%s: URL has no host", fieldPath)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("%s: URL host must be a DNS name, not a raw IP address (%s)", fieldPath, host)
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
