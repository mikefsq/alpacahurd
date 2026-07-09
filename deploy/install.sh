#!/usr/bin/env bash
# install.sh — install alpacahurd as a systemd service on Linux (e.g. a Raspberry Pi).
#
# Usage (run from the repo root, as root — `sudo make install` does this):
#   sudo deploy/install.sh [path-to-alpacahurd-binary]
#
# Defaults to ./alpacahurd. Build one first with `make`.
set -euo pipefail

BIN_SRC="${1:-./alpacahurd}"
BIN_DST=/usr/local/bin/alpacahurd
CONF_DIR=/etc/alpacahurd
CONF_DST="$CONF_DIR/hurd.json"
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
	# Seed a starter config from the binary itself: every compiled-in driver,
	# disabled. Enable yours, fill in serials/addresses, and restart.
	"$BIN_DST" -example >"$CONF_DST"
	chmod 0644 "$CONF_DST"
	echo "installed starter config -> $CONF_DST   *** EDIT THIS for your hardware ***"
fi

echo "installing unit -> $UNIT_DST"
install -m 0644 "$HERE/deploy/alpacahurd.service" "$UNIT_DST"

echo "installing udev rules -> $RULES_DST"
install -m 0644 "$HERE/deploy/99-alpacahurd.rules" "$RULES_DST"
udevadm control --reload && udevadm trigger
echo "  (replug USB cameras/devices so the new permissions apply)"

systemctl daemon-reload
systemctl enable --now alpacahurd.service
echo
systemctl --no-pager --full status alpacahurd.service || true
echo
echo "done. edit $CONF_DST then: sudo systemctl restart alpacahurd"
echo "logs: journalctl -u alpacahurd -f"
