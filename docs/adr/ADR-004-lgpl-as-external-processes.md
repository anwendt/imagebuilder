---
document-id: ADR-004
title: LGPL Dependencies Accessed Only as External Processes
status: Accepted
date: 2026-04-18
deciders: Platform Engineering
classification: Internal
---

# ADR-004 — LGPL Dependencies Accessed Only as External Processes

## Status

**Accepted**

---

## Context

The VM build engine requires interaction with virtualisation tooling. Two key components
are needed:

- **libvirt**: The standard API for managing virtual machines via KVM/QEMU.
- **libguestfs**: A library for reading, writing, and modifying VM disk images.

Both are licensed under the **GNU Lesser General Public License (LGPL v2.1+)**.

The VM Image Builder is licensed under **Apache License 2.0**. The Apache Software Foundation
(ASF) has clarified that LGPL dependencies create redistribution complications:

- **Static linking** against LGPL code makes the combined binary LGPL-governed.
- **Dynamic linking** (`.so`) against LGPL code triggers LGPL §4, requiring the user to
  be able to swap the LGPL component — which is incompatible with typical Go distribution.
- **Process invocation** (subprocess or socket communication) does not create a derived
  work under LGPL and is license-safe.

---

## Decision

**libvirt and libguestfs SHALL be accessed exclusively as external processes**, never as
linked libraries.

Specifically:
- **libvirt**: Accessed via `go-libvirt` (Apache 2.0), which communicates with the
  `libvirtd` daemon over a **Unix socket** using the libvirt wire protocol. No C library
  (`libvirt.so`) is linked.
- **libguestfs**: If used, accessed by invoking the `guestfish` or `virt-customize` CLI
  tools as child processes via `os/exec`, not via CGo bindings.

**CGo is disabled** for all operator and provider binaries (`CGO_ENABLED=0`).

---

## Rationale

### LGPL Linking Analysis

| Access Method | Legal Assessment | Decision |
|---|---|---|
| Static linking (`libvirt.a`) | Binary becomes LGPL-governed. Redistribution requires source of modified LGPL components. | Prohibited |
| Dynamic linking (`libvirt.so`) | LGPL §4 applies. User must be able to relink — impractical for Go binaries. | Prohibited |
| CGo binding to `libvirt.h` | Same as dynamic linking; CGo produces dynamically linked output by default. | Prohibited |
| Unix socket (go-libvirt) | Inter-process communication. Separate address spaces. No derived work. | Approved |
| CLI subprocess (`guestfish`) | Same as Unix socket: process boundary is license boundary. | Approved |

### `go-libvirt` vs. `libvirt-go`

| Library | Linking | License | Decision |
|---|---|---|---|
| `github.com/digitalocean/go-libvirt` | **Pure Go** — communicates via libvirt wire protocol over Unix socket. No CGo. | Apache 2.0 | Approved |
| `github.com/libvirt/libvirt-go` | **CGo** — wraps `libvirt.so`. Requires C library at runtime. | LGPL 2.1+ | Prohibited |

---

## Consequences

### Positive
- The operator binary is fully Apache-2.0-redistributable.
- `CGO_ENABLED=0` simplifies cross-compilation and produces statically linked Go binaries.
- No runtime dependency on `libvirt.so` or `libguestfs.so` in the operator container image.
- Smaller, more portable container images (no C library dependencies).

### Negative
- `go-libvirt` provides a lower-level API than `libvirt-go`; some abstractions must be built manually.
- libguestfs CLI tools must be present in the build node environment (not the operator image).
- Performance: Unix socket communication has negligible overhead for VM lifecycle operations.

---

## Implementation Notes

### Build Node Requirements

Build nodes (where QEMU VMs run) must have:
- `libvirtd` running and accessible via `/var/run/libvirt/libvirt-sock`
- `qemu-system-x86_64` for AMD64 local ISO builds
- `qemu-system-aarch64` for ARM64 local ISO builds
- AAVMF/EDK2 firmware for ARM64 local ISO builds when required by the guest
- Optionally: `guestfish` / `virt-customize` for disk manipulation

The operator pod does not require any of these tools.

### CGo Enforcement

The Makefile enforces `CGO_ENABLED=0` for all builds:

```makefile
build:
    CGO_ENABLED=0 go build -o bin/operator ./cmd/operator/
```

---

## Compliance Evidence

- `go.mod` contains `github.com/digitalocean/go-libvirt` (Apache 2.0), not `github.com/libvirt/libvirt-go` (LGPL).
- `Makefile` sets `CGO_ENABLED=0`.
- `go-licenses check ./...` passes without LGPL findings.

---

## Related Documents

- [REQ-003 — License & Compliance Requirements](../requirements/REQ-003-license-compliance-requirements.md) (LR-003, LR-004)
- [ADR-001 — Do Not Use HashiCorp Packer](ADR-001-no-packer.md)
