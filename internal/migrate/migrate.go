// Package migrate applies ClickHouse schema migrations via goose.
//
// Migrations are embedded into the binary at build time — no external files
// needed at runtime. Operators invoke via `clicktrics migrate <command>`
// where <command> is one of up/down/status/version/redo/reset.
//
// Design choice: migrations are idempotent (CREATE TABLE IF NOT EXISTS).
// Replaying the full set on a cluster that already has the schema is a
// no-op and simply bumps the goose tracking table to the latest version.
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2" // register "clickhouse" driver
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var embedded embed.FS

const migrationsDir = "migrations"

// Commands lists the migrate subcommands we support. Kept tight — goose has
// more but these are the ones an operator realistically needs in prod.
var Commands = []string{"up", "down", "status", "version", "redo", "reset"}

// Run executes a goose command against the ClickHouse instance at dsn. The
// dsn must include a database (path segment or ?database=).
//
// Output (from goose) goes to stdout/stderr.
func Run(ctx context.Context, dsn, command string, stdout io.Writer) error {
	db, err := openCH(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := goose.SetDialect("clickhouse"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	goose.SetBaseFS(embedded)
	// goose writes status lines to its default logger (stderr); that's fine
	// for a CLI. The stdout parameter is accepted for API symmetry but
	// currently unused — left in place for a future switch to a silent
	// logger + structured output on stdout.
	_ = stdout

	switch command {
	case "up":
		return goose.UpContext(ctx, db, migrationsDir)
	case "down":
		return goose.DownContext(ctx, db, migrationsDir)
	case "status":
		return goose.StatusContext(ctx, db, migrationsDir)
	case "version":
		return goose.VersionContext(ctx, db, migrationsDir)
	case "redo":
		return goose.RedoContext(ctx, db, migrationsDir)
	case "reset":
		return goose.ResetContext(ctx, db, migrationsDir)
	default:
		return fmt.Errorf("unknown migrate command %q (want one of: %v)", command, Commands)
	}
}

// openCH opens a database/sql handle against the given DSN and confirms it
// speaks ClickHouse by pinging. clickhouse-go's database/sql driver accepts
// the same DSN shapes as the native driver.
func openCH(dsn string) (*sql.DB, error) {
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	ctx, cancel := contextWithTimeout(10 * time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}

// contextWithTimeout is a tiny helper that isolates the stdlib import.
func contextWithTimeout(d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
