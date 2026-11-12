#!/bin/sh
# Automation user with passwordless sudo. @ADMIN_USER@ is substituted by
# generate.py. The account is locked and keyless until cloud-init or the
# deployment tooling injects a key.
set -eu
ADMIN_USER="@ADMIN_USER@"
id "$ADMIN_USER" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash --groups users "$ADMIN_USER"
passwd -l "$ADMIN_USER" >/dev/null
echo "${ADMIN_USER} ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/${ADMIN_USER}"
chmod 440 "/etc/sudoers.d/${ADMIN_USER}"
visudo -cf "/etc/sudoers.d/${ADMIN_USER}"
timedatectl set-timezone Europe/Berlin || ln -sf /usr/share/zoneinfo/Europe/Berlin /etc/localtime
localectl set-keymap de || echo "WARN: keymap not set"
