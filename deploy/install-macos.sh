#!/usr/bin/env bash
# install-macos.sh — install alpacahurd as a launchd daemon on macOS.
#
# Usage (run from the repo root, as root — `sudo make install` does this):
#   sudo deploy/install-macos.sh [path-to-alpacahurd-binary]
#
# Defaults to ./alpacahurd. Build one first with `make` (needs the Xcode
# command-line tools: the macOS USB/HID transports use IOKit via cgo).
set -euo pipefail

BIN_SRC="${1:-./alpacahurd}"
BIN_DST=/usr/local/bin/alpacahurd
CONF_DIR="/Library/Application Support/alpacahurd"
CONF_DST="$CONF_DIR/hurd.json"
DEVICES_DIR="$CONF_DIR/devices.d"
STATE_DIR="$CONF_DIR/state"
LOG_DIR=/Library/Logs/alpacahurd
LABEL=com.mikefsq.alpacahurd
PLIST_DST="/Library/LaunchDaemons/$LABEL.plist"
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

mkdir -p "$CONF_DIR" "$STATE_DIR/devices" "$LOG_DIR"
if [[ -f "$CONF_DST" ]]; then
	echo "keeping existing config $CONF_DST"
else
	"$BIN_DST" -example >"$CONF_DST"
	chmod 0644 "$CONF_DST"
	echo "installed server config -> $CONF_DST"
fi
# Devices are added on the setup page; make seed writes disabled templates instead.
mkdir -p "$DEVICES_DIR"

echo "installing launchd unit -> $PLIST_DST"
launchctl bootout system "$PLIST_DST" 2>/dev/null || true
install -m 0644 "$HERE/deploy/$LABEL.plist" "$PLIST_DST"
# launchd keeps a disabled mark across uninstalls, and bootstrap of a disabled
# job fails with error 5.
launchctl enable "system/$LABEL"
launchctl bootstrap system "$PLIST_DST"
launchctl kickstart -k "system/$LABEL"

echo
launchctl print "system/$LABEL" 2>/dev/null | head -20 || true
echo
echo "done. add devices at http://$(hostname):32227/setup"
echo "restart: sudo launchctl kickstart -k system/$LABEL"
echo "logs: tail -f /Library/Logs/alpacahurd/alpacahurd.log"
