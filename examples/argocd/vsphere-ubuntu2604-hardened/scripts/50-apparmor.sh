#!/bin/sh
# Port of scripts/pre/05-apparmor.sh. It runs after the addon step here
# because aa-enforce comes from apparmor-utils. The original globbed
# /etc/apparmor.d/* including directories; only profile files are enforced,
# in one aa-enforce call (one process instead of one per profile).
set -u
echo "Configure apparmor"
find /etc/apparmor.d -maxdepth 1 -type f -print0 | xargs -0 aa-enforce \
  || echo "WARN: aa-enforce reported problems for some profiles"
aa-status --enabled && echo "apparmor enabled" || echo "WARN: apparmor is not enabled in the running kernel"
exit 0
