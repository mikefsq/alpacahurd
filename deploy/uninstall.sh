#!/usr/bin/env bash
# uninstall.sh — stop and remove the alpacahurd service, binary, and udev rules.
# The config in /etc/alpacahurd is kept.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
	echo "error: run as root (sudo make uninstall)" >&2
	exit 1
fi

# Device instances first, then the orchestrator and both units.
for u in $(systemctl list-units --all --plain --no-legend 'alpacahurd-device@*' 2>/dev/null | awk '{print $1}'); do
	systemctl disable --now "$u" 2>/dev/null || true
done
systemctl disable --now alpacahurd.service 2>/dev/null || true
rm -f /etc/systemd/system/alpacahurd.service /etc/systemd/system/alpacahurd-device@.service
systemctl daemon-reload

rm -f /usr/local/bin/alpacahurd
rm -f /etc/udev/rules.d/99-alpacahurd.rules
udevadm control --reload || true

echo "removed. config kept in /etc/alpacahurd, state in /var/lib/alpacahurd (delete manually if wanted)"
