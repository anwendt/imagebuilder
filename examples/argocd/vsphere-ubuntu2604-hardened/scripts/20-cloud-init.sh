#!/bin/sh
# Port of scripts/pre/20-cloud-init.sh.
# 26.04 cloud images ship no installer artifacts (99-installer.cfg,
# subiquity-disable-cloudinit-networking.cfg); removal is kept for safety.
set -eu
echo "configuration of cloud-init"
rm -f /etc/cloud/cloud.cfg.d/99-installer.cfg
rm -f /etc/cloud/cloud.cfg.d/subiquity-disable-cloudinit-networking.cfg
echo "datasource_list: [ OVF, VMware, None ]" > /etc/cloud/cloud.cfg.d/90_dpkg.cfg
