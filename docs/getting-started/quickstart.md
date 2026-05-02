# Quickstart

This guide installs the operator and validates the API surface in a Kubernetes
cluster. It does not require a real cloud provider account.

## Prerequisites

- Kubernetes cluster or `kind`
- `kubectl`
- Go 1.22+
- cert-manager, if validating webhooks should run fail-closed

Optional components:

- Prometheus Operator for `ServiceMonitor` and `PrometheusRule`
- Kyverno for the example image signature policy
- Dedicated build nodes with `/dev/kvm` when KVM acceleration is enabled

## Build And Test Locally

```bash
make generate
make manifests
go test ./...
make build
make build-builder
make build-uploader
```

## Install CRDs

```bash
kubectl apply -f config/crd/
```

## Deploy The Operator

For a production-like install with webhook TLS:

```bash
kubectl apply -f config/deploy/operator.yaml
kubectl apply -f config/policy/networkpolicies.yaml
kubectl apply -f config/certmanager/webhook-certificate.yaml
kubectl apply -f config/webhook/manifests.yaml
```

The operator runs in `imagebuilder-system` and exposes:

- metrics on port `8080`
- health checks on port `8081`
- validating webhooks on port `9443`

## Optional Observability

Only apply these when the Prometheus Operator CRDs are installed:

```bash
kubectl apply -f config/deploy/servicemonitor.yaml
kubectl apply -f config/deploy/prometheusrule.yaml
```

## Optional Supply-Chain Policies

Only apply the Kyverno policy when Kyverno is installed:

```bash
kubectl apply -f config/policy/kyverno-image-signatures.yaml
```

## Validate A Sample Manifest

The sample contains placeholder checksums and image digests. Replace them before
running a real build.

```bash
kubectl apply --dry-run=server -f config/samples/vmimage-ubuntu-aws-vsphere.yaml
```

## Run The Kind Smoke Test

```bash
make test-e2e
```

The smoke test creates or reuses a `kind` cluster, installs CRDs, deploys the
operator, waits for rollout, and validates a sample manifest with server-side
dry-run.
