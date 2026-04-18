---
document-id: REQ-006
title: Development Standards — Test-Driven Development & Twelve-Factor App
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
references:
  - "Beck, K. (2002). Test-Driven Development: By Example. Addison-Wesley."
  - "Wiggins, A. (2012). The Twelve-Factor App. https://12factor.net"
---

# REQ-006 — Development Standards

## 1. Purpose

This document defines the mandatory development standards for the VM Image Builder system,
covering two pillars:

1. **Test-Driven Development (TDD)** — the required process for writing production code.
2. **Twelve-Factor App Principles** — the architectural and operational guidelines for
   building cloud-native, Kubernetes-deployable software.

These standards apply to all code merged into the main branch and serve as
verifiable criteria for ISAE audit purposes.

---

## 2. Test-Driven Development (TDD)

### 2.1 Principle

Test-Driven Development is the mandatory development process. Code SHALL be written
following the Red-Green-Refactor cycle:

```
1. RED    — Write a failing test that describes the desired behaviour.
2. GREEN  — Write the minimum production code to make the test pass.
3. REFACTOR — Clean up the code while keeping tests green.
```

No production code may be submitted in a pull request without a corresponding test
written before (or alongside) the production code.

### 2.2 TDD Process Requirements

| ID | Requirement | Priority |
|---|---|---|
| DR-001 | All new production code SHALL be preceded by a failing test that defines the expected behaviour (Red phase). | Must |
| DR-002 | Production code SHALL be written to satisfy the minimum set of failing tests, not to anticipate future requirements (Green phase, YAGNI principle). | Must |
| DR-003 | After tests pass, code SHALL be refactored to eliminate duplication and improve clarity without changing observable behaviour (Refactor phase). Tests must remain green after refactoring. | Must |
| DR-004 | Pull requests MUST include both the test(s) and the production code in the same commit or commit sequence that demonstrates the TDD cycle (reviewable via `git log`). | Must |
| DR-005 | Bug fixes SHALL be accompanied by a regression test that reproduces the bug before the fix is applied. | Must |

### 2.3 Test Levels

| ID | Requirement | Scope | Tooling |
|---|---|---|---|
| DR-006 | **Unit Tests** — Each exported function or method SHALL have unit tests covering normal cases, edge cases, and error cases. | Single function/method | `testing` stdlib, `t.Run()` |
| DR-007 | **Integration Tests** — Controller reconcilers SHALL be tested against a real Kubernetes API server via `envtest` (controller-runtime). No fake clients. | Controller + K8s API | `sigs.k8s.io/controller-runtime/pkg/envtest` |
| DR-008 | **Interface Compliance Tests** — Each concrete implementation of a Go interface (`Plugin`, `Provisioner`) SHALL have a compliance test that exercises every interface method. | Interface implementations | `testing` stdlib |
| DR-009 | **Contract Tests** — The init-container filesystem contract (`/workspace/config.json` / `status.json`) SHALL be tested by both the operator (writer) and at least one reference provisioner (reader). | Cross-component | Docker-based test |
| DR-010 | **End-to-End Tests** — At least one E2E test SHALL validate a full VMImage build lifecycle (create resource → build → ready status) against a real or kind-based cluster. | Full system | `kind` + `kubectl` |

### 2.4 Coverage Requirements

| ID | Requirement | Target |
|---|---|---|
| DR-011 | Unit test coverage for all packages under `pkg/` SHALL be measured and reported in CI. | Report required |
| DR-012 | Packages containing business logic (`pkg/controller/`, `pkg/plugin/`, `pkg/provisioner/`) SHALL maintain ≥ 80 % statement coverage. | ≥ 80 % |
| DR-013 | Coverage MUST NOT decrease between releases. PRs that reduce coverage below the threshold SHALL be blocked by CI. | Non-regressing |
| DR-014 | Generated code (`zz_generated.*`, `*.pb.go`, `*_grpc.pb.go`) is excluded from coverage measurement. | Excluded |

### 2.5 Test Quality Standards

| ID | Requirement | Priority |
|---|---|---|
| DR-015 | Tests SHALL use **table-driven test patterns** (`[]struct{ name, input, expected }`) for functions with multiple input/output combinations. | Must |
| DR-016 | Tests SHALL be **deterministic** — no random data without a fixed seed, no timing-dependent assertions. | Must |
| DR-017 | Tests SHALL be **independent** — each test case must be runnable in isolation without depending on state from previous tests. | Must |
| DR-018 | Test helper functions SHALL use `t.Helper()` so failure messages point to the test case, not the helper. | Must |
| DR-019 | Mock objects SHALL be generated from interfaces using `mockery` or hand-written — never from concrete types. | Must |
| DR-020 | Tests that require external services (cloud APIs) SHALL be guarded by a build tag (`//go:build integration`) and excluded from default `make test`. | Must |

