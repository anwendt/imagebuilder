#!/bin/sh
# Short GRUB timeout. GRUB drop-ins in /etc/default/grub.d override the cloud image's own
# settings reliably; editing /etc/default/grub alone would be overridden by
# 50-cloudimg-settings.cfg.
set -eu
echo "change grub configuration"
install -d -m 0755 /etc/default/grub.d
cat > /etc/default/grub.d/99-imagebuilder.cfg <<'GRUBEOF'
GRUB_TIMEOUT=1
GRUB_RECORDFAIL_TIMEOUT=0
GRUBEOF
update-grub
