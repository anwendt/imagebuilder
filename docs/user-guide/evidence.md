# VM Image Evidence (controller publisher)

With `spec.evidence.publisher: controller`
([ADR-011](../adr/ADR-011-controller-published-vm-image-evidence.md)), a remote
build produces signed, immutable supply-chain evidence for every platform build
of a logical image. The format is the same as the ImageFactory's for container
images. Responsibilities are split like this:

| Who | Does |
|---|---|
| Guest (your evidence script) | writes `sbom.cdx.json` and `trivy-report.json` into `/var/lib/imagebuilder/evidence` |
| Provider (Azure, vSphere) | retrieves the outbox as `tar.gz` (max 900 KiB), validates it, deletes it in the guest, returns it with the Done response |
| Controller | publishes the VM image descriptor, signs it, attaches SLSA provenance, CycloneDX SBOM and Trivy report, merges the per-version OCI index |

The guest never sees registry or signing credentials.

## VMImage

```yaml
apiVersion: imagebuilder.io/v1alpha1
kind: VMImage
metadata:
  name: ubuntu-2604-base-azure
spec:
  build:
    mode: remote
  evidence:
    required: true
    publisher: controller
    subject:
      name: ubuntu-2604-base     # OCI repository name below registryRepository
      version: 1.2.0             # OCI tag of the index
      platform: azure            # e.g. azure, vsphere-fci-c042
    sbomFormat: cyclonedx-json
    vulnerabilityScanner: trivy
    failOnSeverity: [HIGH, CRITICAL]
    registryRepository: oci://tsigitopsacr.azurecr.io/tsigitops/vmimages
  provisioners:
    - type: shell
      source: {git: {url: https://git.example.com/images.git, ref: <commit>, path: scripts/ubuntu/20-hardening.sh}}
    - type: evidence
      source: {git: {url: https://git.example.com/images.git, ref: <commit>, path: scripts/ubuntu/90-evidence.sh}}
      env:
        - name: HTTPS_PROXY
          value: http://build-proxy.example.com:3128
    # Allowed with publisher=controller: steps after the evidence collection,
    # for example the Azure deprovision that must run last.
    - type: shell
      inline: systemd-run --on-active=30 /usr/sbin/waagent -force -deprovision+user
  targets:
    - providerConfigRef: {name: azure-gallery}
      format: vhd
```

Admission rules for `publisher: controller`:

- the build must be remote (`spec.build.mode: remote`);
- `required: true`, `sbomFormat: cyclonedx-json` and `vulnerabilityScanner: trivy`;
- the subject is complete: `name` and `platform` lowercase (`[a-z0-9._-]`), and
  `version` a valid OCI tag;
- there is exactly one `evidence` provisioner, and other provisioners may follow it;
- the evidence provisioner carries no credentials: no `valueFrom`, no env names like
  `*PASSWORD*`/`*TOKEN*`/`*SECRET*`/`*KEY*`/`*SAS*`, and no `user:password@` URLs.
  Git `auth` for fetching the script is fine, because the provider fetches it and the
  credentials never reach the guest.

The logical image version (`subject.version`, or the annotation
`imagebuilder.io/image-version` when there is no subject) also names the Azure
gallery version when the ProviderConfig has no `galleryVersion`.

## Outbox contract

The evidence provisioner writes only into `/var/lib/imagebuilder/evidence`
(`$IMAGEBUILDER_EVIDENCE_OUTBOX`):

| File | Required | Content |
|---|---|---|
| `sbom.cdx.json` | yes | CycloneDX JSON (`"bomFormat": "CycloneDX"`) |
| `trivy-report.json` | yes | Trivy JSON report |
| `collector.json` | no | tool versions, free JSON |

No other files, no symlinks, at most 900 KiB as `tar.gz`. The provider also sets
these non-secret variables:

- `IMAGEBUILDER_EVIDENCE_OUTBOX`, `IMAGEBUILDER_EVIDENCE_PUBLISHER`
- `IMAGEBUILDER_BUILD_ID`, `IMAGEBUILDER_IMAGE_NAME`, `IMAGEBUILDER_SOURCE_REF`
- `IMAGEBUILDER_EVIDENCE_SUBJECT_NAME`, `IMAGEBUILDER_EVIDENCE_SUBJECT_VERSION`,
  `IMAGEBUILDER_EVIDENCE_SUBJECT_PLATFORM`
