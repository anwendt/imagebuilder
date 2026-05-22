# Azure Tomcat ArgoCD Resource Examples

This directory contains the same Azure Tomcat example as individual Kubernetes
resources instead of a Helm chart.

Apply order:

```text
00-namespace.yaml
10-azure-credentials.example.yaml
20-platformprovider.yaml
30-providerconfig.yaml
40-vmimage.yaml
```

Do not commit real credentials in `10-azure-credentials.example.yaml`. For an
ArgoCD repository, replace that file with External Secrets, Sealed Secrets, or
SOPS-managed encrypted Secret manifests.

Before using the example, replace:

- Azure provider image digest in `20-platformprovider.yaml`
- storage account and resource group in `30-providerconfig.yaml`
- build NIC resource ID in `30-providerconfig.yaml`
- source snapshot resource ID in `40-vmimage.yaml`

The VMImage performs a remote Azure build from an existing Snapshot and installs
Apache Tomcat from the upstream tar archive.
