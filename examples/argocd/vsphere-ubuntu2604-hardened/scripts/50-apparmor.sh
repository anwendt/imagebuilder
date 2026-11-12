#!/bin/sh
# Enforce all AppArmor profiles. Runs after the addon step because aa-enforce
# comes from apparmor-utils. Only profile files are enforced, in one call.
set -u
echo "Configure apparmor"
find /etc/apparmor.d -maxdepth 1 -type f -print0 | xargs -0 aa-enforce \
  || echo "WARN: aa-enforce reported problems for some profiles"
aa-status --enabled && echo "apparmor enabled" || echo "WARN: apparmor is not enabled in the running kernel"
exit 0
