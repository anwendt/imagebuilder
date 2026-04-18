---
document-id: REQ-009
title: Software Assurance Maturity — OWASP SAMM v2.0
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
references:
  - "OWASP SAMM v2.0 — https://owaspsamm.org/model/"
  - "OWASP SAMM Benchmark — https://owaspsamm.org/benchmarking/"
---

# REQ-009 — Software Assurance Maturity (OWASP SAMM v2.0)

## 1. Purpose

This document maps the VM Image Builder project to the
**OWASP Software Assurance Maturity Model (SAMM) v2.0**.
SAMM defines a measurable, risk-driven framework for integrating security across the
entire software development lifecycle. It covers five business functions, each with
three security practices and three maturity levels (L0–L3).

This document defines the **target maturity level** for each practice, provides
verifiable requirements, and serves as an ISAE audit reference.

### Target Maturity Profile

| Business Function | Target Level |
|---|---|
| Governance | Level 1 across all practices |
| Design | Level 2 for Threat Assessment and Security Architecture; Level 1 for Security Requirements |
| Implementation | Level 2 for Secure Build and Defect Management; Level 1 for Secure Deployment |
| Verification | Level 2 for Requirements-driven Testing and Security Testing; Level 1 for Architecture Assessment |
| Operations | Level 1 across all practices |

---

## 2. Governance

### 2.1 Strategy & Metrics (G-SM)

*Establish and maintain a security programme aligned to business objectives.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-G-SM-01 | Platform Engineering SHALL maintain a written security strategy document that identifies the top security risks for the VM Image Builder and defines mitigation objectives. | L1 | Must |
| SAMM-G-SM-02 | Security activities SHALL be tracked as regular backlog items, not as one-off tasks. At least one security-focused sprint item SHALL appear in each release cycle. | L1 | Must |
| SAMM-G-SM-03 | Key security metrics (open CVEs, SLSA compliance status, test coverage, SAST finding count) SHALL be reviewed monthly by the team. | L1 | Must |

### 2.2 Policy & Compliance (G-PC)

*Define security policies and ensure compliance with external regulations.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-G-PC-01 | The security and license requirements defined in REQ-003, REQ-004, REQ-007, REQ-008 collectively constitute the project security policy. They SHALL be reviewed and updated at least annually. | L1 | Must |
| SAMM-G-PC-02 | Compliance with the Apache 2.0 license constraint (REQ-003) SHALL be verified automatically in CI (`go-licenses check`) and reviewed at each release gate. | L1 | Must |
| SAMM-G-PC-03 | Any deviation from a stated requirement SHALL be documented as an accepted risk with justification, owner, and expiry date, and reviewed at the next release. | L1 | Must |

### 2.3 Education & Guidance (G-EG)

*Increase security awareness and provide training to development teams.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-G-EG-01 | All contributors SHALL complete basic secure coding training covering OWASP Top 10 and Go-specific security pitfalls before their first merged pull request. | L1 | Must |
| SAMM-G-EG-02 | Security requirements (REQ-004, REQ-008) SHALL be part of the project onboarding documentation accessible to all contributors. | L1 | Must |
| SAMM-G-EG-03 | A security-focused retrospective item SHALL be included in post-mortems for any production security incident or Critical CVE. | L1 | Should |

---

## 3. Design

### 3.1 Threat Assessment (D-TA)

*Identify and evaluate security threats to the system.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-D-TA-01 | A **threat model** SHALL be created and maintained for the VM Image Builder using STRIDE or an equivalent structured method. The threat model SHALL cover: operator attack surface, provider pod communication, build job isolation, credential access paths, and supply chain risks. | L1 | Must |
| SAMM-D-TA-02 | The threat model SHALL be stored in `docs/architecture/THREAT-MODEL.md` and reviewed at each major feature addition or architectural change. | L1 | Must |
| SAMM-D-TA-03 | Identified threats SHALL each have a corresponding mitigation mapped to a requirement in REQ-004 or REQ-008. Unmitigated threats SHALL be documented as accepted risks (SAMM-G-PC-03). | L2 | Must |
| SAMM-D-TA-04 | New feature proposals (pull request descriptions) SHALL include a brief security impact assessment: "Does this change expose a new attack surface? Does it handle untrusted input?" | L2 | Should |

### 3.2 Security Requirements (D-SR)

*Derive security requirements from business and regulatory needs.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-D-SR-01 | Security requirements (this document and REQ-004, REQ-007, REQ-008) SHALL be available to all contributors and linked from the project `CONTRIBUTING.md`. | L1 | Must |
| SAMM-D-SR-02 | Acceptance criteria for pull requests SHALL explicitly include: "Does this change satisfy or conflict with any requirement in REQ-004, REQ-007, REQ-008, or REQ-009?" | L1 | Should |
| SAMM-D-SR-03 | User-facing API fields in CRD specs SHALL have security requirements derived from threat model findings and documented in the CRD schema as validation rules. | L1 | Must |

