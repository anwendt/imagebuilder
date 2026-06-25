# vSphere Tomcat ArgoCD Resource Example

This example clones an existing vSphere Ubuntu VM/template, installs Apache
Tomcat from the upstream tar archive through VMware Guest Operations, and
publishes the result to vSphere.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-vsphere-credentials.example.yaml`
- vCenter endpoint and vSphere placement values in `30-providerconfig.yaml`
- marketplace-to-template mapping in `30-providerconfig.yaml`

The default `source.marketplaceRef` is resolved through the
`marketplace.canonical.ubuntu.24.04.latest` ProviderConfig extra value. Set that
value to a Content Library item, existing vSphere VM/template name, inventory
path, or MoID such as `vm-123`.

Supported source mappings:

- `content-library:/Golden Images/ubuntu-24-template`
- `library-item:xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`
- `ubuntu-24-template`
- `vm-123`

The source template or library item must be reachable by the vSphere provider
and must allow guest operations with the `guestUsername` and `guestPassword`
from the Secret.
