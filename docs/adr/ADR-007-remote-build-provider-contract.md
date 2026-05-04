---
document-id: ADR-007
title: Remote Build as Provider-Owned Execution Mode
status: Accepted
date: 2026-05-01
deciders: Platform Engineering
supersedes: —
superseded-by: —
classification: Internal
---

# ADR-007 — Remote Build as Provider-Owned Execution Mode

## Status

**Accepted**

---

## Context

Local ISO builds use QEMU in a Kubernetes build pod. This works well on build nodes with
hardware virtualization, but many Kubernetes environments do not expose `/dev/kvm` to pods
or do not allow privileged virtualization workloads.

The system still needs to support image builds for platforms such as AWS and vSphere in
those environments. These platforms already provide native virtualization and image-capture
APIs:

- AWS can launch EC2 instances, provision them, create snapshots, and register AMIs.
- vSphere can create or clone VMs, attach ISO or seed media, provision guests, shut down,
  and convert the VM into a template or export an OVA.

Without a first-class remote build model, each provider would implement its own lifecycle,
status model, retry behavior, cleanup semantics, and credential handling rules. That would
create inconsistent behavior and duplicate core orchestration concepts outside the operator.

---

## Decision

Remote build is a first-class execution mode in the core API and controller flow, but the
actual platform operations are provider-owned.

The core operator owns:

- selecting `local` or `remote` build mode from the VMImage build specification;
- checking provider capabilities before scheduling a remote build;
- constructing a provider-neutral remote build request;
- enforcing common status, event, timeout, credential, cleanup, and hygiene semantics;
- recording provider-returned phase details and final image references without secrets.

The provider owns:

- creating the temporary VM or instance on the target platform;
- attaching source media, seed media, disks, and metadata using platform APIs;
- running or enabling guest readiness paths appropriate for the platform;
- executing or coordinating provisioners according to the core-provided plan;
- shutting down, sanitizing, capturing, uploading, registering, and cleaning up platform
  artifacts.

Remote build is not a replacement for local QEMU builds. It is an additional execution path
for environments where local virtualization is unavailable or where the target platform is
the best place to perform the build.

---

## Rationale

| Concern | Decision |
|---|---|
| No local KVM | Remote build allows AWS/vSphere-backed builds without exposing `/dev/kvm` to build pods. |
| Provider diversity | AWS, vSphere, Azure, GCP, and OpenStack have different image-capture APIs, so implementation belongs in providers. |
| Consistent UX | Status, Events, errors, timeouts, and cleanup stay governed by the core operator. |
| Security | Core credential and logging rules remain mandatory even when the work happens remotely. |
| Extensibility | External providers can add remote build support without modifying the core once the contract is stable. |

---

## Alternatives Considered

### Local QEMU Only

Rejected. It would make ISO and full guest builds impractical in Kubernetes environments
without KVM and would force users to maintain dedicated privileged build nodes.

### Provider-Specific Remote Build Without Core Contract

Rejected. It would duplicate status, cleanup, credential, and provisioner semantics across
providers and make behavior inconsistent for users.

### Core Implements AWS/vSphere Remote Build Directly

Rejected. It would couple the core operator to provider SDKs and platform-specific lifecycle
logic, weakening the provider extension model established in ADR-002 and ADR-006.

---

## Consequences

### Positive

- Builds can run without local KVM when the selected provider supports remote build.
- AWS and vSphere can use native platform workflows for image capture.
- The user-facing VMImage status model stays consistent across local and remote builds.
- Provider implementations remain independently versioned and independently released.

### Negative

- The provider contract must grow to represent remote build capability and lifecycle calls.
- Providers become responsible for more complex cleanup and failure handling.
- End-to-end tests need provider-specific integration environments or mocked provider
  implementations.

### Mitigations

- Add remote build support as an additive provider contract extension to preserve v1
  compatibility.
- Require explicit provider capability advertising before remote builds are scheduled.
- Start with one provider path, preferably AWS or vSphere, before generalizing the contract
  to all platforms.
- Keep remote build phase names aligned with the existing VMImage phase model.

---

## Implementation Priority

1. Add `build.mode` to the VMImage API and validation with supported values `local` and
   `remote`.
2. Extend provider capabilities with remote build support and supported source/OS/protocol
   combinations.
3. Define the provider-neutral remote build request and result contract.
4. Add core controller orchestration for remote build status, events, timeout, cancellation,
   cleanup, and image hygiene result handling.
5. Implement production provider paths for AWS, Azure, vSphere, and OpenStack.
6. Add provider-backed E2E tests and mocked-provider E2E tests for deterministic CI.
7. Extend remote build support to GCP after the current provider paths are stable.

---

## Related Documents

- [REQ-001 — Functional Requirements](../requirements/REQ-001-functional-requirements.md)
- [ADR-002 — Platform Providers as Separate Kubernetes Containers](ADR-002-providers-as-separate-containers.md)
- [ADR-005 — Protobuf Schema as Versioned Provider Contract](ADR-005-protobuf-versioned-contract.md)
- [ADR-006 — No Go Plugin Mechanism](ADR-006-no-go-plugin-mechanism.md)
