# OpenStack Provider

The OpenStack provider supports local artifact upload to Glance and provider
owned remote builds through Nova server snapshots.

## ProviderConfig

Use the Keystone endpoint as `spec.endpoint` and pass credentials through the
referenced Secret. Supported Secret keys are:

| Key | Purpose |
|---|---|
| `username` / `password` | Keystone username/password auth |
| `userID` | Optional Keystone user ID |
| `projectID` / `tenantID` | Project scope by ID |
| `projectName` / `tenantName` | Project scope by name |
| `domainID` / `domainName` | Keystone domain |
| `applicationCredentialID` | Application credential ID |
| `applicationCredentialName` | Application credential name |
| `applicationCredentialSecret` | Application credential secret |
| `token` | Existing Keystone token |
| `remotePrivateKey` | SSH private key for remote provisioners |

`spec.insecure` is rejected. Use a trusted Keystone certificate chain.

## Local Upload

Local builds upload the produced artifact to Glance. Supported target formats:

- `qcow2`
- `raw`
- `vmdk`
- `vhd`

Useful `spec.extra` keys:

| Key | Default | Purpose |
|---|---|---|
| `image.visibility` | `private` | Glance visibility |
| `image.protected` | `false` | Glance protected flag |
| `image.diskFormat` | target format | Glance disk format |
| `image.containerFormat` | `bare` | Glance container format |
| `image.minDiskGB` | `0` | Glance min disk |
| `image.minRAMMB` | `0` | Glance min RAM |
| `image.property.<name>` | unset | Additional Glance image property |
| `httpProxy` | unset | HTTP proxy URL for provider API traffic when per-provider proxy support is enabled |
| `httpsProxy` | unset | HTTPS proxy URL for provider API traffic when per-provider proxy support is enabled |
| `noProxy` | unset | Comma-separated proxy bypass list, for example Keystone/Glance/Nova internal endpoints |

Example proxy values:

```yaml
extra:
  httpProxy: http://proxy.example.com:8080
  httpsProxy: http://proxy.example.com:8443
  noProxy: localhost,127.0.0.1,.svc,.cluster.local,169.254.169.254
```

## Remote Builds

Remote builds start a temporary Nova server from an existing Glance image,
optionally run Linux SSH provisioners, stop the server, create a Glance image
from it, and delete the temporary server.

The source can be a concrete Glance image ID:

```yaml
spec:
  source:
    type: cloud-image
    providerRef: 00000000-0000-0000-0000-000000000000
```

For Open Telekom Cloud and similar OpenStack clouds, the provider also accepts
the provider-neutral marketplace form. The OpenStack provider interprets this
as a lookup over active public Glance images and selects the newest matching
image:

```yaml
spec:
  source:
    type: marketplace
    marketplaceRef:
      publisher: Open Telekom Cloud
      offer: ubuntu
      sku: "24.04"
      version: latest
```

Use a concrete `source.providerRef` when your cloud image naming is ambiguous
or when a build must pin an exact source image.

Required `spec.extra` keys for remote builds:

| Key | Required | Purpose |
|---|---|---|
| `remote.flavorRef` | yes | Nova flavor ID or name accepted by the cloud |
| `remote.networkID` | no | Neutron network UUID; omitted uses Nova `auto` networking |
| `remote.securityGroups` | no | Comma-separated security group names |
| `remote.keyName` | only with provisioners | Nova keypair injected into the server |
| `remote.sshUser` | only with provisioners | Guest SSH user |
| `remote.networkName` | no | Preferred server address pool for SSH |
| `remote.sshPort` | no | SSH port, default `22` |
| `remote.configDrive` | no | Enable Nova config drive |

Remote provisioners currently support Linux `shell` and `file` provisioners.
The provider must be able to reach the temporary VM over the selected network.
OpenStack does not provide a cloud-native run-command API equivalent to AWS SSM
or Azure Run Command, so SSH reachability is a production prerequisite.

## Required OpenStack Permissions

The service account or application credential needs least-privilege access for:

- Glance image create, upload, get, delete
- Nova server create, get, stop, delete
- Nova create image / server snapshot
- Neutron network attachment through Nova for the selected network

For remote provisioners, use a dedicated keypair and security group that allows
SSH only from the provider network boundary.

## Live E2E

The real OpenStack remote build E2E test is opt-in because it creates Nova and
Glance resources in a live tenant:

```bash
OPENSTACK_E2E=1 \
OPENSTACK_E2E_AUTH_URL=https://keystone.example.com/v3 \
OPENSTACK_E2E_REGION=RegionOne \
OPENSTACK_E2E_USERNAME=imagebuilder \
OPENSTACK_E2E_PASSWORD=... \
OPENSTACK_E2E_PROJECT_NAME=images \
OPENSTACK_E2E_SOURCE_IMAGE_ID=00000000-0000-0000-0000-000000000000 \
OPENSTACK_E2E_FLAVOR_REF=m1.small \
OPENSTACK_E2E_NETWORK_ID=11111111-1111-1111-1111-111111111111 \
OPENSTACK_E2E_KEY_NAME=imagebuilder-e2e \
OPENSTACK_E2E_REMOTE_PRIVATE_KEY="$(cat ./imagebuilder-e2e.key)" \
make test-e2e-openstack
```

Optional variables:

| Variable | Default |
|---|---|
| `OPENSTACK_E2E_SOURCE_TYPE` | `cloud-image` |
| `OPENSTACK_E2E_OS_FAMILY` | `linux` |
| `OPENSTACK_E2E_OS_DISTRIBUTION` | `ubuntu` |
| `OPENSTACK_E2E_OS_VERSION` | `24.04` |
| `OPENSTACK_E2E_FORMAT` | `qcow2` |
| `OPENSTACK_E2E_SECURITY_GROUPS` | unset |
| `OPENSTACK_E2E_NETWORK_NAME` | unset |
| `OPENSTACK_E2E_SSH_USER` | `ubuntu` |
| `OPENSTACK_E2E_DISABLE_PROVISIONER` | `false` |
| `OPENSTACK_E2E_TIMEOUT` | `55m` |
