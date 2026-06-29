# Quickstart

This guide installs VM Image Builder from the published Helm chart and gets the
operator ready in a Kubernetes cluster. It does not require a real cloud
provider account.

## Prerequisites

- Kubernetes cluster
- `kubectl`
- Helm 3.8 or newer with OCI registry support

Optional components:

- Prometheus Operator for `ServiceMonitor` and `PrometheusRule`
- Kyverno for the example image signature policy
- Dedicated build nodes with `/dev/kvm` when KVM acceleration is enabled

## Install

For a production-like install with webhooks, network policies, resource
guardrails, and strict provider admission defaults:

```bash
helm install imagebuilder oci://ghcr.io/anwendt/charts/imagebuilder \
  --version 0.4.0 \
  --namespace imagebuilder-system \
  --create-namespace
```

For a local development cluster, use the development profile from a checkout. It
disables webhooks, cert-manager integration, network policies, namespace quotas,
and strict provider mTLS/digest/signature requirements:

```bash
helm install imagebuilder oci://ghcr.io/anwendt/charts/imagebuilder \
  --version 0.4.0 \
  --namespace imagebuilder-system \
  --create-namespace \
  -f charts/imagebuilder/values-development.yaml
```

## Verify

```bash
kubectl get pods -n imagebuilder-system
kubectl get crd | grep imagebuilder.io
```

The operator pod should become `Running`. The chart installs CRDs, RBAC,
webhook resources, metrics Service, network policies, and the operator
Deployment with released image tags. Kyverno is not required for the default
install.

If Kyverno is installed and you want the example image signature policy, enable
it explicitly:

```bash
helm upgrade imagebuilder oci://ghcr.io/anwendt/charts/imagebuilder \
  --version 0.4.0 \
  --namespace imagebuilder-system \
  --reuse-values \
  --set imageSignaturePolicy.enabled=true
```

## Create A Provider Configuration

Real image builds need credentials and a provider. The repository includes
ArgoCD-ready examples under `examples/argocd/`:

- `azure-tomcat-resources`
- `aws-tomcat-resources`
- `open-telekom-cloud-ubuntu24-resources`
- `vsphere-tomcat-resources`

Start by copying the matching credentials example, fill in your secret values,
and apply the resource set:

```bash
kubectl apply -f examples/argocd/azure-tomcat-resources/
```

## Validate A Sample Manifest

The sample contains placeholder checksums and image digests. Replace them before
running a real build.

```bash
kubectl apply --dry-run=server -f config/samples/vmimage-ubuntu-aws-vsphere.yaml
```

## Use Git-Backed Scripts

Keep image customization scripts in a separate Git repository when multiple
images should share the same provisioning baseline. A common layout is:

```text
image-scripts/
└── scripts/
    └── ubuntu/
        ├── 10-basic-tools.sh
        ├── 20-hardening.sh
        └── 30-monitoring.sh
```

Reference the directory from a `shell` provisioner:

```yaml
provisioners:
  - type: shell
    source:
      git:
        url: https://github.com/yourorg/image-scripts.git
        ref: 7f6e5d4c3b2a190817263544536271809abcdef0
        path: scripts/ubuntu
```

The scripts are executed in lexicographic order. See
[Git-backed provisioner scripts](../user-guide/git-provisioners.md) for a full
public/private repository example.

## Local Development

Use the local chart or Makefile targets when developing from source:

```bash
make generate
make manifests
go test ./...
make helm-lint
make helm-template
make helm-template-dev
```

For a local smoke test with `kind`:

```bash
make test-e2e
```
