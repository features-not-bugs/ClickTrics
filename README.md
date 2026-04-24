# ClickTrics

Linux host metrics scraper → ClickHouse → Grafana.

Runs as a systemd unit on each target host, collects metrics from `/proc`,
`/sys`, SMART, and CPU MSRs, and ships rows to a shared ClickHouse cluster
for fleet-wide dashboards.

## What it collects

| Area | Source | Table |
|------|--------|-------|
| Per-core CPU %, freq (avg + busy), temp, Vcore | `/proc/stat`, `/dev/cpu/*/msr` (APERF/MPERF/TSC, IA32_THERM_STATUS, IA32_PERF_STATUS) | `cpu_core_stats` |
| System CPU aggregates | `/proc/stat`, `/proc/loadavg` | `cpu_system_stats` |
| CPU power + package thermal | `/dev/cpu/*/msr` (Intel RAPL) | `cpu_power` |
| Memory | `/proc/meminfo` | `memory_stats` |
| Page faults / swap / OOM | `/proc/vmstat` | `memory_vmstat` |
| Pressure Stall Info (PSI) | `/proc/pressure/*` | `pressure_stall` |
| Disk I/O | `/proc/diskstats` | `disk_io` |
| Mount capacity | `/proc/self/mountinfo` + `statfs` | `filesystem_stats` |
| NIC counters + link state | `/proc/net/dev`, `/sys/class/net` | `network_interface` |
| TCP counters | `/proc/net/snmp`, `/proc/net/netstat` | `network_tcp` |
| Socket state distribution | `/proc/net/{tcp,tcp6,udp,udp6}` | `network_socket_states` |
| Conntrack utilisation | `/proc/sys/net/netfilter` | `network_conntrack` |
| Per-PID stats | `/proc/[pid]/*` | `process_stats`, `process_summary` |
| Uptime, users, FDs | `/proc/uptime`, utmp, `/proc/sys/fs/file-nr` | `system_info` |
| SMART drive health | NVMe / ATA SMART (via `anatol/smart.go`) | `smart_stats` |

All three CPU tables are populated from the `cpu` package (per-core
stats, per-package RAPL); the config-level collector name for RAPL is
`msr` for brevity.

## Install

### 1. Install the package

