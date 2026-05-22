# AWS Tomcat ArgoCD Resource Example

This example builds an AWS AMI from an existing Ubuntu AMI and installs Apache
Tomcat from the upstream tar archive during an AWS remote build.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-aws-credentials.example.yaml`
- source AMI ID in `40-vmimage.yaml`
- subnet, security group, IAM instance profile, KMS key, and role ARN in `30-providerconfig.yaml`

Do not commit real AWS credentials. Prefer GitHub/AWS OIDC, IRSA, External
Secrets, Sealed Secrets, or SOPS-managed encrypted manifests.
