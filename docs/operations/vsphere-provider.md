# vSphere Provider Operations

The vSphere provider supports local build output registration for `ova`, `ovf`,
and `vmdk` targets.

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
```

The aliases `user` and `pass` are accepted for compatibility, but `username`
and `password` are preferred.

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

## Required vCenter Permissions

The service account needs permissions to:

- Read datacenters, folders, networks, clusters, hosts, resource pools, and datastores.
- Create datastore directories and upload/delete datastore files in the staging path.
- Import vApps into the selected resource pool.
- Create VMs in the selected folder.
- Mark imported VMs as templates when `markAsTemplate=true`.
- Create and update Content Library items when `contentLibrary` or
  `contentLibraryID` is configured.

## Validation

The default unit test suite covers validation, upload/register orchestration,
SDK behavior, and VMDK registration. A govmomi simulator integration test covers
real HealthCheck, datastore upload, register, and cleanup behavior without a
live vCenter:

```bash
IMAGEBUILDER_VSPHERE_SIMULATOR_TESTS=1 go test ./plugins/vsphere
```

## Limitations

- vSphere remote builds are not supported yet. Set `spec.build.mode: local`.
- Bare `vmdk` registration is intentionally a datastore-artifact reference. Use
  OVA/OVF if a template is required.
