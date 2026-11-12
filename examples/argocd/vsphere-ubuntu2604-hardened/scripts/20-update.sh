#!/bin/sh
# Apply all pending package updates.
# open-vm-tools is held during the upgrade: its restart would kill the guest
# process that VMware Guest Operations runs this script in. It is not upgraded
# here (the next regular update takes it).
set -eu
export DEBIAN_FRONTEND=noninteractive
dpkg --configure -a
if dpkg -s open-vm-tools >/dev/null 2>&1; then
  apt-mark hold open-vm-tools
fi
echo "Updating system with the latest patches..."
apt-get update -y
apt-get upgrade -y -o Dpkg::Options::=--force-confold
apt-get full-upgrade -y -o Dpkg::Options::=--force-confold
apt-get autoremove -y
apt-mark unhold open-vm-tools || true
echo "System update and upgrade completed successfully."
