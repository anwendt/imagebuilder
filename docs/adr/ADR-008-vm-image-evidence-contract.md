---
document-id: ADR-008
title: Provider-Neutral VM Image Evidence Contract
status: Accepted
date: 2026-08-22
deciders: Platform Engineering
supersedes: —
superseded-by: —
classification: Internal
---

# ADR-008 — Provider-Neutral VM Image Evidence Contract

## Status

**Accepted**

## Context

The golden-image promotion path requires an SPDX SBOM, a vulnerability report,
signed SLSA provenance, and immutable registry references. Remote builds execute
inside provider-owned temporary VMs, while promotion and policy decisions remain
provider-neutral. Returning reports through Kubernetes status would exceed
reasonable status sizes and could expose build details. Registering an image
before checking its evidence would also allow an unverified image to become
consumable.

## Decision

`VMImage.spec.evidence` is the provider-neutral policy. When evidence is
required:

- exactly one `evidence` provisioner is configured and it is the final
  provisioner;
- the provider executes evidence collection after all image customization and
  before image registration;
- the evidence provisioner emits one versioned result marker,
  `IMAGEBUILDER_EVIDENCE_V1=<base64-json>`;
- the result contains status plus OCI references for the SBOM, vulnerability
  report, provenance, and signature bundle;
- every reference is pinned by a SHA-256 descriptor digest and is located below
  `spec.evidence.registryRepository`;
- the provider and core controller both validate the result and fail closed;
- only the compact references and outcome are stored in `VMImage.status`.

The core owns policy, status, events, and final admission of the result. The
provider owns execution, capture ordering, parsing provider output, cleanup, and
preventing registration after an evidence failure. The provider contract is
extended additively in accordance with ADR-005.

Evidence scripts obtain registry authentication and signing authority through
the temporary VM's managed identity and KMS. Private keys, passwords, access tokens, reports, and
signature payloads are not transported in provider operation references or
Kubernetes status.

## Rationale

- A common API allows promotion policy to work across providers.
- Provider-side collection can inspect the fully provisioned guest.
- Digest-pinned OCI references are compact, immutable, and independently
  retrievable.
- Validation in both the provider and core creates a fail-closed trust boundary.
- The versioned marker avoids coupling the core to Azure Run Command output
  formatting.

## Alternatives Considered

### Store complete reports in VMImage status

Rejected because reports are large, status is not an artifact store, and the
data would be duplicated in etcd.

### Trust arbitrary URLs returned by a provisioner

Rejected because mutable URLs and tags cannot establish artifact integrity.

### Implement evidence policy separately in every provider

Rejected because promotion semantics and Ready conditions would diverge.

### Register first and validate later

Rejected as the default because an image could briefly be consumable without
passing policy. A future provider may capture into a quarantined, non-consumable
state and publish it only after verification.

## Consequences

### Positive

- A VMImage cannot become Ready without all required evidence.
- Promotion consumes the same immutable references on every provider.
- Reports remain outside Kubernetes and can follow registry retention policy.
- Remote build reconciliation remains resumable because compact evidence state
  is part of the opaque operation reference.

### Negative

- The temporary VM needs the approved Syft, Trivy, ORAS, and Cosign toolchain.
- Registry and KMS access must be granted to its managed identity.
- Provider output limits require the marker to contain references, not reports.
- Local builds require a future builder-owned implementation before they can use
  this contract; admission currently limits it to remote mode.

## Related Documents

- [ADR-002 — Platform Providers as Separate Kubernetes Containers](ADR-002-providers-as-separate-containers.md)
- [ADR-003 — Complex Provisioners as Kubernetes Restartable Init Containers](ADR-003-provisioners-as-init-containers.md)
- [ADR-005 — Protobuf Schema as Versioned Provider Contract](ADR-005-protobuf-versioned-contract.md)
- [ADR-007 — Remote Build as Provider-Owned Execution Mode](ADR-007-remote-build-provider-contract.md)
- [REQ-007 — Supply Chain Security](../requirements/REQ-007-supply-chain-security.md)
