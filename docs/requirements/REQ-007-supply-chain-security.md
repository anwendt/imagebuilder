---
document-id: REQ-007
title: Supply Chain Security — SLSA Build Level 1 & 2
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
references:
  - "SLSA Specification v1.0 — https://slsa.dev/spec/v1.0/"
  - "SLSA Build Levels — https://slsa.dev/spec/v1.0/levels"
  - "NIST SP 800-218: Secure Software Development Framework (SSDF)"
  - "OpenSSF Scorecard — https://securityscorecards.dev"
  - "Sigstore / cosign — https://sigstore.dev"
---

# REQ-007 — Supply Chain Security (SLSA Build Level 1 & 2)

## 1. Purpose

This document defines the supply chain security requirements for the VM Image Builder system.
The target maturity level is **SLSA Build Level 2** (Supply chain Levels for Software Artefacts,
specification v1.0), which provides:

- **Level 1**: Build process is documented and produces provenance.
- **Level 2**: Build runs on a hosted, controlled build service with authenticated provenance.

Meeting SLSA L2 provides a verifiable, auditable chain of custody from source code to
deployable artefact, which is a key control objective for ISAE audits.

---

## 2. SLSA Build Level Requirements

### 2.1 SLSA Build Level 1 — Provenance Exists

Level 1 establishes that the build process is defined and produces a provenance document
describing how each artefact was built.

| ID | Requirement | SLSA Criterion | Priority |
|---|---|---|---|
| SC-001 | Every build of the operator container image SHALL produce a **provenance document** in SLSA provenance format (v1.0 `slsa.dev/provenance/v1`). | Provenance — Exists | Must |
| SC-002 | The provenance document SHALL include: builder identity, source repository URL, source commit SHA, build trigger (branch/tag/PR), build invocation ID, and a digest of each output artefact. | Provenance — Exists | Must |
| SC-003 | The build script (`Makefile` / CI workflow) SHALL be stored in the same repository as the source code and be version-controlled. | Build definition — Exists | Must |
| SC-004 | The provenance document SHALL be published alongside the container image in the OCI registry, attached as an OCI referrer (cosign attestation). | Provenance — Available | Must |
| SC-005 | The `go.sum` file SHALL be committed and verified by the build system, ensuring all Go module dependencies are cryptographically pinned. | Dependency pinning | Must |

### 2.2 SLSA Build Level 2 — Hosted Build, Authenticated Provenance

Level 2 requires that builds run on a hosted, controlled build service and that provenance
is authenticated (signed) by the build service itself.

| ID | Requirement | SLSA Criterion | Priority |
|---|---|---|---|
| SC-006 | Builds SHALL run exclusively on a **hosted CI/CD service** (GitHub Actions, GitLab CI, or equivalent) — never on developer laptops or unmanaged build servers. | Hosted build — Hosted | Must |
| SC-007 | The CI/CD platform SHALL use **ephemeral, isolated build environments** (fresh VM or container per build job). Build state does not persist between jobs. | Hosted build — Isolated | Must |
| SC-008 | The provenance document SHALL be **signed by the build service** using a non-forgeable identity (e.g., GitHub Actions OIDC token → Sigstore/cosign keyless signing). The signing key is controlled by the CI platform, not by individuals. | Provenance — Authenticated | Must |
| SC-009 | The signed provenance SHALL be verifiable by consumers using: `cosign verify-attestation --type slsaprovenance <image>`. | Provenance — Authenticated | Must |
| SC-010 | **No human** SHALL have write access to the production container registry that bypasses the CI/CD pipeline. Images are pushed only by the build service identity. | Isolated build | Must |
| SC-011 | Build workflows SHALL be protected by **branch protection rules**: direct pushes to `main` are prohibited; all changes require a pull request with at least one approving review. | Source integrity | Must |
| SC-012 | CI workflow files (`.github/workflows/*.yml` or equivalent) SHALL be reviewed as part of the pull request process. Workflow changes require the same approval as source changes. | Build definition — Verified | Must |

