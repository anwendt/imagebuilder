#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y --no-install-recommends prometheus-node-exporter

if command -v systemctl >/dev/null 2>&1; then
  systemctl enable prometheus-node-exporter
fi

apt-get clean
rm -rf /var/lib/apt/lists/*