- `IMAGEBUILDER_EVIDENCE_SBOM_FORMAT`, `IMAGEBUILDER_EVIDENCE_VULNERABILITY_SCANNER`,
  `IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY`

[`examples/evidence/ubuntu-evidence.sh`](../../examples/evidence/ubuntu-evidence.sh)
is a reference collector for Ubuntu. It downloads Syft and Trivy pinned by sha256,
honours the build proxy and Trivy DB mirrors, scans the root filesystem, fails on
fixable findings of `failOnSeverity`, and removes its tools and caches. Copy it into
your image repository and pin the tool versions you approve.

The controller checks the Trivy report again: a fixable finding (`FixedVersion` set)
with a `failOnSeverity` severity fails the build with
`EvidenceVulnerabilityPolicyFailed`. Unfixed findings do not block.

## What gets published

Assume `registryRepository: oci://R`, subject `N`, `V`, `P`. The controller
publishes:

| Artifact | Reference |
|---|---|
| VM image descriptor (artifactType `application/vnd.tsigitops.vmimage.v1+json`) | `R/N:V-P` |
| cosign signature, SLSA v1 provenance, CycloneDX SBOM, Trivy report | cosign v3 bundles as OCI referrers of the descriptor |
| OCI index of all platforms of `V` (entry annotation `io.tsigitops.vmimage.platform`) | `R/N:V`, signed |

The descriptor lists the subject, provider, registered image refs (gallery version
ID or library item ID, location, checksum), the source, the provisioner Git sources,
the VMImage identity, the imagebuilder version and timestamps. It is signed with the
ImageFactory cosign key (no transparency log).

`status.evidence` after a successful publication:

```yaml
status:
  phase: Ready
  evidence:
    status: passed
    publisher: controller
    bundleDigest: sha256:...
    descriptorRef: oci://R/N@sha256:...
    indexRef: oci://R/N@sha256:...
    signatureRef: oci://R/N@sha256:...          # cosign signature bundle
    provenanceRef: oci://R/N@sha256:...         # SLSA provenance attestation
    sbomRef: oci://R/N@sha256:...               # CycloneDX attestation
    vulnerabilityReportRef: oci://R/N@sha256:...# Trivy attestation
```

While the controller publishes, the VMImage is in phase `Uploading`, and the
`Evidence` step is `Running`. It becomes `Ready` only after the publication Job
succeeded; any failure fails the VMImage.

Verify with the ImageFactory public key:

```bash
cosign verify --key imagefactory-cosign.pub --insecure-ignore-tlog R/N@<descriptor digest>
cosign verify-attestation --key imagefactory-cosign.pub --insecure-ignore-tlog \
  --type cyclonedx R/N@<descriptor digest>
cosign verify-attestation --key imagefactory-cosign.pub --insecure-ignore-tlog \
  --type https://tsigitops.t-systems.net/attestations/trivy/v1 R/N@<descriptor digest>
```

## Verify boot

Set `remote.verifyBoot: "true"` on the ProviderConfig to boot one throwaway VM from
the published artifact before the build is reported Done:

- Azure: VM from the gallery version, with customData.
- vSphere: deployed from the Content Library template, with user-data in the OVF
  vApp `user-data` property as VMware Aria Automation passes it.

The checks are:

- cloud-init `status: done`;
- the hostname from the user-data is set;
- the marker file from the user-data exists;
- `walinuxagent` (Azure) or `open-vm-tools` (vSphere) is active.

The VM is deleted right away. A failure fails the build with `VerifyBootFailed` and
removes the artifact this build just published. Success adds the hygiene check
`verify-boot-passed`. Provider details are in
[Azure](../operations/azure-provider.md) and [vSphere](../operations/vsphere-provider.md).

## Failure reasons

| Reason | Meaning |
|---|---|
| `EvidencePublisherNotConfigured` | the controller evidence publisher is disabled or incomplete; the build does not start |
| `RemoteEvidenceRejected` | the outbox is missing, invalid or larger than 900 KiB, or the provider lost the bundle |
| `RemoteEvidenceMissing` | the provider completed without a bundle |
| `EvidenceVulnerabilityPolicyFailed` | fixable findings with a `failOnSeverity` severity |
| `EvidencePublicationFailed` | the publication Job failed; `status.evidence.message` names the step |
| `VerifyBootFailed` | the published artifact did not pass the verify boot |
