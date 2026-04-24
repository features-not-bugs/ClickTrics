# ClickTrics Implementation Plan

Linux host metrics scraper → ClickHouse → Grafana. Runs as a systemd unit on
each target host, collects from `/proc`, `/sys`, SMART, and CPU MSRs, ships
rows to a shared ClickHouse cluster for fleet-wide dashboards.

**Status:** all six implementation phases are code-complete. Remaining work
is live validation on a Linux host (real `/proc`/ClickHouse round-trip) and a
small set of hardening items in Phase 5 that were intentionally deferred.

## Locked decisions

| # | Decision | Value |
|---|----------|-------|
| 1 | Module path | `github.com/features-not-bugs/clicktrics` |
| 2 | Config format | YAML |
| 3 | Host label | `os.Hostname()` with `host:` override in config |
| 4 | Process filtering | All processes (no top-N filter) — cadence defaults to 10s to control volume |
| 5 | ClickHouse insert | `async_insert=1` always; client flush interval configurable, default 1s |
| 6 | Schema migrations | **None.** Operator applies DDL manually; app errors fast if tables missing |

## Module layout (as built)

```
ClickTrics/
├── cmd/clicktrics/main.go
├── internal/
│   ├── collector/
│   │   ├── collector.go         # Collector interface
│   │   ├── runner.go            # sync.WaitGroup + per-collector tickers,
│   │   │                        # per-tick timeout, error budget, metrics
│   │   ├── runner_test.go
│   │   ├── cpu/                 # /proc/stat deltas + per-core MSR (freq/temp/Vcore)
│   │   │                        # + per-package RAPL (cpu_power table)
│   │   ├── memory/              # /proc/meminfo
│   │   ├── pressure/            # /proc/pressure/{cpu,io,memory}
│   │   ├── sysstats/            # /proc/stat aggregates + loadavg
│   │   ├── vmstat/              # /proc/vmstat
│   │   ├── disk/                # /proc/diskstats
│   │   ├── filesystem/          # /proc/self/mountinfo + statfs
│   │   ├── network/             # /proc/net/dev + /sys/class/net
│   │   ├── nettcp/              # /proc/net/{snmp,netstat}
│   │   ├── sockets/             # /proc/net/{tcp,tcp6,udp,udp6}
│   │   ├── conntrack/           # /proc/sys/net/netfilter
│   │   ├── process/             # walks /proc/[pid]/
│   │   ├── sysinfo/             # uptime, utmp users, FDs, kernel/OS
│   │   ├── smart/               # anatol/smart.go (linux build-tagged)
│   │   └── (MSR-related code lives in cpu/ — it's all CPU-register state)
│   ├── config/config.go
│   ├── exporter/
│   │   ├── exporter.go
│   │   ├── stdout/              # JSON-lines for bring-up
│   │   └── clickhouse/          # async_insert + per-table batcher
│   ├── hostenv/hostenv.go       # PROC_ROOT / SYS_ROOT overrides
│   ├── httpobs/httpobs.go       # /healthz, /readyz, /metrics on :9090
│   ├── metrics/metrics.go       # Prometheus self-counters
│   └── sample/sample.go         # Batch interface, TypedBatch, MultiBatch
├── schemas/clickhouse/*.sql          # operator-applied schema + migrations
├── deploy/
│   ├── grafana/dashboards/           # importable Grafana dashboards
│   └── systemd/clicktrics.service
├── .github/workflows/release.yml     # builds linux/amd64 binary + .deb, attaches to Release
├── .golangci.yml
└── go.mod
```

## Phase status

### ✅ Phase 0 — Scaffold

- `go.mod`, entry point, `Collector` interface, runner with per-collect
  timeout + error budget, stdout exporter, config loader, runner unit tests.

### ✅ Phase 1 — CPU + memory collectors

- `collector/cpu/cpu.go` — `/proc/stat` stateful deltas, per-core freq from
  sysfs, per-core temperature via hwmon coretemp labels.
- `collector/memory/memory.go` — `/proc/meminfo` snapshot including swap,
  slab, hugepages, dirty/writeback.
- Table-driven tests for the CPU delta math and path helpers.

### ✅ Phase 2 — ClickHouse exporter

- `exporter/clickhouse/clickhouse.go` — `clickhouse-go/v2` native protocol,
  forces `async_insert=1` + `wait_for_async_insert=0` on every session.
- Per-table in-memory buffer, flush on interval (default 1s). Bounded queue
  with drop-oldest; drops recorded in
  `clicktrics_exporter_rows_dropped_total`.
- Startup preflight: `SELECT 1 FROM <each required table> LIMIT 0`. Missing
  tables → fatal error naming the table and pointing at
  `schemas/clickhouse/001_init.sql`.
- `sample.MultiBatch` unpacked to per-table sub-batches so the process
  collector can target two tables from one `Collect` call.

### ✅ Phase 3 — Dashboards + docs

- `deploy/grafana/dashboards/host-overview.json` — comprehensive dashboard
  covering every collected metric; 14 collapsible sections, per-entity
  time-series with `$__timeInterval(ts)` bucketing, three cascading
  variables (datasource → database → host).
- `README.md` documents install, schema setup, systemd rollout, config
  reference, and operations.

