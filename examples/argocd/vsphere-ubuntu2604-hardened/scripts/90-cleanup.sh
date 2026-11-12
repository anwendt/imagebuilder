#!/bin/sh
# Generalize the image: logs, SSH host keys, machine-id, cloud-init state.
# The provider revokes the temporary build login afterwards.
set -u
systemctl stop rsyslog 2>/dev/null || true
apt-get clean

logrotate -f /etc/logrotate.conf || true
rm -f /var/log/*.gz /var/log/dmesg.old
[ -d /var/log/audit ] && : > /var/log/audit/audit.log
: > /var/log/wtmp
: > /var/log/lastlog

rm -rf /etc/ssh/ssh_host_*

rm -fr /var/log/aide/* /var/log/audit/* /var/log/chrony/* /var/log/pluto/* /var/log/private/*
journalctl --rotate >/dev/null 2>&1 || true
journalctl --vacuum-time=1s >/dev/null 2>&1 || true
rm -rf /var/log/journal/*

rm -f /etc/udev/rules.d/70-persistent-net.rules

rm -rf /var/lib/apt/lists/*
rm -rf /tmp/* /var/tmp/*

: > /etc/machine-id
rm -f /var/lib/dbus/machine-id
ln -s /etc/machine-id /var/lib/dbus/machine-id

unset HISTFILE
: > /root/.bash_history
rm -f /root/.bash_history

sed -i 's/preserve_hostname: false/preserve_hostname: true/g' /etc/cloud/cloud.cfg
: > /etc/hostname
: > /etc/hosts
: > /etc/resolv.conf 2>/dev/null || true
hostnamectl set-hostname localhost 2>/dev/null || true

# 26.04: besides seed and logs, drop the rendered network config so the next
# boot renders it for the new instance (the old netplan matches the build NIC).
cloud-init clean --seed --logs --configs network

# The temporary build login is locked and verified by the provider after the
# last provisioner (ProviderConfig revokeBuildCredential, default on), not here:
# locking it in this script would make Guest Operations reject the login the
# provider still uses to read the exit code.
rm -f /var/log/imagebuilder-provision.log
sync
