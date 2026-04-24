// Package clickhouse ships rows to ClickHouse via the native protocol.
//
// Design: per-table in-memory buffers drained on a configurable flush
// interval. async_insert=1 is always set so the server buffers incoming
// inserts into larger blocks; the client-side buffer only exists to reduce
// RTTs. On buffer overflow the oldest rows are dropped and counted.
//
// The exporter refuses to start if any required table is missing — ClickTrics
// does not run migrations.
package clickhouse

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/features-not-bugs/clicktrics/internal/metrics"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Config is the exporter's runtime config. The ClickHouse connection —
// including the target database — is fully described by DSN.
type Config struct {
	DSN           string
	FlushInterval time.Duration
	// SendTimeout caps a single table's prepare + append + send round trip.
	// Zero → 30s default. Independent of FlushInterval.
	SendTimeout    time.Duration
	QueueSize      int
	RequiredTables []string
}

const defaultSendTimeout = 30 * time.Second

// Exporter implements exporter.Exporter.
type Exporter struct {
	conn   driver.Conn
	cfg    Config
	logger *slog.Logger

	mu      sync.Mutex
	buffers map[string][]any // table → row pointers

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// New connects to ClickHouse, runs preflight table checks, and starts the
// periodic flusher. Close() must be called for graceful drain.
//
// The target database comes from the DSN (path component or `?database=`);
// there is no separate field. Unqualified INSERT/SELECT then route to it via
// the connection's default database.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Exporter, error) {
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = defaultSendTimeout
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 100_000
	}

	opts, err := clickhouse.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	if opts.Auth.Database == "" {
		return nil, fmt.Errorf("DSN must specify a database " +
			"(e.g. clickhouse://user:pass@host:9000/clicktrics)")
	}
	// Force async_insert on every session.
	if opts.Settings == nil {
		opts.Settings = clickhouse.Settings{}
	}
	opts.Settings["async_insert"] = 1
	opts.Settings["wait_for_async_insert"] = 0

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open ClickHouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping ClickHouse: %w", err)
	}
	if err := preflight(ctx, conn, opts.Auth.Database, cfg.RequiredTables); err != nil {
		_ = conn.Close()
		return nil, err
	}

	e := &Exporter{
		conn:    conn,
		cfg:     cfg,
		logger:  logger,
		buffers: map[string][]any{},
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go e.flushLoop()
	logger.Info("clickhouse exporter ready",
		"database", opts.Auth.Database,
		"addr", opts.Addr,
		"flush_interval", cfg.FlushInterval,
		"send_timeout", cfg.SendTimeout,
		"queue_size", cfg.QueueSize,
		"tables", len(cfg.RequiredTables),
	)
	return e, nil
}

// preflight verifies every required table exists in the connection's default
// database. Returns an error naming the first missing table so operators know
// what DDL to apply.
func preflight(ctx context.Context, conn driver.Conn, database string, tables []string) error {
	for _, t := range tables {
		q := fmt.Sprintf("SELECT 1 FROM %s LIMIT 0", t)
		if err := conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("preflight: table %s.%s not ready: %w\n"+
				"hint: apply schemas/clickhouse/001_init.sql",
				database, t, err)
		}
	}
	return nil
}

// Send implements exporter.Exporter. For MultiBatch it fans out to each
// sub-batch.
func (e *Exporter) Send(_ context.Context, b sample.Batch) error {
	if m, ok := b.(*sample.MultiBatch); ok {
		for _, p := range m.Parts() {
			if err := e.enqueue(p); err != nil {
				return err
			}
		}
		return nil
	}
	return e.enqueue(b)
}

func (e *Exporter) enqueue(b sample.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	table := b.Table()
	e.mu.Lock()
	defer e.mu.Unlock()

	buf := e.buffers[table]
	for i := 0; i < b.Len(); i++ {
		if len(buf) >= e.cfg.QueueSize {
			// Drop-oldest: slide the window forward by one.
			buf = buf[1:]
			metrics.ExporterRowsDropped.WithLabelValues(table).Inc()
		}
		buf = append(buf, b.At(i))
	}
	e.buffers[table] = buf
	return nil
}

// Close flushes remaining buffers and closes the ClickHouse connection.
func (e *Exporter) Close() error {
	e.stopOnce.Do(func() { close(e.stopCh) })
	<-e.doneCh
	return e.conn.Close()
}

func (e *Exporter) flushLoop() {
	defer close(e.doneCh)
	t := time.NewTicker(e.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-e.stopCh:
			// Final drain — give sub-goroutines a bit more than a single
			// per-table timeout to finish.
			ctx, cancel := context.WithTimeout(context.Background(), e.cfg.SendTimeout+5*time.Second)
			e.flushAll(ctx)
			cancel()
			return
		case <-t.C:
			// No outer deadline — each table gets its own timeout inside
			// flushAll. The ticker's natural 1-element buffer prevents tick
			// pile-up when a flush takes longer than flush_interval.
			e.flushAll(context.Background())
		}
	}
}

func (e *Exporter) flushAll(parent context.Context) {
	e.mu.Lock()
	snapshot := make(map[string][]any, len(e.buffers))
	for k, v := range e.buffers {
		if len(v) > 0 {
			snapshot[k] = v
			e.buffers[k] = nil
		}
	}
	e.mu.Unlock()

	if len(snapshot) == 0 {
		return
	}

	// Parallelise per-table so slow tables don't block their siblings. CH
	// handles concurrent INSERTs well; async_insert=1 also coalesces on the
	// server side.
	var wg sync.WaitGroup
	for table, rows := range snapshot {
		wg.Add(1)
		go func(table string, rows []any) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, e.cfg.SendTimeout)
			defer cancel()

			t0 := time.Now()
			err := e.flushTable(ctx, table, rows)
			metrics.ExporterFlushDuration.WithLabelValues(table).Observe(time.Since(t0).Seconds())
			if err != nil {
				metrics.ExporterBatches.WithLabelValues(table, "error").Inc()
				e.logger.Warn("flush failed", "table", table, "rows", len(rows), "err", err)
				return
			}
			metrics.ExporterBatches.WithLabelValues(table, "ok").Inc()
			metrics.ExporterRows.WithLabelValues(table).Add(float64(len(rows)))
		}(table, rows)
	}
	wg.Wait()
}

func (e *Exporter) flushTable(ctx context.Context, table string, rows []any) error {
	if len(rows) == 0 {
		return nil
	}
	// Unqualified table name → routed to the connection's default database,
	// which was set from the DSN at open time.
	stmt := fmt.Sprintf("INSERT INTO %s", table)
	b, err := e.conn.PrepareBatch(ctx, stmt)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	defer func() {
		// Explicitly Abort in case Send isn't reached.
		_ = b.Abort()
	}()
	for _, r := range rows {
		if err := b.AppendStruct(r); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := b.Send(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

// DefaultRequiredTables is the canonical set of tables ClickTrics writes.
// Passed into Config so preflight catches any missing DDL before accepting
// the first collector batch.
var DefaultRequiredTables = []string{
	"cpu_core_stats",
	"cpu_system_stats",
	"cpu_power",
	"memory_stats",
	"memory_vmstat",
	"pressure_stall",
	"disk_io",
	"filesystem_stats",
	"network_interface",
	"network_tcp",
	"network_socket_states",
	"network_conntrack",
	"process_stats",
	"process_summary",
	"system_info",
	"smart_stats",
}
