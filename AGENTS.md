# VM Image Builder — Codex Context

This document contains all architectural decisions and design principles for the
**VM Image Builder** — a Kubernetes-native, declarative image builder fully
licensed under **Apache 2.0**.

---

## Project Goal

A Kubernetes Operator that enables building VM images for various platforms via
declarative Kubernetes manifests with:
- Fully **Apache 2.0** licensed (no BSL, no GPL linking)
- Kubernetes-native (CRDs, Operator Pattern, Reconciliation Loop)
- Extensible via a **plugin system** for platforms and provisioners

---

## License Constraints — CRITICAL

All runtime dependencies must remain compatible with Apache-2.0 redistribution.

Permitted dependencies:
| Component | License | Usage |
|---|---|---|
| controller-runtime / kubebuilder | Apache 2.0 | Operator Framework |
| govmomi | Apache 2.0 | vSphere/VCF SDK |
| gophercloud | Apache 2.0 | OpenStack SDK |
| aws-sdk-go-v2 | Apache 2.0 | AWS SDK |
| azure-sdk-go | MIT | Azure SDK |
| google-cloud-go | Apache 2.0 | GCP SDK |
| QEMU (Userspace) | Apache 2.0 | Build Backend |
| diskimage-builder | Apache 2.0 | Build Backend (OpenStack) |
| go-libvirt | Apache 2.0 | libvirt Bindings via Socket |

**LGPL Rule**: libvirt and libguestfs are LGPL — they are accessed exclusively as
external processes via Unix sockets, **never statically linked**.
This keeps the project Apache-2.0-clean.

---

## Supported Target Platforms

- vSphere (incl. VMware Cloud Foundation)
- OpenStack
- AWS (AMI)
- Azure (Managed Image / Compute Gallery)
- GCP (Custom Image)

## Supported Operating Systems

- Linux: Ubuntu, Debian, RHEL/CentOS, Rocky, AlmaLinux, Fedora, SLES
- Windows: Server 2019, 2022, 2025; Windows 10/11

---

## Architecture Overview

```
VMImage Manifest (YAML)
    ↓
Kubernetes API (CRD Validation)
    ↓
Operator Controller (Reconciliation Loop)
    ↓
Build Engine (Kubernetes Job)
    ├── QEMU/libvirt Backend      → vSphere, VCF, local
    ├── diskimage-builder Backend → OpenStack
    └── Cloud-API Backend         → AWS, Azure, GCP (directly via SDK)
    ↓
Provisioner (sequential, Init Containers)
    ├── cloud-init, Shell, File, PowerShell (In-Process)
    └── Ansible, Chef, Custom (Init Container / OCI Image)
    ↓
Platform Provider (Pod, gRPC)
    ├── provider-vsphere
    ├── provider-openstack
    ├── provider-aws
    ├── provider-azure
    ├── provider-gcp
    └── provider-* (Community)
```

---

## Plugin System — Two Independent Layers

### Layer 1: Platform Provider (analogous to Crossplane)

Platform Providers are **separate containers** that are dynamically loaded via a `PlatformProvider` CRD.
The core operator starts them as Kubernetes Deployments and communicates
via **gRPC over ClusterIP Services**. Production deployments enforce a
namespace-local NetworkPolicy boundary and can require mTLS for provider gRPC.

**Core principle**: A new provider requires no fork, no core patch — only an
OCI image that implements the Protobuf interface.

```
PlatformProvider CR → Core Operator → Start Deployment → gRPC Handshake → Registry
```

Each provider implements:
- `GetCapabilities()` — Name, version, supported formats and OS families
- `ValidateConfig()` — Validate credentials and endpoint
- `UploadArtifact()` — Streaming upload of the build artifact
- `RegisterImage()` — Register as platform image (AMI, Template, UUID...)
- `DeleteArtifact()` — Cleanup on failure
- `HealthCheck()` — Liveness

**File**: `api/provider/v1/provider.proto` — this is the stable contract, never change it in a breaking way.

The Go Provider SDK lives in `pkg/provider/sdk`. It already supports provider
gRPC mTLS via `sdk.ServerOptionsFromEnv()` and the following environment
variables injected by the core operator:

