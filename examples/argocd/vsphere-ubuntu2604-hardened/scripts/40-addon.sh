#!/bin/sh
# Port of scripts/pre/40-addon.sh plus the package list of http/user-data-tpl.
# Packages that no longer exist on 26.04 are reported and skipped:
# ntp is replaced by the distribution time sync service, cloud-utils is split
# into cloud-guest-utils/cloud-image-utils.
set -eu
export DEBIAN_FRONTEND=noninteractive
echo "Install addon components"
for pkg in unzip ca-certificates bash-completion open-vm-tools ufw wireguard \
           apparmor apparmor-utils apparmor-profiles apparmor-profiles-extra \
           net-tools cloud-guest-utils cloud-image-utils open-iscsi auditd acct; do
  if apt-cache show "$pkg" >/dev/null 2>&1; then
    apt-get install -y "$pkg"
  else
    echo "SKIP: package $pkg is not available on this release"
  fi
done
