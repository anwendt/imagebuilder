---
title: Requirements Index
version: 1.0.0
date: 2026-04-18
classification: Internal
---

# Requirements Index — VM Image Builder

This directory contains the complete requirements baseline for the VM Image Builder system.
All documents are maintained as controlled artefacts and serve as audit evidence.

## Documents

| Document | Title | Status |
|---|---|---|
| [REQ-001](REQ-001-functional-requirements.md) | Functional Requirements | Draft |
| [REQ-002](REQ-002-non-functional-requirements.md) | Non-Functional Requirements | Draft |
| [REQ-003](REQ-003-license-compliance-requirements.md) | License & Compliance Requirements | Draft |
| [REQ-004](REQ-004-security-requirements.md) | Security Requirements (Baseline) | Draft |
| [REQ-005](REQ-005-operational-requirements.md) | Operational Requirements | Draft |
| [REQ-006](REQ-006-development-standards.md) | Development Standards — TDD & Twelve-Factor App | Draft |
| [REQ-007](REQ-007-supply-chain-security.md) | Supply Chain Security — SLSA Build Level 1 & 2 | Draft |
| [REQ-008](REQ-008-application-security-owasp.md) | Application Security — OWASP Top 10 & ASVS | Draft |
| [REQ-009](REQ-009-owasp-samm.md) | Software Assurance Maturity — OWASP SAMM v2.0 | Draft |
| [REQ-010](REQ-010-secure-coding-openssf.md) | Secure Coding Standards (SEI CERT) & OpenSSF Best Practices | Draft |

## Requirement ID Scheme

- **FR-xxx** — Functional Requirement
- **NFR-xxx** — Non-Functional Requirement
- **LR-xxx** — License / Compliance Requirement
- **SR-xxx** — Security Requirement (baseline, REQ-004)
- **OR-xxx** — Operational Requirement
- **DR-xxx** — Development Requirement (TDD)
- **TF-xxx** — Twelve-Factor App Requirement
- **SC-xxx** — Supply Chain Security Requirement (SLSA)
- **AS-xxx** — Application Security Requirement (OWASP Top 10)
- **SAMM-xxx** — OWASP SAMM Maturity Requirement
- **CERT-xxx** — SEI CERT Secure Coding Requirement
- **OSSF-xxx** — OpenSSF Best Practices Requirement

## Secure Development Standard

The full development standard comprises:

| Standard | Document |
|---|---|
| Test-Driven Development + Twelve-Factor App | REQ-006 |
| Supply Chain Security (SLSA L1 & L2) | REQ-007 |
| Application Security (OWASP Top 10 / ASVS) | REQ-008 |
| Security Process Maturity (OWASP SAMM v2.0) | REQ-009 |
| SEI CERT Secure Coding + OpenSSF Best Practices | REQ-010 |

## Change Control

Changes to requirements documents require review and approval by Platform Engineering
and must be committed with a descriptive commit message referencing the changed REQ document ID.
