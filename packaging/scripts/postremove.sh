#!/bin/sh
# Runs after files are removed (dpkg: postrm; rpm: %postun).
# On upgrade, dpkg passes "upgrade" and rpm passes $1=1 — keep the user +
# config intact. On purge (dpkg "purge"), fully clean up.
set -e

# Reload systemd in all cases so removed/changed units are picked up.
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

case "$1" in
    purge)
        # dpkg purge — remove service user, our modules-load.d drop-in,
        # and state dirs. /etc/clicktrics/config.yaml was created by our
        # postinst (not shipped as a conffile) so it needs explicit cleanup.
        if getent passwd clicktrics >/dev/null 2>&1; then
            userdel clicktrics 2>/dev/null || true
        fi
        if getent group clicktrics >/dev/null 2>&1; then
            groupdel clicktrics 2>/dev/null || true
        fi
        rm -f /etc/modules-load.d/clicktrics.conf 2>/dev/null || true
        rm -f /etc/clicktrics/config.yaml 2>/dev/null || true
        rmdir /etc/clicktrics 2>/dev/null || true
        rm -rf /var/lib/clicktrics 2>/dev/null || true
        # The msr module might be used by other tools (turbostat,
        # cpufrequtils) — don't unload it here.
        ;;
esac

exit 0
