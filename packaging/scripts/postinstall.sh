#!/bin/sh
# Runs after files are unpacked (dpkg: postinst configure; rpm: %post).
# Must be idempotent — re-runs on package upgrade.
set -e

CONFIG_DIR=/etc/clicktrics
ACTIVE_CONFIG="$CONFIG_DIR/config.yaml"
TEMPLATE="$CONFIG_DIR/config.yaml.example"

# Create dedicated system user on first install.
if ! getent passwd clicktrics >/dev/null 2>&1; then
    useradd \
        --system \
        --home-dir /var/lib/clicktrics \
        --shell /usr/sbin/nologin \
        --user-group \
        --comment "ClickTrics host metrics scraper" \
        clicktrics || true
fi

# Seed the active config from the template ONLY if it doesn't exist.
# Re-installs and upgrades leave any existing file untouched — even if the
# operator wiped it and kept an empty placeholder, we still skip, because
# recreating a file the admin removed on purpose would be rude.
if [ ! -e "$ACTIVE_CONFIG" ] && [ -f "$TEMPLATE" ]; then
    cp "$TEMPLATE" "$ACTIVE_CONFIG"
    echo "Seeded $ACTIVE_CONFIG from template."
fi

# Lock down config dir perms so only the service user (and root) can read
# secrets in the active config. Idempotent.
if [ -d "$CONFIG_DIR" ]; then
    chgrp clicktrics "$CONFIG_DIR" 2>/dev/null || true
    chmod 0750 "$CONFIG_DIR" 2>/dev/null || true
    if [ -f "$ACTIVE_CONFIG" ]; then
        chgrp clicktrics "$ACTIVE_CONFIG" 2>/dev/null || true
        chmod 0640 "$ACTIVE_CONFIG" 2>/dev/null || true
    fi
fi

# Ensure the 'msr' kernel module is loaded now and persisted across reboots.
# The MSR collector needs /dev/cpu/*/msr; without the module those files
# don't exist and the collector self-disables silently.
MSR_MODULES_CONF=/etc/modules-load.d/clicktrics.conf
if [ ! -f "$MSR_MODULES_CONF" ]; then
    echo msr > "$MSR_MODULES_CONF"
    chmod 0644 "$MSR_MODULES_CONF"
fi

# Load immediately so the service can start without a reboot.
if command -v modprobe >/dev/null 2>&1; then
    if ! modprobe msr 2>/dev/null; then
        # Common benign cases: running in an unprivileged container, or the
        # CPU doesn't expose MSRs. Print a note but don't fail the install —
        # the collector will self-disable cleanly if /dev/cpu/0/msr is absent.
        echo "Warning: 'modprobe msr' failed. If this host has an Intel/AMD CPU"
        echo "         and the kernel supports the msr module, check that the"
        echo "         package is installed (e.g. linux-modules-\$(uname -r))."
    fi
fi

# Pick up the new unit.
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
fi

# First install → print next-steps. Upgrades skip the banner.
if [ "$1" = "configure" ] && [ -z "$2" ]; then
    cat <<'EOF'

ClickTrics installed. The active config is at /etc/clicktrics/config.yaml
(seeded from /etc/clicktrics/config.yaml.example on first install only —
subsequent package upgrades never modify it). Default is all collectors
enabled, stdout exporter.

The 'msr' kernel module was loaded and persisted via
/etc/modules-load.d/clicktrics.conf so per-core CPU MSR reads work across
reboots.

To start immediately and see metrics in the journal:

  sudo systemctl enable --now clicktrics
  sudo journalctl -u clicktrics -f

To ship metrics to ClickHouse:

  1. Edit the config:
     sudo $EDITOR /etc/clicktrics/config.yaml
       – change `exporter.type: stdout` → `clickhouse`
       – fill in `exporter.clickhouse.dsn`

  2. Apply the schema on your CH cluster (once):
     sudo clicktrics migrate up

  3. Restart the scraper:
     sudo systemctl restart clicktrics

Grafana dashboard JSON lives at
/usr/share/clicktrics/grafana/host-overview.json — import via the UI.

EOF
fi

exit 0
