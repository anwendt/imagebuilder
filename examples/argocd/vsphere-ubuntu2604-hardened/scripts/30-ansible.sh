#!/bin/sh
# ansible-core comes from the Ubuntu archive (only ansible.builtin modules
# are used by the role).
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y --no-install-recommends ansible-core python3-apt
ansible --version | head -n 1
