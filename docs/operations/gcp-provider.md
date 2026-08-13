# GCP Provider

The GCP provider publishes local build artifacts as Compute Engine Custom Images
and supports provider-native remote image or snapshot copies.

## ProviderConfig

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: ProviderConfig
metadata:
  name: gcp-prod
  namespace: images
spec:
  provider: gcp
  region: eu
  credentials:
    secretRef:
      name: gcp-provider
      key: service-account.json
  extra:
    project: my-gcp-project
    gcsBucket: my-image-imports
    gcsPrefix: imagebuilder
    imageFamily: ubuntu-2404
    guestOsFeatures: VIRTIO_SCSI_MULTIQUEUE,UEFI_COMPATIBLE
```

`project` is required. It may also be read from `projectId` in the credentials
JSON. `gcsBucket` is required for local builds. The bucket must already exist;
the provider intentionally does not manage bucket lifecycle.

Credentials may be supplied as service-account JSON using the Secret keys
`serviceAccountJSON`, `serviceAccountKey`, or `credentials`. When no JSON is
provided, Application Default Credentials are used, including GKE Workload
Identity Federation.

## Local builds

Use target format `gcetarball`:

```yaml
spec:
  build:
    mode: local
  targets:
    - providerConfigRef:
        name: gcp-prod
      format: gcetarball
```

The builder converts the disk to raw format, pads it sparsely to a whole GiB,
and creates the GCE import archive as a sparse old-GNU tar.gz containing exactly
`disk.raw`. Disks larger than Compute Engine's 2 TiB manual-import limit are
rejected. The provider uploads that tar.gz to a checksum-addressed GCS object,
imports it through its HTTPS Storage URL, creates a Custom Image, waits for the
image to reach `READY`, and removes the staging object.
Set `extra.retainUpload: "true"` to retain it.

Required IAM permissions include object create/delete in the staging bucket,
`compute.images.create`, `compute.images.get`, `compute.images.delete`, and
`compute.projects.get`.

## Remote image and snapshot copy

Remote mode can create an image from an existing GCP image or snapshot without
booting a guest:

```yaml
spec:
  build:
    mode: remote
  source:
    type: cloud-image
    providerRef: projects/debian-cloud/global/images/debian-12-bookworm-v20260801
  targets:
    - providerConfigRef:
        name: gcp-prod
      format: gcetarball
```

For snapshots use `source.type: snapshot` and a
`projects/<project>/global/snapshots/<name>` reference. Remote requests are
idempotent through a deterministic Compute `requestId`.

Remote guest provisioning is intentionally not supported yet. Use local mode
when provisioners or guest access are configured. Remote image copies return a
provider hygiene attestation because no temporary guest is booted.

## Optional settings

| Key | Purpose |
|---|---|
| `gcsPrefix` | Object prefix; default `imagebuilder` |
| `storageLocation` | Image storage location; falls back to `spec.region` |
| `imageName` | Default Custom Image name |
| `imageFamily` | Compute image family |
| `description` | Image description |
| `architecture` | `X86_64` or `ARM64`; normally inferred from the VMImage |
| `guestOsFeatures` | Comma-separated Compute guest OS features |
| `licenses` | Comma-separated license URLs |
| `retainUpload` | Keep the GCS staging archive after image creation |
| `computeEndpoint` | Optional Compute API endpoint override; `spec.endpoint` has precedence |
| `storageEndpoint` | Optional Cloud Storage endpoint override |
