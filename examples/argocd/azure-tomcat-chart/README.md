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

## Deploy With ArgoCD

Use `../azure-tomcat-application.yaml` as a starting point and replace
`repoURL`, `targetRevision`, provider digest, Marketplace image reference, and
build NIC ID.
