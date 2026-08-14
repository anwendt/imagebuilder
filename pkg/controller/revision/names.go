package revision

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const maxDNSLabelLength = 63

// ResourceName returns base unchanged for the initial empty revision and adds a
// stable hash for explicit revisions. This keeps existing resource names
// compatible while preventing collisions between rebuild attempts.
func ResourceName(base, buildRevision string) string {
	if buildRevision == "" {
		return base
	}
	digest := sha256.Sum256([]byte(buildRevision))
	suffix := fmt.Sprintf("%x", digest[:6])
	maxBaseLength := maxDNSLabelLength - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-.")
	}
	return base + "-" + suffix
}

// BuildID scopes provider-side idempotency to the explicit build revision.
func BuildID(uid, buildRevision string) string {
	if buildRevision == "" {
		return uid
	}
	digest := sha256.Sum256([]byte(buildRevision))
	return fmt.Sprintf("%s-%x", uid, digest[:6])
}

// Hash is a stable non-secret label value for grouping revision resources.
func Hash(buildRevision string) string {
	if buildRevision == "" {
		return "initial"
	}
	digest := sha256.Sum256([]byte(buildRevision))
	return fmt.Sprintf("%x", digest[:6])
}
