---
document-id: REQ-001
title: Functional Requirements
version: 1.1.0
status: Draft
date: 2026-05-04
author: Platform Engineering
classification: Internal
---

# REQ-001 — Functional Requirements

## 1. Purpose

This document defines the functional requirements for the **VM Image Builder** system.
It serves as the authoritative reference for what the system must do and provides
traceability for architecture decisions and audit purposes.

## 2. Scope

The VM Image Builder is a Kubernetes Operator that enables the automated, declarative
building of virtual machine (VM) images for multiple cloud and on-premises platforms.

---

## 3. Image Build Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-001 | The system SHALL accept declarative VM image specifications as Kubernetes Custom Resources (VMImage CRD). | Must |
| FR-002 | The system SHALL validate VMImage specifications against a defined schema before initiating a build. | Must |
| FR-003 | The system SHALL build VM images from ISO or pre-built cloud images as source. | Must |
| FR-004 | The system SHALL support marketplace images as source (cloud-provider-provided base images). | Should |
| FR-005 | The system SHALL verify the integrity of downloadable source images via checksum (SHA-256) before use. Provider-native source identifiers are validated by the selected provider instead of by checksum. | Must |
| FR-006 | The system SHALL execute provisioners sequentially in the order defined in the VMImage spec. | Must |
| FR-007 | The system SHALL report build phase transitions via Kubernetes status conditions. | Must |
| FR-008 | The system SHALL record start time and completion time of each build in the VMImage status. | Must |
| FR-009 | The system SHALL support configurable build timeouts. | Must |
| FR-009A | VMImage status SHALL expose the observed metadata generation and build revision, and every condition SHALL identify its observed generation. | Must |
| FR-009B | Ready and Failed VMImages SHALL support declarative rebuild or retry by changing an opaque build revision token. Active build specifications SHALL remain immutable, and generated resources and provider operation IDs SHALL be revision-scoped. | Must |
| FR-006A | The system SHALL support Git-backed provisioner content with an explicit HTTPS URL, ref, and path. Directory paths SHALL expand into regular files executed in lexicographic order. | Should |
| FR-006B | Git-backed provisioner content SHALL be expanded before provisioner validation and execution so local and remote build providers observe the same ordered provisioner plan. | Must |
| FR-006C | Git-backed provisioner content SHALL support private repositories through Kubernetes Secret references for token or username/password authentication. | Must |
| FR-010 | The system SHALL publish the built image to all configured target platforms in one build run. | Must |

---

## 4. Platform Support Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-011 | The system SHALL support VMware vSphere (including VMware Cloud Foundation) as a target platform. | Must |
| FR-012 | The system SHALL support OpenStack as a target platform. | Must |
| FR-013 | The system SHALL support Amazon Web Services (AWS) as a target platform, producing AMIs. | Must |
| FR-014 | The system SHALL support Microsoft Azure as a target platform, producing Managed Images. | Must |
| FR-015 | The system SHALL support Google Cloud Platform (GCP) as a target platform, producing Custom Images. | Must |
| FR-016 | The system SHALL support additional community-contributed platforms via a provider extension mechanism. | Should |

---

## 5. Operating System Support Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-017 | The system SHALL support building images for Linux distributions: Ubuntu, Debian, RHEL/CentOS, Rocky Linux, AlmaLinux, Fedora, and SLES. | Must |
| FR-018 | The system SHALL support building images for Windows Server 2019, 2022, and 2025. | Must |
| FR-019 | The system SHALL support building images for Windows 10 and Windows 11 (desktop). | Should |
| FR-020 | The system SHALL support AMD64 (x86_64) architecture. | Must |
| FR-021 | The system SHALL support ARM64 architecture. | Should |

---

## 6. Provisioner Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-022 | The system SHALL support cloud-init as an in-process provisioner. | Must |
| FR-023 | The system SHALL support shell scripts as an in-process provisioner. | Must |
| FR-024 | The system SHALL support file injection as an in-process provisioner. | Must |
| FR-025 | The system SHALL support PowerShell scripts as an in-process provisioner for Windows images. | Must |
| FR-026 | The system SHALL support Windows Sysprep as an in-process provisioner. | Must |
| FR-027 | The system SHALL support Ansible playbooks as an init-container provisioner. | Must |
| FR-028 | The system SHALL support Chef as an init-container provisioner. | Should |
| FR-029 | The system SHALL support Puppet as an init-container provisioner. | Should |
| FR-030 | The system SHALL support SaltStack as an init-container provisioner. | Should |
| FR-031 | The system SHALL support arbitrary custom provisioners via the OCI init-container contract. | Must |
| FR-032 | The system SHALL guarantee sequential execution of provisioners and abort the build on any provisioner failure. | Must |

---

## 7. Provider Lifecycle Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-033 | The system SHALL allow installation of platform providers via a PlatformProvider CRD without restarting the core operator. | Must |
| FR-034 | The system SHALL allow configuration of provider credentials per platform instance via a ProviderConfig CRD. | Must |
| FR-035 | The system SHALL store all credentials as Kubernetes Secrets, never embedded in CRD specs. | Must |
| FR-036 | The system SHALL verify provider health continuously and report status on the PlatformProvider resource. | Must |
| FR-037 | The system SHALL perform cleanup (artifact deletion) on the target platform if a build or registration fails. | Must |