```bash
PROVIDER_GRPC_TLS_MODE=Mutual
PROVIDER_GRPC_TLS_CERT_FILE=/var/run/imagebuilder/provider-tls/tls.crt
PROVIDER_GRPC_TLS_KEY_FILE=/var/run/imagebuilder/provider-tls/tls.key
PROVIDER_GRPC_TLS_CLIENT_CA_FILE=/var/run/imagebuilder/provider-client-ca/ca.crt
```

Digest pinning, signature verification, and registry allow-lists are enforced
by `PlatformProvider` admission/operator policy and do not require provider SDK
interface changes.

### Layer 2: Provisioner (Three Levels)

| Level | Mechanism | Usage |
|---|---|---|
| In-Process | Go Interface, compile-time | cloud-init, Shell, File, PowerShell, Sysprep |
| Init Container | OCI Image, dynamic | Ansible, Chef, Puppet, SaltStack, Custom |
| Sidecar | Parallel to build | Vault Agent, SSH Proxy (only when needed) |

**Init Container Contract** (no SDK required):
```
/workspace/provisioners/step-N/config.json  ← Builder writes when step N may run
/workspace/provisioners/step-N/status.json  → Provisioner writes success/error
success=true                            → Builder continues to next provisioner
success=false or timeout                → Build fails
```

Restartable init containers are started before the main builder. The builder
keeps provisioner execution sequential by writing one step config at a time and
waiting for the matching status file.

---

## CRD Structure

### VMImage (Main Resource)

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: VMImage
metadata:
  name: ubuntu-24-04-hardened
spec:
  os:
    family: linux
    distribution: ubuntu
    version: "24.04"
    arch: amd64
  source:
    type: cloud-image   # iso | cloud-image | marketplace
    url: https://...
    checksum: sha256:...
  provisioners:
    - type: cloud-init
      inline: |
        packages: [nginx]
    - type: ansible
      image: ghcr.io/yourorg/provisioner-ansible:v2.16  # optional override
      playbook: s3://bucket/harden.yml
    - type: custom
      image: ghcr.io/mycompany/provisioner-inspec:v1.0
      args: ["--profile", "cis-ubuntu-22"]
  targets:
    - providerConfigRef:
        name: aws-eu-west-1
      format: ami
    - providerConfigRef:
        name: vsphere-prod
      format: ova
  build:
    timeout: 2h
    nodeSelector:
      kubernetes.io/os: linux
    security:
      enableKVM: false
status:
  phase: Building | Uploading | Ready | Failed
  images:
    - provider: aws
      imageRef: ami-0abc123
      location: eu-west-1
  uploadOperations:
    - provider: aws
      providerConfig: aws-eu-west-1
      format: ami
      phase: Succeeded
      operationRef: s3://bucket/key
      imageRef: ami-0abc123
      uploadBytes: 123456789
  conditions: [...]
```

### PlatformProvider (Install a Provider)

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: PlatformProvider
metadata:
  name: provider-aws
spec:
  package: ghcr.io/yourorg/imagebuilder-provider-aws@sha256:...
  packagePullPolicy: IfNotPresent
  security:
    allowedRegistries:
      - ghcr.io/yourorg
    requireDigest: true
    verifySignature: true
  transport:
    tls:
      mode: Mutual
      serverName: provider-aws.imagebuilder-system.svc
      caSecretRef:
        name: provider-grpc-ca
        namespace: imagebuilder-system
      clientCertificateSecretRef:
        name: operator-provider-client-tls
        namespace: imagebuilder-system
      serverCertificateSecretRef:
        name: provider-aws-server-tls
        namespace: imagebuilder-system
```

### ProviderConfig (Credentials per Instance)

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: ProviderConfig
metadata:
  name: aws-eu-west-1
spec:
  provider: aws
  credentials:
    secretRef:
      name: aws-credentials
      key: credentials
  region: eu-west-1
```

---

## Go Conventions in This Project

- **Go Version**: 1.22+
- **Module**: `github.com/anwendt/imagebuilder`
- **Error handling**: always `fmt.Errorf("context: %w", err)`, never panic in production code
- **Logging**: `log/slog` (stdlib), structured with `slog.With()`
- **Context**: Every function doing I/O receives `ctx context.Context` as its first parameter
- **Interfaces**: Keep small — max 5-7 methods per interface (Go idiom)
- **Tests**: Table-driven tests with `t.Run()`, mocks via interface implementations
- **Generated Code**: Never edit manually — comment `// Code generated ... DO NOT EDIT.`

