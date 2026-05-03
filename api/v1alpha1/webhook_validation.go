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
	"context"

	"github.com/anwendt/imagebuilder/pkg/security/netguard"
)

// validateNoSSRF returns an error if rawURL is unsafe:
//   - not HTTPS
//   - host is a raw IP
//   - host resolves to a blocked CIDR
//
// fieldPath is used in the error message for kubectl feedback.
func validateNoSSRF(fieldPath, rawURL string) error {
	return netguard.ValidatePublicHTTPSURL(context.Background(), fieldPath, rawURL, netguard.Options{
		AllowUnresolved: true,
	})
}
