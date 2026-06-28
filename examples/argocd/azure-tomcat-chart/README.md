# Azure Tomcat ArgoCD Helm Example

This chart is a GitOps-ready example for building an Azure Managed Image from
an Ubuntu Marketplace image and installing Apache Tomcat from the upstream tar
archive during a remote build.

## ArgoCD Path

Use this repository path in an ArgoCD Application:

```text
examples/argocd/azure-tomcat-chart
```

## Required Cluster State

- The imagebuilder operator and CRDs are already installed.
- The Azure provider image digest in `values.yaml` is replaced with a real
  released digest.
- A dedicated build NIC already exists.
- The provider credential Secret exists in the target namespace.

## Credentials

By default the chart references an existing Secret and does not render secret
values:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: azure-credentials
  namespace: imagebuilder-system
type: Opaque
stringData:
  credentials: |
    {
      "subscriptionId": "<subscription-id>",
      "tenantId": "<tenant-id>",
      "clientId": "<client-id>",
      "clientSecret": "<client-secret>",
      "storageAccountKey": "<storage-account-key>"
    }
```

In production, prefer External Secrets, Sealed Secrets, SOPS, or another
secret-management flow instead of committing this Secret to Git.

## Render Locally

```bash
helm template azure-tomcat examples/argocd/azure-tomcat-chart \
  --namespace imagebuilder-system
```

## Proxy Configuration

`values.yaml` includes a commented `providerConfig.extra.proxy` block. When
enabled, the chart renders flat `ProviderConfig.spec.extra` keys:

```yaml
providerConfig:
  extra:
    proxy:
      httpProxy: http://proxy.example.com:8080
      httpsProxy: http://proxy.example.com:8443
      noProxy: localhost,127.0.0.1,.svc,.cluster.local,169.254.169.254
```

Use these values for provider API traffic only when the provider supports
per-provider proxy configuration. Configure the imagebuilder operator chart
`proxy` values when the operator pod itself must reach endpoints through a
proxy.

## Deploy With ArgoCD

Use `../azure-tomcat-application.yaml` as a starting point and replace
`repoURL`, `targetRevision`, provider digest, Marketplace image reference, and
build NIC ID.
