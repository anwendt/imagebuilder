#!/usr/bin/env bash
set -euo pipefail

install -d -m 0755 /etc/ssh/sshd_config.d
cat >/etc/ssh/sshd_config.d/90-imagebuilder-hardening.conf <<'EOF'
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
X11Forwarding no
ClientAliveInterval 300
ClientAliveCountMax 2
EOF

install -d -m 0755 /etc/sysctl.d
cat >/etc/sysctl.d/90-imagebuilder-hardening.conf <<'EOF'
net.ipv4.ip_forward = 0
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.rp_filter = 1
net.ipv4.conf.default.rp_filter = 1
EOF

sysctl --system

if command -v systemctl >/dev/null 2>&1; then
  systemctl reload ssh || systemctl reload sshd || true
fi

