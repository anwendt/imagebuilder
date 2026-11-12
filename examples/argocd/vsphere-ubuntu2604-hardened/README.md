# vSphere Ubuntu 26.04 LTS hardened golden image

Builds `ubuntu-2604-vsphere-v1-0-0` from the Canonical Ubuntu 26.04 LTS cloud image OVA
(`release-20261016`, sha256 pinned, `SHA256SUMS` verified against the Ubuntu image signing
key) with the vSphere provider (ADR-009) and publishes it as a VM template item of a
Content Library. No external build tool is involved (ADR-001).

```text
source.type cloud-image  ->  provider downloads + verifies the OVA (optional proxy)
                         ->  deploys imagebuilder-<image>-<build> in the build folder
                         ->  provisioners via VMware Guest Operations
                         ->  shutdown, vApp properties removed, library template, VM destroyed
```

## Files

| File | Purpose |
|---|---|
| `generate.py` | renders `40-vmimage.yaml` and `scripts/70-system-role.sh`; parameters: `--proxy`, `--admin-user`, `--provider-config`, `--image-name`, `--out` |
| `scripts/*.sh` | baseline hardening steps, one VMImage `shell` provisioner each |
| `ansible/` | `system` role, run with `ansible-playbook -c local` in the guest |
| `10-providerconfig.yaml`, `20-platformprovider.yaml` | provider and target, with placeholder values |
| `40-vmimage.yaml` | the generated VMImage (no proxy, admin user `admin`) |

## What the baseline does

| Step | Purpose |
|---|---|
| `05-grub` | short GRUB timeout through a drop-in |
| `10-repo` | optional apt proxy (`--proxy`) |
| `20-cloud-init` | cloud-init datasources `OVF`, `VMware`, `None` |
| `20-update` | all pending updates |
| `30-ansible`, `40-addon` | `ansible-core` from the Ubuntu archive, baseline packages (open-vm-tools, ufw, apparmor, auditd, acct, ...) |
| `50-apparmor` | enforce all AppArmor profiles |
| `70-system-role` | sysctl and limits drop-ins, module blacklist, PAM `nullok` removal, session timeout, removal of network-facing packages, service enable/disable |
| `80-remove-containers` | remove podman |
| `85-admin-user` | locked automation user with passwordless sudo (`--admin-user`) |
| `90-cleanup` | logs, SSH host keys, machine-id, cloud-init state, build login locked last |

Safety choices in the role (see `ansible/roles/system/defaults/main.yml`): `squashfs`,
`fat` and `vfat` are not blocked (snapd, EFI partition); user `adm` and `dbus` are kept;
package removal is simulated first and aborts if cloud-init, openssh-server,
open-vm-tools, netplan.io, systemd or the kernel would go.

A cloud image has a single root disk; partition layouts with separate `/var`, `/tmp` or
`/var/log` volumes are not reproduced.

## Network

The build VM needs outbound access for package updates. With DHCP nothing is required;
without DHCP set `buildIPv4`, `buildGateway` and `buildDNS` in the ProviderConfig. The
address is applied through a volatile netplan file and probed with ARP first; a probe
answer is not proof that an address is free, so take the address from your IPAM.
