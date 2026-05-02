---
document-id: ADR-002
title: Platform Providers as Separate Kubernetes Containers (Crossplane Model)
status: Accepted
date: 2026-04-18
deciders: Platform Engineering
supersedes: —
superseded-by: —
classification: Internal
---

# ADR-002 — Platform Providers as Separate Kubernetes Containers

## Status

**Accepted**

---

## Context

The system needs to support multiple cloud and on-premises platforms (AWS, Azure, GCP,
vSphere, OpenStack) as image upload and registration targets. New platforms must be
addable by community contributors without requiring changes to the core operator.

Three main extensibility models were evaluated:
1. **Go plugin mechanism** (`.so` shared libraries)
2. **In-process compile-time plugins** (all platforms compiled into the binary)
3. **Out-of-process plugins** (separate processes with a defined IPC contract)

---

## Decision

Platform providers are implemented as **separate Kubernetes containers** (Pods / Deployments)
that are dynamically instantiated by the core operator when a `PlatformProvider` CRD is applied.

The core operator communicates with provider pods via the Kubernetes networking model:
the `PlatformProvider` controller creates a provider Deployment and a namespaced
ClusterIP Service, and the operator connects to the provider through **gRPC over TCP**
inside the cluster.

For production, TCP provider communication is not treated as a trusted implicit
loopback channel. It must be constrained by namespace-scoped RBAC, NetworkPolicy,
provider image signature policy, and, where the provider endpoint is reachable beyond
the operator namespace or cluster-local trust boundary, **mTLS**. The provider gRPC API
remains the stable contract defined in `api/provider/v1/provider.proto` (see ADR-005).

Unix domain sockets remain an allowed optimization for future same-Pod sidecar provider
topologies, but they are no longer the required or assumed transport for the default
external provider model.

---

## Architecture

```
PlatformProvider CR applied
        ↓
Core Operator detects CR (Reconciliation Loop)
        ↓
Operator creates Kubernetes Deployment for provider image
        ↓
Provider pod starts, exposes gRPC on a ClusterIP Service
        ↓
Operator performs gRPC Handshake → GetCapabilities()
        ↓
Provider registered in in-memory Plugin Registry
        ↓
VMImage builds can now reference this provider via ProviderConfig
```

### Provider Pod Lifecycle

| Phase | Description |
|---|---|
| Installing | Operator has created the Deployment, waiting for pod readiness |
| Healthy | gRPC handshake succeeded, HealthCheck() returns OK |
| Unhealthy | HealthCheck() failed or pod not ready |
| Unknown | Operator cannot reach provider |

---

## Rationale

### Why Not Go Plugin Mechanism (`.so`)

| Problem | Detail |
|---|---|
| **Go version coupling** | Plugin and host binary must be compiled with the exact same Go version and module graph. A provider update requires the entire operator to be recompiled. |
| **No Windows support** | `plugin.Open()` is not supported on Windows, which would prevent Windows image build providers. |
| **No cross-compilation** | `.so` plugins cannot be cross-compiled, making CI/CD pipelines for multiple architectures significantly more complex. |
| **Static linking restriction** | All dependencies of the plugin must also be present in the host binary, eliminating the license isolation benefit. |

### Why Not All-in-One Binary (Compile-Time)

| Problem | Detail |
|---|---|
| **License contamination** | A proprietary or GPL-licensed provider compiled into the core binary would contaminate the Apache 2.0 project. |
| **Release coupling** | Every provider update requires a new core operator release and cluster rollout. |
| **Binary size** | Including all cloud SDKs (AWS, Azure, GCP, vSphere, OpenStack) results in a large binary even when only one provider is used. |

### Why Separate Deployments + ClusterIP gRPC (Chosen)

| Benefit | Detail |
|---|---|
| **Independent versioning** | Provider image `v1.2.0` can be deployed without updating the core operator. |
| **Language independence** | Providers can be written in any language that has a gRPC implementation (Go, Python, Rust, Java). |
| **License isolation** | A proprietary provider running in its own container does not affect the Apache 2.0 license of the core operator. |
| **Kubernetes-native** | Providers benefit from Kubernetes health management, rolling updates, resource limits, and RBAC. |
| **Proven pattern** | This is the exact model used by Crossplane, the CNCF-graduated Kubernetes extension framework. |
| **Fault isolation** | A provider crash does not crash the core operator. |
| **Kubernetes-native networking** | Service discovery, readiness, NetworkPolicy, rollout behaviour, and observability fit the standard Pod/Service model. |

### Transport Security Model

| Scope | Transport | Required Controls |
|---|---|---|
| Default in-cluster provider | gRPC over TCP through namespaced ClusterIP | Default-deny NetworkPolicy, operator-only ingress to provider TCP/50051, provider image signature policy, ServiceAccount/RBAC least privilege, no public Service type |
| Cross-namespace or remote provider endpoint | gRPC over TCP | `PlatformProvider.spec.transport.tls.mode: Mutual`, provider certificate identity verification, operator client certificate verification, plus NetworkPolicy/firewall restrictions |
| Future same-Pod sidecar provider | gRPC over Unix domain socket | Shared `emptyDir` socket path, no Service exposure |

For `mode: Mutual`, the core loads the operator client certificate and CA bundle
from referenced Secrets, verifies the provider server certificate against
`serverName`, and mounts the provider server certificate plus client CA into the
provider Deployment. Providers using the Go SDK call `sdk.ServerOptionsFromEnv()`
to require and verify the operator client certificate.

---

## Consequences

### Positive
- Community contributors can publish providers as OCI images without touching the core repository.
- The core operator's Apache 2.0 license is not affected by provider licenses.
- Providers can be updated independently in production.

### Negative
- Running a provider requires an additional Kubernetes Deployment and Pod per platform.
- gRPC over TCP introduces a slight serialisation overhead compared to direct function calls.
- The operator must manage Deployment lifecycle for providers (create, update, delete).
- Provider endpoints exist as Kubernetes Services and must be protected by policy.

### Mitigations
- The gRPC overhead is negligible compared to image build and upload times (minutes to hours).
- The Deployment lifecycle is managed by the PlatformProvider controller, which is a standard Kubernetes reconciliation pattern.
- Default provider Services are ClusterIP-only; production deployments install the supplied NetworkPolicies by default and should also install image-signature policies.
- mTLS is required when provider TCP endpoints cross the local cluster trust boundary.

---

## Interface Contract

The gRPC service definition is the stable contract between the core operator and all providers.
Refer to:
- `api/provider/v1/provider.proto`
- [ADR-005 — Protobuf Schema as Versioned Contract](ADR-005-protobuf-versioned-contract.md)

---

## Related Documents

- [REQ-001 — Functional Requirements](../requirements/REQ-001-functional-requirements.md) (FR-033 – FR-037)
- [REQ-004 — Security Requirements](../requirements/REQ-004-security-requirements.md) (SR-021 – SR-024)
- [ADR-005 — Protobuf Schema as Versioned Contract](ADR-005-protobuf-versioned-contract.md)
- [ADR-006 — No Go Plugin Mechanism](ADR-006-no-go-plugin-mechanism.md)
