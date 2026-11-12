#!/bin/sh
# Port of the late-commands of http/user-data-tpl: the T-Systems automation
# user with passwordless sudo. The password and SSH key of the Packer build
# are CI variables and are not part of this image; the account is locked and
# keyless until cloud-init/vRA injects a key.
set -eu
ADMIN_USER="tsiadm"
id "$ADMIN_USER" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash --groups users "$ADMIN_USER"
passwd -l "$ADMIN_USER" >/dev/null
echo "${ADMIN_USER} ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/${ADMIN_USER}"
chmod 440 "/etc/sudoers.d/${ADMIN_USER}"
visudo -cf "/etc/sudoers.d/${ADMIN_USER}"
timedatectl set-timezone Europe/Berlin || ln -sf /usr/share/zoneinfo/Europe/Berlin /etc/localtime
localectl set-keymap de || echo "WARN: keymap not set"
