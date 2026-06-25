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
