---
document-id: ADR-011
title: Guest-Collected, Controller-Published VM Image Evidence
status: Accepted
date: 2026-11-20
deciders: Platform Engineering
supersedes: ADR-008 (in part: who publishes and signs), ADR-009 (in part: vSphere evidence limitation)
superseded-by: —
classification: Internal
---

# ADR-011 — Guest-Collected, Controller-Published VM Image Evidence

## Status

**Accepted** (decision 2026-11-20, implemented in v0.6.0)

---

## Context

One logical golden image (for example `ubuntu-2604-base` 1.2.0) is built once per
platform: Azure Compute Gallery through the Azure provider (Run Command) and vSphere
Content Library through the vSphere provider (Guest Operations). Promotion needs, per
platform build, an SBOM, a vulnerability report, signed SLSA provenance and immutable
registry references.

ADR-008 lets the guest publish its own evidence: the evidence provisioner pushes to OCI
and signs through the temporary VM's managed identity and KMS. That has three problems:

1. It only works where the guest has a cloud identity. ADR-009 records that vSphere
   has none, so vSphere images cannot carry evidence.
2. Every guest needs ORAS, Cosign, registry push rights and signing authority. The
   signing key effectively lives in a short-lived but untrusted build VM.
3. The format differs from the container supply chain. The ImageFactory
   (artifactbuild ADR-0010) already signs with one cosign key and attaches SLSA
   provenance, CycloneDX SBOM and Trivy report as cosign v3 attestations stored as OCI
   referrers. Consumers would need two verification paths.

## Decision

Evidence is **collected in the guest, retrieved by the provider and published and
signed by the core controller**. It is selected per VMImage with
`spec.evidence.publisher: controller`; `provider` (the default) keeps ADR-008
unchanged.

### 1. API (additive)

| Field | Meaning |
|---|---|
| `spec.evidence.publisher` | `provider` (default, ADR-008) or `controller` (this ADR) |
| `spec.evidence.subject.{name,version,platform}` | logical image, e.g. `ubuntu-2604-base`, `1.2.0`, `azure` / `vsphere-fci-c042` |
| `spec.evidence.registryRepository` | e.g. `oci://tsigitopsacr.azurecr.io/tsigitops/vmimages` |
| `spec.evidence.sbomFormat` | now also accepts `cyclonedx-json`; required for `controller` |
| `status.evidence.descriptorRef`, `indexRef` | new immutable references |
| `status.evidence.publisher`, `bundleDigest`, `publishJobRef` | new, non-secret |

Admission (`publisher: controller`): build mode `remote`; `required: true`;
`sbomFormat: cyclonedx-json`; scanner `trivy`; complete subject (OCI-safe name,
version usable as tag, lowercase platform); exactly one `evidence` provisioner.
Provisioners **after** it are allowed. The Azure `waagent -deprovision` scheduled with
`systemd-run` has to run last, after the outbox was retrieved. The evidence provisioner
must not carry credentials: no `env[].valueFrom`, no credential-like env names
(`*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*KEY*` variants, `*SAS*`), no URL with user
information in its inline script.

### 2. Guest outbox contract

The evidence provisioner (a script owned by the image repository; reference in
`examples/evidence/ubuntu-evidence.sh`) writes only into
`/var/lib/imagebuilder/evidence/`:

| File | Required | Content |
|---|---|---|
| `sbom.cdx.json` | yes | CycloneDX JSON (`bomFormat: CycloneDX`) |
| `trivy-report.json` | yes | Trivy JSON report (`SchemaVersion` present) |
| `collector.json` | no | collector tool versions |

The guest receives only non-secret variables (`IMAGEBUILDER_EVIDENCE_OUTBOX`,
`IMAGEBUILDER_EVIDENCE_SUBJECT_*`, `IMAGEBUILDER_EVIDENCE_FAIL_ON_SEVERITY`, ...). It may
download Syft and Trivy pinned by sha256 through the build proxy and must remove its
tools and caches.

