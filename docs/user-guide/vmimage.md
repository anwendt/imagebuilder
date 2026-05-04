# VMImage Authoring Guide

`VMImage` is the primary user-facing API. It describes the source image,
provisioning steps, target platforms, and build runtime settings.

## Minimal Shape

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: VMImage
metadata:
  name: ubuntu-base
spec:
  os:
    family: linux
    distribution: ubuntu
    version: "24.04"
    arch: amd64
  source:
    type: cloud-image
    url: https://example.com/base.img
    checksum: sha256:...
  targets:
    - providerConfigRef:
        name: aws-eu-central-1
      format: vmdk
```

## Source Types

| Type | Usage |
|---|---|
| `cloud-image` | Starts from an existing cloud image or generic disk image. |
| `iso` | Boots an installer ISO through QEMU. |
| `marketplace` | Reserved for platform-native marketplace sources. |

When `source.url` is set, `source.checksum` is required. Admission validation
also rejects unsafe URL shapes such as raw IPs, private IPs, loopback, and
non-HTTPS schemes.

## Architecture

`spec.os.arch` supports:

| Value | Meaning | Local QEMU behavior |
|---|---|---|
| `amd64` | x86_64 image build. Default. | Uses `qemu-system-x86_64`, PCI VirtIO devices, and `accel=tcg` or `accel=kvm:tcg`. |
| `arm64` | AArch64 image build. | Uses `qemu-system-aarch64`, `virt`, `cpu max`, MMIO VirtIO networking, and `accel=tcg` or `accel=kvm:tcg`. |

For local ISO builds, set `QEMU_SYSTEM_PATH_ARM64` in the builder image when
the ARM64 QEMU binary is not at `/usr/bin/qemu-system-aarch64`. Set
`QEMU_EFI_CODE_PATH_ARM64` when the ARM64 guest needs explicit AAVMF/EDK2
firmware. For remote builds, the selected provider receives `osArch` and must
map it to the native platform architecture.

## ISO Installer Media

For ISO builds, `spec.source.installer` automates unattended installation.

Linux options:

- `nocloud`
- `autoinstall`
- `kickstart`
- `preseed`

Windows option:

- `autounattend`

Example:

```yaml
source:
  type: iso
  url: https://releases.ubuntu.com/24.04/ubuntu.iso
  checksum: sha256:...
  installer:
    type: autoinstall
    userData: |
      #cloud-config
      autoinstall:
        version: 1
```

Windows ISO builds can also install and configure Cloudbase-Init during first
logon. The MSI path must be visible to Windows Setup, usually from an attached
driver/tools ISO:

```yaml
source:
  type: iso
  url: https://example.com/windows-server-2022.iso
  checksum: sha256:...
  installer:
    type: autounattend
    windows:
      virtioDriverPath: E:\viostor\2k22\amd64
      cloudbaseInitMsi: E:\CloudbaseInitSetup.msi
      cloudbaseInit:
        metadataServices:
          - cloudbaseinit.metadata.services.configdrive.ConfigDriveService
          - cloudbaseinit.metadata.services.nocloudservice.NoCloudConfigDriveService
        addUserToLocalGroups:
          - Administrators
```

When `cloudbaseInitMsi` is set, the builder writes
`cloudbase-init.conf` and `cloudbase-init-unattend.conf`, enables the
`cloudbase-init` service, and keeps WinRM bootstrap separate from final image
hygiene checks.

The live Windows ISO E2E gate exercises Cloudbase-Init installation, WinRM
readiness, a PowerShell provisioner, generated credential cleanup, Sysprep, and
artifact conversion:

```bash
IMAGEBUILDER_WINDOWS_E2E=1 \
IMAGEBUILDER_WINDOWS_E2E_ISO_PATH=/srv/images/windows-server-2022.iso \
IMAGEBUILDER_WINDOWS_E2E_CLOUDBASE_INIT_MSI='E:\CloudbaseInitSetup.msi' \
IMAGEBUILDER_WINDOWS_E2E_VIRTIO_DRIVER_PATH='E:\viostor\2k22\amd64' \
make test-e2e-windows-cloudbase
```

The MSI and driver paths are guest-visible paths. The runner must attach or
provide media that makes those paths available to Windows Setup.

## Guest Access

Guest access is required when provisioning needs to connect into a booted VM.

Linux should use SSH:

```yaml
build:
  guestAccess:
    protocol: ssh
    host: 127.0.0.1
    hostPort: 2222
    credentials:
      generate:
        sshKey: true
      injection:
        method: cloud-init
