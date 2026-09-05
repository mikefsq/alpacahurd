#!/usr/bin/env bash
# uninstall-macos.sh — stop and remove the alpacahurd launchd daemon and binary.
# The config in /Library/Application Support/alpacahurd is kept.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
	echo "error: run as root (sudo make uninstall)" >&2
	exit 1
fi

LABEL=com.mikefsq.alpacahurd
PLIST=/Library/LaunchDaemons/$LABEL.plist

launchctl bootout system "$PLIST" 2>/dev/null || true
rm -f "$PLIST"
rm -f /usr/local/bin/alpacahurd

echo "removed. config and state kept in /Library/Application Support/alpacahurd (delete manually if wanted)"