### 3.3 Security Architecture (D-SA)

*Define and promote secure design patterns and architectural principles.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-D-SA-01 | The architecture principles defined in `docs/architecture/ARCHITECTURE.md` Section 2 (P-01 to P-08) are the authoritative secure design patterns. They SHALL be applied to all new components without exception. | L2 | Must |
| SAMM-D-SA-02 | Reusable security patterns (credential injection via `secretRef`, Unix-socket gRPC, init-container isolation) SHALL be documented in `docs/architecture/` as reference implementations for contributors. | L2 | Should |
| SAMM-D-SA-03 | Architecture Decision Records (ADRs) SHALL be created for any security-relevant design decision that deviates from the established patterns. | L2 | Must |

---

## 4. Implementation

### 4.1 Secure Build (I-SB)

*Ensure the build process produces secure, reproducible artefacts.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-I-SB-01 | The build process SHALL be fully automated. No manual steps are required between source commit and published container image. | L1 | Must |
| SAMM-I-SB-02 | The build SHALL be reproducible: the same source commit and `go.sum` SHALL produce a byte-identical binary (modulo timestamps). Reproducibility SHALL be verified via `go build -trimpath`. | L2 | Must |
| SAMM-I-SB-03 | Static analysis (`gosec`, `staticcheck`, `go vet`) SHALL run as a required CI gate. PRs cannot be merged if any finding is at `HIGH` severity or above. | L2 | Must |
| SAMM-I-SB-04 | SLSA Build Level 2 requirements (REQ-007, SC-006 – SC-012) constitute the secure build pipeline requirements. | L2 | Must |
| SAMM-I-SB-05 | Dependency integrity SHALL be verified at build time: `go mod verify` ensures no module has been tampered with since `go.sum` was written. | L2 | Must |

### 4.2 Secure Deployment (I-SD)

*Ensure deployments are secure and auditable.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-I-SD-01 | Deployment to production SHALL only occur from a tagged, CI-built, and signed container image. Manual `docker push` to the production registry is prohibited. | L1 | Must |
| SAMM-I-SD-02 | Kubernetes manifests SHALL be validated by `kube-linter` and `kubectl --dry-run=server` before applying to production. | L1 | Must |
| SAMM-I-SD-03 | The deployment pipeline SHALL verify the cosign signature and SLSA provenance of the container image before deploying (REQ-007, SC-040). | L1 | Must |

### 4.3 Defect Management (I-DM)

*Track and remediate security defects systematically.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-I-DM-01 | All security findings (SAST, CVE scan, penetration test, threat model) SHALL be tracked as issues in the project issue tracker with a `security` label. | L1 | Must |
| SAMM-I-DM-02 | Security issues SHALL be classified by severity (Critical / High / Medium / Low) and remediated within the SLAs defined in REQ-007 (SC-039 – SC-041). | L2 | Must |
| SAMM-I-DM-03 | Closed security issues SHALL include a root-cause note and a reference to the fix commit. Regression tests SHALL be added for each fixed vulnerability (DR-005 in REQ-006). | L2 | Must |
| SAMM-I-DM-04 | A vulnerability disclosure policy (`SECURITY.md`) SHALL be published in the repository root, describing how external researchers can report vulnerabilities. | L1 | Must |

---

## 5. Verification

### 5.1 Architecture Assessment (V-AA)

*Validate that the architecture meets security requirements.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-V-AA-01 | The architecture description (`docs/architecture/ARCHITECTURE.md`) SHALL be reviewed against the threat model (SAMM-D-TA-01) at least annually and before any major release. | L1 | Must |
| SAMM-V-AA-02 | ADRs SHALL be reviewed for security impact by at least one reviewer with security expertise before being accepted. | L1 | Should |

### 5.2 Requirements-driven Testing (V-RT)

*Verify that security requirements are tested.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-V-RT-01 | Each security requirement in REQ-004, REQ-007, REQ-008, and REQ-009 SHALL have at least one automated test or CI check that verifies the requirement is met. | L2 | Must |
| SAMM-V-RT-02 | The traceability matrices in all REQ documents SHALL be maintained: for each requirement, the implementing code path and verifying test SHALL be identifiable. | L2 | Must |
| SAMM-V-RT-03 | Security regression tests SHALL be added for every remediated vulnerability (linked to DR-005 in REQ-006). | L2 | Must |

### 5.3 Security Testing (V-ST)

*Perform security-specific testing activities.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-V-ST-01 | **SAST** (`gosec`, `staticcheck`) runs on every PR (AS-060 in REQ-008). | L1 | Must |
| SAMM-V-ST-02 | **Dependency scanning** (`govulncheck`, `trivy`) runs on every PR and daily (AS-061, AS-062 in REQ-008). | L2 | Must |
| SAMM-V-ST-03 | **Secret scanning** (`gitleaks`) runs on every commit (AS-063 in REQ-008). | L2 | Must |
| SAMM-V-ST-04 | **Penetration testing** is conducted before the first production release and annually thereafter (AS-065 in REQ-008). Findings are tracked per SAMM-I-DM-02. | L2 | Should |
| SAMM-V-ST-05 | **Container image scanning** runs as part of the image build CI step (AS-062 in REQ-008). | L2 | Must |

