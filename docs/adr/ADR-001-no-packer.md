---
document-id: ADR-001
title: Do Not Use HashiCorp Packer
status: Accepted
date: 2026-04-18
deciders: Platform Engineering
supersedes: —
superseded-by: —
classification: Internal
---

# ADR-001 — Do Not Use HashiCorp Packer

## Status

**Accepted**

---

## Context

The VM Image Builder system needs a mechanism to orchestrate the construction of VM images
for multiple target platforms (vSphere, OpenStack, AWS, Azure, GCP). HashiCorp Packer is
the industry-standard tool for this purpose and provides broad platform support.

However, HashiCorp changed the license of Packer from **Mozilla Public License 2.0 (MPL-2.0)**
to **Business Source License 1.1 (BSL 1.1)** effective August 2023, starting with Packer v1.9.5.

The VM Image Builder project is released under **Apache License 2.0** and must be fully
redistributable for commercial use without additional license obligations.

---

## Decision

**HashiCorp Packer will not be used** — neither as a statically linked library, a dynamically
loaded plugin, nor as a subprocess executed by the operator.

This prohibition applies to:
- `github.com/hashicorp/packer` and all its sub-packages
- The `packer` CLI binary bundled in any container image
- Any derivative that is itself BSL-licensed

---

## Rationale

| Factor | Detail |
|---|---|
| **License incompatibility** | BSL 1.1 prohibits competitive use and is not OSI-approved. It cannot be combined with Apache 2.0 in a redistributable binary. |
| **Redistribution restriction** | The BSL 1.1 restricts use in "production" by competing products, creating legal risk for the organisation. |
| **Upstream dependency** | Using Packer as a subprocess still creates a deployment dependency on a BSL-licensed binary, which complicates distribution. |
| **Community precedent** | The OpenTofu fork of Terraform demonstrates the same pattern: the community replaced a BSL tool with an Apache-licensed equivalent. |

---

## Alternatives Considered

| Alternative | Decision | Reason |
|---|---|---|
| Use Packer under BSL | Rejected | Legal risk, redistribution restrictions |
| Use OpenStack Diskimage Builder | Accepted (partially) | Apache 2.0, suitable for Linux image assembly on OpenStack |
| Use QEMU directly | Accepted | Apache 2.0 (userspace), full control over VM lifecycle |
| Use Cloud Provider native SDKs (AWS, Azure, GCP) | Accepted | Direct API calls, no intermediate tool required |
| Fork Packer at last MPL-licensed version | Rejected | Maintenance burden, security patches, no upstream |

---

## Consequences

### Positive
- The system is fully Apache-2.0-redistributable.
- No dependency on HashiCorp release cadence or pricing changes.
- Direct control over the build pipeline allows tighter Kubernetes integration.

### Negative
- Packer's extensive plugin ecosystem (100+ builders and provisioners) is not available.
- Significant implementation effort is required for the build engine (QEMU, diskimage-builder, Cloud APIs).
- Community familiarity with Packer's HCL configuration language is not transferable.

### Neutral
- Platform providers are implemented natively in Go using official cloud SDKs.
- The operator build engine must be maintained by the team.

---

## Compliance Evidence

- `go.mod` contains no `github.com/hashicorp/packer` dependency.
- `go-licenses check ./...` is gated in the build pipeline (see `Makefile` target `license-check`).
- No Packer binary is present in any container image defined in this repository.

---

## Related Documents

- [REQ-003 — License & Compliance Requirements](../requirements/REQ-003-license-compliance-requirements.md)
- [ADR-004 — LGPL Dependencies as External Processes](ADR-004-lgpl-as-external-processes.md)
