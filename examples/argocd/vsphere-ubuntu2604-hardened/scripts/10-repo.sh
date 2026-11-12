#!/bin/sh
# Port of scripts/pre/10-repo.sh: apt through the corporate proxy.
# The proxy is part of the template, as in the Packer build (the FCI network
# only reaches archives through it).
set -eu
PROXY="http://172.28.236.246:8080"
echo "configuring apt proxy ${PROXY}"
cat > /etc/apt/apt.conf.d/proxy.conf <<APTEOF
Acquire::http::Proxy "${PROXY}";
Acquire::https::Proxy "${PROXY}";
APTEOF
cat /etc/apt/apt.conf.d/proxy.conf
