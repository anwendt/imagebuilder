# vSphere Provider Operations

The vSphere provider supports local build output registration for `ova`, `ovf`,
and `vmdk` targets.

## Provider Image Release

The standalone vSphere PlatformProvider image is built from
`cmd/provider-vsphere`:

```bash
make build-provider-vsphere
make docker-build-provider-vsphere REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
make docker-push-provider-vsphere REGISTRY=ghcr.io/anwendt IMAGE_TAG=v0.5.0
VSPHERE_PROVIDER_DIGEST=sha256:<digest> make sign-provider-vsphere REGISTRY=ghcr.io/anwendt
VSPHERE_PROVIDER_DIGEST=sha256:<digest> make update-vsphere-provider-samples REGISTRY=ghcr.io/anwendt
```

`docker-push-provider-vsphere` publishes a multi-arch image for `linux/amd64`
and `linux/arm64` by default. Override `VSPHERE_PROVIDER_PLATFORMS` when a
release needs a narrower platform set. The `vSphere Provider Image Release`
workflow performs the same lifecycle in GitHub Actions with provenance, SBOM,
and keyless Cosign signing.

## Image Registration Modes

| Format | Production behavior |
|---|---|
| `ova` | Imports the local OVA through the vSphere NFC lease API and marks the imported VM as a template by default. |
| `ovf` | Imports the OVF descriptor and referenced local files through the vSphere NFC lease API and marks the imported VM as a template by default. |
| `vmdk` | Uploads the VMDK to the configured datastore and returns the datastore path as an artifact reference. It does not create a bootable VM template on its own. |

Use `ova` or `ovf` when the target should be a vSphere VM template. Use `vmdk`
only when the downstream process expects a datastore artifact.

If `contentLibrary` or `contentLibraryID` is set, `ova` and `ovf` targets are
published as Content Library OVF items instead of VM templates.

## Secret Format

The referenced Secret must contain:

```yaml
stringData:
  username: administrator@vsphere.local
  password: redacted
  # Required only for remote builds with provisioners.
  guestUsername: imagebuilder
  guestPassword: redacted
```

The aliases `user` and `pass` are accepted for compatibility, but `username`
and `password` are preferred. The aliases `remoteGuestUsername` and
`remoteGuestPassword` are accepted for remote guest credentials, but
`guestUsername` and `guestPassword` are preferred.

## ProviderConfig

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: ProviderConfig
metadata:
  name: vsphere-prod-dc01
spec:
  provider: vsphere
  credentials:
    secretRef:
      name: vsphere-credentials
  endpoint: https://vcenter.example.com/sdk
  insecure: false
  extra:
    datacenter: DC0
    datastore: LocalDS_0
    cluster: DC0_C0
    folder: /DC0/vm/templates
    network: "VM Network"
    uploadPathPrefix: imagebuilder
    diskProvisioning: thin
    markAsTemplate: "true"
    contentLibrary: golden-images
