# vSphere Tomcat ArgoCD Resource Example

This example clones an existing vSphere Ubuntu VM/template, installs Apache
Tomcat from the upstream tar archive through VMware Guest Operations, and
publishes the result to vSphere.

Replace before use:

- provider image digest in `20-platformprovider.yaml`
- credentials handling in `10-vsphere-credentials.example.yaml`
- vCenter endpoint and vSphere placement values in `30-providerconfig.yaml`
- source VM/template name or MoID in `40-vmimage.yaml`

The source template must be reachable by the vSphere provider and must allow
guest operations with the `guestUsername` and `guestPassword` from the Secret.
