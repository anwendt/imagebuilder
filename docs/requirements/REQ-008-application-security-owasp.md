---
document-id: REQ-008
title: Application Security — OWASP Principles
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
references:
  - "OWASP Top 10 2021 — https://owasp.org/Top10/"
  - "OWASP Application Security Verification Standard (ASVS) v4.0.3"
  - "OWASP Kubernetes Security Cheat Sheet"
  - "OWASP Docker Security Cheat Sheet"
  - "CIS Kubernetes Benchmark v1.9"
---

# REQ-008 — Application Security (OWASP Principles)

## 1. Purpose

This document defines the application security requirements for the VM Image Builder system,
structured around the **OWASP Top 10 (2021)** and the **OWASP Application Security
Verification Standard (ASVS) v4.0.3**, adapted to the Kubernetes-native, Go-based context
of this operator.

These requirements extend the baseline security controls defined in REQ-004 and are
intended to be verifiable through code review, automated scanning, and ISAE audit.

---

## 2. OWASP Top 10 — Control Mapping

### A01:2021 — Broken Access Control

*Ranked #1. Moving up from #5. 94% of applications tested for some form of broken access control.*

| ID | Requirement | Priority |
|---|---|---|
| AS-001 | The operator SHALL enforce namespace-scoped access to VMImage and ProviderConfig resources. Users in namespace A cannot read or trigger builds in namespace B. | Must |
| AS-002 | Kubernetes RBAC SHALL be the sole access control mechanism. No application-level access control that could be bypassed via direct API calls. | Must |
| AS-003 | Provider pods SHALL have no Kubernetes API access (no ServiceAccount token mounted, or token with zero permissions). | Must |
| AS-004 | Build job pods SHALL have no Kubernetes API access unless explicitly required and documented. | Must |
| AS-005 | The operator SHALL validate that a `ProviderConfig` referenced by a `VMImage` is in the **same namespace** as the `VMImage`, preventing cross-namespace credential access. | Must |
| AS-006 | All RBAC roles SHALL be periodically reviewed (minimum: annually) and any permissions not actively used SHALL be removed. | Must |

### A02:2021 — Cryptographic Failures

*Previously "Sensitive Data Exposure". Focus on failures related to cryptography.*

| ID | Requirement | Priority |
|---|---|---|
| AS-007 | Credentials, tokens, and API keys SHALL NEVER appear in: log output, Kubernetes events, VMImage status fields, error messages returned to the API, or environment variables visible in pod specs. | Must |
| AS-008 | All communication with cloud provider APIs SHALL use TLS 1.2 or higher. TLS 1.0 and 1.1 are prohibited. | Must |
| AS-009 | The gRPC channel between operator and provider pods SHALL be protected according to ADR-002: ClusterIP-only TCP plus NetworkPolicy within the local trust boundary, and mTLS when provider endpoints cross namespace, cluster, or network trust boundaries. | Must |
| AS-010 | Source image checksums SHALL use SHA-256 or stronger. MD5 and SHA-1 checksums are rejected as insecure. | Must |
| AS-011 | Container image signatures SHALL use ECDSA with P-256 or Ed25519 keys (via cosign). RSA-2048 is the minimum if ECDSA is unavailable. | Must |
| AS-012 | Kubernetes Secrets SHALL be encrypted at rest in etcd (Kubernetes `EncryptionConfiguration` with AES-256-GCM or KMS provider). This is a cluster configuration requirement, documented as an operational dependency. | Must |

### A03:2021 — Injection

*Includes XSS, SQL injection, command injection, and other injection flaws.*

