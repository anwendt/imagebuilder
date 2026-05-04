#!/usr/bin/env sh
set -eu

image="${1:?image is required}"
digest="${2:?digest is required}"

exec "$(dirname "$0")/update-provider-digest.sh" vsphere "$image" "$digest"
