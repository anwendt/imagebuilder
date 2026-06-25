# Open Telekom Cloud Ubuntu 24.04 ArgoCD Resource Example

This example builds a private Open Telekom Cloud image from an Ubuntu 24.04
public Glance image. OTC is OpenStack-based, so this path uses the `openstack`
provider with OTC Keystone and Nova/Glance settings.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-openstack-credentials.example.yaml`
- project, domain, network, keypair, security group, and flavor in `30-providerconfig.yaml`
- marketplace reference in `40-vmimage.yaml` if you do not want the default
  Ubuntu 24.04 lookup
- `remote.sshUser` if your OTC Ubuntu image uses a different default user

The default `source.marketplaceRef` is resolved by listing active public Glance
images and selecting the newest matching Ubuntu 24.04 image. If your tenant
needs a deterministic source image, replace it with a concrete OTC Glance image
UUID:

```yaml
source:
  type: cloud-image
  providerRef: 00000000-0000-0000-0000-000000000000
```

You can obtain public image UUIDs with the OpenStack CLI, for example:

```bash
openstack image list --public --long | grep -i ubuntu
```

Shell provisioning uses SSH, so the Secret must include `remotePrivateKey` for
the keypair configured as `remote.keyName`.