| ID | Requirement | Priority |
|---|---|---|
| AS-013 | All user-supplied values from `VMImage` spec fields (URLs, script content, argument strings, environment variable values) SHALL be treated as **untrusted input** and validated against a strict schema before use. | Must |
| AS-014 | Shell script content in `spec.provisioners[].inline` or loaded from `spec.provisioners[].source.git` SHALL NOT be executed by the operator process directly. It is expanded into the build/provider workspace and executed only by the selected provisioner runtime. | Must |
| AS-015 | URLs in `spec.source.url` and `spec.provisioners[].source.git.url` SHALL be validated as well-formed HTTPS URLs. HTTP (unencrypted) URLs are rejected. | Must |
| AS-015A | Credentials for private `spec.provisioners[].source.git.url` repositories SHALL be supplied through `auth.secretRef`; embedding credentials in URLs is prohibited. | Must |
| AS-016 | Arguments passed to init-container provisioners (`spec.provisioners[].args[]`) SHALL be written to the provisioner config as a JSON array. Provisioner runtimes that translate those arguments into remote shell commands SHALL quote each argument independently before execution. | Must |
| AS-017 | The operator SHALL NOT use `os/exec` with shell interpretation (`exec.Command("sh", "-c", userInput)`). All subprocess invocations use the array form with no shell expansion. | Must |
| AS-018 | `go vet`, `staticcheck`, and `gosec` SHALL be run in CI to detect injection vulnerabilities, unsafe use of `os/exec`, and Go-specific security anti-patterns. | Must |

### A04:2021 — Insecure Design

*New category. Focus on design flaws, not implementation bugs.*

| ID | Requirement | Priority |
|---|---|---|
| AS-019 | Threat modelling SHALL be conducted before implementing each major feature (new provider, new provisioner type, new API field). The threat model SHALL be documented and reviewed. | Must |
| AS-020 | The principle of **fail-secure** SHALL apply: if the operator cannot verify a provider's identity (gRPC handshake fails) or a source image's integrity (checksum mismatch), the build MUST be aborted. | Must |
| AS-021 | The operator SHALL implement **rate limiting** on VMImage build admission to prevent resource exhaustion (denial of service via excessive build requests). | Should |
| AS-022 | Build isolation SHALL be enforced: one VMImage build SHALL NOT be able to access the workspace volume of another concurrent build. Each build Job uses a unique `emptyDir` volume. | Must |
| AS-023 | The init-container filesystem contract under `/workspace/provisioners/step-N/` SHALL use restrictive file permissions. `config.json` is written with mode `0600` (owner read/write only). | Must |

### A05:2021 — Security Misconfiguration

*Including XML external entities (XXE) and other misconfigurations.*

