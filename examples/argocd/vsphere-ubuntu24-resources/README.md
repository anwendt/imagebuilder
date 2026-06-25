# vSphere Ubuntu 24.04 ArgoCD Resource Example

This example clones an existing vSphere Ubuntu 24.04 VM/template and publishes
the result as a vSphere template.

The `source.marketplaceRef` is provider-neutral. vSphere resolves it through
the ProviderConfig mapping:

```yaml
extra:
  marketplace.canonical.ubuntu.24.04.latest: content-library:/Golden Images/ubuntu-24-template
```

Supported source mappings:

- `content-library:/Golden Images/ubuntu-24-template`
- `library-item:xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`
- `ubuntu-24-template`
- `vm-123`

The source template or library item must be reachable by the vSphere provider
and must allow guest operations with the `guestUsername` and `guestPassword`
from the Secret when provisioners are configured.