Right after the evidence provisioner succeeds, in the same reconcile step, the provider
packs the outbox as gzip tar (`evidence/...`). It validates the bundle: compressed size
at most **900 KiB**, both required files present, valid JSON, no other files, no links,
no path escapes. It then deletes the outbox in the guest and proves that it is gone.
Any failure fails the build closed (`EvidenceCollectionFailed`, `EvidenceBundleTooLarge`,
`EvidenceBundleInvalid`).

- **vSphere:** a guest process runs `tar czf` into
  `/var/tmp/imagebuilder-evidence-<build>.tar.gz`. The provider downloads it with
  `InitiateFileTransferFromGuest` over the existing Guest Operations login, through the
  same transport as NFC uploads, so ESXi host names resolve through the vCenter
  inventory (`hostresolve.go`). The build VM guard (ADR-009) is checked before every
  guest operation.
- **Azure:** the provider creates a write-only (create+write), HTTPS-only SAS valid for
  15 minutes for exactly one blob
  `<blobPrefix>/evidence/<build>.tar.gz` in its staging container. It is a service SAS
  with the storage account key, otherwise a user delegation SAS (provider identity
  needs *Storage Blob Delegator*). Run Command streams
  `tar czf - ... | curl --fail -X PUT -H 'x-ms-blob-type: BlockBlob' --data-binary @-`,
  first without a proxy and then through ProviderConfig `httpsProxy`, deletes the outbox
  and prints a completion marker. The SAS is redacted from every error and output the
  provider propagates and is never logged.

### 3. Provider → core (proto additive, ADR-005)

- `RemoteBuildResponse.evidence_bundle` (`content`, `sha256`, `media_type`
  `application/vnd.imagebuilder.evidence.bundle.v1.tar+gzip`) is returned with every
  Done response.
- `RemoteEvidencePolicy.publisher` and `subject`.
- `RemoteBuildRequest.evidence_bundle_acknowledged` and `image_version`.

Done responses can repeat (controller retries), so the provider persists the bundle
until the core acknowledges it: vSphere on the provider volume
(`<downloadDir>/evidence/<build>.tar.gz`), Azure as the staging blob. Its sha256 lives
in the operation reference. After the core stored the bundle it sends one acknowledging
`ReconcileRemoteBuild`, and the provider deletes its copy. `CleanupRemoteBuild` also
deletes it. A lost copy without acknowledgement fails closed (`EvidenceBundleLost`).

### 4. Core publication

When a controller-mode remote build is Done with a bundle, the controller:

1. validates the bundle again (digest, structure) and enforces
   `spec.evidence.failOnSeverity` against the Trivy report. Only fixable findings
   (`FixedVersion` set) count, the same as `trivy --ignore-unfixed`; suppressed
   findings are already absent from the report (`EvidenceVulnerabilityPolicyFailed`);
2. renders the **VM image descriptor** (JSON, artifactType
   `application/vnd.tsigitops.vmimage.v1+json`). It holds the subject, provider and
   ProviderConfig, image refs from `RemoteImageRef` (gallery version ID or library item
   ID, location, checksum, tags), the source (marketplace URN, or URL plus sha256, or
   provider ref) and OS. It also holds the provisioners: Git sources as URL, ref and
   path, inline ones only as sha256. Finally the evidence digests, the VMImage
   namespace/name/uid/generation/revision/build ID, the imagebuilder version and the
   build timestamps;
3. renders an **SLSA v1 provenance** predicate (buildType
   `https://github.com/anwendt/imagebuilder/buildtypes/vmimage-remote/v1`). Its
   resolved dependencies are the source and the Git provisioner repositories, with a
   `gitCommit` digest when the ref is a commit. Its byproducts are the bundle and
   descriptor digests;
