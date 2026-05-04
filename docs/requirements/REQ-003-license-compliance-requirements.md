---
document-id: REQ-003
title: License & Compliance Requirements
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
---

# REQ-003 — License & Compliance Requirements

## 1. Purpose

This document defines the open-source license and legal compliance requirements for the
**VM Image Builder** system. These requirements are critical for commercial use,
redistribution, and ISAE audit traceability. Non-compliance with these requirements
would constitute a legal risk to the organization.

---

## 2. Core License Constraint

The system and all its statically-linked dependencies MUST be licensed under
**Apache License 2.0** or a compatible permissive license (MIT, BSD-2-Clause, BSD-3-Clause).

**Prohibited licenses for static linking**: BSL 1.1, GPL v2, GPL v3, AGPL, LGPL (static only), SSPL, EUPL.

---

## 3. License Requirements

| ID | Requirement | Priority | Rationale |
|---|---|---|---|
| LR-001 | The core operator binary and all Go dependencies used via `import` SHALL be Apache 2.0 or MIT licensed. | Must | Apache 2.0 redistribution terms |
| LR-002 | HashiCorp Packer SHALL NOT be used as a dependency, library, or subprocess. | Must | BSL 1.1 since August 2023 — not redistributable |
| LR-003 | LGPL-licensed libraries (libvirt, libguestfs) SHALL NOT be statically or dynamically linked into any operator binary. | Must | LGPL §4 copyleft on dynamic linking |
| LR-004 | LGPL-licensed libraries MAY be used exclusively as external processes, accessed via Unix sockets, pipes, or child process invocation. | Must | Process boundary = license boundary |
| LR-005 | All third-party dependency licenses SHALL be verifiable via `go-licenses check ./...` as part of the build pipeline. | Must | Automated compliance gate |
| LR-006 | A `NOTICE` file SHALL be generated and maintained via `go-licenses report ./... > NOTICE` before each release. | Must | Apache 2.0 §4(d) requirement |
| LR-007 | Platform provider OCI images MAY carry their own licenses independently of the core operator license. | Must | Container boundary = license boundary |
| LR-008 | No source code, data, or asset with unclear or proprietary provenance SHALL be included in the repository. | Must | ISAE audit requirement |

---

## 4. Approved Dependency Matrix

The following table lists all approved direct dependencies with their license status:

| Component | Version | License | Usage | Status |
|---|---|---|---|---|
| sigs.k8s.io/controller-runtime | v0.18.x | Apache 2.0 | Operator framework | Approved |
| k8s.io/api | v0.30.x | Apache 2.0 | Kubernetes API types | Approved |
| k8s.io/apimachinery | v0.30.x | Apache 2.0 | Kubernetes utilities | Approved |
| k8s.io/client-go | v0.30.x | Apache 2.0 | Kubernetes client | Approved |
| google.golang.org/grpc | v1.64.x | Apache 2.0 | gRPC provider communication | Approved |
| google.golang.org/protobuf | v1.34.x | BSD-3-Clause | Protobuf serialization | Approved |
| github.com/vmware/govmomi | — | Apache 2.0 | vSphere/VCF SDK | Approved |
| github.com/gophercloud/gophercloud/v2 | — | Apache 2.0 | OpenStack SDK | Approved |
| github.com/aws/aws-sdk-go-v2 | — | Apache 2.0 | AWS SDK | Approved |
| github.com/Azure/azure-sdk-for-go | — | MIT | Azure SDK | Approved |
| cloud.google.com/go | — | Apache 2.0 | GCP SDK | Approved |
| libvirt (go-libvirt) | — | Apache 2.0 | libvirt socket binding | Approved |
| QEMU (userspace) | — | Apache 2.0 | VM build backend | Approved |
| diskimage-builder | — | Apache 2.0 | OpenStack image build | Approved |

---

## 5. Prohibited Dependencies

| Component | License | Reason |
|---|---|---|
| HashiCorp Packer | BSL 1.1 | Not Apache-2.0-compatible, not redistributable since 2023 |
| HashiCorp Terraform | BSL 1.1 | Same reason |
| libvirt (linked) | LGPL 2.1+ | LGPL copyleft applies to linked binaries |
| libguestfs (linked) | LGPL 2.1+ | Same reason |
| Any GPL-licensed library | GPL v2/v3 | GPL copyleft incompatible with Apache 2.0 redistribution |

---

## 6. Compliance Process

### 6.1 Pre-Merge Check
Every pull request that modifies `go.mod` or `go.sum` MUST pass the automated license check:

```bash
go-licenses check ./...
```

This command fails the build if any dependency with a prohibited license is detected.

### 6.2 Pre-Release Check
Before every tagged release, the following steps MUST be executed:

```bash
go-licenses check ./...
go-licenses report ./... > NOTICE
git add NOTICE
```

### 6.3 Annual Review
The approved dependency matrix (Section 4) SHALL be reviewed annually or whenever
a major dependency version is upgraded, to account for license changes (as occurred
with HashiCorp in 2023).

---

## 7. ISAE Audit Evidence

The following artifacts serve as audit evidence for license compliance:

| Evidence | Location | Description |
|---|---|---|
| `LICENSE` | `/LICENSE` | Apache 2.0 full text |
| `NOTICE` | `/NOTICE` | Third-party component attributions (go-licenses generated) |
| `go.mod` | `/go.mod` | Pinned dependency versions |
| `go.sum` | `/go.sum` | Cryptographic dependency verification |
| `Makefile` target `license-check` | `/Makefile` | Automated license gate |
| CI pipeline license step | CI configuration | Automated enforcement |

---

## 8. Traceability Matrix

| Requirement | ADR Reference | Evidence |
|---|---|---|
| LR-001 – LR-006 | ADR-001 (No Packer), ADR-004 (LGPL) | `NOTICE`, `go-licenses check` |
| LR-007 | ADR-002 (Containers) | OCI image manifests |
| LR-008 | — | Git history, code review |