---

## 8. Remote Build Requirements

Remote build means that VM instantiation, boot, provisioning, shutdown, image capture,
and platform registration are executed by a platform provider on the target platform
instead of by the local QEMU build backend in the build pod.

| ID | Requirement | Priority |
|---|---|---|
| FR-038 | The system SHALL model build execution mode explicitly as local or remote in the VMImage build specification. | Must |
| FR-039 | The system SHALL keep provider-specific remote build implementation details out of the core operator. | Must |
| FR-040 | The system SHALL require providers to advertise remote build support through provider capabilities before the operator schedules a remote build. | Must |
| FR-041 | The system SHALL pass a provider-neutral remote build request to the selected provider, including source, OS metadata, provisioner plan, guest access policy, timeouts, target format, and artifact requirements. | Must |
| FR-042 | The system SHALL report remote build phase transitions in VMImage status using the same phase model as local builds, including source, boot, readiness, provisioning, sanitization, shutdown, upload, and registration. | Must |
| FR-043 | The system SHALL support cancellation and cleanup for remote builds through provider-owned cleanup operations. | Must |
| FR-044 | The system SHALL apply the same credential handling rules to remote builds as local builds, including no secrets in logs, ephemeral credentials where possible, and status output without secret material. | Must |
| FR-045 | The system SHALL apply final image hygiene checks or provider-attested hygiene results before a remotely built image is marked Ready. | Must |
| FR-046 | The system SHOULD support remote build implementations for AWS, Azure, vSphere, and OpenStack because they remove the local KVM requirement for common target platforms. | Should |
| FR-047 | The GCP provider SHALL support local GCE import archives and idempotent remote Custom Image creation from existing GCP image or snapshot references. Remote guest provisioning MAY be added separately. | Must |
| FR-060 | The OpenStack provider SHALL support local Glance image upload/register and remote builds from existing Glance image UUIDs through temporary Nova servers and server snapshots. | Must |

---

## 9. Source Cache Requirements

| ID | Requirement | Priority |
|---|---|---|
| FR-048 | The system SHALL support an explicit PVC-backed source cache for local builds. | Should |
| FR-049 | The system SHALL key source cache entries by verified checksum, not by source URL. | Must |
| FR-050 | The system SHALL delete and refetch corrupt or expired source cache entries before using them. | Must |
| FR-051 | The system SHALL fail the build and avoid updating the cache when a freshly downloaded source fails checksum verification. | Must |
| FR-052 | The system SHALL support cache retention policy for keeping or removing matching cache entries after a build. | Should |

---

## 10. Provider-Native Source Requirements

Provider-native sources are platform identifiers that do not represent
downloadable artifacts, for example an AWS AMI ID or EBS snapshot ID. They are
passed through `spec.source.providerRef` and handled by the selected provider.

| ID | Requirement | Priority |
|---|---|---|
| FR-053 | The system SHALL support provider-native snapshot sources for remote builds through `spec.source.type: snapshot` and `spec.source.providerRef`. | Must |
| FR-054 | The core admission layer SHALL reject snapshot sources unless the build mode is `remote`, `providerRef` is set, and `url` is empty. Provider implementations SHALL decide whether provisioners are supported for their provider-native source type. | Must |
| FR-055 | The AWS provider SHALL support registering an AMI directly from an existing completed EBS snapshot. | Must |
| FR-056 | The Azure provider SHALL support remote builds from existing Azure Snapshot and Managed Disk resource IDs, including provider-owned provisioning through Azure VM Run Command when provisioners are configured. | Must |
| FR-057 | The vSphere provider SHALL support remote builds from existing VM or template references, including provider-owned provisioning through VMware Guest Operations when provisioners are configured. | Must |
| FR-058 | Snapshot-source registration without provisioners SHALL be treated as a direct provider registration or clone operation without temporary guest boot, guest readiness, or provisioner execution. | Must |
| FR-059 | Provider implementations SHALL preserve user-owned provider-native source artifacts during failure cleanup unless the provider itself created the artifact for the build. | Must |

---

## 11. Traceability Matrix

| Requirement | Architecture Component | ADR Reference |
|---|---|---|
| FR-001 – FR-010 | VMImage CRD, Operator Controller | ADR-001 |
| FR-011 – FR-016 | Platform Provider Plugin System | ADR-002, ADR-006 |
| FR-017 – FR-021 | Build Engine (QEMU/diskimage) | ADR-001, ADR-004 |
| FR-022 – FR-032 | Provisioner System | ADR-003 |
| FR-033 – FR-037 | PlatformProvider / ProviderConfig CRDs | ADR-002, ADR-005 |
| FR-038 – FR-047 | Remote Build Contract and Provider Orchestration | ADR-002, ADR-005, ADR-007 |
| FR-048 – FR-052 | Source Cache and Build Engine | ADR-001, ADR-004 |
| FR-053 – FR-057 | VMImage admission, Remote Build Contract, AWS Provider | ADR-002, ADR-007 |
