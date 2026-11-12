#!/bin/sh
# Apply all pending package updates.
# The upgrade runs as a transient systemd unit: it can restart VMware Tools,
# which ends the Guest Operations process that runs this script. The unit
# keeps going, and the provider starts this script again, which then only waits
# for the unit (re-run safe).
set -eu
UNIT=imagebuilder-update
RC=/var/lib/imagebuilder-update.rc
if [ ! -f "$RC" ] && ! systemctl is-active --quiet "$UNIT"; then
  cat > /usr/local/sbin/imagebuilder-update <<'UPDEOF'
#!/bin/sh
export DEBIAN_FRONTEND=noninteractive
dpkg --configure -a
apt-get update -y
apt-get upgrade -y -o Dpkg::Options::=--force-confold
apt-get full-upgrade -y -o Dpkg::Options::=--force-confold
apt-get autoremove -y
UPDEOF
  chmod 0755 /usr/local/sbin/imagebuilder-update
  rm -f "$RC"
  systemd-run --unit="$UNIT" --collect --no-block \
    --property=StandardOutput=append:/var/log/imagebuilder-update.log \
    --property=StandardError=append:/var/log/imagebuilder-update.log \
    /bin/sh -c '/usr/local/sbin/imagebuilder-update; echo $? > /var/lib/imagebuilder-update.rc'
fi
echo "waiting for the package upgrade unit"
while [ ! -f "$RC" ]; do
  sleep 5
done
rc=$(cat "$RC")
tail -n 30 /var/log/imagebuilder-update.log || true
rm -f "$RC" /usr/local/sbin/imagebuilder-update
[ "$rc" = "0" ] || { echo "package upgrade failed with code $rc"; exit 1; }
echo "System update and upgrade completed successfully."
