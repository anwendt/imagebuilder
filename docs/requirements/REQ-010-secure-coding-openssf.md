---
document-id: REQ-010
title: Secure Coding Standards (SEI CERT) & OpenSSF Best Practices
version: 1.0.0
status: Draft
date: 2026-04-18
author: Platform Engineering
classification: Internal
references:
  - "SEI CERT Coding Standards — https://wiki.sei.cmu.edu/confluence/display/seccode"
  - "CERT Go Secure Coding Practices (community mapping) — https://github.com/securego/gosec"
  - "OpenSSF Best Practices Badge — https://www.bestpractices.dev"
  - "OpenSSF Scorecard — https://securityscorecards.dev"
  - "OpenSSF SLSA — https://slsa.dev (see REQ-007)"
  - "OpenSSF Sigstore — https://sigstore.dev (see REQ-007)"
---

# REQ-010 — Secure Coding Standards (SEI CERT) & OpenSSF Best Practices

## 1. Purpose

This document defines two complementary sets of requirements:

1. **SEI CERT Secure Coding Standards** (adapted for Go) — low-level, rule-based
   requirements for writing secure Go code. SEI CERT is published by the Software
   Engineering Institute at Carnegie Mellon University and provides the most widely
   cited language-level secure coding guidance.

2. **OpenSSF Best Practices** — project-level requirements derived from the
   **OpenSSF Best Practices Badge Program** and the **OpenSSF Scorecard**, covering
   source code management, vulnerability handling, build quality, and supply chain
   security for open-source (and internal) projects.

Together with REQ-006 (TDD, Twelve-Factor), REQ-007 (SLSA), REQ-008 (OWASP Top 10),
and REQ-009 (SAMM), this document completes the Development Standards baseline.

---

## 2. SEI CERT Secure Coding — Go Adaptation

The SEI CERT Coding Standards are organised by category. The categories below are
mapped to Go-specific constructs and enforced by automated tooling where possible.

### 2.1 ERR — Error Handling

Improper error handling is the most common source of security vulnerabilities in Go.

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-ERR-01 | ERR00-J / analogous | Every function that returns an `error` SHALL have its error checked by the caller. Blank-identifier discards (`_ = f()`) of error values are prohibited in production code. | `errcheck`, `staticcheck` | Must |
| CERT-ERR-02 | ERR01-J | Errors SHALL be wrapped with context using `fmt.Errorf("operation: %w", err)`. Bare `return err` without context is prohibited in non-leaf functions. | Code review | Must |
| CERT-ERR-03 | ERR09-J | `panic()` SHALL NOT be used for expected error conditions in production code. Panics are reserved for programmer errors (violated invariants) and SHALL be recovered at the top-level HTTP/gRPC handler boundary only. | `gosec G401`, code review | Must |
| CERT-ERR-04 | ERR04-J | Error messages exposed to API consumers (Kubernetes status conditions, gRPC status messages) SHALL NOT contain internal system details (file paths, stack traces, credentials) that could aid an attacker. | Code review | Must |
| CERT-ERR-05 | ERR06-J | Sentinel errors (`var ErrNotFound = errors.New(...)`) SHALL be used for expected error conditions that callers need to handle specifically. Type assertions on error strings are prohibited. | Code review | Must |

### 2.2 CON — Concurrency

Go's concurrency model introduces race conditions and deadlocks as common vulnerability classes.

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-CON-01 | CON00-J | Shared mutable state (the plugin registry, build status cache) SHALL be protected by `sync.RWMutex` or accessed exclusively through channels. | `go test -race`, `go vet` | Must |
| CERT-CON-02 | CON01-J | The Go race detector (`-race`) SHALL be enabled for all tests (`go test -race ./...`). The build SHALL fail if any race condition is detected. | `go test -race` in CI | Must |
| CERT-CON-03 | CON02-J | Goroutines SHALL have defined lifetimes and SHALL be bounded by a `context.Context`. Goroutine leaks SHALL be detected using `goleak` in tests. | `goleak` | Must |
| CERT-CON-04 | CON06-J | `sync.Mutex` and `sync.RWMutex` SHALL NOT be copied after first use. Mutex-containing structs SHALL be passed by pointer. | `go vet` (copylocks) | Must |
| CERT-CON-05 | CON08-J | All use of `sync/atomic` SHALL be reviewed for correctness. Prefer higher-level synchronisation (`sync.Mutex`, channels) unless performance profiling demonstrates atomic operations are necessary. | Code review | Should |