| ID | Requirement | Priority |
|---|---|---|
| AS-024 | Container images SHALL NOT contain default credentials, sample configurations, or unnecessary open ports. | Must |
| AS-025 | All Kubernetes manifests (Deployment, Job, Pod templates) SHALL have explicit `securityContext` settings: `runAsNonRoot: true`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`. | Must |
| AS-026 | The operator SHALL use a **Validating Admission Webhook** (kubebuilder) to reject `VMImage` resources that fail server-side validation. Client-side validation alone is insufficient. | Must |
| AS-027 | CRD schemas SHALL define `additionalProperties: false` where applicable to prevent arbitrary unknown fields from being accepted and forwarded. | Must |
| AS-028 | Default Kubernetes service account tokens SHALL be disabled (`automountServiceAccountToken: false`) for build job pods and provider pods that do not require Kubernetes API access. | Must |
| AS-029 | A `NetworkPolicy` SHALL restrict ingress and egress for the operator namespace to only the required endpoints (Kubernetes API, cloud provider endpoints, and provider gRPC ClusterIP Services). | Should |

### A06:2021 — Vulnerable and Outdated Components

| ID | Requirement | Priority |
|---|---|---|
| AS-030 | Automated dependency scanning (Dependabot / Renovate) SHALL create pull requests for dependency updates within **24 hours** of a new release. | Must |
| AS-031 | The container base image SHALL be updated on a regular cycle (minimum: monthly) or immediately upon a Critical CVE in the base OS packages. | Must |
| AS-032 | `trivy image` or `grype` SHALL be run against the built container image in CI and SHALL block the build on Critical/High severity CVEs (CVSS ≥ 7.0) in direct packages. | Must |
| AS-033 | `go list -m all` vulnerability scanning via `govulncheck` SHALL be part of the CI pipeline to detect Go-specific CVEs in the module dependency graph. | Must |

### A07:2021 — Identification and Authentication Failures

| ID | Requirement | Priority |
|---|---|---|
| AS-034 | The operator SHALL authenticate to the Kubernetes API using the pod's **ServiceAccount token** (short-lived, automatically rotated by Kubernetes). Static long-lived kubeconfig credentials are prohibited in production. | Must |
| AS-035 | Cloud provider authentication SHALL use **short-lived credentials** where the platform supports it (AWS IAM Roles for Service Accounts, Azure Workload Identity, GCP Workload Identity Federation). Long-lived API keys are the fallback only when workload identity is unavailable. | Should |
| AS-036 | The gRPC provider interface SHALL support mutual authentication (mTLS) for TCP-based communication that crosses namespace, cluster, or network trust boundaries. The provider identity is established by its TLS certificate, not by a shared secret. | Must |
| AS-037 | All Kubernetes Secrets referenced by ProviderConfig SHALL have an expiry policy enforced by the external secret management system (e.g., Vault lease TTL). Perpetual/non-expiring credentials are prohibited. | Should |

### A08:2021 — Software and Data Integrity Failures

*New category. Includes CI/CD pipeline integrity.*

| ID | Requirement | Priority |
|---|---|---|
| AS-038 | The build pipeline SHALL enforce **integrity verification** of all downloaded dependencies and base images (go.sum, container digest pinning) before use. | Must |
| AS-039 | CI workflow files SHALL be protected against modification by untrusted contributors (GitHub Actions: `pull_request_target` with explicit permission scoping). | Must |
| AS-040 | The SLSA provenance attestation (REQ-007) is the primary control for software integrity. It SHALL be verified by the deployment pipeline before deploying a new operator version. | Must |
| AS-041 | Deserialisation of user-supplied YAML/JSON (VMImage spec) SHALL use strict schema validation (CRD OpenAPI schema) and SHALL NOT use `interface{}` unmarshal paths that bypass type safety. | Must |

### A09:2021 — Security Logging and Monitoring Failures

| ID | Requirement | Priority |
|---|---|---|
| AS-042 | The operator SHALL log all security-relevant events at `WARN` or `ERROR` level: authentication failures, authorisation denials, checksum mismatches, signature verification failures, unexpected pod terminations. | Must |
| AS-043 | Kubernetes audit logging SHALL be enabled at the cluster level with a policy that captures `ResponseComplete` stage for all write operations on `VMImage`, `PlatformProvider`, `ProviderConfig`, and `Secret` resources. | Must |
| AS-044 | Logs SHALL NOT contain sensitive data (credentials, private keys, checksums of secrets). Structured logging fields SHALL be reviewed for accidental secret inclusion. | Must |
| AS-045 | An alerting rule SHALL be defined for repeated build failures (> 5 failures in 1 hour) and for provider health state transitions (Healthy → Unhealthy), as these may indicate an attack or misconfiguration. | Should |
| AS-046 | Log retention SHALL comply with the organisational policy (minimum 90 days operational, 1 year for audit) per OR-028. | Must |

### A10:2021 — Server-Side Request Forgery (SSRF)

| ID | Requirement | Priority |
|---|---|---|
| AS-047 | The operator SHALL validate `spec.source.url` and `spec.provisioners[].source.git.url` against an allowlist of approved URL schemes (`https://` only) and, optionally, an allowlist of approved hostnames or CIDR ranges. | Must |
| AS-048 | The operator SHALL NOT proxy or forward arbitrary URLs on behalf of users. Source image downloads are performed by the build job pod with network egress restricted by `NetworkPolicy`. | Must |
| AS-049 | Cloud provider API endpoints in `ProviderConfig.spec.endpoint` SHALL be validated to prevent SSRF via internal metadata service addresses (e.g., `169.254.169.254`, `fd00:ec2::254`). | Must |
| AS-049A | Git-backed provisioner sources SHALL be revalidated at runtime before clone/checkout. Runtime validation SHALL fail closed on DNS failures or blocked address ranges. | Must |
| AS-049B | Git-backed provisioner authentication data SHALL be read from Secret-mounted files for local builds or from transient provider request fields for remote builds. It SHALL NOT be written to logs, status, or generated provisioner content. | Must |

---

## 3. Additional OWASP Controls

### 3.1 OWASP Kubernetes Security Cheat Sheet