See the [latest release](https://github.com/features-not-bugs/clicktrics/releases/latest)
for download links and install commands (both `.deb` and static binary for
linux/amd64).

The `.deb` installs:

- `/usr/local/bin/clicktrics` — the binary
- `/lib/systemd/system/clicktrics.service` — the unit
- `/etc/clicktrics/config.yaml.example` — pristine config template (refreshed on every upgrade)
- `/etc/clicktrics/config.yaml` — **seeded from the template only if it doesn't already exist**. Upgrades and re-installs never touch an existing file, so operator edits are safe.
- `/etc/modules-load.d/clicktrics.conf` — ensures `msr` loads at boot. Postinstall also runs `modprobe msr` immediately so you don't need to reboot.
- `/usr/share/clicktrics/migrations/00001_init.sql` — ClickHouse schema (also embedded in the binary)
- `/usr/share/clicktrics/grafana/host-overview.json` — Grafana dashboard
- `clicktrics` system user with `/etc/clicktrics` group-readable at `0750`

The default seeded config has all collectors enabled with the stdout
exporter, so the service starts producing data immediately.

If you install the static binary instead, you're responsible for writing
`/etc/clicktrics/config.yaml`, installing the systemd unit at
[deploy/systemd/clicktrics.service](deploy/systemd/clicktrics.service),
and creating a `clicktrics` system user. The `.deb` postinstall in
[packaging/scripts/postinstall.sh](packaging/scripts/postinstall.sh) is
the canonical reference.

### 2. Start (stdout mode — for sanity checking)

Straight after install, the service is disabled. Start it once to confirm
every collector is producing rows:

```sh
sudo systemctl enable --now clicktrics
sudo journalctl -u clicktrics -f
```

You should see JSON lines streaming — one per row per collector tick. If
not, check `systemctl status clicktrics` for the offending collector.

### 3. Prepare ClickHouse (database + user)

Create the target database and a user for the scraper. ClickTrics uses
ClickHouse in three distinct phases, each needing slightly different
privileges:

| Phase | What it does | Permissions needed |
|-------|--------------|--------------------|
| Preflight (startup) | `SELECT 1 FROM <table> LIMIT 0` for every required table | `SELECT` |
| Runtime insert | `INSERT INTO <table>`, preceded by implicit column introspection | `SELECT`, `INSERT` |
| `clicktrics migrate` | Applies DDL + maintains goose's `goose_db_version` tracking table | `CREATE TABLE`, `DROP TABLE`, `ALTER` |

**Typical setup — single scraper user that can migrate itself:**

```sql
CREATE DATABASE IF NOT EXISTS clicktrics;

CREATE USER clicktrics IDENTIFIED WITH sha256_password BY 'CHANGEME';
GRANT SELECT, INSERT ON clicktrics.* TO clicktrics;
GRANT CREATE TABLE, DROP TABLE, ALTER ON clicktrics.* TO clicktrics;
```

**Tighter setup — admin user for migrations, minimal runtime user:**

```sql
-- One-time, run under an admin account
CREATE USER clicktrics_migrator IDENTIFIED WITH sha256_password BY 'CHANGEME';
GRANT SELECT, INSERT, CREATE TABLE, DROP TABLE, ALTER ON clicktrics.* TO clicktrics_migrator;

-- Long-running scraper — only what it needs at runtime
CREATE USER clicktrics_scraper IDENTIFIED WITH sha256_password BY 'CHANGEME';
GRANT SELECT, INSERT ON clicktrics.* TO clicktrics_scraper;
```

Run `clicktrics migrate up --dsn=…clicktrics_migrator…` once from a
deploy box, then roll the scraper out with `clicktrics_scraper`
credentials.

**Grafana (read-only, separate user):**

```sql
CREATE USER clicktrics_reader IDENTIFIED WITH sha256_password BY 'CHANGEME';
GRANT SELECT ON clicktrics.* TO clicktrics_reader;
GRANT SELECT ON system.databases TO clicktrics_reader;  -- dashboard's "database" template variable
```

Notes:
- `async_insert=1` (always set by the driver) is a session setting, not
  a permission — no extra grant needed.
- For `ReplicatedMergeTree` the user also needs whatever your ZooKeeper
  ACLs require; ClickHouse itself adds no new grants.
- SMART / MSR collectors have zero ClickHouse involvement; they just
  produce rows that flow through the same INSERT path.

### 4. Configure the scraper's DSN

Edit `/etc/clicktrics/config.yaml` — at minimum change the exporter type
and fill in the DSN:

```yaml
exporter:
  type: clickhouse                     # was: stdout
  clickhouse:
    dsn: "https://clicktrics:CHANGEME@ch.example.com:443/clicktrics?secure=true"
    flush_interval: 1s
    send_timeout: 30s
    queue_size: 100000
```

DSN forms: `clickhouse://` (native, 9000), `clickhouse://…?secure=true`
(native over TLS, 9440), `http://` (HTTP, 8123), `https://…?secure=true`
(HTTP over TLS, 443). For `https://`, **both** the scheme and
`?secure=true` are required — the driver needs both signals.

### 5. Apply the ClickHouse schema (once per cluster)

Migrations ship embedded in the binary:

```sh
sudo clicktrics migrate up
```

Other subcommands: `down` (rollback one), `status`, `version`, `redo`,
`reset` (rollback all). DSN can also be passed explicitly via `--dsn=…`.

The migration is idempotent (`CREATE TABLE IF NOT EXISTS`) so running it
against a cluster that already has the schema is safe. For a clustered
ClickHouse, the shipped migration uses `MergeTree` — if you need
`ReplicatedMergeTree`, fork
[internal/migrate/migrations/00001_init.sql](internal/migrate/migrations/00001_init.sql)
and apply via `clickhouse-client --multiquery` instead.

The scraper runs a preflight check on startup and fails fast if any table
is missing, naming the table in the error.

### 6. Restart and import the dashboard

```sh
sudo systemctl restart clicktrics
```

In Grafana: **Dashboards → New → Import**, upload
[deploy/grafana/dashboards/host-overview.json](deploy/grafana/dashboards/host-overview.json)
(or from a host that installed the `.deb`:
`/usr/share/clicktrics/grafana/host-overview.json`), and select a
ClickHouse datasource when prompted. The dashboard has three cascading
variables: datasource → database → host.

## Capabilities

The systemd unit runs as a dedicated `clicktrics` user with the minimum
capabilities required:

| Capability | Needed for |
|------------|-----------|
| `CAP_SYS_RAWIO` | Open `/dev/cpu/*/msr` + SATA/SAS SMART `SG_IO` ioctls |
| `CAP_SYS_PTRACE` | Read `/proc/[pid]/` entries for other UIDs |
| `CAP_SYS_ADMIN` | NVMe SMART `NVME_IOCTL_ADMIN_CMD` — drop if no NVMe drives |
| `CAP_DAC_READ_SEARCH` | Read `/proc`, `/sys`, `/var/run/utmp` |

## CLI

```
clicktrics                              # run the scraper (default config: /etc/clicktrics/config.yaml)
clicktrics --config=path.yaml           # override config path
clicktrics --version                    # one-line version info
clicktrics version                      # multi-line version info
clicktrics version --check              # also compare against latest GitHub release
clicktrics version --json               # machine-readable output
clicktrics migrate up|down|status|…     # ClickHouse schema migrations
```

## Configuration reference

See [clicktrics.example.yaml](clicktrics.example.yaml) for all tunables.
Each collector is individually enabled with its own cadence; unknown keys
are ignored so forward-compatible configs are safe on older binaries.

Key environment overrides:

| Env | Purpose |
|-----|---------|
| `CLICKTRICS_HOST` | Override the `host` label (defaults to `os.Hostname()`) |
| `PROC_ROOT` | Root of `/proc` (defaults to `/proc`) |
| `SYS_ROOT` | Root of `/sys` (defaults to `/sys`) |

## Operations

- **Self-metrics**: `:9090/metrics` exposes `clicktrics_*` counters —
  collector runs, errors, flush latency, rows shipped/dropped. Scrape
  this separately (e.g. from another Prometheus) so ClickTrics is
  observable even when the ClickHouse sink is down.
- **Update awareness**: each host periodically polls
  `api.github.com/repos/<owner>/<repo>/releases/latest` and exports
  `clicktrics_build_info`, `clicktrics_upstream_release_info`, and
  `clicktrics_update_available` (0 or 1). A fleet-wide "outdated
  scrapers" alert is a one-liner:
  `count by (version) (clicktrics_update_available == 1) > 0`.
  Disable via `update_check.enabled: false` in the config.
- **Error budget**: each collector self-disables after N consecutive
  failures (configurable). Siblings continue running.
- **Multi-host**: set a unique `host` label per scraper. The schema uses
  `(host, <entity>, ts)` ordering so Grafana queries narrow to host
  cheaply.
- **Clustered ClickHouse**: swap `MergeTree` → `ReplicatedMergeTree` in
  the schema and front with `Distributed` tables. Point the scraper DSN
  at the Distributed database. No application changes needed.

## Development

```sh
make                  # build for current platform → dist/clicktrics
make test             # go test -race ./...
make build-linux      # cross-compile linux/amd64
make deb              # build the .deb (requires nfpm)
make help             # list all targets
```

Quick local iteration — run against your laptop; every collector that
needs `/proc` or `/sys` self-disables on non-Linux, but the stdout
exporter and `:9090` observability endpoints still respond:

```sh
go run ./cmd/clicktrics --config=clicktrics.example.yaml
```

Building `.deb`s locally requires [nfpm](https://nfpm.goreleaser.com/install/):

```sh
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
make deb VERSION=v0.1.0
```

Releases fire on `vX.Y.Z` tag pushes — the workflow builds the binary
with `VERSION=v0.1.0` baked in via `-ldflags`, produces the matching
`clicktrics_0.1.0_amd64.deb`, and uploads both to the GitHub Release
with a SHA256 sum file.

Platform support: Linux is the only supported runtime target. The binary
builds cleanly on macOS and BSD for development, but every collector
other than the exporter/runner depends on `/proc`, `/sys`,
`/dev/cpu/*/msr`, or Linux-specific SMART and utmp formats.

## Roadmap

- AMD RAPL MSRs (`MSR_CORE_ENERGY_STAT`, etc.) — currently RAPL path is Intel only
- Netlink `SOCK_DIAG` socket stats (current path: `/proc/net/tcp*`, O(sockets))
- eBPF / XDP flow tracking
- Reboot-resilient cumulative-counter handling across restarts

## License

MIT.
