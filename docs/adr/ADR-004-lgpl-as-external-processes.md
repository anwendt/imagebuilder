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

**libvirt and libguestfs SHALL NOT be linked into any operator binary**, neither
statically nor dynamically.

Specifically:
- **QEMU / VM lifecycle**: The build engine invokes `qemu-system-x86_64` /
  `qemu-system-aarch64` directly as child processes via `os/exec`
  (`pkg/builder/process.go`). Machine control (shutdown, disk conversion) is performed
  over the **QEMU Machine Protocol (QMP)**, implemented as a pure-Go Unix-socket client
  (`pkg/builder/qmp.go`). No libvirt, no `libvirtd`, no `go-libvirt` binding.
- **libguestfs**: If disk-image inspection is needed in future, it SHALL be accessed by
  invoking the `guestfish` or `virt-customize` CLI tools as child processes via
  `os/exec`, not via CGo bindings.

**CGo is disabled** for all operator and provider binaries (`CGO_ENABLED=0`).

---

## Rationale

### LGPL Linking Analysis

| Access Method | Legal Assessment | Decision |
|---|---|---|
| Static linking (`libvirt.a`) | Binary becomes LGPL-governed. Redistribution requires source of modified LGPL components. | Prohibited |
| Dynamic linking (`libvirt.so`) | LGPL §4 applies. User must be able to relink — impractical for Go binaries. | Prohibited |
| CGo binding to `libvirt.h` | Same as dynamic linking; CGo produces dynamically linked output by default. | Prohibited |
| Unix socket / pure-Go binding (e.g. go-libvirt) | Inter-process communication. Separate address spaces. No derived work. | Approved (not used — see below) |
| CLI subprocess (`guestfish`) | Same as Unix socket: process boundary is license boundary. | Approved |
| Direct QEMU exec + QMP Unix socket | Pure Go, no libvirt at all. QEMU is Apache 2.0. | **Adopted** |

### libvirt access options considered

| Access Method | Legal Assessment | Decision |
|---|---|---|
| `github.com/digitalocean/go-libvirt` (pure Go) | Apache 2.0, no CGo — communicates via libvirt wire protocol over Unix socket | Not used — direct QEMU invocation is simpler and avoids any libvirtd runtime requirement |
| `github.com/libvirt/libvirt-go` (CGo) | LGPL 2.1+ — wraps `libvirt.so`, requires C library at runtime | Prohibited |
| Direct `qemu-system-*` invocation + QMP | Pure Go, Apache 2.0-compatible system tools. No libvirt daemon required. | **Adopted** |

---

## Consequences

### Positive
- The operator binary is fully Apache-2.0-redistributable.
- `CGO_ENABLED=0` simplifies cross-compilation and produces statically linked Go binaries.
- No runtime dependency on `libvirt.so` or `libguestfs.so` in the operator container image.
- Smaller, more portable container images (no C library dependencies).

### Negative
- No high-level libvirt abstraction; VM lifecycle operations (boot, monitor, shutdown) are implemented directly via QEMU QMP commands in `pkg/builder/`.
- libguestfs CLI tools, if used, must be present in the build node environment (not the operator image).

---

## Implementation Notes

### Build Node Requirements

Build nodes (where QEMU VMs run) must have:
- `/dev/kvm` accessible (KVM kernel module loaded)
- `qemu-system-x86_64` for AMD64 local ISO builds
- `qemu-system-aarch64` for ARM64 local ISO builds
- AAVMF/EDK2 firmware for ARM64 local ISO builds when required by the guest
- Optionally: `guestfish` / `virt-customize` for disk manipulation (invoked as child processes)

**libvirtd is not required.** The QEMU process is started directly by the build engine
and controlled via a QMP Unix socket (`pkg/builder/qmp.go`).

The operator pod does not require any of these tools.

### CGo Enforcement

The Makefile enforces `CGO_ENABLED=0` for all builds:

```makefile
build:
    CGO_ENABLED=0 go build -o bin/operator ./cmd/operator/
```

---

## Compliance Evidence

- `go.mod` contains **neither** `github.com/digitalocean/go-libvirt` nor
  `github.com/libvirt/libvirt-go`. No libvirt Go binding is imported.
- QEMU is invoked as a child process (`pkg/builder/qemu_iso_backend.go`,
  `pkg/builder/qemu_backend.go`). QMP control uses a pure-Go Unix-socket client
  (`pkg/builder/qmp.go`). libvirtd is not required at runtime.
- `Makefile` sets `CGO_ENABLED=0` globally.
- `go-licenses check ./...` passes without LGPL findings.

---

## Related Documents

- [REQ-003 — License & Compliance Requirements](../requirements/REQ-003-license-compliance-requirements.md) (LR-003, LR-004)
