# AWS Ubuntu 24.04 ArgoCD Resource Example

This example builds an AWS AMI from the Canonical Ubuntu 24.04 LTS Marketplace
source. It uses the provider-neutral `source.marketplaceRef` form and a minimal
shell provisioner to prepare a reusable base image.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-aws-credentials.example.yaml`
- subnet, security group, IAM instance profile, KMS key, and role ARN in `30-providerconfig.yaml`
- region in `30-providerconfig.yaml` if you do not build in `eu-west-1`

Do not commit real AWS credentials. Prefer GitHub/AWS OIDC, IRSA, External
Secrets, Sealed Secrets, or SOPS-managed encrypted manifests.
