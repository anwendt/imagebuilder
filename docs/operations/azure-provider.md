# Azure Provider

The Azure PlatformProvider publishes local build artifacts as Azure Managed
Images. It accepts fixed VHD artifacts, uploads them as Azure Page Blobs, and
can optionally publish a Compute Gallery image version after the managed image
is created. Remote mode can register an image directly from an existing Azure
Snapshot or Managed Disk, or boot a temporary Azure VM and run supported
provisioners through Azure VM Run Command before image capture.

## ProviderConfig

Use a digest-pinned provider image in production:

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: PlatformProvider
metadata:
  name: azure
spec:
  package: ghcr.io/anwendt/imagebuilder-provider-azure@sha256:<digest>
  security:
    allowedRegistries:
      - ghcr.io/anwendt
    requireDigest: true
    verifySignature: true
```

### Authentication

Production clusters should use Workload Identity or Managed Identity. Set
`extra.authMode` to one of:

| Mode | Required secret keys | Required extra keys |
| --- | --- | --- |
| `workloadIdentity` | `subscriptionId`, `tenantId`, `clientId` | `storageAccount` plus Azure Workload Identity token mount/env from the platform |
| `managedIdentity` | `subscriptionId` | `storageAccount`, optional `managedIdentityID` |
| `clientSecret` | `subscriptionId`, `tenantId`, `clientId`, `clientSecret`, `storageAccountKey` | `storageAccount` |

When `ProviderConfig.spec.credentials.secretRef.key` is set to `credentials`,
that Secret value must be a JSON object. Example for client-secret auth:

```json
{
  "subscriptionId": "<azure-subscription-id>",
  "tenantId": "<entra-tenant-id>",
  "clientId": "<service-principal-client-id>",
  "clientSecret": "<service-principal-client-secret>",
  "storageAccountKey": "<staging-storage-account-key>"
}
```

Alternatively, omit `secretRef.key` and store those keys directly in the Secret
data map.

The `ProviderConfig` uses `region` as the Azure image location and the following
`extra` keys:

| Key | Required | Description |
| --- | --- | --- |
| `resourceGroup` | yes | Resource group for managed images and optional gallery versions. |
| `storageAccount` | yes | Staging storage account for uploaded VHDs. |
| `storageContainer` | no | Staging container; defaults to `imagebuilder`. |
| `blobPrefix` | no | Prefix for uploaded VHD blobs. |
| `hyperVGeneration` | no | `V1` or `V2`; defaults to `V2`. |
| `osState` | no | `generalized` or `specialized`; defaults to `generalized`. |
| `storageAccountType` | no | Managed image disk type; defaults to `Standard_LRS`. |
| `diskSizeGiB` | no | Optional OS disk size override. |
| `remote.vmSize` | no | Temporary VM size for remote builds with provisioners; defaults to `Standard_B2s`. |
| `remote.networkInterfaceId` | only for remote provisioners or SSH guest access | Existing NIC resource ID attached to the temporary build VM. |
| `remote.managedIdentityId` | for required evidence with `publisher: provider` | User-assigned managed identity attached to the temporary build VM for registry and KMS access. Not used with `publisher: controller`. |
| `remote.evidence.cosignKeyRef` | for required evidence with `publisher: provider` | Non-secret Cosign KMS URI used to sign VM-image provenance, for example `azurekms://vault/key`. Not used with `publisher: controller`. |
| `remote.verifyBoot` | no | `"true"` boots a throwaway VM from the published image after registration and smoke-tests it (ADR-011). Requires `remote.networkInterfaceId`, a Linux image and `osState: generalized`. |
| `galleryName` | no | Compute Gallery name. |
| `galleryImageName` | no | Gallery image definition name. |
| `galleryVersion` | no | Gallery version. When empty, the provider uses the logical image version from the VMImage (`spec.evidence.subject.version`, else the annotation `imagebuilder.io/image-version`; it must be `Major.Minor.Patch`), and only without either falls back to a timestamp version. |
| `galleryTargetRegions` | no | Comma-separated replication regions. |
| `pageUploadConcurrency` | no | Parallel Azure Page Blob range uploads; defaults to `4`. |
| `pageUploadChunkMiB` | no | Upload chunk size in MiB; defaults to `4` and must be 512-byte aligned. |
| `authMode` | no | `clientSecret`, `workloadIdentity`, or `managedIdentity`; defaults to `clientSecret`. |
| `managedIdentityID` | no | User-assigned managed identity client ID for `managedIdentity` mode. |
| `tokenFilePath` | no | Workload identity token path override. |
| `cloud` | no | `public`, `government`, or `china`; defaults to `public`. |
| `armEndpoint` | no | Custom ARM endpoint for private clouds. |
| `armAudience` | no | ARM token audience override for private clouds. |
| `authorityHost` | no | Entra authority host override for private clouds. |
| `httpProxy` | no | HTTP proxy URL for provider API traffic. |
| `httpsProxy` | no | HTTPS proxy URL for provider API traffic. With `publisher: controller` the guest also retries the evidence upload through it when the direct upload fails. |
| `noProxy` | no | Comma-separated proxy bypass list, for example cluster-local names and metadata endpoints. |

