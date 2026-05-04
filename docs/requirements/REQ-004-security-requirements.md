---
document-id: REQ-004
title: Security Requirements
version: 1.1.0
status: Draft
date: 2026-05-04
author: Platform Engineering
classification: Internal
---

# REQ-004 — Security Requirements

## 1. Purpose

This document defines the security requirements for the **VM Image Builder** system.
It covers credential management, access control, supply chain security, and network
isolation. These requirements are mandatory for ISAE audit compliance.

---

## 2. Credential & Secret Management

| ID | Requirement | Priority |
|---|---|---|
| SR-001 | Cloud provider credentials (API keys, service accounts, certificates) SHALL be stored exclusively as Kubernetes Secrets. | Must |
| SR-002 | Credentials SHALL never be embedded in VMImage, ProviderConfig, or PlatformProvider CRD specs. | Must |
| SR-003 | Credentials SHALL be referenced by name via `secretRef` in ProviderConfig only; the secret value is never copied into operator memory beyond the scope of a single operation. | Must |
| SR-004 | The system SHALL support external secret management systems (e.g., HashiCorp Vault, AWS Secrets Manager) via standard Kubernetes Secret synchronisation mechanisms (ESO). | Should |
| SR-005 | All Secrets SHALL be namespace-scoped; cluster-scoped secrets are not permitted for credential storage. | Must |

---

## 3. Access Control (RBAC)

| ID | Requirement | Priority |
|---|---|---|
| SR-006 | The operator ServiceAccount SHALL use a minimal ClusterRole — only the Kubernetes API verbs actually required SHALL be granted. | Must |
| SR-007 | The operator SHALL NOT require `cluster-admin` privileges. | Must |
| SR-008 | Access to VMImage, PlatformProvider, and ProviderConfig CRDs SHALL be governed by standard Kubernetes RBAC. | Must |
| SR-009 | Build pods and provider pods SHALL run under dedicated ServiceAccounts with no Kubernetes API access unless explicitly required. | Must |
| SR-010 | Platform provider pods SHALL be isolated per namespace and SHALL NOT share a ServiceAccount with the core operator. | Must |

---

## 4. Container & Pod Security

| ID | Requirement | Priority |
|---|---|---|
| SR-011 | All operator and provider containers SHALL run as non-root users. | Must |
| SR-012 | Container file systems SHALL be set to read-only where possible; writable volumes are limited to `/workspace` and `/tmp`. | Must |
| SR-013 | Containers SHALL drop all Linux capabilities; no `CAP_NET_ADMIN`, `CAP_SYS_ADMIN`, or privileged mode. Exception: QEMU build pods may require specific capabilities and SHALL be confined to dedicated build nodes. | Must |
| SR-014 | Pod Security Standards (PSS) `restricted` profile SHALL be applied to operator and provider namespaces. | Must |
| SR-015 | QEMU build pods that require elevated capabilities SHALL be deployed to dedicated nodes with appropriate node taints and tolerations, isolated from production workloads. | Must |

---

## 5. Supply Chain Security

| ID | Requirement | Priority |
|---|---|---|
| SR-016 | All provider OCI images used as PlatformProvider packages SHALL be referenced by digest (sha256), not by mutable tag, in production configurations. | Must |
| SR-017 | Provider images SHALL be signed using cosign / Sigstore. The operator SHALL verify signatures before loading a new provider. | Should |
| SR-018 | Source images (ISOs, cloud images) SHALL have their checksum verified (SHA-256) before use in a build. This is enforced by FR-005. | Must |
| SR-019 | The Go module graph SHALL be reproducibly verifiable via `go.sum`. No `replace` directives pointing to local or unverified sources are permitted in production. | Must |
| SR-020 | The container image build pipeline SHALL use a pinned base image digest and be reproducible. | Should |

---

## 6. Network Security

| ID | Requirement | Priority |
|---|---|---|
| SR-021 | Communication between the core operator and platform provider pods SHALL use the ADR-002 provider transport: gRPC over TCP through ClusterIP for the default Deployment model, or gRPC over Unix domain sockets for future same-Pod sidecar providers. | Must |
| SR-022 | TCP-based provider gRPC SHALL be protected by NetworkPolicy and ClusterIP-only Services inside the local trust boundary; mutual TLS (mTLS) SHALL be enforced when the endpoint crosses namespace, cluster, or network trust boundaries. | Must |
| SR-023 | Build pods SHALL have network egress restricted to the minimum required: source image download, cloud provider API endpoints. | Should |
| SR-024 | The operator metrics endpoint (:8080) SHALL NOT be exposed outside the cluster without authentication. | Must |

---

## 7. Audit Logging

| ID | Requirement | Priority |
|---|---|---|
| SR-025 | All VMImage lifecycle events (creation, build start, provisioner execution, upload, registration, deletion) SHALL be logged with structured fields: timestamp, namespace, resource name, actor, outcome. | Must |
| SR-026 | Kubernetes audit logging SHALL capture all CRD write operations (create, update, delete) on VMImage, PlatformProvider, and ProviderConfig. | Must |
| SR-027 | Logs SHALL NOT contain credential values, private key material, or session tokens. | Must |
| SR-028 | Audit logs SHALL be retained for a minimum of 90 days (configurable per organisational policy). | Must |

---

## 8. Provider-Native Source Security

| ID | Requirement | Priority |
|---|---|---|
| SR-029 | Provider-native source references SHALL be passed via `spec.source.providerRef`, not via downloadable `url` fields, to avoid bypassing URL validation and SSRF controls. | Must |
| SR-030 | Providers SHALL validate provider-native source references immediately before use, including existence, ownership/accessibility, and provider-specific ready state. | Must |
| SR-031 | Providers SHALL NOT delete or mutate user-owned provider-native source artifacts during failure cleanup unless the provider created the artifact as part of the same build operation. | Must |
| SR-032 | Direct provider-native image registration paths SHALL produce provider-attested hygiene results before the VMImage is marked Ready. | Must |

---

## 9. Traceability Matrix

| Requirement | Architecture Component | ADR Reference |
|---|---|---|
| SR-001 – SR-005 | ProviderConfig CRD, Secret references | ADR-002 |
| SR-006 – SR-010 | RBAC manifests, ServiceAccounts | — |
| SR-011 – SR-015 | Pod spec templates, build nodes | ADR-003 |
| SR-016 – SR-020 | PlatformProvider loader, go.sum, CI | ADR-002, ADR-005 |
| SR-021 – SR-024 | Provider gRPC transport, NetworkPolicy | ADR-002 |
| SR-025 – SR-028 | Operator logging, Kubernetes audit | — |
| SR-029 – SR-032 | VMImage admission, provider validation, provider cleanup | ADR-002, ADR-007 |
