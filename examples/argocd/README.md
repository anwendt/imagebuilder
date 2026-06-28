# ArgoCD Examples

This directory contains example GitOps paths for imagebuilder.

## Helm

- `azure-tomcat-chart` - Helm chart for Azure remote build with Apache Tomcat.

## Plain Kubernetes Resources

- `azure-tomcat-resources` - Azure Snapshot source, Azure Managed Image target.
- `aws-ubuntu24-resources` - AWS Ubuntu 24.04 Marketplace source, AWS AMI target.
- `aws-tomcat-resources` - AWS AMI source, AWS AMI target.
- `open-telekom-cloud-ubuntu24-resources` - OTC Ubuntu 24.04 Glance source, OTC private image target.
- `openstack-tomcat-resources` - OpenStack Glance image source and target.
- `vsphere-ubuntu24-resources` - vSphere Ubuntu 24.04 template source, vSphere template target.
- `vsphere-tomcat-resources` - Ubuntu cloud image source, vSphere OVA target.

The `*-credentials.example.yaml` files contain placeholders only. Replace them
with your preferred secret-management mechanism before using these paths with
ArgoCD.

## Proxy Configuration

The plain resource examples include commented `ProviderConfig.spec.extra`
proxy keys:

```yaml
extra:
  # httpProxy: http://proxy.example.com:8080
  # httpsProxy: http://proxy.example.com:8443
  # noProxy: localhost,127.0.0.1,.svc,.cluster.local,169.254.169.254
```

Use those keys for provider API traffic only when the selected provider
implementation supports per-provider proxy configuration. If the operator pod
itself must use a proxy to reach Kubernetes, registries, provider Services, or
external endpoints, configure the operator chart `proxy` values instead.
