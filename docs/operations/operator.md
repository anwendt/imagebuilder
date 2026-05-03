# Operator Operations Guide

## Components

The deployment consists of:

- core operator Deployment
- metrics Service
- webhook Service
- CRDs
- optional cert-manager Certificate/Issuer
- optional Prometheus and policy resources

## Deployment Commands

Development or basic cluster deployment:

```bash
make deploy
```

Production-like deployment with webhook TLS:

```bash
make deploy-production
```

Helm deployment:

```bash
helm install imagebuilder ./charts/imagebuilder \
  --namespace imagebuilder-system \
  --create-namespace
```

Optional observability resources:

```bash
make deploy-observability
```

Optional hardening policies:

```bash
make deploy-policies
```

`make deploy-production` installs NetworkPolicies by default. The Helm chart
also defaults `networkPolicy.enabled=true` and its schema rejects disabling it
for production chart installs.

## Runtime Flags

| Flag | Default | Purpose |
|---|---:|---|
| `--metrics-bind-address` | `:8080` | Prometheus metrics endpoint. |
| `--health-probe-bind-address` | `:8081` | Health and readiness endpoint. |
| `--leader-elect` | `false` | Enables HA leader election. |
| `--max-concurrent-builds` | `3` | Global parallel build limit. |
| `--max-concurrent-builds-per-node` | `1` | Per-node build limit. |
| `--scheduler-namespace` | `$POD_NAMESPACE` | Namespace used for Lease objects. |
| `--provider-namespace` | `$POD_NAMESPACE` | Namespace used for PlatformProvider Deployments and Services. |
| `--require-provider-mtls` | `false` | Reject PlatformProvider resources unless `spec.transport.tls.mode=Mutual`. |
| `--require-provider-digest` | `false` | Reject PlatformProvider packages that are not pinned by digest. |
| `--require-provider-signature` | `false` | Reject PlatformProvider resources unless `spec.security.verifySignature=true`. |
| `--allowed-provider-registries` | empty | Comma-separated registry prefixes allowed for PlatformProvider packages. |
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. |

## Image Pinning

For production, pin the operator, builder, uploader, provisioner, and provider
images by digest. The Helm chart accepts `image.digest`, `builderImage.digest`,
`uploaderImage.digest`, and `provisionerImages.<type>.digest`; when set, the
rendered reference is `repository@sha256:...` instead of `repository:tag`.

The operator passes `BUILDER_IMAGE` and `UPLOADER_IMAGE` to the control loop.
`spec.build.upload.image` can still override the uploader image for a specific
`VMImage`. Provisioner defaults are passed via `PROVISIONER_ANSIBLE_IMAGE`,
`PROVISIONER_CHEF_IMAGE`, `PROVISIONER_PUPPET_IMAGE`, and
`PROVISIONER_SALTSTACK_IMAGE`.

Provider images are pinned directly in `PlatformProvider.spec.package`. In
production chart installs, `providerSecurity.requireDigest=true`,
`providerSecurity.requireSignature=true`, and
`providerSecurity.allowedRegistries` render global operator policy flags and the
PlatformProvider admission webhook rejects non-compliant resources before they
reach reconciliation.

## Build Scheduling

The operator uses Kubernetes Leases to protect the cluster from too many
parallel QEMU processes.

- Global slots enforce `--max-concurrent-builds`.
- Node slots enforce `--max-concurrent-builds-per-node`.
- A queued image reports `BuildQueued` in status and emits a Kubernetes Event.
- Slots are released after build success, failure, timeout, or deletion.

KVM builds are treated as a dedicated-node workload. When
`spec.build.security.enableKVM=true`, admission requires:

```yaml
spec:
  build:
    nodeSelector:
      imagebuilder.io/kvm: "true"
      imagebuilder.io/dedicated: imagebuilder
    security:
      enableKVM: true
```

