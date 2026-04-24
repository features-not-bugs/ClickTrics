#!/bin/sh
# Runs before files are removed (dpkg: prerm; rpm: %preun).
# On upgrade, dpkg calls prerm "upgrade" and rpm passes $1=1 — in that case
# we must NOT stop the service (the new version will restart it in postinst).
set -e

# Detect upgrade vs remove.
#   dpkg: $1 == "upgrade" on upgrade, "remove" on removal
#   rpm:  $1 == 1 on upgrade, 0 on removal
case "$1" in
    upgrade|1)
        exit 0
        ;;
esac

if command -v systemctl >/dev/null 2>&1; then
    if systemctl is-active --quiet clicktrics 2>/dev/null; then
        systemctl stop clicktrics || true
    fi
    if systemctl is-enabled --quiet clicktrics 2>/dev/null; then
        systemctl disable clicktrics || true
    fi
fi

exit 0