### 2.3 MEM — Memory Safety

Go's garbage collector eliminates most memory safety bugs, but unsafe operations require explicit control.

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-MEM-01 | MEM30-C / analogous | The `unsafe` package SHALL NOT be used in production code without explicit written justification and security review. Any use of `unsafe` requires a code comment explaining why it is safe. | `gosec G103`, code review | Must |
| CERT-MEM-02 | MEM33-C | Slices shared between goroutines SHALL NOT be modified concurrently without synchronisation. | `go test -race` | Must |
| CERT-MEM-03 | — | Large build artefacts (disk images) SHALL be streamed using `io.Reader`/`io.Writer` interfaces. They SHALL NOT be fully loaded into memory (`ioutil.ReadAll` on multi-GB files is prohibited). | Code review | Must |

### 2.4 STR — String and Data Handling

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-STR-01 | STR01-J | All user-supplied strings used in filesystem paths SHALL be sanitised with `filepath.Clean` and validated to prevent path traversal (e.g., `../../etc/passwd`). | `gosec G304`, code review | Must |
| CERT-STR-02 | STR02-J | User-supplied strings used in URL construction SHALL be percent-encoded using `url.PathEscape` or `url.QueryEscape`. String concatenation into URLs is prohibited. | Code review | Must |
| CERT-STR-03 | STR51-CPP / analogous | JSON deserialisation of untrusted input (VMImage spec fields) SHALL use strongly-typed Go structs, not `map[string]interface{}`. Unknown fields SHALL be rejected (`DisallowUnknownFields`). | Code review | Must |

### 2.5 INT — Integer Operations

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-INT-01 | INT30-C | Integer operations on user-supplied values (e.g., resource sizes from VMImage spec) SHALL be validated for overflow before use. Conversion from `int64` to `int32` or smaller types SHALL include a bounds check. | `gosec G115`, code review | Must |
| CERT-INT-02 | INT31-C | File sizes and offsets used in streaming upload (gRPC `UploadChunk`) SHALL be validated to be non-negative and within platform limits before use. | Code review | Must |

### 2.6 FIO — File I/O

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-FIO-01 | FIO01-J | Temporary files SHALL be created with `os.CreateTemp()` (not `os.Create()` with a predictable name) to prevent symlink attacks. | `gosec G304`, code review | Must |
| CERT-FIO-02 | FIO04-J | File descriptors SHALL be closed with `defer f.Close()` immediately after opening. Unclosed file descriptors under error paths are prohibited. | `staticcheck` | Must |
| CERT-FIO-03 | FIO06-J | Files written to `/workspace` by the operator SHALL use mode `0600` (owner read/write only). No world-readable or world-writable files in the workspace. | Code review | Must |
| CERT-FIO-04 | FIO13-J | Paths derived from user input SHALL be validated to lie within an expected directory using `filepath.Rel()` or equivalent to prevent escaping the workspace. | Code review, `gosec G304` | Must |

### 2.7 ENV — Environment and Configuration

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-ENV-01 | ENV01-J | Environment variables used for configuration SHALL be read once at startup and validated. Repeated `os.Getenv()` calls in hot paths are prohibited (TOCTOU risk). | Code review | Must |
| CERT-ENV-02 | ENV03-J | Credentials SHALL NOT be passed via environment variables to build job pods. They are injected as Kubernetes Secret volume mounts or `envFrom.secretRef`. | Code review, REQ-004 SR-001 | Must |
| CERT-ENV-03 | ENV06-J | All command-line flags and environment variables accepted by the operator SHALL be documented with their expected format and safe defaults. | `cmd/operator/main.go` | Must |

### 2.8 API — API Design and Use

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-API-01 | — | All exported functions that accept untrusted input SHALL validate the input before use. Validation SHALL be at the system boundary (CRD admission webhook, gRPC server handler), not scattered through internal code. | Code review | Must |
| CERT-API-02 | — | The `context.Context` parameter SHALL be the first parameter of every function that performs I/O or can be cancelled. Blocking I/O without context propagation is prohibited. | Code review | Must |
| CERT-API-03 | — | gRPC server handlers SHALL validate all request fields before processing. Missing required fields return `codes.InvalidArgument`, not a panic. | Code review | Must |
| CERT-API-04 | — | Timeouts SHALL be set on all outbound network calls (cloud API, gRPC). Calls without a timeout context SHALL NOT be used in production code. | Code review | Must |

