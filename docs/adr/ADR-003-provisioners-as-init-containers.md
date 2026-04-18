---
document-id: ADR-003
title: Complex Provisioners as Kubernetes Init Containers
status: Accepted
date: 2026-04-18
deciders: Platform Engineering
supersedes: —
superseded-by: —
classification: Internal
---

# ADR-003 — Complex Provisioners as Kubernetes Init Containers

## Status

**Accepted**

---

## Context

After a VM image is built by the build engine (QEMU or diskimage-builder), it must be
customised by one or more provisioners (e.g., install packages, apply hardening policies,
run compliance checks). Provisioners must execute in a defined sequential order.

Two categories of provisioners were identified:

1. **Simple provisioners**: cloud-init, shell scripts, file injection, PowerShell.
   These are stateless transformations that can be implemented in Go and run in-process.

2. **Complex provisioners**: Ansible, Chef, Puppet, SaltStack, InSpec, custom.
   These require large external tool installations (Python, Ruby, etc.), have their own
   dependency trees, and are often contributed by the community.

The key requirement is that provisioners run **strictly sequentially** and any failure
must abort the entire build.

---

## Decision

**Simple provisioners** are implemented as in-process Go implementations of the
`Provisioner` interface (`pkg/provisioner/interface.go`), compiled into the operator binary.

**Complex provisioners** are implemented as **Kubernetes Init Containers** within the
build Job Pod. Each provisioner is a separate OCI container image.

The contract between the operator and an init-container provisioner is filesystem-based:

```
/workspace/config.json   ← Written by operator before build starts
                            Contains: VM IP, SSH credentials, OS info, provisioner spec
/workspace/status.json   → Written by provisioner upon completion
                            Contains: success/error message, artifact paths
Exit code 0              → Provisioner succeeded, next init container starts
Exit code != 0           → Build fails, operator reads status.json for error details
```

---

## Architecture

```
Build Job Pod
┌─────────────────────────────────────────────────────────┐
│  Init Container 1: cloud-init-writer (in-process equiv) │
│  Init Container 2: provisioner-ansible:v2.16            │
│  Init Container 3: provisioner-inspec:v1.0              │
│  Main Container:   artifact-upload                       │
└─────────────────────────────────────────────────────────┘

Kubernetes guarantees: Init Containers run sequentially, one at a time.
If any init container exits with non-zero, the Pod fails.
```

---

## Rationale

### Why Kubernetes Init Containers?

| Benefit | Detail |
|---|---|
| **Sequential semantics built-in** | Kubernetes guarantees init containers run one at a time in order. This is exactly the semantics required for provisioners. No custom orchestration code needed. |
| **Zero SDK requirement** | A community provisioner needs only to be an OCI image that reads `/workspace/config.json` and writes `/workspace/status.json`. No Go SDK, no gRPC, no operator dependency. |
| **Independent tool environments** | Ansible (Python), Chef (Ruby), InSpec (Ruby) run in their own isolated containers without conflicting dependencies. |
| **Failure atomicity** | Pod-level failure semantics ensure the entire build job fails if any provisioner fails, with automatic cleanup. |
| **Retryability** | The operator can restart the Job if the build environment allows it (idempotent provisioners). |

### Why Not gRPC (Like Providers)?

| Factor | Detail |
|---|---|
| **Overhead** | Provisioners are long-running tools (minutes), not short RPC calls. A simple file-based contract is sufficient. |
| **Accessibility** | Init-container contract requires no library dependency — any scripting language or compiled tool can read/write JSON. |
| **Isolation** | Provisioners don't need to communicate back to the operator in real time; status is polled via the Job/Pod status. |

### Why Not Sidecar Containers?

Sidecar containers run in parallel with the main container. Provisioners must run
sequentially. Init containers are the correct Kubernetes primitive for sequential,
ordered setup tasks.

---

## Consequences

### Positive
- Any OCI image can be a provisioner with no SDK changes required.
- Sequential ordering is enforced by Kubernetes, not by custom code.
- Complex provisioner ecosystems (Ansible Galaxy roles, Chef Cookbooks) are available without porting to Go.
- Clear failure semantics via exit code and status.json.

### Negative
- Provisioners cannot stream real-time progress to the operator; only terminal status is captured.
- The `/workspace` volume must be a shared `emptyDir` between init containers, requiring careful volume management.
- Provisioner OCI images may be large (Ansible: ~500 MB with Python); pull time affects build latency.

### Mitigations
- Node-level image caching (pre-pulling provisioner images on build nodes) reduces pull latency.
- The operator can optionally stream logs from init containers via the Kubernetes log streaming API for observability.

---

## Filesystem Contract (Normative)

### `/workspace/config.json` — Input (written by operator)

```json
{
  "vm": {
    "address": "192.168.100.10",
    "sshPort": 22,
    "sshUser": "imagebuilder",
    "sshKeyPath": "/workspace/ssh/id_ed25519"
  },
  "os": {
    "family": "linux",
    "distribution": "ubuntu",
    "version": "24.04",
    "arch": "amd64"
  },
  "provisioner": {
    "type": "ansible",
    "spec": { ... }
  }
}
```

### `/workspace/status.json` — Output (written by provisioner)

```json
{
  "success": true,
  "message": "Ansible playbook completed successfully",
  "artifacts": []
}
```

---

## Related Documents

- [REQ-001 — Functional Requirements](../requirements/REQ-001-functional-requirements.md) (FR-022 – FR-032)
- [REQ-004 — Security Requirements](../requirements/REQ-004-security-requirements.md) (SR-011 – SR-015)
- [ADR-002 — Platform Providers as Separate Containers](ADR-002-providers-as-separate-containers.md)
