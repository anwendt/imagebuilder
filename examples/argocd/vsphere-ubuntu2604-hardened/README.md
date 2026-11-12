# vSphere Ubuntu 26.04 LTS hardened golden image

Builds `ubuntu-2604-vsphere-v1-0-0` from the Canonical Ubuntu 26.04 LTS cloud image OVA
(`release-20261016`, sha256 pinned, `SHA256SUMS` verified against the Ubuntu image signing
key) with the vSphere provider (ADR-009) and publishes it as a VM template item of the
Content Library `classic-os`. No Packer is involved (ADR-001).

```text
source.type cloud-image  ->  provider downloads + verifies OVA (proxy)
                         ->  deploys imagebuilder-<image>-<build> in the build folder
                         ->  provisioners via VMware Guest Operations
                         ->  shutdown, vApp properties removed, library template, VM destroyed
```

Files:

| File | Purpose |
|---|---|
| `generate.py` | renders `40-vmimage.yaml` and `scripts/70-system-role.sh` from the sources |
| `scripts/*.sh` | ported hardening scripts (one VMImage `shell` provisioner each) |
| `ansible/` | ported `system` role, run with `ansible-playbook -c local` in the guest |
| `10-providerconfig.yaml`, `20-platformprovider.yaml` | provider and target |
| `40-vmimage.yaml` | the generated VMImage |

## Hardening source and adaptation to 26.04

Source: the Packer project `fci-dt/ubuntu-2204` (read only): `scripts/pre/*.sh`,
`scripts/post/90-cleanup.sh`, `ansible/_org/roles/system`, `http/user-data-tpl`,
`docs/hardening.md`.

| Source | Result for 26.04 |
|---|---|
| `05-apparmor.sh` | kept; runs after `apparmor-utils` is installed, enforces profile files only |
| `05-grub.sh` | kept as `/etc/default/grub.d/99-imagebuilder.cfg` (cloud image drop-ins override `/etc/default/grub`) |
| `10-repo.sh` | kept: apt proxy `http://172.28.236.246:8080` stays in the template as before |
| `20-cloud-init.sh` | kept; `datasource_list: [ OVF, VMware, None ]`; installer artifacts do not exist in cloud images |
| `20-update.sh` | kept, `--force-confold` added |
| `30-ansible.sh` | the `ppa:ansible/ansible` PPA has no 26.04 series: `ansible-core` + `python3-apt` from the Ubuntu archive |
| `40-addon.sh` + packages of `user-data-tpl` | kept; unavailable packages are reported and skipped (`ntp`, `cloud-utils`) |
| `80-remove-containerd.sh` | kept (removes podman only, as the original) |
| Ansible role `system` | RHEL to Ubuntu: `yum`/`/etc/sysconfig`/`system-auth-ac` replaced by `apt`, `sysctl.d`, `limits.d`, `pam-auth-update`, `profile.d`; see deviations below |
| `late-commands` of `user-data-tpl` | `tsiadm` with passwordless sudo (locked, no key, see open points) |
| `90-cleanup.sh` | kept; no bashisms, guarded paths, journald, `cloud-init clean --configs network`, root login of the build locked last |
| `docs/hardening.md` (OpenSCAP) | not ported: `scripts/pre/80-oscap.sh` is referenced but absent in the source repository |

Deviations in the role (all commented in `ansible/roles/system/defaults/main.yml`):
`squashfs`, `fat`, `vfat` are not blocked (snapd, EFI partition in fstab);
user `adm` is kept (group owns `/var/log`); `messagebus` is not disabled;
`parted`, `linux-firmware` are not removed; package removal is simulated first and aborts
if cloud-init, openssh-server, open-vm-tools, netplan.io, systemd or the kernel would go.

Not reproducible with a cloud image: the LVM partition layout of the autoinstall
(`/tmp`, `/var`, `/var/tmp`, `/var/log`, `/var/log/audit` as separate volumes).