---

## Directory Structure

```
imagebuilder/
├── AGENTS.md                          ← this file
├── LICENSE                            ← Apache 2.0
├── NOTICE                             ← Third-party components (generated by go-licenses)
├── go.mod
├── go.sum
│
├── api/
│   ├── v1alpha1/                      ← CRD Go types (generated by kubebuilder)
│   │   ├── vmimage_types.go
│   │   ├── platformprovider_types.go
│   │   ├── providerconfig_types.go
│   │   └── zz_generated.deepcopy.go  ← generated, do not touch
│   └── provider/v1/
│       └── provider.proto             ← gRPC interface for providers
│
├── pkg/
│   ├── plugin/
│   │   ├── platform/
│   │   │   └── interface.go           ← Plugin interface (stable, never change in a breaking way)
│   │   ├── registry.go                ← Runtime registry of active providers
│   │   └── grpc/
│   │       └── adapter.go             ← gRPC → Plugin interface adapter
│   │
│   ├── provisioner/
│   │   ├── interface.go               ← Provisioner interface
│   │   ├── cloudinit/
│   │   ├── shell/
│   │   ├── file/
│   │   └── powershell/
│   │
│   ├── builder/
│   │   ├── engine.go                  ← Build engine
│   │   └── qemu_iso_backend.go        ← QEMU/libvirt ISO backend
│   │
│   └── controller/
│       ├── vmimage/                   ← VMImage reconciler
│       ├── provider/                  ← PlatformProvider package controller
│       └── buildpod/                  ← Pod assembler (init container logic)
│
├── pkg/provider/sdk/                  ← External provider SDK and gRPC server helpers
├── pkg/security/netguard/             ← Runtime SSRF protection helpers
│
├── plugins/                           ← Built-in platform providers (compile-time)
│   ├── vsphere/
│   ├── openstack/
│   ├── aws/
│   ├── azure/
│   └── gcp/
│
├── cmd/
│   └── operator/
│       └── main.go                    ← Entry point, plugin imports
│
└── config/
    ├── crd/                           ← Generated CRD YAMLs
    ├── rbac/                          ← ClusterRole, ServiceAccount
    └── samples/                       ← Example manifests
```

---

## Key Design Decisions (ADRs)

### ADR-001: Apache-Compatible Build Engine
**Decision**: The build engine is implemented with Apache-compatible components.
**Reason**: Runtime dependencies must remain redistributable.
**Alternative**: Custom build engine with QEMU/libvirt + diskimage-builder + direct Cloud APIs.

### ADR-002: Providers as Separate Containers (Crossplane Model)
**Decision**: Platform providers run as dedicated Kubernetes pods, not as Go plugins (.so).
**Reason**: Go's plugin mechanism (.so) is impractical (same Go version required, no Windows,
no cross-compile). Separate containers enable independent versioning, arbitrary languages,
and clean license separation (a proprietary provider does not contaminate the core project).
**Communication**: gRPC over ClusterIP Service. Production deployments use
NetworkPolicies and mTLS when providers are outside the strict namespace-local
trust boundary.

### ADR-003: Provisioners as Init Containers
**Decision**: Complex provisioners (Ansible, Chef) run as Kubernetes restartable init containers.
**Reason**: The builder coordinates sequential execution with per-step config/status files while each complex tool runs in an isolated OCI image.
No gRPC overhead needed; the contract is simple (`/workspace/provisioners/step-N/config.json` and `status.json`).
Community provisioners need no SDK, only an OCI image that follows the file-path contract.

### ADR-004: LGPL Dependencies Only as External Processes
**Decision**: libvirt and libguestfs are accessed via CLI/socket only.
**Reason**: Static or dynamic linking against LGPL would restrict Apache-2.0 redistribution.
Process communication is license-safe.

### ADR-005: Protobuf Schema is a Versioned Contract
**Decision**: `api/provider/v1/provider.proto` is the stable interface.
**Consequence**: Breaking changes → `api/provider/v2/`. Never remove fields from v1.
Field numbers in Proto are immutable.

### ADR-006: No Go Plugin Mechanism
**Decision**: No .so files, no use of the Go plugin package.
**Alternative for compile-time plugins**: `init()` pattern with blank import in main.go
(analogous to database/sql drivers). For runtime plugins: gRPC containers.