| ID | Requirement | Priority |
|---|---|---|
| AS-050 | Pod Security Standards (PSS) `restricted` profile SHALL be enforced in the `imagebuilder-system` namespace via a Kubernetes admission policy. | Must |
| AS-051 | Resource quotas (`ResourceQuota`) and limit ranges (`LimitRange`) SHALL be defined for the `imagebuilder-system` namespace to prevent resource exhaustion. | Must |
| AS-052 | CRD validation webhooks SHALL use `failurePolicy: Fail` — if the webhook is unavailable, resource creation is denied. | Must |
| AS-053 | The operator SHALL not use `hostNetwork`, `hostPID`, or `hostIPC` in any pod spec. | Must |
| AS-054 | `HostPath` volumes SHALL NOT be used in operator or provider pod specs. Build job pods may use `hostPath` only for `/dev/kvm` access on dedicated build nodes, with explicit documentation of the security rationale. | Must |

### 3.2 OWASP Docker Security Cheat Sheet

| ID | Requirement | Priority |
|---|---|---|
| AS-055 | Container images SHALL be built from a minimal base (`gcr.io/distroless/static` or `scratch` for Go binaries). General-purpose OS images (`ubuntu`, `debian`) are prohibited for production images. | Must |
| AS-056 | The `USER` instruction SHALL be set in all Dockerfiles to a non-root, non-zero UID. | Must |
| AS-057 | `COPY --chown` SHALL be used for all file copy operations in Dockerfiles. | Must |
| AS-058 | Multi-stage builds SHALL be used to exclude build tooling (Go compiler, protoc) from the final runtime image. | Must |
| AS-059 | The final image layer SHALL contain only the operator binary and required CA certificates. | Must |

---

## 4. Security Testing Requirements

| ID | Requirement | Tooling | Priority |
|---|---|---|---|
| AS-060 | **Static Application Security Testing (SAST)** SHALL run on every PR. | `gosec`, `staticcheck` | Must |
| AS-061 | **Dependency vulnerability scanning** SHALL run on every PR and on a daily schedule. | `govulncheck`, `trivy` | Must |
| AS-062 | **Container image scanning** SHALL run as part of the container build step. | `trivy image`, `grype` | Must |
| AS-063 | **Secret scanning** SHALL run on every commit to detect accidentally committed credentials. | `gitleaks`, GitHub Secret Scanning | Must |
| AS-064 | **Infrastructure-as-Code scanning** SHALL run on Kubernetes manifest changes. | `kube-linter`, `checkov` | Should |
| AS-065 | **Penetration testing** SHALL be conducted before the first production release and annually thereafter by an independent team. Findings SHALL be remediated before release. | Manual | Should |

---

## 5. Traceability Matrix

| Requirement Group | OWASP Category | Related REQ | ADR |
|---|---|---|---|
| AS-001 – AS-006 | A01 Broken Access Control | REQ-004 (SR-006–SR-010) | — |
| AS-007 – AS-012 | A02 Cryptographic Failures | REQ-004 (SR-001–SR-005) | ADR-002 |
| AS-013 – AS-018 | A03 Injection | REQ-006 (DR-018) | — |
| AS-019 – AS-023 | A04 Insecure Design | REQ-004, REQ-006 | ADR-003 |
| AS-024 – AS-029 | A05 Security Misconfiguration | REQ-004 (SR-011–SR-015) | ADR-004 |
| AS-030 – AS-033 | A06 Vulnerable Components | REQ-007 (SC-038–SC-042) | — |
| AS-034 – AS-037 | A07 Auth Failures | REQ-004 (SR-001–SR-005) | — |
| AS-038 – AS-041 | A08 Integrity Failures | REQ-007 (SC-023–SC-027) | ADR-005 |
| AS-042 – AS-046 | A09 Logging & Monitoring | REQ-004 (SR-025–SR-028) | — |
| AS-047 – AS-049 | A10 SSRF | REQ-001 (FR-015) | — |
| AS-050 – AS-059 | Kubernetes/Docker Security | REQ-004 (SR-011–SR-015) | ADR-004 |
| AS-060 – AS-065 | Security Testing | REQ-006 (DR-021–DR-023) | — |
