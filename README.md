# VM Image Builder

VM Image Builder is a Kubernetes-native operator for declarative VM image
builds. It is designed around CRDs, Kubernetes Jobs, external platform
providers, and an Apache-2.0-compatible dependency model.

## Kubernetes Compatibility

Kubernetes 1.29 or newer is required because OCI provisioners use native
sidecar containers represented by restartable init containers
(`initContainers[].restartPolicy: Always`). This feature is enabled by default
from Kubernetes 1.29 and stable from Kubernetes 1.33; Kubernetes 1.33 or newer
is recommended for production. Helm and operator startup both enforce the 1.29
minimum.

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

## Provisioner Images

Complex provisioners run as restartable init containers. The released default
provisioner images include the matching runtime binaries, so builds do not need
to install the provisioner tool before executing a step:

| Provisioner image | Included runtime |
|---|---|
| `imagebuilder-provisioner-ansible` | `ansible-playbook` |
| `imagebuilder-provisioner-chef` | `chef-client`, `chef-apply` |
| `imagebuilder-provisioner-puppet` | `puppet` |
| `imagebuilder-provisioner-saltstack` | `salt-call`, `salt-minion` |
| `imagebuilder-provisioner-custom` | Only the Image Builder provisioner runner and base OS tools |

Custom provisioner images can still be supplied per `VMImage` when a different
runtime version, extra collections/cookbooks/modules, or organisation-specific
tooling is required.

## Quick Links

- [Quickstart](docs/getting-started/quickstart.md)
- [VMImage authoring guide](docs/user-guide/vmimage.md)
- [Git-backed provisioner scripts](docs/user-guide/git-provisioners.md)
- [Operations guide](docs/operations/operator.md)
- [GCP provider guide](docs/operations/gcp-provider.md)
- [Security guide](docs/security/security.md)
- [Provider SDK guide](docs/development/provider-sdk.md)
- [Troubleshooting](docs/operations/troubleshooting.md)
- [Architecture](docs/architecture/ARCHITECTURE.md)
- [Architecture diagrams](docs/architecture/diagrams.md)
- [ADRs](docs/adr/README.md)

## Quick Install

Install the latest released chart from GHCR:

```bash
helm install imagebuilder oci://ghcr.io/anwendt/charts/imagebuilder \
  --version 0.4.2 \
  --namespace imagebuilder-system \
  --create-namespace

kubectl get pods -n imagebuilder-system
```

The chart installs CRDs, the operator, webhook resources, metrics Service,
network policies, and secure default image references. Kyverno image signature
policies are optional and can be enabled with
`--set imageSignaturePolicy.enabled=true` when Kyverno is installed. For real
image builds, add one or more provider configurations from `examples/argocd/`.

For local clusters where you want the least friction, use the development
profile from a checkout:

```bash
helm install imagebuilder oci://ghcr.io/anwendt/charts/imagebuilder \
  --version 0.4.2 \
  --namespace imagebuilder-system \
  --create-namespace \
  -f charts/imagebuilder/values-development.yaml
```

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