```

Required `extra` keys:

| Key | Purpose |
|---|---|
| `datacenter` | Datacenter inventory name or path. |
| `datastore` | Datastore used for staging uploads and OVF imports. |

Optional `extra` keys:

| Key | Default | Purpose |
|---|---|---|
| `folder` | `vm` | VM folder for imported templates. |
| `cluster` | default resource pool | Cluster used to resolve the default resource pool. |
| `resourcePool` | derived | Explicit resource pool path. Takes precedence over `cluster`. |
| `host` | empty | ESXi host for imports that require host placement. |
| `network` | OVF default | Target vSphere network for OVF network mappings. |
| `ovfNetworkName` | empty | OVF network name to map when the descriptor has no discoverable network section. |
| `uploadPathPrefix` | `imagebuilder` | Datastore folder prefix for staged artifacts. |
| `diskProvisioning` | `thin` | OVF import disk provisioning mode. |
| `deployment` | empty | OVF deployment option. |
| `ipAllocationPolicy` | empty | OVF IP allocation policy. |
| `ipProtocol` | empty | OVF IP protocol. |
| `annotation` | empty | VM annotation applied during import. |
| `markAsTemplate` | `true` | Marks imported OVA/OVF VMs as templates. |
| `contentLibrary` | empty | Content Library name for OVA/OVF publishing. |
| `contentLibraryID` | empty | Content Library ID; takes precedence over `contentLibrary`. |
| `requireManifest` | `false` | Requires `.mf` manifest files when publishing to Content Library. |
| `buildFolder` | none | **Required for remote builds.** Absolute inventory path of the dedicated build folder; its name must start with `imagebuilder`. Build VMs (`imagebuilder-*`) live and die only here (ADR-009). The provider creates the folder below an existing parent. |
| `protectedNames` | empty | Comma separated, case-insensitive substrings. ProviderConfig inventory names, build VM names and the build folder must not contain them. |
| `downloadProxy` | empty | Proxy used only to download `cloud-image` sources and to fetch Git provisioner sources (`source.git`), not for vCenter traffic. |
| `downloadDir` | `/var/lib/imagebuilder/uploads` | Scratch directory for downloaded source images. |
| `buildDiskGiB` | image default | Grows the first disk of a cloud-image build VM to this size. |
| (evidence) | | `vsphere-template-config-clean` is reported when the published template VM was inspected: all vApp property values and defaults are empty (the definitions stay, `user-data` must exist), and no guestinfo configuration remains. It does not boot a clone. |
| `requireUserDataProperty` | `true` | The post-publish check fails when the template has no `user-data` vApp property definition, because clones could not receive cloud-init through the OVF environment. Set `"false"` for templates that do not use OVF user-data. |
| `templateFolder` | none | **Required with `remote.publishTo`.** Absolute inventory path of the folder that receives the published template VM. It must be outside the build folder (which holds `imagebuilder-*` build VMs only). Created below an existing parent when missing. |
| `revokeBuildCredential` | `true` | After the last provisioner the provider locks the temporary build login (`passwd -l` through a delayed systemd timer), then verifies that Guest Operations reject it, and fails the build with `BuildCredentialNotRevoked` otherwise. Only for cloud-image builds that use the derived build login; credentials from the Secret are never locked. Evidence check: `vsphere-build-credential-revoked`. |
| `remote.publishTo` | none | `content-library` publishes the finished remote build as VM template item of `contentLibrary`/`contentLibraryID` and destroys the build VM. |
| `remote.verifyBoot` | `false` | `"true"` boots a throwaway VM from the published template before the build is reported Done (see [Verify Boot](#verify-boot-adr-011)). Requires `remote.publishTo: content-library`; anything else fails validation. Evidence check: `verify-boot-passed`. |
| `httpProxy` | empty | HTTP proxy URL for provider API traffic. |
| `httpsProxy` | empty | HTTPS proxy URL for provider API traffic. |
| `noProxy` | empty | Comma-separated proxy bypass list, for example vCenter and cluster-local endpoints. |

Example proxy values:

```yaml
extra:
  httpProxy: http://proxy.example.com:8080
  httpsProxy: http://proxy.example.com:8443
  noProxy: localhost,127.0.0.1,.svc,.cluster.local,169.254.169.254
