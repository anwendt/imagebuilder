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
| `--log-level` | `info` | `debug`, `info`, `warn`, or `error`. |

## Build Scheduling

The operator uses Kubernetes Leases to protect the cluster from too many
parallel QEMU processes.

- Global slots enforce `--max-concurrent-builds`.
- Node slots enforce `--max-concurrent-builds-per-node`.
- A queued image reports `BuildQueued` in status and emits a Kubernetes Event.
- Slots are released after build success, failure, timeout, or deletion.

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
| `imagebuilder_register_duration_seconds` | Provider image registration duration. |
| `imagebuilder_failures_total` | Failures by phase/reason/provider. |

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

## Logs

Operator and builder logs are JSON structured. Builder logs include:

- `buildID`
- `vmimage`
- `namespace`
- `phase`

Do not log credentials or generated guest credential material.

## Cleanup

The operator cleans up:

- scheduler Leases
- owned build/upload Jobs through Kubernetes ownership and TTL
- operator-created artifact PVCs according to retain policy
- temporary QEMU disks, sockets, seed ISOs, and generated credential files

Existing user-managed PVCs are not deleted by the operator.
