#!/bin/sh
# Remove podman if present.
set -eu
export DEBIAN_FRONTEND=noninteractive
echo "remove existing container services"
if dpkg -s podman >/dev/null 2>&1; then
  apt-get remove -y podman
fi
