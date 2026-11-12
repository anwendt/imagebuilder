#!/bin/sh
# Baseline packages. Packages that do not exist on the release are reported
# and skipped.
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