### 2.6 CI Enforcement

| ID | Requirement | Priority |
|---|---|---|
| DR-021 | `make test` SHALL run unit and integration tests and fail if any test fails. | Must |
| DR-022 | CI SHALL fail the pull request if coverage drops below the defined threshold (DR-012). | Must |
| DR-023 | The test suite SHALL complete within 10 minutes for unit and envtest-based integration tests. | Should |

---

## 3. Twelve-Factor App Principles

The Twelve-Factor App methodology defines best practices for building software-as-a-service
applications. All twelve factors SHALL be applied to the VM Image Builder operator and its
provider components.

### Factor I — Codebase

> *One codebase tracked in version control, many deploys.*

| ID | Requirement | Priority |
|---|---|---|
| TF-001 | The operator, all built-in providers, and CRD definitions SHALL be maintained in a single Git repository. | Must |
| TF-002 | Community/proprietary providers are separate repositories; they must implement the same gRPC contract. | Must |
| TF-003 | Every deployment (dev, staging, production) is deployed from the same codebase at a specific Git tag. No environment-specific branches. | Must |

### Factor II — Dependencies

> *Explicitly declare and isolate dependencies.*

| ID | Requirement | Priority |
|---|---|---|
| TF-004 | All Go dependencies SHALL be declared in `go.mod` and pinned via `go.sum`. | Must |
| TF-005 | The operator binary SHALL NOT rely on any system-installed tool (no implicit `PATH` lookups). Exception: QEMU/libvirt on build nodes is an infrastructure concern, not an operator dependency. | Must |
| TF-006 | Container images SHALL be built `FROM scratch` or a minimal base image (distroless); no general-purpose OS tools included unless required. | Should |

### Factor III — Configuration

> *Store configuration in the environment.*

| ID | Requirement | Priority |
|---|---|---|
| TF-007 | All operator configuration that varies between environments (log level, metrics port, leader election namespace, max concurrent builds) SHALL be injectable via **environment variables** or **command-line flags**, not hardcoded. | Must |
| TF-008 | Credentials and secrets SHALL NEVER be baked into container images or source code. They are injected via Kubernetes Secrets at runtime. | Must |
| TF-009 | Default values for all configuration flags SHALL be documented and safe for production use. | Must |

### Factor IV — Backing Services

> *Treat backing services as attached resources.*

| ID | Requirement | Priority |
|---|---|---|
| TF-010 | The Kubernetes API server, cloud provider APIs, and platform provider gRPC endpoints SHALL be treated as attached resources, reachable via configuration (endpoint URL, credentials). | Must |
| TF-011 | Swapping a cloud provider (e.g., replacing a vSphere endpoint with a different vCenter) SHALL require only a ProviderConfig change, not an operator redeployment. | Must |

### Factor V — Build, Release, Run

> *Strictly separate build and run stages.*

| ID | Requirement | Priority |
|---|---|---|
| TF-012 | **Build stage**: `go build` produces a self-contained binary from source. No environment-specific code paths during the build. | Must |
| TF-013 | **Release stage**: The binary is combined with a specific configuration (Helm values, Kustomize overlays) to produce a versioned release artifact. | Must |
| TF-014 | **Run stage**: The release artifact is deployed to the target environment. Runtime configuration is injected via environment variables and Kubernetes Secrets. | Must |
| TF-015 | Releases SHALL be immutable. A deployed release cannot be changed; only a new release can be deployed. | Must |

### Factor VI — Processes

> *Execute the app as one or more stateless processes.*

| ID | Requirement | Priority |
|---|---|---|
| TF-016 | The operator process SHALL be stateless. All persistent state (build status, provider capabilities) is stored in Kubernetes etcd via the API server. | Must |
| TF-017 | The operator SHALL NOT use in-memory session state that would be lost on pod restart. The plugin registry is rebuilt from PlatformProvider CRD status on startup. | Must |
| TF-018 | Build artifacts (disk images) SHALL be stored on external volumes (`emptyDir` for ephemeral, PVC or object storage for caching), not in the operator process memory. | Must |

### Factor VII — Port Binding

> *Export services via port binding.*

