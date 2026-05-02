---
title: Architecture Decision Records Index
version: 1.0.0
date: 2026-04-18
classification: Internal
---

# Architecture Decision Records (ADR) Index — VM Image Builder

Architecture Decision Records document significant design decisions made during
the development of the VM Image Builder. Each ADR captures the context, decision,
rationale, alternatives considered, and consequences.

ADRs are immutable once accepted. Superseded decisions create a new ADR that
references the old one.

## Records

| ID | Title | Status |
|---|---|---|
| [ADR-001](ADR-001-no-packer.md) | Do Not Use HashiCorp Packer | Accepted |
| [ADR-002](ADR-002-providers-as-separate-containers.md) | Platform Providers as Separate Kubernetes Containers | Accepted |
| [ADR-003](ADR-003-provisioners-as-init-containers.md) | Complex Provisioners as Kubernetes Init Containers | Accepted |
| [ADR-004](ADR-004-lgpl-as-external-processes.md) | LGPL Dependencies Accessed Only as External Processes | Accepted |
| [ADR-005](ADR-005-protobuf-versioned-contract.md) | Protobuf Schema as Versioned and Immutable Provider Contract | Accepted |
| [ADR-006](ADR-006-no-go-plugin-mechanism.md) | No Go Plugin Mechanism (.so Files) | Accepted |
| [ADR-007](ADR-007-remote-build-provider-contract.md) | Remote Build as Provider-Owned Execution Mode | Accepted |

## ADR Status Values

| Status | Meaning |
|---|---|
| Proposed | Decision is under discussion |
| Accepted | Decision is final and implemented |
| Deprecated | Decision is outdated but not yet replaced |
| Superseded | Decision has been replaced by a newer ADR |

## Template

New ADRs follow this structure:

```markdown
# ADR-NNN — Title

## Status
## Context
## Decision
## Rationale
## Alternatives Considered
## Consequences
## Related Documents
```