Example proxy values in `spec.extra`:

```yaml
extra:
  httpProxy: http://proxy.example.com:8080
  httpsProxy: http://proxy.example.com:8443
  noProxy: localhost,127.0.0.1,.svc,.cluster.local,169.254.169.254
```

## Artifact Requirements

Azure Managed Images from blob sources require a fixed VHD stored as a Page Blob.
The provider validates the VHD footer before upload:

- file size must be 512-byte aligned
- footer cookie must be `conectix`
- disk type must be fixed
- footer checksum must be valid
- footer current size must match the file size minus the footer

Dynamic VHD/VHDX/QCOW2/raw artifacts must be converted to fixed VHD before they
are handed to the Azure provider.

## Required Azure Permissions

The identity needs, at minimum:

- Blob container create/read/write/delete on the staging storage account.
- With `publisher: controller` and no `storageAccountKey` (Workload or Managed
  Identity): `Storage Blob Delegator` on the staging storage account (to sign
  the user delegation SAS, action
  `Microsoft.Storage/storageAccounts/blobServices/generateUserDelegationKey/action`,
  included in the starter role) in addition to `Storage Blob Data Contributor`.
- `Microsoft.Compute/images/*` on the target resource group.
- For remote builds with provisioners, `Microsoft.Compute/disks/*` and
  `Microsoft.Compute/virtualMachines/*` on the target resource group.
- If gallery publishing is enabled, `Microsoft.Compute/galleries/images/versions/*`
  on the gallery resource group.

A starter custom role is available at
`docs/operations/azure-provider-role.json`. Narrow `AssignableScopes` to the
target resource group and storage account scopes before assigning it.

For AKS Workload Identity, see
`config/samples/platformprovider-azure-workload-identity.yaml`. It enables
ServiceAccount token projection and the required pod label for the provider pod.

## Remote Sources

Remote mode supports direct registration from an existing Azure source:

- `source.type: snapshot` with `source.providerRef` set to an Azure Snapshot resource ID
- `source.type: managed-disk` with `source.providerRef` set to a Managed Disk resource ID
- `source.type: marketplace` with `source.marketplaceRef` set to a cloud image
  reference containing `publisher`, `offer`, `sku`, and `version`

When no provisioners are configured, the provider registers the source directly.
For Snapshot and Managed Disk sources with provisioners, the provider copies
the source into a temporary managed OS disk, starts a temporary VM, runs
supported provisioners through Azure VM Run Command, deallocates/generalizes
the VM, creates the Managed Image from the VM, and deletes the temporary VM and
disk. Marketplace sources always use the temporary VM flow because Azure
requires a VM capture to turn a marketplace image plus provisioners into a
custom Managed Image.

Supported remote provisioners:

| OS family | Provisioners |
| --- | --- |
| Linux | `shell`, `file`, `evidence` (final for `publisher: provider`; any position for `publisher: controller`) |
| Windows | `powershell`, `file`, final `evidence` (`publisher: provider` only) |

Remote provisioner mode and explicit `spec.build.guestAccess.protocol: ssh`
require `extra.remote.networkInterfaceId`. The NIC must already exist, be
dedicated to the build at runtime, and allow the Azure VM agent to reach Azure
control-plane endpoints needed by Run Command. Use
`extra.osState: specialized` when the source should not be generalized; the
default `generalized` mode expects the image content/provisioners to run the
appropriate Linux deprovision or Windows sysprep step before image capture.

Required evidence additionally needs:

- `extra.remote.managedIdentityId`, whose identity has registry push and KMS
  signing permissions;