| ID | Requirement | Priority |
|---|---|---|
| TF-019 | The operator SHALL expose metrics on a well-defined port (`:8080`) and health probes on (`:8081`) via HTTP, without requiring an external web server. | Must |
| TF-020 | Platform providers SHALL expose their gRPC service on a well-defined Unix socket path or TCP port, configured via environment variable. | Must |

### Factor VIII — Concurrency

> *Scale out via the process model.*

| ID | Requirement | Priority |
|---|---|---|
| TF-021 | The operator SHALL scale horizontally via multiple replicas with leader election (only the leader reconciles; standby replicas take over on failure). | Must |
| TF-022 | Concurrent build jobs SHALL be handled by creating multiple Kubernetes Jobs, not by goroutine-based concurrency within the operator process for build execution. | Must |
| TF-023 | The maximum number of concurrent builds SHALL be configurable and enforced by the operator (queue-based admission). | Should |

### Factor IX — Disposability

> *Maximize robustness with fast startup and graceful shutdown.*

| ID | Requirement | Priority |
|---|---|---|
| TF-024 | The operator SHALL start and be ready to reconcile within 30 seconds of pod creation. | Must |
| TF-025 | On SIGTERM, the operator SHALL complete in-flight reconciliation loops and shut down within 30 seconds. | Must |
| TF-026 | In-progress build Jobs SHALL NOT be terminated by operator shutdown; they run independently as Kubernetes Jobs and are picked up by the next leader. | Must |
| TF-027 | Provider pods SHALL implement graceful shutdown: complete any in-progress streaming upload before terminating. | Must |

### Factor X — Dev/Prod Parity

> *Keep development, staging, and production as similar as possible.*

| ID | Requirement | Priority |
|---|---|---|
| TF-028 | The operator SHALL run identically in local development (kind cluster), staging, and production. No code paths that are dev-only or prod-only. | Must |
| TF-029 | Integration tests SHALL use the same CRD schemas as production (generated manifests, not hand-written test fixtures). | Must |
| TF-030 | Build node requirements (QEMU, libvirtd) SHALL be documented so developers can replicate the production build environment locally. | Should |

### Factor XI — Logs

> *Treat logs as event streams.*

| ID | Requirement | Priority |
|---|---|---|
| TF-031 | The operator SHALL write all logs to **stdout** (not to files, not to syslog). Log collection is the responsibility of the cluster log aggregation stack. | Must |
| TF-032 | Logs SHALL be structured JSON (`log/slog`), one JSON object per line, parseable by standard log platforms (ELK, Loki, Splunk). | Must |
| TF-033 | The operator SHALL NOT manage log rotation, archiving, or retention. These are infrastructure concerns. | Must |

### Factor XII — Admin Processes

> *Run admin/management tasks as one-off processes.*

| ID | Requirement | Priority |
|---|---|---|
| TF-034 | Administrative tasks (CRD migration, licence report generation, manual build trigger) SHALL be implemented as standalone CLI commands or Kubernetes Jobs, not as operator REST endpoints. | Must |
| TF-035 | `make` targets SHALL be the authoritative interface for all development lifecycle tasks (generate, build, test, lint, license-check). | Must |

---

## 4. Traceability Matrix

| Requirement Group | Architecture Component | ADR Reference |
|---|---|---|
| DR-001 – DR-020 (TDD) | All `pkg/` packages, controller reconcilers | — |
| DR-021 – DR-023 (CI) | Makefile, CI pipeline | ADR-005 |
| TF-001 – TF-003 (Codebase) | Git repository structure | — |
| TF-004 – TF-006 (Dependencies) | go.mod, container images | ADR-004 |
| TF-007 – TF-009 (Config) | cmd/operator/main.go flags | REQ-004 |
| TF-010 – TF-011 (Backing Services) | ProviderConfig CRD | ADR-002 |
| TF-012 – TF-015 (Build/Release/Run) | Makefile, CI pipeline | REQ-007 |
| TF-016 – TF-018 (Processes) | Operator stateless design | ADR-002, ADR-003 |
| TF-019 – TF-020 (Port Binding) | Operator main.go, Provider gRPC | ADR-002 |
| TF-021 – TF-023 (Concurrency) | Leader election, Kubernetes Jobs | NFR-001, NFR-006 |
| TF-024 – TF-027 (Disposability) | Operator shutdown handler | NFR-001 |
| TF-028 – TF-030 (Dev/Prod Parity) | kind cluster, envtest | DR-007 |
| TF-031 – TF-033 (Logs) | log/slog, stdout | NFR-018, OR-011 |
| TF-034 – TF-035 (Admin Processes) | Makefile, standalone CLI | OR-001 |
