# ArgoCD Examples

This directory contains example GitOps paths for imagebuilder.

## Helm

- `azure-tomcat-chart` - Helm chart for Azure remote build with Apache Tomcat.

## Plain Kubernetes Resources

- `azure-tomcat-resources` - Azure Snapshot source, Azure Managed Image target.
- `aws-tomcat-resources` - AWS AMI source, AWS AMI target.
- `openstack-tomcat-resources` - OpenStack Glance image source and target.
- `vsphere-tomcat-resources` - Ubuntu cloud image source, vSphere OVA target.

The `*-credentials.example.yaml` files contain placeholders only. Replace them
with your preferred secret-management mechanism before using these paths with
ArgoCD.
