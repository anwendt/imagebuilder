# VM Image Builder

VM Image Builder is a Kubernetes-native operator for declarative VM image
builds. It is designed around CRDs, Kubernetes Jobs, external platform
providers, and an Apache-2.0-compatible dependency model.

## What It Does

- Builds VM images from cloud images or ISO sources.
- Boots ISO installers with QEMU and seed media for cloud-init/autoinstall,
  kickstart, preseed, or Windows autounattend.
- Runs provisioners in order: cloud-init, shell, file, PowerShell, sysprep,
  Ansible, Chef, Puppet, SaltStack, or custom commands.
- Uploads and registers the result through provider plugins.
- Supports provider images and provisioner images with digest/signature policy.
- Exposes build status, Kubernetes Events, JSON logs, and Prometheus metrics.

## Core Concepts

| Concept | Purpose |
|---|---|
| `VMImage` | Declarative image build request. |
| `ProviderConfig` | Target platform configuration and credential reference. |
| `PlatformProvider` | External provider deployment implementing the gRPC provider API. |
| Build Job | Per-image Kubernetes Job running QEMU/build/provisioning. |
| Upload Job | Separate job that uploads/registers artifacts after a successful build. |

## Quick Links

- [Quickstart](docs/getting-started/quickstart.md)
- [VMImage authoring guide](docs/user-guide/vmimage.md)
- [Operations guide](docs/operations/operator.md)
- [Security guide](docs/security/security.md)
- [Provider SDK guide](docs/development/provider-sdk.md)
- [Troubleshooting](docs/operations/troubleshooting.md)
- [Architecture](docs/architecture/ARCHITECTURE.md)
- [Architecture diagrams](docs/architecture/diagrams.md)
- [ADRs](docs/adr/README.md)

## Basic Development Commands

```bash
make generate
make manifests
go test ./...
make build
make build-builder
make build-uploader
```

## Local Cluster Smoke Test

Requires `kind` and `kubectl`:

```bash
make test-e2e
```

## License Boundary

LGPL components such as libvirt/libguestfs must only be accessed through
process or socket boundaries, never linked into the core binaries. See
[ADR-004](docs/adr/ADR-004-lgpl-as-external-processes.md).