### 2.9 DCL — Declarations and Initialization

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-DCL-01 | DCL00-CPP / analogous | `const` SHALL be preferred over `var` for values that do not change. Magic numbers SHALL be named constants, not inline literals. | Code review | Should |
| CERT-DCL-02 | DCL01-C | Package-level `init()` functions SHALL only perform plugin registration (blank-import pattern). Side effects (network calls, file I/O) in `init()` are prohibited. | Code review | Must |

### 2.10 MSC — Miscellaneous

| ID | SEI CERT Rule | Go Requirement | Tooling | Priority |
|---|---|---|---|---|
| CERT-MSC-01 | MSC61-J | Cryptographically random numbers SHALL be generated with `crypto/rand`. Use of `math/rand` for security-relevant operations (token generation, nonce creation) is prohibited. | `gosec G404` | Must |
| CERT-MSC-02 | MSC02-J | Hash functions used for integrity verification (source image checksums) SHALL be SHA-256 or stronger. MD5 and SHA-1 are prohibited for integrity checks. | `gosec G401`, `gosec G501` | Must |
| CERT-MSC-03 | MSC03-J | TLS configurations SHALL set `MinVersion: tls.VersionTLS12`. TLS 1.0 and 1.1 SHALL NOT be accepted. | `gosec G402` | Must |
| CERT-MSC-04 | — | `gosec` SHALL run in CI with a configuration file that enables all relevant rules. `//nolint:gosec` suppressions require a comment explaining why the finding is a false positive. | `gosec` + code review | Must |

---

## 3. OpenSSF Best Practices

### 3.1 Basics

| ID | OpenSSF Criterion | Requirement | Priority |
|---|---|---|---|
| OSSF-B-01 | floss\_license | The project is licensed under Apache 2.0, an OSI-approved FLOSS licence. `LICENSE` SHALL be present in the repository root. | Must |
| OSSF-B-02 | documentation\_basics | A `README.md` SHALL describe: what the project does, how to install/use it, and how to contribute. | Must |
| OSSF-B-03 | interact | The repository SHALL provide a public issue tracker for bug reports and feature requests. | Must |
| OSSF-B-04 | contribution | A `CONTRIBUTING.md` SHALL describe how to contribute, coding standards, and the PR process. | Must |
| OSSF-B-05 | vulnerability\_report\_process | A `SECURITY.md` SHALL document the vulnerability reporting process (SAMM-I-DM-04). Responsible disclosure is the default; a security contact SHALL be named. | Must |
| OSSF-B-06 | vulnerability\_response\_process | The team SHALL respond to vulnerability reports within **14 calendar days** with an acknowledgement. Remediation follows REQ-007 SLAs (SC-039 – SC-041). | Must |

### 3.2 Change Control

| ID | OpenSSF Criterion | Requirement | Priority |
|---|---|---|---|
| OSSF-CC-01 | version\_unique | Each release SHALL have a unique version number following **Semantic Versioning (semver 2.0.0)**. | Must |
| OSSF-CC-02 | version\_semver | Versioning: MAJOR for breaking changes to the provider API (`provider.proto`), MINOR for new features, PATCH for bug fixes. | Must |
| OSSF-CC-03 | release\_notes | A `CHANGELOG.md` SHALL be maintained. Each release entry SHALL document: breaking changes, new features, bug fixes, and security fixes. Security fixes SHALL be marked explicitly. | Must |
| OSSF-CC-04 | repo\_public | The repository SHALL be hosted in a version control system with full history. All changes are traceable to an authenticated author. | Must |
| OSSF-CC-05 | repo\_track | The `main` branch is protected (SC-013). All changes go through reviewed pull requests (SC-011). | Must |

### 3.3 Reporting & Transparency

