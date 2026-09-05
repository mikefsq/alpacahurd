#!/usr/bin/env bash
# install.sh — install alpacahurd as a systemd service on Linux (e.g. a Raspberry Pi).
#
# Usage (run from the repo root, as root — `sudo make install` does this):
#   sudo deploy/install.sh [path-to-alpacahurd-binary]
#
# Defaults to ./alpacahurd. Build one first: `make fat` for the compiled-in
# layout, `make` for the bare orchestrator over separate driver binaries.
set -euo pipefail

BIN_SRC="${1:-./alpacahurd}"
BIN_DST=/usr/local/bin/alpacahurd
CONF_DIR=/etc/alpacahurd
CONF_DST="$CONF_DIR/hurd.json"
DEVICES_DIR="$CONF_DIR/devices.d"
STATE_DIR=/var/lib/alpacahurd
UNIT_DST=/etc/systemd/system/alpacahurd.service
RULES_DST=/etc/udev/rules.d/99-alpacahurd.rules
HERE="$(cd "$(dirname "$0")/.." && pwd)" # repo root

if [[ $EUID -ne 0 ]]; then
	echo "error: run as root (sudo make install)" >&2
	exit 1
fi
if [[ ! -f "$BIN_SRC" ]]; then
	echo "error: binary '$BIN_SRC' not found — run 'make' first" >&2
	exit 1
fi

echo "installing binary -> $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"

mkdir -p "$CONF_DIR"
if [[ -f "$CONF_DST" ]]; then
	echo "keeping existing config $CONF_DST"
else
	"$BIN_DST" -example >"$CONF_DST"
	chmod 0644 "$CONF_DST"
	echo "installed server config -> $CONF_DST"
fi
# Preserve existing device files.
"$BIN_DST" -example-devices "$DEVICES_DIR"
echo "device files -> $DEVICES_DIR/   *** EDIT THESE for your hardware ***"
# Create state storage before the service starts.
mkdir -p "$STATE_DIR/devices"

echo "installing unit -> $UNIT_DST"
install -m 0644 "$HERE/deploy/alpacahurd.service" "$UNIT_DST"
install -m 0644 "$HERE/deploy/alpacahurd-device@.service" /etc/systemd/system/alpacahurd-device@.service

echo "installing udev rules -> $RULES_DST"
install -m 0644 "$HERE/deploy/99-alpacahurd.rules" "$RULES_DST"
udevadm control --reload && udevadm trigger
echo "  (replug USB cameras/devices so the new permissions apply)"

systemctl daemon-reload
systemctl enable --now alpacahurd.service
echo
systemctl --no-pager --full status alpacahurd.service || true
echo
echo "done. edit $DEVICES_DIR/*.json then: sudo systemctl restart alpacahurd"
echo "logs: journalctl -u alpacahurd -f"