### ✅ Phase 4 — Collector expansion

| Collector pkg | Source | Default interval | Table(s) |
|---------------|--------|------------------|----------|
| `pressure`    | `/proc/pressure/{cpu,io,memory}` | 1s | `pressure_stall` |
| `sysstats`    | `/proc/stat` + `/proc/loadavg` | 1s | `cpu_system_stats` |
| `vmstat`      | `/proc/vmstat` | 5s | `memory_vmstat` |
| `disk`        | `/proc/diskstats` | 1s | `disk_io` |
| `filesystem`  | `/proc/self/mountinfo` + `statfs` | 10s | `filesystem_stats` |
| `network`     | `/proc/net/dev` + `/sys/class/net` | 1s | `network_interface` |
| `nettcp`      | `/proc/net/snmp` + `/proc/net/netstat` | 5s | `network_tcp` |
| `sockets`     | `/proc/net/{tcp,tcp6,udp,udp6}` | 10s | `network_socket_states` |
| `conntrack`   | `/proc/sys/net/netfilter/*` | 10s | `network_conntrack` |
| `process`     | walks `/proc/[pid]/` | 10s | `process_stats`, `process_summary` |
| `sysinfo`     | `/proc/uptime`, utmp, `/proc/sys/fs/file-nr` | 10s | `system_info` |
| `smart`       | `anatol/smart.go` (ATA + NVMe) | 60s | `smart_stats` |
| `msr`         | `/dev/cpu/*/msr` (Intel RAPL + thermal) | 1s | `cpu_power` |

**Resolved watch-outs**
- Mount allowlist implemented via fstype denylist (tmpfs, overlay, cgroup,
  proc, sys, etc.).
- `msr` collector probes `/dev/cpu/0/msr` in `New()` and returns an error
  if unavailable; `main.buildCollectors` logs and skips.
- `sockets` parses `/proc/net/tcp*` today, not netlink `SOCK_DIAG` — faster
  netlink path is roadmap (see README).
- Process collector tolerates `ENOENT` on any `/proc/[pid]/*` read.

### ⚠ Phase 5 — Hardening (partial)

Done:
- Per-collector error budget (disable after N consecutive failures, siblings
  continue).
- Per-tick timeout at 90% of interval.
- Graceful drain: exporter `Close()` blocks until final flush (up to 10s).
- Structured logging via `log/slog` with per-collector fields.
- Prometheus self-metrics on the runner (duration, runs, errors, rows).

Deferred (clear followups):
- Fuzz corpora for `/proc` parsers (`parseProtoPairs`, `parseVmstat`,
  `countStates`, sysinfo parsers).
- Benchmarks for hot paths (process walker, diskstats parser) — target
  <5 ms per collect on a 500-proc host.
- `rlimit`-based resource ceiling at startup (the systemd unit enforces
  `MemoryMax=256M` / `CPUQuota=100%` externally today).

### ✅ Phase 6 — Release polish

- `/healthz` (liveness), `/readyz` (gated on exporter connect + all
  collectors constructed), `/metrics` (Prometheus) on `:9090`.
- `deploy/systemd/clicktrics.service` — ambient capability set pinned to
  `CAP_SYS_RAWIO`, `CAP_SYS_PTRACE`, `CAP_DAC_READ_SEARCH`; hardened with
  `ProtectSystem=strict`, `ProtectKernelTunables`, `RestrictRealtime`, etc.
- `.github/workflows/release.yml` builds a stripped linux/amd64 binary +
  matching `.deb` on `v*.*.*` tags, attaches them to the GitHub Release
  with a SHA256 sum file for verification. No container images — deployment
  is systemd + static binary.

## Validation outstanding

These aren't new phases — they're the live tests that can only run on a
Linux host with real ClickHouse:

1. First systemd install on Linux → verify every collector's procfs-API
   assumptions match reality.
2. First live insert → verify `AppendStruct` column mapping per row type;
   any mismatches surface as `append row: ...` errors in the exporter logs.
3. `go test -race ./...` on a Linux host (blocked locally on macOS by a Go
   1.26 / macOS 15 linker issue).

## Risks / watch-outs

- **Clock skew**: the `ts` column is `time.Now().UTC()`; host NTP is
  required for cross-host dashboards.
- **Counter wrap**: rate-deriving collectors (CPU, MSR energy) detect
  decreases and treat the tick as the new baseline rather than emitting
  negatives.
- **MSR permissions**: missing `SYS_RAWIO` or unloaded `msr` kernel module
  → collector fails in `New()` and the runner logs + skips it.
- **CH backpressure**: bounded queue (`queue_size`, default 100k per table)
  with drop-oldest; drops surface on `clicktrics_exporter_rows_dropped_total`.
- **Schema drift across fleet**: with no in-app migrations, rolling out a
  new field requires applying DDL on the CH cluster *before* shipping
  scrapers that reference the new column — otherwise preflight fails and
  the binary refuses to start. See README "Production deployment".

## Out of scope

- XDP / eBPF flow tracking (future work — see README roadmap).
- Non-Linux runtime targets.
- Agentless collection (SSH, SNMP, etc.).
- Alerting rules (handled in Grafana / operator's Alertmanager).
