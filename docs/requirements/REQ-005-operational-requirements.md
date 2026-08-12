---
document-id: REQ-005
title: Operational Requirements
version: 1.1.0
status: Draft
date: 2026-05-04
author: Platform Engineering
classification: Internal
---

# REQ-005 — Operational Requirements

## 1. Purpose

This document defines the operational requirements for the **VM Image Builder** system,
covering deployment, monitoring, lifecycle management, and day-2 operations. These
requirements ensure the system can be reliably operated in a production environment
and supports ISAE audit traceability.

---

## 2. Deployment Requirements

| ID | Requirement | Priority |
|---|---|---|
| OR-001 | The operator SHALL be deployable using standard Kubernetes manifests (`kubectl apply -f`). | Must |
| OR-002 | All required Kubernetes resources (CRDs, ClusterRole, ServiceAccount, Deployment) SHALL be bundled in a versioned release artifact. | Must |
| OR-003 | The system SHALL support Helm chart deployment as an alternative installation method. | Should |
| OR-004 | The operator SHALL support zero-downtime upgrades via rolling deployment strategy. | Must |
| OR-005 | CRD schema updates SHALL be backward-compatible within the `v1alpha1` API version. Breaking changes SHALL introduce a new API version (`v1beta1`, `v1`). | Must |
| OR-006 | The operator SHALL support installation in a dedicated namespace, isolated from user workloads. | Must |

---

## 3. Monitoring & Alerting Requirements

| ID | Requirement | Priority |
|---|---|---|
| OR-007 | The operator SHALL expose Prometheus metrics on port :8080 at the `/metrics` endpoint. | Must |
| OR-008 | The following metrics SHALL be exposed: | Must |
| | `imagebuilder_build_total{status="success\|failed"}` — total builds by outcome | |
| | `imagebuilder_build_duration_seconds` — build duration histogram | |
| | `imagebuilder_active_builds` — current concurrent builds gauge | |
| | `imagebuilder_provider_health{provider="..."}` — provider health gauge | |
| | `imagebuilder_upload_bytes_total{provider="..."}` — bytes uploaded per provider | |
| OR-009 | The operator SHALL provide a Kubernetes liveness probe (`/healthz`) and readiness probe (`/readyz`) on port :8081. | Must |
| OR-010 | Alert rules SHALL be definable as PrometheusRule CRs for: build failure rate > 10 % over 1 h; provider unhealthy > 5 min; no successful builds in 24 h. | Should |

---

## 4. Logging Requirements

| ID | Requirement | Priority |
|---|---|---|
| OR-011 | All logs SHALL be structured JSON, emitted to stdout/stderr for collection by the cluster log aggregation stack. | Must |
| OR-012 | Log level SHALL be configurable at runtime via operator flag (`--log-level`). | Must |
| OR-013 | Every log entry SHALL include: `timestamp`, `level`, `logger`, `namespace`, `name` (resource), `message`. | Must |
| OR-014 | Build-phase transitions SHALL be logged at `INFO` level; errors at `ERROR` level with full error chain. | Must |

---

## 5. Backup & Recovery Requirements

| ID | Requirement | Priority |
|---|---|---|
| OR-015 | VMImage CRD instances (desired state) SHALL be recoverable via standard Kubernetes etcd backup/restore. | Must |
| OR-016 | A failed build SHALL leave the VMImage in `Failed` phase with a human-readable reason in `.status.conditions`. The resource SHALL be retryable declaratively by changing `.spec.build.revision`, or by deleting and re-creating it. | Must |
| OR-017 | Provider configuration (ProviderConfig) SHALL be recoverable from Kubernetes etcd backup. Credentials (Secrets) SHALL be recoverable from the Secret backup or external secret store. | Must |
| OR-026 | Direct provider-native source operations, such as AWS AMI registration from an EBS snapshot, SHALL document which source artifacts are user-owned and therefore excluded from automated cleanup. | Must |

---

## 6. Upgrade & Lifecycle Requirements

| ID | Requirement | Priority |
|---|---|---|
| OR-018 | Each release SHALL have a `CHANGELOG.md` entry documenting breaking changes, new features, and bug fixes. | Must |
| OR-019 | The `provider.proto` gRPC interface SHALL follow semantic versioning; new required fields are `v2` changes. | Must |
| OR-020 | Provider OCI images SHALL be versioned independently of the core operator; the PlatformProvider CRD specifies the image version. | Must |
| OR-021 | Deprecated API fields SHALL be marked with `// Deprecated:` in Go types and documented in the changelog for at least one release cycle before removal. | Must |

---

## 7. Capacity & Resource Planning Requirements

| ID | Requirement | Priority |
|---|---|---|
| OR-022 | The operator control plane pod SHALL have defined resource requests and limits in the Deployment manifest. | Must |
| OR-023 | Build job resource requests (CPU, memory, storage) SHALL be configurable per VMImage (`spec.build.resources`). | Must |
| OR-024 | Default resource limits for build jobs SHALL be documented to assist cluster capacity planning. | Should |
| OR-025 | The system documentation SHALL specify minimum node requirements for build nodes (QEMU workloads). | Should |

---

## 8. Traceability Matrix

| Requirement | Architecture Component | ADR Reference |
|---|---|---|
| OR-001 – OR-006 | CRD/RBAC manifests, Deployment | ADR-002 |
| OR-007 – OR-010 | Operator main.go, metrics endpoint | — |
| OR-011 – OR-014 | Structured logging (`log/slog`) | — |
| OR-015 – OR-017, OR-026 | Kubernetes etcd, VMImage status, provider cleanup documentation | ADR-003, ADR-007 |
| OR-018 – OR-021 | Release process, proto versioning | ADR-005 |
| OR-022 – OR-025 | Build job spec, resource config | ADR-003 |
