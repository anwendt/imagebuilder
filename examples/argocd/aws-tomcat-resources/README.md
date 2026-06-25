# AWS Tomcat ArgoCD Resource Example

This example builds an AWS AMI from an Ubuntu Marketplace source and installs
Apache Tomcat from the upstream tar archive during an AWS remote build.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-aws-credentials.example.yaml`
- marketplace image reference in `40-vmimage.yaml` if you do not want the
  default Ubuntu 24.04 LTS Canonical source
- subnet, security group, IAM instance profile, KMS key, and role ARN in `30-providerconfig.yaml`

Do not commit real AWS credentials. Prefer GitHub/AWS OIDC, IRSA, External
Secrets, Sealed Secrets, or SOPS-managed encrypted manifests.
