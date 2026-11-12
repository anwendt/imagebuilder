#!/bin/sh
# Optional apt proxy. @PROXY@ is substituted by generate.py; an empty value
# configures nothing. The proxy stays in the template when set.
set -eu
PROXY="@PROXY@"
if [ -z "${PROXY}" ]; then
  echo "no apt proxy configured"
  exit 0
fi
echo "configuring apt proxy ${PROXY}"
cat > /etc/apt/apt.conf.d/proxy.conf <<APTEOF
Acquire::http::Proxy "${PROXY}";
Acquire::https::Proxy "${PROXY}";
APTEOF
cat /etc/apt/apt.conf.d/proxy.conf