---

## Production Hardening Baseline

The Helm chart is the production installation path. `config/deploy/operator.yaml`
is explicitly a development manifest and is marked with
`imagebuilder.io/profile=development` and
`imagebuilder.io/production-ready=false`.

Production defaults and invariants:

- Operator, builder, uploader, provisioner, and provider images are configurable
  and digest-pinnable. Provider packages should be `repository@sha256:...`.
- `providerSecurity.requireMTLS`, `requireDigest`, and `requireSignature`
  default to `true` in the chart and are enforced through PlatformProvider
  admission policy.
- Provider image signatures are verified through the rendered Kyverno
  `ClusterPolicy` when `imageSignaturePolicy.enabled=true`.
- Admission is fail-closed for `VMImage`, `ProviderConfig`, and
  `PlatformProvider` webhooks.
- DNS/URL SSRF checks are performed at admission and again at runtime before
  builder source download and uploader/provider endpoint use. Runtime checks
  reject unresolved hosts and blocked/private IP ranges.
- NetworkPolicies default-deny the operator namespace, restrict provider gRPC
  to operator pods, and render scoped build/upload egress policies for each
  configured tenant workload namespace.
- KVM builds require dedicated build-node selectors:
  `imagebuilder.io/kvm=true` and `imagebuilder.io/dedicated=imagebuilder`.
  Build pods receive the matching `NoSchedule` toleration.
- RBAC grants `update` only on controller-owned `VMImage` and
  `PlatformProvider` resources because controller-runtime writes their
  finalizers through the normal resource endpoint. `ProviderConfig` remains
  read-only (`get/list/watch`); no CRD create/delete permission is granted, and
  Secrets remain `get`-only.
- The Helm chart renders operator-namespace `ResourceQuota` and `LimitRange`
  guardrails by default. Tenant namespaces need their own quotas sized for
  approved build concurrency and storage.

Important metrics:

- `imagebuilder_build_duration_seconds`
- `imagebuilder_queue_duration_seconds`
- `imagebuilder_active_builds`
- `imagebuilder_provisioner_duration_seconds`
- `imagebuilder_upload_duration_seconds`
- `imagebuilder_upload_bytes_total`
- `imagebuilder_upload_throughput_bytes_per_second`
- `imagebuilder_register_duration_seconds`
- `imagebuilder_provider_healthy`
- `imagebuilder_failures_total`
- `imagebuilder_cleanup_failures_total`

---

## Build & Development

```bash
# Generate CRDs (after changes to api/v1alpha1/)
make generate
make manifests

# Run operator locally (against current kubeconfig context)
make run

# Tests
make test

# Focused tests with local caches when needed
GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/gomod TMPDIR=$PWD/.cache/tmp go test ./...

# Helm validation
helm lint ./charts/imagebuilder
helm template imagebuilder ./charts/imagebuilder

# License check (before every release)
go install github.com/google/go-licenses@latest
go-licenses check ./...

# Build provider image
docker build -t ghcr.io/yourorg/imagebuilder-provider-aws:dev ./plugins/aws/

# Update NOTICE file
go-licenses report ./... > NOTICE
```

---

## Not Yet Decided / TODO

- [x] Image caching strategy (PVC-backed source cache with checksum keys, TTL invalidation, checksum-mismatch refetch, and retention policy)
- [x] Parallelization of builds (global and max concurrent builds per node via Lease scheduler)
- [x] Webhook validation for VMImage, ProviderConfig, and PlatformProvider specs
- [x] OCI signing policy for provider/imagebuilder images (Kyverno/Sigstore policy hooks)
- [x] Metrics (Prometheus) — build duration, queue time, active builds, provider health, upload/register duration, upload throughput, failures, cleanup failures
- [x] Provider production policies (mTLS, digest pinning, signature opt-in, allowed registries)
- [x] Runtime SSRF hardening for source downloads and provider endpoints
- [x] Tenant-aware NetworkPolicies for build/upload Jobs
- [x] Namespace ResourceQuota and LimitRange guardrails in the production Helm chart
- [ ] Multi-arch support (arm64)
- [x] Windows: Cloudbase-Init/Sysprep live E2E gate
- [ ] Set up provider SDK repository
