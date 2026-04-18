---
document-id: ADR-005
title: Protobuf Schema as Versioned and Immutable Provider Contract
status: Accepted
date: 2026-04-18
deciders: Platform Engineering
classification: Internal
---

# ADR-005 — Protobuf Schema as Versioned and Immutable Provider Contract

## Status

**Accepted**

---

## Context

The core operator communicates with platform provider pods via gRPC (see ADR-002).
Both the operator and provider pods must agree on the message format and RPC signatures.

As the system evolves, new capabilities will be added to the provider interface. Providers
built at version 1.x must continue to work with a core operator upgraded to version 1.y.
Breaking the interface contract would require simultaneous upgrades of all provider images —
an operational burden that undermines the independent versioning benefit established in ADR-002.

A stable, versioned contract mechanism is required.

---

## Decision

The provider interface is defined exclusively in **Protocol Buffers (protobuf)** and stored
in `api/provider/v1/provider.proto`. This file is the **normative, binding contract** between
the core operator and all platform providers.

### Stability Rules

1. **Field numbers are immutable.** A field number assigned in `v1` is reserved for that
   field forever, even if the field is later deprecated and removed from generated code.
2. **Existing RPC methods SHALL NOT be removed or renamed** within a major version.
3. **New optional fields MAY be added** to existing messages; consumers must handle unknown fields gracefully (protobuf wire format guarantee).
4. **New RPC methods MAY be added** within a major version. Providers implementing an older
   version of the interface should return `UNIMPLEMENTED` for unknown methods.
5. **Breaking changes** (field type changes, required→optional, removing methods) MUST
   introduce a new package version: `api/provider/v2/provider.proto`.

### Deprecation Process

```proto
message RegisterRequest {
  string artifact_id = 1;
  string format = 2;
  // Deprecated: use target_config instead. Will be removed in v2.
  string region = 3 [deprecated = true];
  TargetConfig target_config = 4;
}
```

Deprecated fields remain in the `.proto` file and generated Go code for at least one
minor version cycle before being candidates for v2 removal.

---

## Interface Definition (Summary)

The current stable interface (`api/provider/v1/provider.proto`) defines:

| RPC Method | Request | Response | Description |
|---|---|---|---|
| `GetCapabilities` | `google.protobuf.Empty` | `CapabilitiesResponse` | Returns provider name, version, supported formats and OS families |
| `ValidateConfig` | `ValidateConfigRequest` | `ValidateConfigResponse` | Validates credentials and endpoint connectivity |
| `UploadArtifact` | `stream UploadChunk` | `stream UploadProgress` | Bidirectional streaming upload of disk image |
| `RegisterImage` | `RegisterRequest` | `ImageRef` | Registers uploaded artifact as platform image (AMI, Template, etc.) |
| `DeleteArtifact` | `DeleteRequest` | `DeleteResponse` | Idempotent artifact cleanup |
| `HealthCheck` | `google.protobuf.Empty` | `HealthResponse` | Liveness check |

---

## Rationale

### Why Protobuf?

| Criterion | Protobuf | JSON/REST | Go Interface (in-process) |
|---|---|---|---|
| Schema enforcement | Strong (generated types) | Weak (any JSON accepted) | Compile-time only |
| Versioning support | Built-in field numbers | Requires manual convention | No runtime versioning |
| Language independence | Yes (any gRPC language) | Yes | Go only |
| Streaming support | Native (bidirectional) | Requires chunked HTTP | N/A |
| Performance | Binary, low overhead | Text, higher overhead | Direct call |
| Backward compatibility | Guaranteed by spec | Convention-based | Requires interface version |

### Why Not REST/OpenAPI?

REST/OpenAPI does not provide the same binary serialisation efficiency needed for streaming
disk image uploads (files of 2 GB to 50 GB). Protobuf streaming over gRPC is designed for
exactly this use case.

### Why Not Thrift or MessagePack?

Protobuf (via gRPC) has the broadest language support in the Kubernetes ecosystem and
is already a dependency of `controller-runtime`. Using a second serialisation framework
would add unnecessary complexity.

---

## Consequences

### Positive
- Providers built against `v1` continue to work after operator upgrades (forward compatibility).
- Generated Go client/server stubs eliminate hand-written serialisation code.
- Strict field numbering prevents accidental wire format breakage.
- Multi-language provider implementations are possible.

### Negative
- Proto file changes require running `make proto` to regenerate Go stubs; developers must not edit generated files.
- Introducing `v2` requires maintaining two active API versions during the transition period.
- gRPC adds a build dependency on the protoc compiler and the gRPC Go plugin.

---

## Generated Code Policy

Files in `api/provider/v1/` with the prefix `zz_generated` or the suffix `.pb.go` or `_grpc.pb.go`
are generated and SHALL NOT be manually edited. They are regenerated via:

```bash
make proto
```

The Makefile enforces this via a `// Code generated ... DO NOT EDIT.` header check in CI.

---

## Related Documents

- [ADR-002 — Platform Providers as Separate Containers](ADR-002-providers-as-separate-containers.md)
- [REQ-002 — Non-Functional Requirements](../requirements/REQ-002-non-functional-requirements.md) (NFR-014)
- [REQ-005 — Operational Requirements](../requirements/REQ-005-operational-requirements.md) (OR-019)
- `api/provider/v1/provider.proto`
