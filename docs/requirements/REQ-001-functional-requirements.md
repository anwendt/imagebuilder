---
document-id: REQ-001
title: Functional Requirements
version: 1.0.0
status: Draft
date: 2026-04-18
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
| FR-005 | The system SHALL verify the integrity of source images via checksum (SHA-256) before use. | Must |
| FR-006 | The system SHALL execute provisioners sequentially in the order defined in the VMImage spec. | Must |
| FR-007 | The system SHALL report build phase transitions via Kubernetes status conditions. | Must |
| FR-008 | The system SHALL record start time and completion time of each build in the VMImage status. | Must |
| FR-009 | The system SHALL support configurable build timeouts. | Must |
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
| FR-020 | The system SHALL support both AMD64 (x86_64) architectures. | Must |
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

## 8. Traceability Matrix

| Requirement | Architecture Component | ADR Reference |
|---|---|---|
| FR-001 – FR-010 | VMImage CRD, Operator Controller | ADR-001 |
| FR-011 – FR-016 | Platform Provider Plugin System | ADR-002, ADR-006 |
| FR-017 – FR-021 | Build Engine (QEMU/diskimage) | ADR-001, ADR-004 |
| FR-022 – FR-032 | Provisioner System | ADR-003 |
| FR-033 – FR-037 | PlatformProvider / ProviderConfig CRDs | ADR-002, ADR-005 |