```

Windows should use WinRM:

```yaml
build:
  guestAccess:
    protocol: winrm
    host: 127.0.0.1
    hostPort: 55986
    credentials:
      generate:
        password: true
      injection:
        method: autounattend
    winrm:
      https: true
      insecureSkipVerify: true
```

`insecureSkipVerify` is only accepted for generated ephemeral WinRM bootstrap
credentials. It is intended for short-lived self-signed installer certificates.

## Provisioners

Provisioners run in declared order. Built-ins include:

- `cloud-init`
- `shell`
- `file`
- `powershell`
- `sysprep`
- `ansible`
- `chef`
- `puppet`
- `saltstack`
- `custom`

Example:

```yaml
provisioners:
  - type: shell
    inline: |
      sudo apt-get update
      sudo apt-get install -y nginx
  - type: ansible
    image: ghcr.io/yourorg/provisioner-ansible@sha256:...
    playbook: s3://bucket/playbooks/harden.yml
```

Provisioner content can also be loaded from a Git repository. `ref` is required;
use an immutable commit SHA in production. If `path` points to a directory, all
regular files are loaded in lexicographic order and executed as separate
provisioner steps of the same type:

```yaml
provisioners:
  - type: shell
    source:
      git:
        url: https://github.com/yourorg/image-scripts.git
        ref: 7f6e5d4c3b2a190817263544536271809abcdef0
        path: ubuntu
```

With files `ubuntu/01-base.sh` and `ubuntu/02-hardening.sh`, `01-base.sh` runs
first and `02-hardening.sh` runs second. Git source URLs must use HTTPS and pass
the same SSRF host checks as image downloads.

Private Git repositories are supported through a Secret in the `VMImage`
namespace. Put credentials in the Secret, never in the Git URL:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: private-git
type: Opaque
stringData:
  token: ghp_example
---
provisioners:
  - type: shell
    source:
      git:
        url: https://github.com/yourorg/private-image-scripts.git
        ref: 7f6e5d4c3b2a190817263544536271809abcdef0
        path: ubuntu
        auth:
          secretRef:
            name: private-git
            tokenKey: token
```

For basic authentication, store `username` and `password` keys instead. Local
build pods mount the Secret read-only and pass only file paths to the builder.
Remote providers receive credentials only in the transient build request.

## Supply Chain Policy For Provisioners

Provisioner images can be restricted by registry and digest pinning:

```yaml
build:
  security:
    provisionerImages:
      allowedRegistries:
        - ghcr.io/yourorg
      requireDigest: true
      verifySignature: true
```

The core enforces digest pinning and registry allow-listing. Signature
verification is enforced by cluster admission policy such as Kyverno or
Sigstore Policy Controller.

## Artifact Storage

Upload jobs require PVC-backed artifact storage:

```yaml
build:
  artifactStorage:
    type: pvc
    pvc:
      size: 50Gi
      retainPolicy: OnFailure
```

Retention policies:

| Policy | Behavior |
|---|---|
| `Never` | Delete operator-created PVCs after success or failure. |
| `OnFailure` | Keep PVCs only for failed builds. |
| `Always` | Keep PVCs after success and failure. |

Existing PVCs referenced by `claimName` are never deleted by the operator.

## Source Cache

Local builds can reuse verified source images through a PVC-mounted source
cache:

```yaml
build:
  cache:
    ref: imagebuilder-source-cache
    ttl: 168h
    retainPolicy: Always
```

Cache semantics:

| Setting | Behavior |
|---|---|
| Cache key | Checksum-addressed as `<algorithm>-<hex>.img`, for example `sha256-...img`. The source URL is not part of the key. |
| `ttl` | Optional age limit based on the cache file mtime. Expired entries are deleted and fetched again. Omitted means no age-based expiry. |
| Checksum mismatch | A corrupt cached entry is deleted and refetched. A newly downloaded source with the wrong checksum fails the build and is not written to cache. |
| `retainPolicy: Always` | Keep verified entries for reuse. This is the default. |
| `retainPolicy: Never` | Use a valid matching entry once, then delete it. Fresh downloads are not persisted to cache. |

`build.cacheRef` remains supported as a legacy shorthand for `build.cache.ref`.
When both fields are set, they must reference the same PVC.
