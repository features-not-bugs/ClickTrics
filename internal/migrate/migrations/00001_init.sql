-- +goose Up
-- ClickTrics initial schema.
--
-- All tables use CREATE TABLE IF NOT EXISTS so replaying this migration
-- on an existing cluster is a no-op. For a clustered ClickHouse, change
-- ENGINE = MergeTree to ReplicatedMergeTree(...) before applying.

-- ============================================================================
-- CPU: per-core utilisation, frequency, temperature
-- ============================================================================
CREATE TABLE IF NOT EXISTS cpu_core_stats (
    ts               DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host             LowCardinality(String),
    core             UInt16,
    user_pct         Float32,
    nice_pct         Float32,
    system_pct       Float32,
    idle_pct         Float32,
    iowait_pct       Float32,
    irq_pct          Float32,
    softirq_pct      Float32,
    steal_pct        Float32,
    guest_pct        Float32,
    guest_nice_pct   Float32,
    freq_hz          UInt64 CODEC(T64, ZSTD(1)),  -- core freq while not halted (turbostat Bzy_MHz × 1e6)
    freq_used_hz     UInt64 CODEC(T64, ZSTD(1)),  -- time-weighted effective freq incl. halt (Avg_MHz × 1e6)
    temp_c           Float32,
    voltage_v        Float32
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, core, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- CPU: system-wide aggregates (load avg, ctxt, intr, run queue)
-- ============================================================================
CREATE TABLE IF NOT EXISTS cpu_system_stats (
    ts               DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host             LowCardinality(String),
    load1            Float32,
    load5            Float32,
    load15           Float32,
    procs_running    UInt32,
    procs_blocked    UInt32,
    context_switches UInt64 CODEC(T64, ZSTD(1)),
    interrupts       UInt64 CODEC(T64, ZSTD(1)),
    softirqs         UInt64 CODEC(T64, ZSTD(1)),
    forks            UInt64 CODEC(T64, ZSTD(1)),
    boot_time        UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- CPU: power + thermal from MSRs (RAPL counters, throttle events)
-- Package-scoped, not per-core. Energy counters are cumulative microjoules.
-- ============================================================================
CREATE TABLE IF NOT EXISTS cpu_power (
    ts                        DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                      LowCardinality(String),
    package                   UInt8,
    energy_pkg_uj             UInt64 CODEC(T64, ZSTD(1)),
    energy_pp0_uj             UInt64 CODEC(T64, ZSTD(1)),
    energy_pp1_uj             UInt64 CODEC(T64, ZSTD(1)),
    energy_dram_uj            UInt64 CODEC(T64, ZSTD(1)),
    power_pkg_watts           Float32,
    temp_pkg_c                Float32,
    thermal_throttle_events   UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, package, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Memory: /proc/meminfo snapshot
-- All *_bytes are bytes (meminfo kB values are pre-multiplied by 1024).
-- ============================================================================
CREATE TABLE IF NOT EXISTS memory_stats (
    ts                   DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                 LowCardinality(String),
    mem_total_bytes      UInt64 CODEC(T64, ZSTD(1)),
    mem_free_bytes       UInt64 CODEC(T64, ZSTD(1)),
    mem_available_bytes  UInt64 CODEC(T64, ZSTD(1)),
    mem_used_bytes       UInt64 CODEC(T64, ZSTD(1)),
    buffers_bytes        UInt64 CODEC(T64, ZSTD(1)),
    cached_bytes         UInt64 CODEC(T64, ZSTD(1)),
    slab_bytes           UInt64 CODEC(T64, ZSTD(1)),
    sreclaimable_bytes   UInt64 CODEC(T64, ZSTD(1)),
    sunreclaim_bytes     UInt64 CODEC(T64, ZSTD(1)),
    dirty_bytes          UInt64 CODEC(T64, ZSTD(1)),
    writeback_bytes      UInt64 CODEC(T64, ZSTD(1)),
    anon_pages_bytes     UInt64 CODEC(T64, ZSTD(1)),
    mapped_bytes         UInt64 CODEC(T64, ZSTD(1)),
    shmem_bytes          UInt64 CODEC(T64, ZSTD(1)),
    swap_total_bytes     UInt64 CODEC(T64, ZSTD(1)),
    swap_free_bytes      UInt64 CODEC(T64, ZSTD(1)),
    swap_cached_bytes    UInt64 CODEC(T64, ZSTD(1)),
    huge_pages_total     UInt64 CODEC(T64, ZSTD(1)),
    huge_pages_free      UInt64 CODEC(T64, ZSTD(1)),
    huge_page_size_bytes UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Memory: /proc/vmstat cumulative counters (page faults, swap, OOM)
-- Monotonic; compute rates in query layer via runningDifference or delta().
-- ============================================================================
CREATE TABLE IF NOT EXISTS memory_vmstat (
    ts                 DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host               LowCardinality(String),
    pgfault            UInt64 CODEC(T64, ZSTD(1)),
    pgmajfault         UInt64 CODEC(T64, ZSTD(1)),
    pswpin             UInt64 CODEC(T64, ZSTD(1)),
    pswpout            UInt64 CODEC(T64, ZSTD(1)),
    pgscan_direct      UInt64 CODEC(T64, ZSTD(1)),
    pgscan_kswapd      UInt64 CODEC(T64, ZSTD(1)),
    pgsteal_direct     UInt64 CODEC(T64, ZSTD(1)),
    pgsteal_kswapd     UInt64 CODEC(T64, ZSTD(1)),
    allocstall         UInt64 CODEC(T64, ZSTD(1)),
    oom_kill           UInt64 CODEC(T64, ZSTD(1)),
    thp_fault_alloc    UInt64 CODEC(T64, ZSTD(1)),
    compact_stall      UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- PSI: pressure stall info. resource ∈ {cpu, io, memory}.
-- `some` = at least one task stalled, `full` = all tasks stalled.
-- avgN is percent-time-stalled over trailing N seconds.
-- total is monotonic microseconds.
-- ============================================================================
CREATE TABLE IF NOT EXISTS pressure_stall (
    ts              DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host            LowCardinality(String),
    resource        LowCardinality(String),
    some_avg10      Float32,
    some_avg60      Float32,
    some_avg300     Float32,
    some_total_us   UInt64 CODEC(T64, ZSTD(1)),
    full_avg10      Float32,
    full_avg60      Float32,
    full_avg300     Float32,
    full_total_us   UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, resource, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Disk I/O: /proc/diskstats per block device. Counters are cumulative.
-- ============================================================================
CREATE TABLE IF NOT EXISTS disk_io (
    ts                      DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                    LowCardinality(String),
    device                  LowCardinality(String),
    reads                   UInt64 CODEC(T64, ZSTD(1)),
    reads_merged            UInt64 CODEC(T64, ZSTD(1)),
    read_sectors            UInt64 CODEC(T64, ZSTD(1)),
    read_time_ms            UInt64 CODEC(T64, ZSTD(1)),
    writes                  UInt64 CODEC(T64, ZSTD(1)),
    writes_merged           UInt64 CODEC(T64, ZSTD(1)),
    write_sectors           UInt64 CODEC(T64, ZSTD(1)),
    write_time_ms           UInt64 CODEC(T64, ZSTD(1)),
    io_in_progress          UInt32,
    io_time_ms              UInt64 CODEC(T64, ZSTD(1)),
    weighted_io_time_ms     UInt64 CODEC(T64, ZSTD(1)),
    discards                UInt64 CODEC(T64, ZSTD(1)),
    discard_sectors         UInt64 CODEC(T64, ZSTD(1)),
    discard_time_ms         UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, device, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Filesystem: per-mount capacity (statfs).
-- ============================================================================
CREATE TABLE IF NOT EXISTS filesystem_stats (
    ts                   DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                 LowCardinality(String),
    mount_point          String,
    fstype               LowCardinality(String),
    device               LowCardinality(String),
    size_total_bytes     UInt64 CODEC(T64, ZSTD(1)),
    size_used_bytes      UInt64 CODEC(T64, ZSTD(1)),
    size_free_bytes      UInt64 CODEC(T64, ZSTD(1)),
    size_available_bytes UInt64 CODEC(T64, ZSTD(1)),
    inodes_total         UInt64 CODEC(T64, ZSTD(1)),
    inodes_used          UInt64 CODEC(T64, ZSTD(1)),
    inodes_free          UInt64 CODEC(T64, ZSTD(1)),
    read_only            UInt8
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, mount_point, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Network: per-interface counters from /proc/net/dev + link state from
-- /sys/class/net/<iface>.
-- ============================================================================
CREATE TABLE IF NOT EXISTS network_interface (
    ts              DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host            LowCardinality(String),
    iface           LowCardinality(String),
    rx_bytes        UInt64 CODEC(T64, ZSTD(1)),
    rx_packets      UInt64 CODEC(T64, ZSTD(1)),
    rx_errors       UInt64 CODEC(T64, ZSTD(1)),
    rx_dropped      UInt64 CODEC(T64, ZSTD(1)),
    rx_fifo         UInt64 CODEC(T64, ZSTD(1)),
    rx_frame        UInt64 CODEC(T64, ZSTD(1)),
    rx_compressed   UInt64 CODEC(T64, ZSTD(1)),
    rx_multicast    UInt64 CODEC(T64, ZSTD(1)),
    tx_bytes        UInt64 CODEC(T64, ZSTD(1)),
    tx_packets      UInt64 CODEC(T64, ZSTD(1)),
    tx_errors       UInt64 CODEC(T64, ZSTD(1)),
    tx_dropped      UInt64 CODEC(T64, ZSTD(1)),
    tx_fifo         UInt64 CODEC(T64, ZSTD(1)),
    tx_collisions   UInt64 CODEC(T64, ZSTD(1)),
    tx_carrier      UInt64 CODEC(T64, ZSTD(1)),
    tx_compressed   UInt64 CODEC(T64, ZSTD(1)),
    speed_mbps      Int32,
    link_up         UInt8,
    duplex          LowCardinality(String)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, iface, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Network: TCP-wide counters from /proc/net/snmp + /proc/net/netstat.
-- ============================================================================
CREATE TABLE IF NOT EXISTS network_tcp (
    ts                        DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                      LowCardinality(String),
    active_opens              UInt64 CODEC(T64, ZSTD(1)),
    passive_opens             UInt64 CODEC(T64, ZSTD(1)),
    attempt_fails             UInt64 CODEC(T64, ZSTD(1)),
    estab_resets              UInt64 CODEC(T64, ZSTD(1)),
    curr_estab                UInt64 CODEC(T64, ZSTD(1)),
    in_segs                   UInt64 CODEC(T64, ZSTD(1)),
    out_segs                  UInt64 CODEC(T64, ZSTD(1)),
    retrans_segs              UInt64 CODEC(T64, ZSTD(1)),
    in_errs                   UInt64 CODEC(T64, ZSTD(1)),
    out_rsts                  UInt64 CODEC(T64, ZSTD(1)),
    syncookies_sent           UInt64 CODEC(T64, ZSTD(1)),
    syncookies_recv           UInt64 CODEC(T64, ZSTD(1)),
    listen_drops              UInt64 CODEC(T64, ZSTD(1)),
    listen_overflows          UInt64 CODEC(T64, ZSTD(1)),
    tcp_lost_retransmit       UInt64 CODEC(T64, ZSTD(1)),
    tcp_fast_retrans          UInt64 CODEC(T64, ZSTD(1)),
    tcp_slow_start_retrans    UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Network: socket counts by state. state ∈ {ESTABLISHED, TIME_WAIT, CLOSE_WAIT,
-- SYN_SENT, SYN_RECV, FIN_WAIT1, FIN_WAIT2, LAST_ACK, LISTEN, CLOSING, NEW_SYN_RECV}.
-- ============================================================================
CREATE TABLE IF NOT EXISTS network_socket_states (
    ts       DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host     LowCardinality(String),
    family   LowCardinality(String),
    state    LowCardinality(String),
    count    UInt32
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, family, state, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Network: conntrack table utilisation.
-- ============================================================================
CREATE TABLE IF NOT EXISTS network_conntrack (
    ts      DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host    LowCardinality(String),
    count   UInt32,
    max     UInt32
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- Process: per-PID snapshot. High cardinality; shorter TTL + top-N filtering
-- in the collector is recommended to keep row count manageable.
-- ============================================================================
CREATE TABLE IF NOT EXISTS process_stats (
    ts              DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host            LowCardinality(String),
    pid             UInt32,
    ppid            UInt32,
    uid             UInt32,
    gid             UInt32,
    comm            LowCardinality(String),
    cmdline         String,
    state           FixedString(1),
    nice            Int8,
    priority        Int16,
    threads         UInt32,
    fds             UInt32,
    cpu_user_pct    Float32,
    cpu_system_pct  Float32,
    rss_bytes       UInt64 CODEC(T64, ZSTD(1)),
    vsz_bytes       UInt64 CODEC(T64, ZSTD(1)),
    minflt          UInt64 CODEC(T64, ZSTD(1)),
    majflt          UInt64 CODEC(T64, ZSTD(1)),
    read_bytes      UInt64 CODEC(T64, ZSTD(1)),
    write_bytes     UInt64 CODEC(T64, ZSTD(1)),
    start_time      UInt64 CODEC(T64, ZSTD(1))
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, pid, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY;

-- ============================================================================
-- Process: state rollup for overall health dashboards.
-- ============================================================================
CREATE TABLE IF NOT EXISTS process_summary (
    ts          DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host        LowCardinality(String),
    total       UInt32,
    running     UInt32,
    sleeping    UInt32,
    disk_sleep  UInt32,
    stopped     UInt32,
    zombie      UInt32,
    idle        UInt32
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- System: uptime, users, FDs, kernel log rate.
-- ============================================================================
CREATE TABLE IF NOT EXISTS system_info (
    ts                     DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                   LowCardinality(String),
    uptime_seconds         UInt64 CODEC(T64, ZSTD(1)),
    users_logged_in        UInt16,
    fds_allocated          UInt64 CODEC(T64, ZSTD(1)),
    fds_max                UInt64 CODEC(T64, ZSTD(1)),
    kernel_log_err_rate    UInt32,
    kernel_version         LowCardinality(String),
    os_release             LowCardinality(String)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY;

-- ============================================================================
-- SMART: per-disk health indicators. Collected on a slower cadence (e.g. 60s).
-- ============================================================================
CREATE TABLE IF NOT EXISTS smart_stats (
    ts                     DateTime64(3, 'UTC') CODEC(Delta(8), ZSTD(1)),
    host                   LowCardinality(String),
    device                 LowCardinality(String),
    model                  LowCardinality(String),
    serial                 LowCardinality(String),
    firmware               LowCardinality(String),
    temp_c                 Float32,
    power_on_hours         UInt64 CODEC(T64, ZSTD(1)),
    power_cycle_count      UInt64 CODEC(T64, ZSTD(1)),
    reallocated_sectors    UInt64 CODEC(T64, ZSTD(1)),
    pending_sectors        UInt64 CODEC(T64, ZSTD(1)),
    uncorrectable_sectors  UInt64 CODEC(T64, ZSTD(1)),
    crc_errors             UInt64 CODEC(T64, ZSTD(1)),
    wear_leveling_pct      Float32,
    total_lba_written      UInt64 CODEC(T64, ZSTD(1)),
    total_lba_read         UInt64 CODEC(T64, ZSTD(1)),
    health_ok              UInt8
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (host, device, ts)
TTL toDateTime(ts) + INTERVAL 90 DAY;

-- +goose Down
DROP TABLE IF EXISTS smart_stats;
DROP TABLE IF EXISTS system_info;
DROP TABLE IF EXISTS process_summary;
DROP TABLE IF EXISTS process_stats;
DROP TABLE IF EXISTS network_conntrack;
DROP TABLE IF EXISTS network_socket_states;
DROP TABLE IF EXISTS network_tcp;
DROP TABLE IF EXISTS network_interface;
DROP TABLE IF EXISTS filesystem_stats;
DROP TABLE IF EXISTS disk_io;
DROP TABLE IF EXISTS pressure_stall;
DROP TABLE IF EXISTS memory_vmstat;
DROP TABLE IF EXISTS memory_stats;
DROP TABLE IF EXISTS cpu_power;
DROP TABLE IF EXISTS cpu_system_stats;
DROP TABLE IF EXISTS cpu_core_stats;
