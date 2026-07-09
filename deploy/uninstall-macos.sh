#!/usr/bin/env bash
# uninstall-macos.sh — stop and remove the alpacahurd launchd daemon and binary.
# The config in /etc/alpacahurd is kept (delete it yourself if you mean it).
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

echo "removed. config kept in /etc/alpacahurd (delete manually if wanted)"