```

## Required vCenter Permissions

The service account needs permissions to:

- Read datacenters, folders, networks, clusters, hosts, resource pools, and datastores.
- Create datastore directories and upload/delete datastore files in the staging path.
- Import vApps into the selected resource pool.
- Create VMs in the selected folder.
- Mark imported VMs as templates when `markAsTemplate=true`.
- Clone VMs/templates, power temporary VMs on/off, run VMware Guest Operations,
  and destroy failed temporary clones for remote builds with provisioners.
- Create and update Content Library items when `contentLibrary` or
  `contentLibraryID` is configured.
- Evidence outbox retrieval (ADR-011): Guest Operations program execution and
  file transfer from the guest (`VirtualMachine.GuestOperations.Execute`,
  `VirtualMachine.GuestOperations.Query`, `VirtualMachine.GuestOperations.Modify`).
- `remote.verifyBoot`: deploy a VM from a Content Library VM template
  (`ContentLibrary.DeployLibraryItem`, `VirtualMachine.Provisioning.DeployTemplate`,
  `VirtualMachine.Inventory.CreateFromExisting`), edit vApp properties
  (`VApp.ApplicationConfig`), power on/off, delete the verify VM and delete the
  library item of a build whose verify boot failed (`ContentLibrary.DeleteLibraryItem`).

## Validation

The default unit test suite covers validation, upload/register orchestration,
SDK behavior, and VMDK registration. A govmomi simulator integration test covers
real HealthCheck, datastore upload, register, and cleanup behavior without a
live vCenter:

```bash
IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS=1 go test ./plugins/vsphere
```

## Remote Builds

vSphere remote mode starts from an existing vSphere VM or template reference in
`spec.source.providerRef`. The reference may be a managed object ID such as
`vm-123` or an inventory path resolvable by the configured datacenter.

The provider also accepts the provider-neutral marketplace form. vSphere does
not expose a cloud marketplace image API, so the provider resolves
`spec.source.marketplaceRef` through ProviderConfig mapping keys and then clones
the mapped VM/template:

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: ProviderConfig
spec:
  provider: vsphere
  extra:
    marketplace.canonical.ubuntu.24.04.latest: content-library:/Golden Images/ubuntu-24-template
---
apiVersion: imagebuilder.io/v1alpha1
kind: VMImage
spec:
  source:
    type: marketplace
    marketplaceRef:
      publisher: Canonical
      offer: ubuntu
      sku: "24.04"
      version: latest
```

Mapping values may be:

- `content-library:/Library Name/Item Name` for Content Library OVF or VM Template items.
- `library-item:<item-id>` for a concrete Content Library item ID.
- VM/template names or inventory paths.
- Managed object IDs such as `vm-123`.

Content Library OVF and VM Template items are deployed as the temporary remote
build VM, then the normal provisioner and template finalization flow continues.
If no mapping is present, the provider tries conservative fallback names such as
`ubuntu-24-template` and `ubuntu-24.04-template`. Use a concrete
`source.providerRef` when the source template must be pinned exactly.

When `spec.provisioners` is empty, the provider clones the source and marks the
clone as a template when `markAsTemplate=true`. When provisioners are present,
the provider clones the source as a temporary VM, powers it on, waits for VMware
Tools, runs supported provisioners through VMware Guest Operations, shuts the
guest down, and then marks the result as a template.

Provisioner `env` (literal values) reaches the guest process as environment
variables and overrides the Linux defaults (`HOME`, `PATH`, `DEBIAN_FRONTEND`,
`LANG`) of the same name. `valueFrom` is rejected for remote builds. The
`RemoteProvisioner.env` field (number 8) is an additive extension of the v1
provider contract (ADR-005).

Supported remote provisioners:

| OS family | Provisioners |
| --- | --- |
| Linux | `shell`, `file` |
| Windows | `powershell`, `file` |

Remote provisioner mode requires VMware Tools in the guest and guest
credentials in the ProviderConfig Secret:

| Secret key | Description |
| --- | --- |
| `guestUsername` | Guest user used by VMware Guest Operations. |
| `guestPassword` | Guest password used by VMware Guest Operations. |

The vCenter identity also needs permissions to clone VMs/templates, power VMs
on/off, run Guest Operations programs, and destroy failed temporary clones.
Set `spec.build.mode: remote`, `spec.source.type: snapshot`, and
`spec.source.providerRef` to the source VM/template reference.

