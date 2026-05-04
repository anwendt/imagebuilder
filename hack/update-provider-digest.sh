#!/usr/bin/env sh
set -eu

provider="${1:?provider is required}"
image="${2:?image is required}"
digest="${3:?digest is required}"

case "$provider" in
  aws|vsphere|azure|openstack) ;;
  *) echo "provider must be one of: aws, vsphere, azure, openstack" >&2; exit 1 ;;
esac

case "$digest" in
  sha256:*) ;;
  *) echo "digest must start with sha256:" >&2; exit 1 ;;
esac

find config/samples -type f -name '*.yaml' -exec grep -El "imagebuilder-provider-${provider}@sha256:[a-fA-F0-9]{64}" {} + |
while IFS= read -r file; do
  perl -0pi -e "s#package: [^\\n]*imagebuilder-provider-${provider}@sha256:[a-fA-F0-9]{64}#package: ${image}@${digest}#g" "$file"
done
