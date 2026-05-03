#!/usr/bin/env sh
set -eu

image="${1:?image is required}"
digest="${2:?digest is required}"

case "$digest" in
  sha256:*) ;;
  *) echo "digest must start with sha256:" >&2; exit 1 ;;
esac

for file in \
  config/samples/vmimage-ubuntu-aws-remote.yaml \
  config/samples/vmimage-ubuntu-aws-vsphere.yaml \
  config/samples/vmimage-windows-aws-remote.yaml
do
  perl -0pi -e "s#package: [^\\n]*imagebuilder-provider-aws@sha256:[a-fA-F0-9]+#package: ${image}@${digest}#g" "$file"
done