---

## 3. Source Integrity Requirements

| ID | Requirement | Priority |
|---|---|---|
| SC-013 | The `main` branch SHALL have **branch protection** enabled: require pull request, require status checks to pass, no force-push, no branch deletion. | Must |
| SC-014 | All commits to `main` SHALL be traceable to an authenticated GitHub/GitLab identity. Anonymous commits are rejected. | Must |
| SC-014A | Git repositories used as provisioner sources SHOULD be protected with branch protection, signed commits or signed tags, and immutable commit refs in production VMImage manifests. | Should |
| SC-014B | Credentials for private Git provisioner repositories SHALL be scoped to read-only access for the required repository or path and SHOULD be short-lived where supported. | Should |
| SC-015 | **Signed commits** (GPG or SSH) are recommended for all core contributors; required for release commits. | Should |
| SC-016 | Git tags used for releases SHALL be **annotated and signed** (`git tag -s vX.Y.Z`). The tag signature identifies the release manager. | Must |

---

## 4. Dependency Integrity Requirements

| ID | Requirement | Priority |
|---|---|---|
| SC-017 | All Go module dependencies SHALL be pinned to a specific version in `go.mod`. No `latest` or floating version specifiers. | Must |
| SC-018 | `go.sum` SHALL be committed and SHALL NOT be manually edited. The CI pipeline verifies `go.sum` consistency (`go mod verify`). | Must |
| SC-019 | Dependency updates SHALL be proposed via automated PRs (Dependabot or Renovate Bot) and reviewed before merging. Ad-hoc manual dependency updates are prohibited on `main`. | Must |
| SC-020 | All container base images SHALL be pinned by **digest** (`FROM gcr.io/distroless/static@sha256:...`) in production Dockerfiles. Mutable tags (`latest`, `stable`) are prohibited for production builds. | Must |
| SC-021 | PlatformProvider OCI images referenced in `PlatformProvider` CRDs SHALL be pinned by digest in production ProviderConfig resources (see also SR-016 in REQ-004). | Must |
| SC-022 | An **OpenSSF Scorecard** scan SHALL be run weekly on the repository. Results SHALL be published to the OpenSSF API and a badge displayed in the repository README. | Should |

---

## 5. Artefact Integrity Requirements

| ID | Requirement | Priority |
|---|---|---|
| SC-023 | Every release container image SHALL have its **SHA-256 digest** recorded in the release notes and provenance document. | Must |
| SC-024 | Container images SHALL be **signed with cosign** (keyless signing via Sigstore Fulcio CA) as part of the CI pipeline. The signing event is logged to the Sigstore Rekor transparency log. | Must |
| SC-025 | The SLSA provenance attestation SHALL be attached to the image in the OCI registry as a referrer (`cosign attest`). | Must |
| SC-026 | Platform providers consuming the operator SDK or implementing the gRPC interface SHALL publish their own signed provenance. The core operator's admission process SHOULD verify provider image signatures before loading. | Should |
| SC-027 | Release artefacts (SBOM, provenance, signatures) SHALL be retained for a minimum of **3 years** in the artefact registry or a designated long-term storage location. | Must |

---

## 6. Software Bill of Materials (SBOM)

| ID | Requirement | Priority |
|---|---|---|
| SC-028 | A **Software Bill of Materials (SBOM)** SHALL be generated for every release in **SPDX 2.3** or **CycloneDX 1.5** format. | Must |
| SC-029 | The SBOM SHALL include all Go module dependencies with their version, license, and package URL (PURL). | Must |
| SC-030 | The SBOM SHALL be generated by `syft` or `trivy` as part of the CI release pipeline and attached to the OCI image as a referrer. | Must |
| SC-031 | The SBOM SHALL be scanned for known CVEs using `grype` or `trivy`. Builds with **Critical** or **High** severity CVEs (CVSS ≥ 7.0) in direct dependencies SHALL be blocked. | Must |
| SC-032 | The CVE scan report SHALL be archived per release and available for ISAE audit review. | Must |

