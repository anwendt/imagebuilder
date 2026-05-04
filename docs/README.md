---
title: Documentation Index — VM Image Builder
version: 1.0.0
date: 2026-04-18
classification: Internal
purpose: ISAE Audit Navigation
---

# Documentation Index — VM Image Builder

This directory contains the complete controlled documentation for the VM Image Builder system.
All documents are intended to support ISAE audit requirements.

---

## Architecture

| Document | Description |
|---|---|
| [ARCHITECTURE.md](architecture/ARCHITECTURE.md) | Complete system architecture description (ISAE audit document) |

## Getting Started

| Document | Description |
|---|---|
| [Quickstart](getting-started/quickstart.md) | Install, test, and validate the operator in a cluster |

## User Guide

| Document | Description |
|---|---|
| [VMImage Authoring Guide](user-guide/vmimage.md) | Source, guest access, provisioners, targets, and artifact storage |

## Operations

| Document | Description |
|---|---|
| [Operator Operations Guide](operations/operator.md) | Deployment, flags, scheduling, metrics, webhooks, cleanup |
| [Production Readiness](operations/production-readiness.md) | Release images, policy flags, CI gates, and rollout checklist |
| [AWS Remote Build](operations/aws-remote-build.md) | AWS remote build configuration, IAM, SSM provisioning, cleanup |
| [vSphere Provider Operations](operations/vsphere-provider.md) | vSphere OVA/OVF import, Content Library publishing, and validation |
| [Azure Provider Operations](operations/azure-provider.md) | Azure Managed Image and Compute Gallery publishing |
| [Azure Provider Runbook](operations/azure-provider-runbook.md) | Azure rollback, quota, throttling, metrics, and troubleshooting |
| [OpenStack Provider Operations](operations/openstack-provider.md) | Glance upload and Nova-backed remote builds |
| [Troubleshooting](operations/troubleshooting.md) | Common failure modes and diagnostics |

## Security

| Document | Description |
|---|---|
| [Security Guide](security/security.md) | Supply chain, runtime hardening, credentials, network policy, TLS |

## Development

| Document | Description |
|---|---|
| [Development Guide](development/development.md) | Tooling, generation, tests, builds, style |
| [External Provider SDK](development/provider-sdk.md) | SDK and starter-template guide for external providers |

## Requirements

| Document | Title |
|---|---|
| [REQ-001](requirements/REQ-001-functional-requirements.md) | Functional Requirements |
| [REQ-002](requirements/REQ-002-non-functional-requirements.md) | Non-Functional Requirements |
| [REQ-003](requirements/REQ-003-license-compliance-requirements.md) | License & Compliance Requirements |
| [REQ-004](requirements/REQ-004-security-requirements.md) | Security Requirements (Baseline) |
| [REQ-005](requirements/REQ-005-operational-requirements.md) | Operational Requirements |
| [REQ-006](requirements/REQ-006-development-standards.md) | Development Standards — TDD & Twelve-Factor App |
| [REQ-007](requirements/REQ-007-supply-chain-security.md) | Supply Chain Security — SLSA Build Level 1 & 2 |
| [REQ-008](requirements/REQ-008-application-security-owasp.md) | Application Security — OWASP Top 10 & ASVS |
| [REQ-009](requirements/REQ-009-owasp-samm.md) | Software Assurance Maturity — OWASP SAMM v2.0 |
| [REQ-010](requirements/REQ-010-secure-coding-openssf.md) | Secure Coding Standards (SEI CERT) & OpenSSF Best Practices |

## Architecture Decision Records

| Document | Title | Status |
|---|---|---|
| [ADR-001](adr/ADR-001-no-packer.md) | Do Not Use HashiCorp Packer | Accepted |
| [ADR-002](adr/ADR-002-providers-as-separate-containers.md) | Platform Providers as Separate Kubernetes Containers | Accepted |
| [ADR-003](adr/ADR-003-provisioners-as-init-containers.md) | Complex Provisioners as Kubernetes Init Containers | Accepted |
| [ADR-004](adr/ADR-004-lgpl-as-external-processes.md) | LGPL Dependencies as External Processes Only | Accepted |
| [ADR-005](adr/ADR-005-protobuf-versioned-contract.md) | Protobuf Schema as Versioned Provider Contract | Accepted |
| [ADR-006](adr/ADR-006-no-go-plugin-mechanism.md) | No Go Plugin Mechanism (.so Files) | Accepted |
| [ADR-007](adr/ADR-007-remote-build-provider-contract.md) | Remote Build as Provider-Owned Execution Mode | Accepted |

---

## Document Classification

All documents are classified **Internal**. External distribution requires explicit approval
from Platform Engineering.

## Change Control

All documentation changes are version-controlled in Git. Changes to `Accepted` ADRs are not
permitted; a new superseding ADR must be created instead.