---

## 6. Operations

### 6.1 Incident Management (O-IM)

*Detect, respond to, and learn from security incidents.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-O-IM-01 | A **Security Incident Response Plan** SHALL be documented describing: detection (alerting rules from OR-010), triage, escalation path, containment, and post-mortem process. | L1 | Must |
| SAMM-O-IM-02 | Alerting rules (OR-010 in REQ-005) SHALL trigger on security-relevant events: repeated build failures, provider health transitions, signature verification failures. | L1 | Must |
| SAMM-O-IM-03 | Post-mortems for security incidents SHALL produce at least one new requirement or ADR that prevents recurrence. | L1 | Should |

### 6.2 Environment Management (O-EM)

*Maintain secure, consistent runtime environments.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-O-EM-01 | The production Kubernetes cluster SHALL have the CIS Kubernetes Benchmark hardening recommendations applied and verified before deploying the operator. | L1 | Must |
| SAMM-O-EM-02 | Build node configuration (QEMU, libvirtd) SHALL be managed by infrastructure-as-code (Ansible, Terraform, or equivalent). Manual node configuration is prohibited in production. | L1 | Must |
| SAMM-O-EM-03 | Cluster-level security controls (PSP/PSA, NetworkPolicy, EncryptionConfiguration) SHALL be reviewed at each Kubernetes version upgrade. | L1 | Must |

### 6.3 Operational Management (O-OM)

*Ensure ongoing operational security health.*

| ID | Requirement | SAMM Level | Priority |
|---|---|---|---|
| SAMM-O-OM-01 | Security metrics (open CVEs, SLSA status, SAST finding trend) SHALL be included in regular operational reviews. | L1 | Must |
| SAMM-O-OM-02 | The `SECURITY.md` vulnerability disclosure policy SHALL be reviewed and updated annually. | L1 | Must |
| SAMM-O-OM-03 | Decommissioned provider images (old PlatformProvider versions) SHALL be removed from the registry and from cluster deployments within 30 days of replacement. | L1 | Should |

---

## 7. SAMM Maturity Scorecard

| Business Function | Practice | Current | Target |
|---|---|---|---|
| Governance | Strategy & Metrics | L0 | L1 |
| Governance | Policy & Compliance | L0 | L1 |
| Governance | Education & Guidance | L0 | L1 |
| Design | Threat Assessment | L0 | L2 |
| Design | Security Requirements | L0 | L1 |
| Design | Security Architecture | L0 | L2 |
| Implementation | Secure Build | L0 | L2 |
| Implementation | Secure Deployment | L0 | L1 |
| Implementation | Defect Management | L0 | L2 |
| Verification | Architecture Assessment | L0 | L1 |
| Verification | Requirements-driven Testing | L0 | L2 |
| Verification | Security Testing | L0 | L2 |
| Operations | Incident Management | L0 | L1 |
| Operations | Environment Management | L0 | L1 |
| Operations | Operational Management | L0 | L1 |

*Current = L0 (project is in initial development phase). Target to be achieved before first production release.*

---

## 8. Traceability Matrix

| SAMM Practice | Related REQ Documents | ADR |
|---|---|---|
| Governance — Strategy & Metrics | REQ-003, REQ-004, REQ-007 | — |
| Governance — Policy & Compliance | REQ-003 (LR), REQ-004 (SR) | ADR-001 |
| Governance — Education & Guidance | REQ-006 (DR), REQ-008 (AS) | — |
| Design — Threat Assessment | REQ-008 (AS-019), ARCH-001 | — |
| Design — Security Requirements | REQ-004, REQ-008 | — |
| Design — Security Architecture | ARCH-001, REQ-004 | ADR-001 – ADR-006 |
| Implementation — Secure Build | REQ-007 (SC-001 – SC-012) | ADR-004 |
| Implementation — Secure Deployment | REQ-007 (SC-023 – SC-027) | — |
| Implementation — Defect Management | REQ-007 (SC-038 – SC-042), REQ-008 | — |
| Verification — Architecture Assessment | ARCH-001, ADR index | — |
| Verification — Requirements-driven Testing | REQ-006 (DR), all REQ | — |
| Verification — Security Testing | REQ-008 (AS-060 – AS-065) | — |
| Operations — Incident Management | REQ-005 (OR-007 – OR-010) | — |
| Operations — Environment Management | REQ-004 (SR-011 – SR-015) | ADR-003 |
| Operations — Operational Management | REQ-005 (OR-018 – OR-021) | — |