---

## 7. Build Pipeline Security

| ID | Requirement | Priority |
|---|---|---|
| SC-033 | CI pipeline secrets (registry credentials, signing keys) SHALL be stored as **encrypted CI/CD secrets** managed by the CI platform, never in repository files. | Must |
| SC-034 | CI workflows SHALL use **pinned action versions by commit SHA** (e.g., `uses: actions/checkout@<sha>`), not mutable tags. | Must |
| SC-035 | CI pipeline steps SHALL follow **least privilege**: each step runs with only the permissions required for that step (no wildcard `permissions: write-all`). | Must |
| SC-036 | The CI pipeline SHALL produce an audit log of every build (inputs, steps, outputs, timing) retained for a minimum of **90 days**. | Must |
| SC-037 | Third-party GitHub Actions / CI plugins SHALL be vetted for license and security before adoption. The allowed list is maintained in `.github/allowed-actions.yml` or equivalent. | Should |

---

## 8. Vulnerability Management

| ID | Requirement | Priority |
|---|---|---|
| SC-038 | Automated CVE scanning SHALL run on every pull request and on a daily schedule against the latest published container image. | Must |
| SC-039 | Critical (CVSS ≥ 9.0) vulnerabilities in direct dependencies SHALL be remediated within **7 calendar days** of disclosure. | Must |
| SC-040 | High (CVSS 7.0–8.9) vulnerabilities in direct dependencies SHALL be remediated within **30 calendar days** of disclosure. | Must |
| SC-041 | Transitive dependency vulnerabilities SHALL be reviewed and remediated on a **best-effort basis** within 90 days. | Should |
| SC-042 | All accepted vulnerability exceptions (false positives, risk acceptances) SHALL be documented in `.vulnignore` or equivalent, with a justification and expiry date. | Must |

---

## 9. SLSA Compliance Evidence

The following artefacts constitute evidence of SLSA Level 2 compliance for ISAE audit:

| Evidence | Location | SLSA Level |
|---|---|---|
| Signed container image | OCI registry (`ghcr.io/anwendt/imagebuilder`) | L1, L2 |
| SLSA provenance attestation | OCI registry (referrer) | L1, L2 |
| Sigstore Rekor log entry | `rekor.sigstore.dev` | L2 |
| CI build logs | CI platform (retained 90 days) | L1, L2 |
| SBOM (SPDX/CycloneDX) | OCI registry (referrer) + release assets | L1 |
| CVE scan report | Release assets | L1 |
| Branch protection configuration | GitHub repository settings | L2 |
| Signed git tag | Git repository | L2 |
| `go.sum` | Repository root | L1 |
| `go-licenses` report (`NOTICE`) | Repository root + release | L1 |

---

## 10. Traceability Matrix

| Requirement Group | Architecture Component | Related REQ |
|---|---|---|
| SC-001 – SC-005 (SLSA L1) | CI pipeline, OCI registry | REQ-003, REQ-004 |
| SC-006 – SC-012 (SLSA L2) | GitHub Actions / CI platform | REQ-006 (TF-012–TF-015) |
| SC-013 – SC-016 (Source integrity) | Git repository, branch protection | REQ-004 (SR-019) |
| SC-017 – SC-022 (Dependency integrity) | go.mod, go.sum, Dependabot | REQ-003 (LR-005, LR-006) |
| SC-023 – SC-027 (Artefact integrity) | OCI registry, cosign | REQ-004 (SR-016, SR-017) |
| SC-028 – SC-032 (SBOM) | syft/trivy, OCI registry | REQ-003 (LR-006) |
| SC-033 – SC-037 (Pipeline security) | CI secrets, workflow files | REQ-004 (SR-033) |
| SC-038 – SC-042 (Vulnerability mgmt) | grype/trivy, CI | REQ-004 (SR-019) |
