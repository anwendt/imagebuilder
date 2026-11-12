#!/bin/sh
# cloud-init datasources for vSphere. Cloud images ship no installer
# artifacts; their removal is kept for safety.
set -eu
echo "configuration of cloud-init"
rm -f /etc/cloud/cloud.cfg.d/99-installer.cfg
rm -f /etc/cloud/cloud.cfg.d/subiquity-disable-cloudinit-networking.cfg
echo "datasource_list: [ OVF, VMware, None ]" > /etc/cloud/cloud.cfg.d/90_dpkg.cfg