| ID | OpenSSF Criterion | Requirement | Priority |
|---|---|---|---|
| OSSF-R-01 | report\_archive | All past security reports and their resolutions SHALL be archived (in the issue tracker or a security advisory) for a minimum of 3 years. | Must |
| OSSF-R-02 | report\_response | Security reports SHALL be handled confidentially until a fix is available. Public disclosure follows the responsible disclosure timeline agreed with the reporter (default: 90 days). | Must |
| OSSF-R-03 | enhancement\_response | Bug reports and feature requests SHALL receive a response (acknowledgement or close with reason) within **30 calendar days**. | Should |

### 3.4 Quality

| ID | OpenSSF Criterion | Requirement | Priority |
|---|---|---|---|
| OSSF-Q-01 | build | The project SHALL have a working build system (`make build`) that produces the operator binary from source. | Must |
| OSSF-Q-02 | automated\_integration\_tests | Automated tests SHALL run in CI on every pull request (DR-021 in REQ-006). | Must |
| OSSF-Q-03 | test\_continuous\_integration | CI results SHALL be publicly visible (or visible to all contributors). | Must |
| OSSF-Q-04 | warning\_flags | The build SHALL enable compiler and linter warnings: `go vet ./...`, `staticcheck ./...`. Builds with `HIGH` severity findings are blocked. | Must |
| OSSF-Q-05 | coding\_standards | This document (REQ-010) together with REQ-006 constitutes the coding standard. Its location SHALL be referenced in `CONTRIBUTING.md`. | Must |

### 3.5 Security (OpenSSF Badge Criteria)

| ID | OpenSSF Criterion | Requirement | Priority |
|---|---|---|---|
| OSSF-S-01 | crypto\_published | Only cryptographic algorithms published in peer-reviewed standards (NIST, IETF) SHALL be used. No custom or proprietary cryptography. | Must |
| OSSF-S-02 | crypto\_call | The project SHALL use high-level cryptographic APIs (Go standard library `crypto/` packages). Low-level primitive use requires documented justification. | Must |
| OSSF-S-03 | crypto\_random | `crypto/rand` is used for all security-relevant random number generation (CERT-MSC-01). | Must |
| OSSF-S-04 | crypto\_pfs | Where TLS is used, cipher suites supporting **Perfect Forward Secrecy (PFS)** (ECDHE-based) SHALL be preferred. | Must |
| OSSF-S-05 | delivery\_mitm | Release artefacts are distributed via HTTPS and signed with cosign (REQ-007). There is no unsigned download path. | Must |
| OSSF-S-06 | vulnerabilities\_critical\_fixed | No known unaddressed Critical vulnerabilities (CVSS ≥ 9.0) SHALL exist at the time of a release. | Must |
| OSSF-S-07 | static\_analysis | SAST runs on every PR (AS-060, AS-061 in REQ-008). | Must |

### 3.6 OpenSSF Scorecard

The **OpenSSF Scorecard** provides an automated score (0–10) across 18 checks covering
branch protection, CI/CD security, dependency management, and more.

| ID | Scorecard Check | Requirement | Target Score | Priority |
|---|---|---|---|---|
| OSSF-SC-01 | Branch-Protection | `main` branch protection enabled (SC-013) | Pass | Must |
| OSSF-SC-02 | CI-Tests | Automated tests run on every PR | Pass | Must |
| OSSF-SC-03 | Code-Review | All PRs require at least 1 approving review (SC-011) | Pass | Must |
| OSSF-SC-04 | Dependency-Update-Tool | Dependabot or Renovate configured (SC-019) | Pass | Must |
| OSSF-SC-05 | Fuzzing | Fuzz targets for input parsing functions (URL, JSON) | ≥ 1 fuzz target | Should |
| OSSF-SC-06 | License | Apache 2.0 licence present in repo root | Pass | Must |
| OSSF-SC-07 | Maintained | Last commit within 90 days; issues responded to | Pass | Must |
| OSSF-SC-08 | Pinned-Dependencies | All CI action steps pinned by SHA (SC-034) | Pass | Must |
| OSSF-SC-09 | SAST | SAST tool configured in CI (AS-060) | Pass | Must |
| OSSF-SC-10 | Security-Policy | `SECURITY.md` present (OSSF-B-05) | Pass | Must |
| OSSF-SC-11 | Signed-Releases | Release tags signed (SC-016); images signed with cosign (SC-024) | Pass | Must |
| OSSF-SC-12 | Token-Permissions | CI workflow tokens use minimum required permissions (SC-035) | Pass | Must |
| OSSF-SC-13 | Vulnerabilities | No open Critical/High CVEs at release time (SC-039) | Pass | Must |
| OSSF-SC-14 | **Overall Score** | Aggregate OpenSSF Scorecard score | **≥ 7.0 / 10** | Must |