The build pod also receives a `NoSchedule` toleration for
`imagebuilder.io/dedicated=imagebuilder`. Taint KVM-capable build nodes with the
same key/value so imagebuilder workloads do not co-locate with normal
application workloads.

## Source Cache Operations

Source caching is explicit and PVC-backed. The operator mounts
`spec.build.cache.ref` as `/cache` into the build container and passes cache
policy through environment variables.

Production semantics:

| Topic | Operational rule |
|---|---|
| Cache key | The builder stores entries by checksum as `<algorithm>-<hex>.img`. Changing the source checksum creates a different entry and naturally invalidates the old one. |
| TTL | `spec.build.cache.ttl` is optional. Entries older than the TTL are removed before checksum verification and downloaded again. |
| Checksum mismatch | Corrupt cache entries are removed and refetched. Downloaded sources that fail checksum verification fail the build and are not cached. |
| Retention | `Always` keeps verified entries. `Never` deletes a matching hit after use and does not persist fresh downloads. |
| Cleanup | PVC lifecycle is user-managed. Entry-level cleanup is controlled by TTL and retain policy. |

`spec.build.cacheRef` is still accepted as a legacy shorthand for
`spec.build.cache.ref`.

## Metrics

The operator exports metrics through controller-runtime on port `8080`.

Important custom metrics:

| Metric | Meaning |
|---|---|
| `imagebuilder_build_duration_seconds` | Build duration by phase/provider/format. |
| `imagebuilder_queue_duration_seconds` | Time spent waiting for a build slot. |
| `imagebuilder_active_builds` | Currently tracked active builds. |
| `imagebuilder_provisioner_duration_seconds` | In-process provisioner duration. |
| `imagebuilder_upload_duration_seconds` | Provider upload duration. |
| `imagebuilder_upload_bytes_total` | Uploaded artifact bytes by provider/format. |
| `imagebuilder_upload_throughput_bytes_per_second` | Provider upload throughput derived from uploaded bytes and upload duration. |
| `imagebuilder_register_duration_seconds` | Provider image registration duration. |
| `imagebuilder_provider_healthy` | Provider health state, where `1` is healthy and `0` is unhealthy. |
| `imagebuilder_failures_total` | Failures by phase/reason/provider. |
| `imagebuilder_cleanup_failures_total` | Cleanup failures by scope/reason/provider. |

Apply `config/deploy/servicemonitor.yaml` and
`config/deploy/prometheusrule.yaml` only when the Prometheus Operator CRDs are
installed.

## Webhooks

The operator registers validating webhooks for:

- `VMImage`
- `ProviderConfig`

The webhook server uses cert-manager material from
`imagebuilder-webhook-server-cert`. The `ValidatingWebhookConfiguration` uses
CA injection from `imagebuilder-webhook-serving-cert`.

Production admission is fail-closed:

- `failurePolicy: Fail` is set for both `VMImage` and `ProviderConfig`.
- The webhook client points to `imagebuilder-webhook-service` in
  `imagebuilder-system`.
- If the webhook service or TLS material is unavailable, Kubernetes denies
  create/update requests instead of allowing unsafe specs through.
- The Helm chart enforces `webhook.enabled=true` and `webhook.failurePolicy=Fail`
  through `values.schema.json`.

## Provider Transport Boundary

External providers use gRPC over TCP through ClusterIP Services as described in
ADR-002. The production default is a namespace-local trust boundary:

- provider Services remain `ClusterIP`
- namespace traffic is default-denied
- only operator pods may connect to provider pods on TCP/50051
- provider pods cannot receive traffic from build/upload jobs or other workloads

Build and upload Jobs run in the namespace of the `VMImage`, not necessarily in
the operator namespace. Configure tenant namespaces explicitly so those Jobs get
the same egress restriction without applying default-deny to unrelated tenant
pods:

```yaml
networkPolicy:
  enabled: true
  workloadNamespaces:
    - team-a-images
    - team-b-images
```

