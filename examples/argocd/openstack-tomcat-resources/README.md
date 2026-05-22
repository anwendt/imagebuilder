# OpenStack Tomcat ArgoCD Resource Example

This example builds a Glance image from an existing Ubuntu Glance image UUID and
installs Apache Tomcat from the upstream tar archive during an OpenStack remote
build.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-openstack-credentials.example.yaml`
- auth URL, region, flavor, network, keypair, and security group values
- source Glance image UUID in `40-vmimage.yaml`

Shell provisioning uses SSH, so the Secret must include `remotePrivateKey` for
the keypair configured as `remote.keyName`.