The Scorecard SHALL run weekly in CI (SC-022 in REQ-007) and its results SHALL be
published via the OpenSSF API. A badge SHALL be displayed in the `README.md`.

---

## 4. Standard Development Set — Summary

This document, together with the following requirements, constitutes the complete
**Secure Development Standard** for the VM Image Builder:

| Standard | Document | Focus |
|---|---|---|
| Test-Driven Development | [REQ-006](REQ-006-development-standards.md) §2 | Implementation quality and regression prevention |
| Twelve-Factor App | [REQ-006](REQ-006-development-standards.md) §3 | Cloud-native application principles |
| OWASP SAMM v2.0 | [REQ-009](REQ-009-owasp-samm.md) | End-to-end security process maturity |
| SEI CERT Secure Coding | [REQ-010](REQ-010-secure-coding-openssf.md) §2 | Language-level secure coding rules (Go) |
| OWASP Top 10 / ASVS | [REQ-008](REQ-008-application-security-owasp.md) | Application security control mapping |
| OpenSSF Best Practices | [REQ-010](REQ-010-secure-coding-openssf.md) §3 | Source management and supply chain |
| SLSA Build Level 1 & 2 | [REQ-007](REQ-007-supply-chain-security.md) | Build and artefact integrity |

---

## 5. Tooling Summary

| Tool | Category | Rules Covered | CI Gate |
|---|---|---|---|
| `go vet` | SAST | CERT-CON-04, CERT-CON-05, misc | Must pass |
| `gosec` | SAST | CERT-MEM-01, STR-01, FIO-01, MSC-01–04, INT-01 | No HIGH+ findings |
| `staticcheck` | SAST | CERT-ERR-01, CERT-FIO-02, general | No errors |
| `errcheck` | SAST | CERT-ERR-01 | Must pass |
| `go test -race` | Concurrency | CERT-CON-01, CERT-CON-02 | No races |
| `goleak` | Concurrency | CERT-CON-03 | No leaks in tests |
| `govulncheck` | CVE | OSSF-S-06, SC-031 | No known vulns |
| `trivy` | CVE + SBOM | AS-062, SC-030, SC-031 | No Critical/High |
| `gitleaks` | Secret scanning | AS-063, CERT-ENV-02 | No secrets |
| OpenSSF Scorecard | Project health | OSSF-SC-01 – SC-14 | ≥ 7.0 / 10 |

---

## 6. Traceability Matrix

| Requirement Group | Related REQ | ADR |
|---|---|---|
| CERT-ERR (Error handling) | REQ-006 (DR-015), REQ-008 (AS-041) | — |
| CERT-CON (Concurrency) | REQ-002 (NFR-006), REQ-006 (DR-016) | — |
| CERT-MEM (Memory) | REQ-004 (SR-011) | ADR-004 |
| CERT-STR (Strings) | REQ-008 (AS-013, AS-015) | — |
| CERT-INT (Integers) | REQ-008 (AS-013) | — |
| CERT-FIO (File I/O) | REQ-008 (AS-022, AS-023) | ADR-003 |
| CERT-ENV (Environment) | REQ-004 (SR-001–SR-005), REQ-006 (TF-007–TF-009) | — |
| CERT-API (API design) | REQ-008 (AS-026), REQ-001 (FR-002) | ADR-005 |
| CERT-MSC (Miscellaneous) | REQ-008 (AS-008–AS-012) | — |
| OSSF Basics | REQ-003 (LR-001) | ADR-001 |
| OSSF Change Control | REQ-007 (SC-013–SC-016) | — |
| OSSF Quality | REQ-006 (DR-021–DR-023) | — |
| OSSF Security | REQ-007 (SC-023–SC-027), REQ-008 (AS-060–AS-065) | — |
| OpenSSF Scorecard | REQ-007 (SC-022), REQ-009 (SAMM-V-ST) | — |