For each listed namespace, the chart renders a scoped policy selecting only pods
with `app.kubernetes.io/managed-by=imagebuilder` and
`imagebuilder.io/job-kind in (build, upload)`.

When a provider endpoint crosses this boundary, for example a remote provider or
cross-namespace provider Service, protect the gRPC channel with mTLS and verify
provider certificate identity. NetworkPolicy alone is only sufficient for the
default namespace-local deployment model.

Enable provider mTLS per `PlatformProvider`:

```yaml
spec:
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

The operator reads the client CA and client certificate Secret directly and
mounts the provider server certificate plus client CA into the provider pod.
Changes to these fields roll the provider Deployment template.

Use cert-manager or an equivalent internal CA to issue three Secrets in the
provider namespace:

- `caSecretRef`: CA bundle trusted by both operator and provider.
- `clientCertificateSecretRef`: operator client certificate and key.
- `serverCertificateSecretRef`: provider server certificate and key.

Rotate by creating the new CA/client/server Secrets first, updating the
`PlatformProvider.spec.transport.tls.*SecretRef` fields in one apply, then
waiting for the provider Deployment rollout and health check. Keep the old CA
valid until the rollout is complete. Because the Secret names are part of the
Deployment template, changing them forces a provider pod restart; changing Secret
data in place should be paired with a Deployment restart or Secret-reloader.

For production chart installs, `providerSecurity.requireMTLS` defaults to `true`
and renders `--require-provider-mtls=true`. With this policy enabled, providers
that omit `spec.transport.tls` or explicitly set `mode: Disabled` are rejected
by admission and also marked `Unhealthy` by reconciliation if admission is not
installed. Use plain TCP only for local development or tightly controlled
namespace-local test clusters by starting the operator with
`--require-provider-mtls=false`.

Provider image signatures are fail-closed in two layers:

- PlatformProvider admission requires `spec.security.verifySignature=true` when
  `--require-provider-signature=true`.
- The supplied Kyverno policy in
  `config/policy/kyverno-image-signatures.yaml`, also rendered by the Helm chart
  when `imageSignaturePolicy.enabled=true`, verifies image signatures and
  digests on imagebuilder-managed Pods. Install Kyverno before using the
  production Helm defaults, or replace the rendered policy with an equivalent
  Sigstore/Gatekeeper policy.

## Logs

Operator and builder logs are JSON structured. Builder logs include:

- `buildID`
- `vmimage`
- `namespace`
- `phase`

Do not log credentials or generated guest credential material.

Metrics are exposed on the operator metrics service and include build duration,
queue duration, active builds, provisioner duration, upload/register duration,
provider health, classified failures, and cleanup failures. The Helm
`PrometheusRule` includes alerts for recent failures, cleanup failures,
unhealthy providers, and active builds stuck for more than three hours.

`config/deploy/operator.yaml` is a development manifest and is marked with
`imagebuilder.io/profile=development` and
`imagebuilder.io/production-ready=false`. Use the Helm chart for production; it
renders the hardened defaults and policy resources.

## Resource Guardrails

The Helm chart renders a `ResourceQuota` and `LimitRange` in the operator
namespace by default through `namespaceResourceGuardrails.enabled=true`. Tune
these values for the expected number of concurrent builds, provider pods,
artifact PVCs, and monitoring sidecars before installing into production.

These namespace guardrails protect only the operator namespace. VMImage build
and upload Jobs run in the VMImage namespace, so each tenant namespace should
also have its own Kubernetes `ResourceQuota` and `LimitRange` sized for its
approved build concurrency and storage budget.

## Cleanup

The operator cleans up:

- scheduler Leases
- owned build/upload Jobs through Kubernetes ownership and TTL
- operator-created artifact PVCs according to retain policy
- temporary QEMU disks, sockets, seed ISOs, and generated credential files

Existing user-managed PVCs are not deleted by the operator.