- `extra.remote.evidence.cosignKeyRef`, containing a non-secret Cosign KMS URI;
- Syft, Trivy, ORAS, Cosign, and registry authentication available to the final
  evidence script.

The provider principal needs permission to assign the configured user-assigned
identity to the temporary VM. The build identity itself receives only the data
plane permissions needed for the evidence repository and signing key.

## Controller-Published Evidence (ADR-011)

With `spec.evidence.publisher: controller` the guest only collects evidence;
it needs no registry access, managed identity or signing key. The evidence
provisioner script writes `/var/lib/imagebuilder/evidence/sbom.cdx.json`,
`trivy-report.json` and optionally `collector.json` (see the user guide for the
outbox contract). Provisioners after the evidence provisioner are allowed, for
example a `waagent -deprovision` scheduled through `systemd-run` that must run
last. Linux only.

Right after the evidence provisioner succeeds the provider:

1. deletes a stale blob and creates a write-only (create + write), HTTPS-only
   SAS valid for 15 minutes for exactly one blob
   `<blobPrefix>/evidence/<buildID>.tar.gz` in the staging container. With
   `storageAccountKey` it is a service SAS, otherwise a user delegation SAS;
2. runs one Run Command that checks both required files, streams
   `tar czf - -C /var/lib/imagebuilder evidence` with `curl --fail -X PUT
   -H 'x-ms-blob-type: BlockBlob'` to the SAS URL (first without any proxy,
   then through `extra.httpsProxy` when set), deletes the outbox and proves it
   is gone;
3. checks the blob size (more than 900 KiB fails the build), downloads and
   validates the bundle (both files present, valid JSON) and records its
   sha256 and blob name in the operation reference.

The SAS is never logged: it only appears inside the Run Command script, and
errors or command output are redacted before they are returned. Anyone allowed
to read Run Command instance views of the build VM could still see it during
its 15 minute lifetime; it only allows writing that one blob.

The blob is the provider-side persisted copy. Every Done response returns the
bundle until the core acknowledges it (then the blob is deleted) or the build is
cleaned up (`CleanupRemoteBuild` deletes it). A Done replay whose blob is gone
fails with `EvidenceBundleLost`; a build that finishes without a collected
outbox fails with `EvidenceBundleMissing`.

## Verify Boot

`extra.remote.verifyBoot: "true"` adds a smoke test after registration and
before the build reports Done:

- a VM `ib-<buildID>-verify` is created from the published image (the gallery
  image version, else the managed image) with the same `remote.vmSize` and the
  configured `remote.networkInterfaceId` (free again because the build VM was
  deleted), key-only login and `customData` cloud-config that sets the hostname
  and writes a marker file with a random nonce;
- one Run Command checks: `cloud-init status --wait` ends in `status: done`,
  the hostname from the user-data is set (the computer name is the same value),
  the marker file holds the nonce and `walinuxagent` is active;
- the verify VM and its OS disk are deleted immediately (the NIC stays).

Success adds the hygiene check `verify-boot-passed`. A failed check fails the
build with `VerifyBootFailed: <reason>` and deletes the gallery image version
and managed image this build created (images that existed before the build are
never deleted). Costs: one extra VM of the build size for a few minutes.

## Live E2E

Run the opt-in live test with a real fixed VHD:

```bash
AZURE_E2E=1 \
AZURE_E2E_SUBSCRIPTION_ID=... \
AZURE_E2E_TENANT_ID=... \
AZURE_E2E_CLIENT_ID=... \
AZURE_E2E_CLIENT_SECRET=... \
AZURE_E2E_LOCATION=westeurope \
AZURE_E2E_RESOURCE_GROUP=rg-imagebuilder-prod \
AZURE_E2E_STORAGE_ACCOUNT=imagebuilderprod \
AZURE_E2E_STORAGE_ACCOUNT_KEY=... \
AZURE_E2E_VHD_PATH=/path/to/fixed.vhd \
make test-e2e-azure
```

## Metrics And Runbook

The standalone provider exposes `/metrics` on `:8080` by default. Disable it
with `--metrics-listen=""`. See `docs/operations/azure-provider-runbook.md` for
rollback, quota, throttling, cleanup, and troubleshooting guidance.

## Release

`Azure Provider Image Release` builds, pushes, signs, and publishes the provider
image digest. After a release, update samples with:

```bash
AZURE_PROVIDER_DIGEST=sha256:<digest> make update-azure-provider-samples
```