4. stores bundle, descriptor and provenance in a Secret in the **controller
   namespace** and runs a publication **Job** there. Its steps are modelled on the
   ImageFactory `attach-slsa`/`sign-files` steps, use the same ORAS v1.3.0 and Cosign
   images (both pinned by digest) and run non-root, read-only root filesystem, no
   service account token:
   - `prepare` (cosign image): verify the bundle sha256 and unpack it.
   - `push-descriptor` (oras): `oras push` the descriptor to
     `<repo>/<name>:<version>-<platform>`.
   - `sign-descriptor` (cosign): `cosign sign` plus `cosign attest` with
     `--type https://slsa.dev/provenance/v1`, `--type cyclonedx` and
     `--type https://tsigitops.t-systems.net/attestations/trivy/v1`, all with
     `--use-signing-config=false --key … --tlog-upload=false`.
   - `publish-index` (oras): create or update the OCI index `<repo>/<name>:<version>`.
     It merges with the existing index and replaces the own platform entry, identified
     by the annotation `io.tsigitops.vmimage.platform`. Concurrent writers are handled
     by retrying until the own entry is still present after a settle delay, then by
     verifying it. An existing tag that is not an imagebuilder VM image index is never
     overwritten.
   - `sign-index` (cosign): sign the index version that contained the own entry.
   - `report` (oras): `oras discover` plus a manifest fetch per referrer. It writes the
     exact digests of signature, provenance, SBOM and Trivy attestation bundles (by
     `dev.sigstore.bundle.predicateType`) and of the index and its signature as the
     termination message.
5. reads the termination message and sets `status.evidence`:
   - `signatureRef`, `provenanceRef`, `sbomRef` and `vulnerabilityReportRef` are the
     respective referrer bundles;
   - `descriptorRef` and `indexRef` are the new fields.

   All refs are `oci://<repo>/<name>@sha256:<digest>`.

The VMImage becomes **Ready only after the Job succeeded**. Any failure is fail-closed
(`EvidencePublicationFailed`), and the step message comes from the failed container. On
success the controller deletes Job and Secret. On failure it deletes the Secret and
keeps the Job for 24 h for its logs. VMImage deletion removes both. If the publisher is
not configured, controller-mode VMImages fail at build start
(`EvidencePublisherNotConfigured`), not after an hour of building.

### 5. Signing key and format

The VM image descriptor is signed with the **ImageFactory cosign key**. The public key
is `imagefactory-core/keys/imagefactory-cosign.pub`, the transparency log is off, and
there is **no separate key** for VM images. The format is identical to the ImageFactory
(artifactbuild ADR-0010): cosign v3 Sigstore bundles as OCI referrers, the signature
plus the three attestations above. One verification policy covers containers and VM
images:

```bash
cosign verify --key imagefactory-cosign.pub --insecure-ignore-tlog <repo>/<name>@<descriptor digest>
cosign verify-attestation --key imagefactory-cosign.pub --insecure-ignore-tlog \
  --type https://slsa.dev/provenance/v1 <repo>/<name>@<descriptor digest>
```

### 6. Verify boot (opt-in, both providers)

`ProviderConfig.extra.remote.verifyBoot: "true"`: after registration the provider boots
one throwaway VM from the **published** artifact, with the same size, and deletes it at
once. It smoke-tests:

- `cloud-init status` reports `status: done`;
- the hostname from the provided user-data is set;
- a marker file written by that user-data exists with the expected nonce;
- the platform agent is active (vSphere `open-vm-tools`/`vmtoolsd`, Azure `walinuxagent`).

Per platform:

- **vSphere:** deploys `imagebuilder-verify-…` from the Content Library template into
  the build folder (guarded like build VMs). It delivers user-data only through the OVF
  vApp `user-data` property, exactly as VMware Aria Automation does, which re-tests the
  v0.5.11 vApp property rule. The temporary root password is derived like the build
  login, and the checks run through Guest Operations.
- **Azure:** creates a VM from the gallery version (or managed image) with customData,
  reuses the configured NIC after the build VM is gone, checks through Run Command and
  deletes VM and OS disk.

The provider reports Done only after the verification. A failure fails the build with
`VerifyBootFailed` and deletes the artifact this build just published. On vSphere that
is the library item carrying the build marker; on Azure, the gallery version or managed
image created by this build. Success adds the hygiene check `verify-boot-passed`.

### 7. Azure gallery version

