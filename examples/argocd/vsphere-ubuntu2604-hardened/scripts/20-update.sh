#!/bin/sh
# Port of scripts/pre/20-update.sh.
set -eu
export DEBIAN_FRONTEND=noninteractive
echo "Updating system with the latest patches..."
apt-get update -y
apt-get upgrade -y -o Dpkg::Options::=--force-confold
apt-get full-upgrade -y -o Dpkg::Options::=--force-confold
apt-get autoremove -y
echo "System update and upgrade completed successfully."