When a remote request explicitly uses `spec.build.guestAccess.protocol: ssh`,
`extra.network` is required. The provider retargets existing NICs on the cloned
VM to that network, or adds a VMXNET3 NIC when the source VM/template has no
NIC. Guest Operations remains the default remote provisioner transport and does
not require SSH reachability.

The real vSphere E2E test is opt-in because it uploads artifacts to a live
datastore and may import OVA/OVF artifacts as templates or Content Library
items depending on the configured extras:

```bash
VSPHERE_E2E=1 \
VSPHERE_E2E_ENDPOINT=https://vcenter.example.com/sdk \
VSPHERE_E2E_USERNAME=administrator@vsphere.local \
VSPHERE_E2E_PASSWORD=... \
VSPHERE_E2E_DATACENTER=DC0 \
VSPHERE_E2E_DATASTORE=vsanDatastore \
VSPHERE_E2E_ARTIFACT_PATH=/path/to/image.vmdk \
go test ./plugins/vsphere -run TestVSphereProviderLive_E2E -count=1 -v -timeout=60m
```

Optional variables:

| Variable | Default |
|---|---|
| `VSPHERE_E2E_FORMAT` | inferred from artifact extension; `vmdk` fallback |
| `VSPHERE_E2E_INSECURE` | `true` |
| `VSPHERE_E2E_TIMEOUT` | `55m` |
| `VSPHERE_E2E_UPLOAD_PATH_PREFIX` | `imagebuilder-e2e` |
| `VSPHERE_E2E_IMAGE_NAME` | timestamped `vsphere-e2e-*` |
| `VSPHERE_E2E_OS_FAMILY` | `linux` |
| `VSPHERE_E2E_CHECKSUM` | unset |
| `VSPHERE_E2E_FOLDER` | unset |
| `VSPHERE_E2E_CLUSTER` | unset |
| `VSPHERE_E2E_RESOURCE_POOL` | unset |
| `VSPHERE_E2E_HOST` | unset |
| `VSPHERE_E2E_NETWORK` | unset |
| `VSPHERE_E2E_OVF_NETWORK_NAME` | unset |
| `VSPHERE_E2E_DISK_PROVISIONING` | provider default `thin` |
| `VSPHERE_E2E_CONTENT_LIBRARY` | unset |
| `VSPHERE_E2E_CONTENT_LIBRARY_ID` | unset |
| `VSPHERE_E2E_MARK_AS_TEMPLATE` | provider default `true` |
| `VSPHERE_E2E_REQUIRE_MANIFEST` | provider default `false` |

## Cloud-Image Builds and the Build VM Guard

`spec.source.type: cloud-image` with `source.url` (https) and `source.checksum`
(sha256) builds from a public cloud image OVA, for example the Ubuntu
`*-server-cloudimg-amd64.ova`. The provider downloads and verifies the OVA,
deploys it as build VM, gives it a temporary per-build root login through
cloud-init, runs the `shell`/`file` provisioners through Guest Operations and
(with `remote.publishTo: content-library`) publishes a VM template item. An
existing library item is never modified.

The provider only creates, changes and destroys VMs that are named
`imagebuilder-*` and sit directly in `extra.buildFolder`. See
[ADR-009](../adr/ADR-009-vsphere-build-vm-guard-and-cloud-image-builds.md).

`ProviderConfig.spec.endpoint` may be omitted when the Secret contains the
vCenter address in the key `server`.

Example: `examples/argocd/vsphere-ubuntu2604-hardened`.

## Evidence Outbox Retrieval (ADR-011)

vSphere supports the `evidence` provisioner only with
`spec.evidence.publisher: controller` and Linux guests; with the default
publisher (`provider`, ADR-008) the build fails validation because the guest has
no registry identity. The provider:

1. runs the evidence provisioner like a `shell` provisioner through Guest
   Operations. The script gets `IMAGEBUILDER_EVIDENCE_OUTBOX`,
   `IMAGEBUILDER_BUILD_ID`, `IMAGEBUILDER_IMAGE_NAME`, `IMAGEBUILDER_SOURCE_REF`,
   `IMAGEBUILDER_EVIDENCE_SUBJECT_{NAME,VERSION,PLATFORM}`,
   `IMAGEBUILDER_EVIDENCE_SBOM_FORMAT`, `IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER`
   and `IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY` plus its literal `env`. No
   credentials are passed to the guest.
2. right after it succeeds, packs `/var/lib/imagebuilder/evidence` with
   `tar czf` into `/var/tmp/imagebuilder-evidence-<build>.tar.gz`, downloads the
   file over the Guest Operations file transfer URL (ESXi host URLs use the same
   host resolution and proxy handling as NFC uploads), rejects bundles above
   900 KiB, validates it (`sbom.cdx.json` CycloneDX and `trivy-report.json`
   Trivy JSON required, `collector.json` optional, nothing else) and deletes the
   outbox and the packed file in the guest. Every failure fails the build
   (`EvidenceCollectionFailed`, `EvidenceBundleTooLarge`, `EvidenceBundleInvalid`).
3. keeps the bundle on the provider volume (`<downloadDir>/evidence/<build>.tar.gz`,
   mode 0600) and records its sha256 in the operation reference
   (`evidenceSha256`). Provisioners after the evidence provisioner still run.
4. returns the bundle with every Done response until the core acknowledges it
   (`evidence_bundle_acknowledged`), then deletes the file. `CleanupRemoteBuild`
   deletes it as well. A build that reaches publication without a retrieved
   bundle fails with `EvidenceBundleMissing`; a bundle that disappeared from the
   provider volume before the core stored it (for example an `emptyDir` after a
   provider restart) fails with `EvidenceBundleLost` and needs a rebuild.

The core controller publishes and signs the evidence; see
[ADR-011](../adr/ADR-011-controller-published-vm-image-evidence.md).

## Verify Boot (ADR-011)

With `remote.verifyBoot: "true"` the provider does not report Done right after
publishing. The operation reference gets `verifyBoot=pending` and the next
reconcile runs the verification as its own step:

1. deploys the published Content Library VM template into the build folder as
   `imagebuilder-verify-<image>-<build>` (the build VM guard applies; a leftover
   of the same name is removed first),
2. sets **only** the value of the OVF vApp property `user-data` (base64
   cloud-config), the way VMware Aria Automation delivers cloud-init. A template
   without the `user-data` property definition fails the verification,
3. the cloud-config sets the hostname `ib-verify-<8 characters of the build ID>`,
   writes a marker file with a random nonce and sets a temporary root password
   derived from the provider secret and the build ID (as for the build VM; SSH
   password login stays disabled),
4. powers the VM on, waits up to 15 minutes until Guest Operations accept that
   login and checks: `cloud-init status` reports `status: done`, the hostname,
   the marker content and an active `open-vm-tools` (or `vmtoolsd`) service,
5. always destroys the verify VM.

On success the operation reference records `verifyBoot=passed` and the hygiene
checks contain `verify-boot-passed`. On failure the build fails with
`VerifyBootFailed: <reason>` and the published library item is deleted if its
description carries this build's marker; `CleanupRemoteBuild` also deletes such
an item while `verifyBoot=pending`. Cost: one additional VM with the template's
size for a few minutes; no other resources are created.

Operation reference fields added for ADR-011: `evidenceSha256`, `verifyBoot`
(`pending`/`passed`), `credentialRevoked`, `templateVerified` (the last two keep
the hygiene checks of the publish step for later Done responses).

## Limitations

- vSphere remote builds require an existing VM/template source with VMware Tools
  and guest credentials when provisioners are configured.
- Bare `vmdk` registration is intentionally a datastore-artifact reference. Use
  OVA/OVF if a template is required.
