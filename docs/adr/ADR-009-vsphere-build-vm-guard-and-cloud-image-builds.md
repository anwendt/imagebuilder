---
document-id: ADR-009
title: vSphere Build VM Guard and Cloud-Image Remote Builds
status: Accepted
date: 2026-11-12
deciders: Platform Engineering
supersedes: —
superseded-by: —
classification: Internal
---

# ADR-009 — vSphere Build VM Guard and Cloud-Image Remote Builds

## Status

**Accepted**

---

## Context

The vSphere provider supports remote builds (ADR-007): it clones or deploys a source
VM, runs provisioners through VMware Guest Operations, and registers the result. Two gaps
block using it on a shared production vCenter and for new Ubuntu releases:

1. **No ownership boundary.** The temporary VM was named after the image
   (`firstNonEmpty(imageName, ...)`), created in the ProviderConfig `folder`, and cleanup
   could resolve and destroy a VM purely by name anywhere in the datacenter. On a vCenter
   that hosts other teams' VMs and templates, a name collision or a mistyped folder could
   make the provider power off and destroy a foreign VM.
2. **No path from a public cloud image to a template.** Remote builds need an existing
   vSphere template as source. A new Ubuntu release has none, and the first template
   cannot be created without local KVM (ADR-007) or a second tool.

## Decision

### 1. Build VM guard

Every VM the vSphere provider creates, modifies or destroys must be provably its own:

| Rule | Enforcement |
|---|---|
| Name carries the prefix `imagebuilder-` (plus a build-specific suffix) | `buildVMName` always generates it; `checkBuildVMName` refuses anything else |
| VM lives directly in a dedicated folder whose name starts with `imagebuilder` | `ProviderConfig.extra.buildFolder` is mandatory for remote builds; `checkBuildFolderPath` rejects other folders; the provider creates the folder one level below an existing parent |
| Lookup is never global | Name based lookup resolves only inside the build folder |
| Check before every state change | `requireOwnedBuildVM` runs before provisioners, shutdown, publish and destroy: prefix, not protected, parent folder is the build folder |
| Protected names | `extra.protectedNames` (comma separated, case-insensitive substrings) is checked against all configured inventory names and against build VM names and folders |

A VM that fails the guard is never touched and the build fails with a guard error. The
guard is code, not convention: `plugins/vsphere/guard.go`, covered by unit tests and by
govmomi simulator tests (foreign VM, look-alike name outside the folder, owned VM).

### 2. Cloud-image source for remote builds

`spec.source.type: cloud-image` with `source.url` and `source.checksum` (sha256) is
accepted by the vSphere remote build:

1. The provider downloads the OVA into its upload volume through `extra.downloadProxy`
   (not the vCenter API traffic), verifies the sha256, and fails closed on mismatch.
2. The OVA is deployed as the build VM into the build folder. Cloud-init receives a
   minimal user-data through the OVF `user-data` property: a temporary root password
   derived with HMAC-SHA256 from the vCenter secret and the build ID. Nothing is stored,
   the password is useless after the build, and `guestUsername`/`guestPassword` in the
   Secret remain an override for source-clone builds.
3. Provisioners run through Guest Operations. Linux `shell` output goes to
   `/var/log/imagebuilder-provision.log`; on failure the tail is returned in the error.
4. With `extra.remote.publishTo: content-library` the powered-off build VM is published
   as a **VM template item** of the configured Content Library and the build VM is
   destroyed. The build configuration is scrubbed first and the template is verified
   afterwards (see section 3).
   An existing item is never modified: the same build finding its own item (marker in the
   description) is a successful retry, anything else is an error.

### 3. Credential revocation and template placement

- **Revocation:** the temporary build login is provider-owned. After the last
  provisioner the provider schedules `passwd -l` with a 30 second systemd timer,
  waits, and then tries to log in with the build password. A rejected login is
  the proof (`vsphere-build-credential-revoked`); a still working login fails the
  build with `BuildCredentialNotRevoked`, an inconclusive probe fails closed.
  Scripts inside the image cannot do this reliably: locking the login in the last
  provisioner makes Guest Operations reject the login the provider still needs to
  read the exit code, and a lock deferred to shutdown through a transient unit
  did not take effect in a live build (the check above caught it). Disable only
  with `revokeBuildCredential: "false"`.
- **Template folder:** the Content Library publishes the template VM into
  `extra.templateFolder`, which is required with `remote.publishTo` and must lie
  outside the build folder, so the build folder keeps holding `imagebuilder-*`
  VMs only. A ProviderConfig without it fails validation with a clear message.

- **Build data in the template:** the build user-data reaches the guest as an OVF
  vApp property (and a root password is derived for it). Before publishing, the
  provider powers the VM off, empties the value and default of every vApp
  property and removes all guestinfo configuration keys that carry data
  (VMware Tools maintained `guestinfo.vmtools.*` keys stay). The vApp
  product, the property **definitions** (`user-data`, `hostname`,
  `instance-id`, `public-keys`, `seedfrom`, `password`) and the OVF environment
  transport stay: a platform such as VMware Aria Automation delivers the
  machine's cloud-init through the OVF `user-data` property of the clone, and
  without the definitions clones boot without cloud-init (v0.5.9 removed them
  and broke this). After publishing it reads the configuration of the template
  VM the Content Library created and fails the build with `BuildDataInTemplate`
  if any property value or default is non-empty, a data-carrying guestinfo key
  exists, or (unless `requireUserDataProperty: "false"`) the `user-data`
  property definition is missing. The evidence check
  `vsphere-template-config-clean` states exactly that. It does not boot a clone,
  and `vsphere-build-credential-revoked` only describes the build VM. Without
  emptied values every clone's cloud-init would read the build user-data again
  on first boot and set the build password on root (found in a live clone test).

### 4. Operational details

- `ProviderConfig.spec.endpoint` may be empty; the provider then reads the vCenter
  address from the Secret key `server`. This keeps the address out of manifests.
- NFC upload URLs name ESXi hosts by inventory name. If the pod DNS cannot resolve a
  host, the provider dials the management address reported by vCenter and still verifies
  the certificate against the host name.
- The provider never logs the vCenter address.

## Consequences

### Positive
- The provider can run against a shared vCenter without being able to reach foreign VMs.
- A new Ubuntu LTS can be turned into a hardened vSphere template without a second build tool, KVM
  or a pre-existing template.
- Publishing to a Content Library is additive only.

### Negative
- `buildFolder` is now mandatory for vSphere remote builds (breaking for existing
  ProviderConfigs that relied on `folder`).
- Cloud-image builds keep the image's single-root-disk layout; partition layouts of
  installer-based images (separate `/var`, `/tmp`, `/var/log`) are not reproduced.
- VM-side evidence (ADR-008) is not available for vSphere: the guest has no managed
  identity for registry access.

## Related Documents

- [ADR-002 — Platform Providers as Separate Kubernetes Containers](ADR-002-providers-as-separate-containers.md)
- [ADR-007 — Remote Build as Provider-Owned Execution Mode](ADR-007-remote-build-provider-contract.md)
- [ADR-008 — Provider-Neutral VM Image Evidence Contract](ADR-008-vm-image-evidence-contract.md)
- [vSphere Provider Operations](../operations/vsphere-provider.md)