If ProviderConfig `galleryVersion` is empty, the Azure provider uses the version the
core resolves from `spec.evidence.subject.version` or the annotation
`imagebuilder.io/image-version`. It must be `Major.Minor.Patch`.

## Deviations from the requested contract (documented)

- **Bundle Secret not owner-referenced:** the Secret and Job live in the controller
  namespace, because the push and cosign credentials must not be copied into tenant
  namespaces and ownerReferences cannot cross namespaces. They carry the VMImage
  identity as labels, and their lifecycle is driven by the controller (deleted on
  success and on VMImage deletion).
- **Index entries:** the index is written with one manifest descriptor per line, so the
  merge needs no JSON processor in the step images. Tags written by other tools are
  rejected, not merged.
- **Concurrent index writers:** registries offer no conditional manifest PUT, so the
  publish step retries until its own entry survives a settle delay. Each writer signs
  the index version it verified; `status.evidence.indexRef` names that version, not
  necessarily the current tag.
- **Referrer digests:** `oras discover` does not show annotations, so the report step
  fetches each referrer manifest to map it by predicate type.
- **Vulnerability policy:** the controller enforces `failOnSeverity` on the retrieved
  Trivy report as a second gate after the guest script.
- **Registration before publication:** the platform image exists in the gallery or
  library before the controller publishes its evidence; VMImage `Ready` and the OCI
  descriptor are the promotion signals. Failed evidence publication leaves the
  registered image in place, while failed verify boot removes it.
- **Azure verify VM hostname:** `computerName` equals the user-data hostname, because
  Azure sets the hostname from it as well. The marker file is the proof that the
  user-data was applied.
- **Linux only:** controller-mode evidence collection and verify boot support Linux
  guests; Windows fails with a clear error.

## Rationale

- One trust anchor and one format for containers and VM images.
- No signing key or registry credential inside an untrusted build VM; the guest needs
  no cloud identity, so vSphere gets evidence.
- The size bound keeps reports in the registry, not in etcd, and the bundle fits one
  Secret.
- The controller owns status, policy and fail-closed admission as in ADR-007/008, and
  the providers stay responsible for platform access only.

## Alternatives Considered

- **Keep ADR-008 and add a vSphere identity:** vSphere has no workload identity for VMs;
  credentials would have to be injected into the guest.
- **Provider publishes and signs:** this puts the signing key into every provider pod and
  duplicates the publication logic per provider.
- **Controller pushes in-process (oras-go/cosign libraries):** this adds large
  dependencies and signing code to the operator binary, while the Job reuses the exact,
  already approved ImageFactory tooling.
- **Return reports through status or gRPC only:** the reports are too large for status,
  and repeated Done responses need durable provider-side storage anyway.

## Consequences

### Positive

- vSphere and Azure images get identical, verifiable evidence; ADR-009's limitation is
  lifted.
- Consumers verify VM images with the ImageFactory key and policy.
- The guest toolchain shrinks to Syft and Trivy.
- Verify boot proves that the published artifact boots with platform-style user-data.

### Negative

- The operator namespace holds the ImageFactory cosign key and registry push credentials.
- The ImageFactory key now signs VM image descriptors as well, so key rotation affects
  both.
- The bundle is limited to 900 KiB compressed; very large SBOMs must be trimmed or
  compressed better.
- Verify boot adds one short-lived VM per build and some minutes of build time.

## Related Documents

- [ADR-005 — Protobuf Schema as Versioned Provider Contract](ADR-005-protobuf-versioned-contract.md)
- [ADR-007 — Remote Build as Provider-Owned Execution Mode](ADR-007-remote-build-provider-contract.md)
- [ADR-008 — Provider-Neutral VM Image Evidence Contract](ADR-008-vm-image-evidence-contract.md)
- [ADR-009 — vSphere Build VM Guard and Cloud-Image Remote Builds](ADR-009-vsphere-build-vm-guard-and-cloud-image-builds.md)
- [User guide: VM image evidence](../user-guide/evidence.md)
- [Operations: controller evidence publisher](../operations/evidence-publisher.md)
- ImageFactory artifactbuild ADR-0010 (signing and attestation format)
