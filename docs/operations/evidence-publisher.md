# Controller Evidence Publisher (ADR-011)

The operator publishes VM image evidence for VMImages with
`spec.evidence.publisher: controller`
([ADR-011](../adr/ADR-011-controller-published-vm-image-evidence.md),
[user guide](../user-guide/evidence.md)). It is disabled by default; while it is
disabled, such VMImages fail at build start with `EvidencePublisherNotConfigured`.

## Chart values

```yaml
evidencePublisher:
  enabled: true
  registryCredentialsSecret: vmimages-acr-push      # kubernetes.io/dockerconfigjson, operator namespace
  cosignKeySecret:
    name: imagefactory-cosign                        # operator namespace
    keyKey: cosign.key
    passwordKey: cosign.password
  # orasImage / cosignImage default to the ImageFactory step images (pinned digests)
  httpsProxy: ""                                     # optional egress proxy of the Job
  noProxy: ""
  jobTimeout: 30m
  resources:
    requests: {cpu: 50m, memory: 64Mi}
    limits: {cpu: 500m, memory: 256Mi}
```

Rendering fails if `enabled` is true and one of the two Secret names is missing. The
chart then renders:

- the controller flags below;
- a namespaced `Role`/`RoleBinding` in the operator namespace (secrets
  get/list/create/update/delete, jobs, pods);
- a `NetworkPolicy` that allows the publication pods egress to 443, the configured
  proxy and DNS.

The operator also gets `IMAGEBUILDER_VERSION` (chart `appVersion`), which is recorded
in descriptors and provenance.

| Flag | Default |
|---|---|
| `--evidence-publisher-enabled` | `false` |
| `--evidence-namespace` | `$POD_NAMESPACE` |
| `--evidence-oras-image` | `ghcr.io/oras-project/oras:v1.3.0@sha256:6ce045ce…` |
| `--evidence-cosign-image` | `cgr.dev/chainguard/cosign:latest-dev@sha256:a790a4f2…` |
| `--evidence-registry-secret` | — (required when enabled) |
| `--evidence-cosign-secret` | — (required when enabled) |
| `--evidence-cosign-key-key` / `--evidence-cosign-password-key` | `cosign.key` / `cosign.password` |
| `--evidence-cpu-request`, `--evidence-memory-request`, `--evidence-cpu-limit`, `--evidence-memory-limit` | `50m`, `64Mi`, `500m`, `256Mi` |
| `--evidence-https-proxy`, `--evidence-no-proxy` | empty |
| `--evidence-job-timeout` | `30m` |
| `--imagebuilder-version` | `$IMAGEBUILDER_VERSION` |

## Secrets

- **Registry push:** a `kubernetes.io/dockerconfigjson` Secret with push rights on
  `spec.evidence.registryRepository`, for example an ACR token scoped to
  `tsigitops/vmimages/*` with content write and read.
- **Cosign key:** the ImageFactory key pair. The private key is under `keyKey`, its
  password under `passwordKey`, and the public key is
  `imagefactory-core/keys/imagefactory-cosign.pub`. VM images use the same key as
  container images; there is no separate key.

Both Secrets stay in the operator namespace. Tenant namespaces never receive them.

## Publication Job

For each finished build the controller creates, in the operator namespace:

- Secret `ib-evidence-<hash>` with `bundle.tar.gz`, `descriptor.json` and
  `provenance.json`;
- Job `ib-evidence-<hash>` (labels `app.kubernetes.io/component=evidence-publisher`,
  `imagebuilder.io/vmimage`, `imagebuilder.io/vmimage-namespace`,
  `imagebuilder.io/vmimage-uid`).

The Job runs these steps:

| Step | Image | Action |
|---|---|---|
| `prepare` | cosign | verify the bundle sha256 and unpack it |
| `push-descriptor` | oras | push the descriptor to `<repo>/<name>:<version>-<platform>` |
| `sign-descriptor` | cosign | `sign`, then `attest` SLSA provenance, CycloneDX and Trivy (`--key`, `--tlog-upload=false`, `--use-signing-config=false`) |
| `publish-index` | oras | merge into `<repo>/<name>:<version>`, replace the own platform entry, retry against concurrent writers |
| `sign-index` | cosign | sign the index version that contains the own entry |
| `report` | oras | discover the referrers; write the digests as termination message |

All containers run as UID 65532 with a read-only root filesystem, no privilege
escalation, all capabilities dropped, seccomp `RuntimeDefault`, and no service
account token. The Job has `backoffLimit: 2` and an active deadline of
`jobTimeout`; the steps are idempotent.

- **On success:** the controller writes the references to `status.evidence`, sets the
  VMImage `Ready` and deletes Job and Secret.
- **On failure:** the VMImage fails with `EvidencePublicationFailed` and the message of
  the failed step. The Secret is deleted, and the Job stays for 24 h so you can read
  its logs:

```bash
kubectl -n imagebuilder-system logs job/ib-evidence-<hash> --all-containers
```

## Local test

`hack/evidence-publication-e2e.sh` runs the exact step scripts against a throwaway
local `registry:3` container. It publishes two platforms concurrently into one index
and verifies signature and attestations with cosign. It needs only Docker.

## Troubleshooting

| Symptom | Check |
|---|---|
| `EvidencePublisherNotConfigured` | `evidencePublisher.enabled`, both Secret names |
| `step push-descriptor failed: … 401/403` | registry Secret, token scope, NetworkPolicy/proxy |
| `step sign-descriptor failed: cosign key file is missing` | `cosignKeySecret.keyKey` |
| `… is not an imagebuilder VM image index; refusing to overwrite it` | the version tag was written by another tool; use a new version |
| `own entry missing … after 6 attempts` | very frequent concurrent writers on the same version; re-run the build |
| `referrer discovery incomplete` | registry without referrers support and without tag fallback |
