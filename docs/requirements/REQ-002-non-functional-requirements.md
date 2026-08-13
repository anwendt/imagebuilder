---
document-id: REQ-002
title: Non-Functional Requirements
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
---

# REQ-002 — Non-Functional Requirements

## 1. Purpose

This document defines the non-functional requirements (quality attributes) for the
**VM Image Builder** system. These requirements govern how the system behaves rather
than what it does and are essential for ISAE audit traceability.

---

## 2. Availability & Reliability

| ID | Requirement | Target |
|---|---|---|
| NFR-001 | The operator control plane SHALL be deployable in a high-availability configuration with leader election. | ≥ 2 replicas |
| NFR-002 | A single operator pod failure SHALL NOT interrupt in-progress builds. | Zero build loss on pod failure |
| NFR-003 | The system SHALL continue operating (accepting new VMImage submissions) if individual platform providers are temporarily unavailable. | Degraded mode |
| NFR-004 | Build jobs SHALL be retryable without side effects on already-completed provisioner steps. | Idempotent cleanup |
| NFR-005 | The system SHALL implement automatic cleanup of failed partial uploads to target platforms. | FR-037 compliance |

---

## 3. Performance & Scalability

| ID | Requirement | Target |
|---|---|---|
| NFR-006 | The system SHALL support concurrent image builds on the same cluster, limited by available build nodes. | Configurable max |
| NFR-007 | Build resource allocation (CPU, memory, storage) SHALL be configurable per VMImage. | Per-spec |
| NFR-008 | The operator reconciliation loop SHALL process VMImage state transitions within 10 seconds under normal load. | ≤ 10 s |
| NFR-009 | Platform provider health checks SHALL complete within 5 seconds. | ≤ 5 s |
| NFR-010 | Artifact upload SHALL stream from gRPC directly into target-platform APIs when they accept sequential readers. Full local spooling is permitted only for formats requiring later random access or archive inspection, such as vSphere OVA/OVF, and must have bounded lifecycle cleanup. | Streaming |

---

## 4. Maintainability & Extensibility

| ID | Requirement | Target |
|---|---|---|
| NFR-011 | Adding a new platform provider SHALL NOT require changes to the core operator source code. | Zero core patches |
| NFR-012 | Adding a new in-process provisioner SHALL require only implementing the Provisioner Go interface and registering it via `init()`. | Defined interface |
| NFR-013 | Adding a new init-container provisioner SHALL require only publishing a compliant OCI image (no SDK required). | OCI image only |
| NFR-014 | The gRPC provider interface (provider.proto) SHALL be backward-compatible across minor versions. Field numbers SHALL NOT be reused. | Semver guarantee |
| NFR-015 | All generated code (CRD manifests, deepcopy, protobuf) SHALL be regenerated via `make generate` without manual edits. | Automated |

---

## 5. Observability

| ID | Requirement | Target |
|---|---|---|
| NFR-016 | The operator SHALL expose Prometheus-compatible metrics on a dedicated metrics port (:8080). | Prometheus |
| NFR-017 | Metrics SHALL include: build duration, build error rate, active builds, provider latency, and upload throughput. | Defined metric set |
| NFR-018 | All operator logs SHALL be structured (JSON) using `log/slog` and include context fields (namespace, name, provider). | Structured logging |
| NFR-019 | The operator SHALL expose liveness and readiness probes on port :8081. | Kubernetes-native |
| NFR-020 | Build lifecycle events (start, provisioner completion, upload, registration, failure) SHALL be emitted as Kubernetes Events on the VMImage resource. | Kubernetes Events |

---

## 6. Portability & Deployment

| ID | Requirement | Target |
|---|---|---|
| NFR-021 | The operator SHALL run on any Kubernetes distribution (vanilla, OpenShift, EKS, AKS, GKE, Rancher). | K8s-native |
| NFR-021A | The minimum supported Kubernetes version SHALL be 1.29 because OCI provisioners use native sidecar containers (`initContainers[].restartPolicy: Always`). Helm installation and operator startup SHALL enforce this boundary. Kubernetes 1.33 or newer SHOULD be used for stable sidecar semantics. | Must |
| NFR-022 | The operator container image SHALL be built for AMD64 and ARM64 architectures. | Multi-arch |
| NFR-023 | All Kubernetes resources (CRDs, RBAC, Deployments) SHALL be deployable via standard `kubectl apply`. | Standard manifests |
| NFR-024 | The operator SHALL not require cluster-admin privileges; a minimal ClusterRole SHALL be defined. | Least privilege |
| NFR-025 | Build jobs SHALL be schedulable to dedicated build nodes via `nodeSelector` and tolerations. | Configurable |

---

## 7. Testability

| ID | Requirement | Target |
|---|---|---|
| NFR-026 | All Go packages SHALL have unit tests using table-driven test patterns. | ≥ 80 % coverage target |
| NFR-027 | Integration tests SHALL use real Kubernetes API interactions (envtest), not mocked clients. | No fake clients |
| NFR-028 | The plugin interface SHALL be testable via mock implementations of the Plugin Go interface. | Interface mocks |
| NFR-029 | Build and test pipelines SHALL be reproducible via `make test`. | CI/CD-ready |

---

## 8. Traceability Matrix

| Requirement | Architecture Component | ADR Reference |
|---|---|---|
| NFR-001 – NFR-005 | Operator Deployment, Leader Election | ADR-002 |
| NFR-006 – NFR-010 | Build Engine, Kubernetes Jobs | ADR-001, ADR-003 |
| NFR-011 – NFR-015 | Plugin System, Provisioner Interface | ADR-002, ADR-003, ADR-005, ADR-006 |
| NFR-016 – NFR-020 | Operator main.go, Metrics, Logging | — |
| NFR-021 – NFR-025 | CRD/RBAC manifests, Deployment spec | — |
| NFR-026 – NFR-029 | Test suite, Makefile | — |
