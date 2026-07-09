#!/usr/bin/env bash
# uninstall.sh — stop and remove the alpacahurd service, binary, and udev rules.
# The config in /etc/alpacahurd is kept (delete it yourself if you mean it).
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
	echo "error: run as root (sudo make uninstall)" >&2
	exit 1
fi

systemctl disable --now alpacahurd.service 2>/dev/null || true
rm -f /etc/systemd/system/alpacahurd.service
systemctl daemon-reload

rm -f /usr/local/bin/alpacahurd
rm -f /etc/udev/rules.d/99-alpacahurd.rules
udevadm control --reload || true

echo "removed. config kept in /etc/alpacahurd (delete manually if wanted)"
