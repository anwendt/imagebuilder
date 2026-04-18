---
document-id: ADR-006
title: No Go Plugin Mechanism (.so Files)
status: Accepted
date: 2026-04-18
deciders: Platform Engineering
classification: Internal
---

# ADR-006 — No Go Plugin Mechanism (.so Files)

## Status

**Accepted**

---

## Context

Go provides a built-in `plugin` package that allows loading shared objects (`.so` files) at
runtime. This appears attractive as a lightweight extensibility mechanism for platform providers
and provisioners.

The alternative to runtime `.so` plugins is a **compile-time plugin pattern** using Go's
`init()` function and blank imports, analogous to the `database/sql` driver registration pattern.

This ADR records the explicit rejection of the `plugin` package for this project and the
adoption of the `init()`/blank-import pattern for compile-time plugins.

---

## Decision

**The Go `plugin` package SHALL NOT be used** anywhere in the VM Image Builder.

Compile-time extensibility (built-in providers loaded at startup) uses the **`init()` +
blank import** pattern in `cmd/operator/main.go`.

Runtime extensibility (community providers added without recompilation) uses the
**gRPC container model** described in ADR-002.

---

## Rationale

The following table documents all known limitations of the Go `plugin` package:

| Limitation | Detail | Impact |
|---|---|---|
| **Go version coupling** | Host binary and `.so` plugin must be compiled with the exact same Go version, GOARCH, and GOOS. A Go patch release upgrade requires all plugins to be recompiled. | Operational nightmare in multi-team environments |
| **Module graph coupling** | If the host and plugin both import a common package (e.g., `k8s.io/api`), they must import the exact same version. A diamond dependency in a plugin breaks the host. | Incompatible with an extensible ecosystem |
| **No Windows support** | `plugin.Open()` is not implemented on Windows (`GOOS=windows`). Windows image builds would require a different code path. | Breaks cross-platform portability |
| **No iOS/Android support** | Same; mobile targets (if ever needed) are excluded. | Future limitation |
| **No cross-compilation** | Plugins must be compiled on the same OS/arch as the host. Cross-compiling with CGo disabled is not possible for `.so` files. | Breaks CI/CD pipelines |
| **Implicit ABI coupling** | Go's internal ABI is not stable across versions. Interface changes in the host break all existing plugins silently at load time. | Runtime panics, not compile errors |
| **No unloading** | Once loaded, a `.so` plugin cannot be unloaded. Memory is leaked if a provider is removed. | Operator memory leak |
| **CGo requirement** | Building a `.so` requires `CGO_ENABLED=1`, which conflicts with the static binary requirement (see ADR-004). | Incompatible with CGO_ENABLED=0 |

---

## Compile-Time Plugin Pattern (Chosen for Built-in Providers)

Built-in providers (AWS, Azure, GCP, OpenStack, vSphere) are registered at startup via
Go's `init()` mechanism, analogous to `database/sql` drivers:

```go
// plugins/aws/plugin.go
func init() {
    plugin.Default().Register(&AWSPlugin{})
}

// cmd/operator/main.go
import (
    _ "github.com/yourorg/imagebuilder/plugins/aws"
    _ "github.com/yourorg/imagebuilder/plugins/azure"
    _ "github.com/yourorg/imagebuilder/plugins/gcp"
    _ "github.com/yourorg/imagebuilder/plugins/openstack"
    _ "github.com/yourorg/imagebuilder/plugins/vsphere"
)
```

This pattern:
- Has zero runtime overhead (direct function calls)
- Works with `CGO_ENABLED=0` and cross-compilation
- Is idiomatic Go (used by `database/sql`, `image`, `net/http` etc.)
- Requires recompilation for new built-in providers (acceptable; out-of-process model handles runtime extensibility)

---

## Runtime Plugin Pattern (Chosen for Community Providers)

Community-contributed or proprietary providers that should not be compiled into the core binary
use the gRPC container model (ADR-002). This provides all extensibility benefits without
the limitations of the `plugin` package.

---

## Consequences

### Positive
- Binary is fully statically linked (`CGO_ENABLED=0`); no `.so` dependencies at runtime.
- No Go version coupling between core operator and provider implementations.
- Community providers can be written in any language.
- Consistent with the Crossplane model; familiar to the Kubernetes community.

### Negative
- Adding a new built-in (compile-time) provider requires a core operator release.
- Runtime providers require running an additional Pod per provider.

### These trade-offs are acceptable because:
- New built-in providers are infrequent; the initial set (AWS, Azure, GCP, vSphere, OpenStack) covers the primary use cases.
- Additional Pods are a trivial operational cost compared to the benefits of independent versioning.

---

## Related Documents

- [ADR-002 — Platform Providers as Separate Containers](ADR-002-providers-as-separate-containers.md)
- [ADR-004 — LGPL Dependencies as External Processes](ADR-004-lgpl-as-external-processes.md) (CGO_ENABLED=0)
- [REQ-001 — Functional Requirements](../requirements/REQ-001-functional-requirements.md) (FR-016)
- `pkg/plugin/registry.go`
- `cmd/operator/main.go`
