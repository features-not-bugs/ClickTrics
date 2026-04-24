// Command clicktrics scrapes Linux host metrics and ships them to ClickHouse.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/collector"
	"github.com/features-not-bugs/clicktrics/internal/collector/conntrack"
	"github.com/features-not-bugs/clicktrics/internal/collector/cpu"
	"github.com/features-not-bugs/clicktrics/internal/collector/disk"
	"github.com/features-not-bugs/clicktrics/internal/collector/filesystem"
	"github.com/features-not-bugs/clicktrics/internal/collector/memory"
	"github.com/features-not-bugs/clicktrics/internal/collector/nettcp"
	"github.com/features-not-bugs/clicktrics/internal/collector/network"
	"github.com/features-not-bugs/clicktrics/internal/collector/pressure"
	"github.com/features-not-bugs/clicktrics/internal/collector/process"
	"github.com/features-not-bugs/clicktrics/internal/collector/smart"
	"github.com/features-not-bugs/clicktrics/internal/collector/sockets"
	"github.com/features-not-bugs/clicktrics/internal/collector/sysinfo"
	"github.com/features-not-bugs/clicktrics/internal/collector/sysstats"
	"github.com/features-not-bugs/clicktrics/internal/collector/vmstat"
	"github.com/features-not-bugs/clicktrics/internal/config"
	"github.com/features-not-bugs/clicktrics/internal/exporter"
	chexp "github.com/features-not-bugs/clicktrics/internal/exporter/clickhouse"
	"github.com/features-not-bugs/clicktrics/internal/exporter/stdout"
	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/httpobs"
	"github.com/features-not-bugs/clicktrics/internal/metrics"
	"github.com/features-not-bugs/clicktrics/internal/migrate"
	"github.com/features-not-bugs/clicktrics/internal/version"
)

// Populated at build time via -ldflags. Named `versionStr` rather than
// `version` to avoid shadowing the imported version package.
var (
	versionStr = "dev"
	commit     = "none"
	buildDate  = "unknown"
)

// defaultConfigPath is the config location the .deb installer seeds and the
// systemd unit references. Override via --config on the CLI.
const defaultConfigPath = "/etc/clicktrics/config.yaml"

// ctorFn is the signature every collector's New() conforms to.
type ctorFn func(host string, interval time.Duration) (collector.Collector, error)

func main() {
	// Publish build info as early as possible so every subsequent subsystem
	// (metrics, version checker, log preamble) can read it.
	version.Set(version.Info{
		Version:   versionStr,
		Commit:    commit,
		BuildDate: buildDate,
	})
	info := version.Get()
	metrics.BuildInfo.WithLabelValues(info.Version, info.Commit, info.BuildDate, info.GoVersion).Set(1)

	// Subcommand routing — each subcommand parses its own flags and exits.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			if err := migrateMain(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "migrate:", err)
				os.Exit(1)
			}
			return
		case "version":
			if err := versionMain(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "version:", err)
				os.Exit(1)
			}
			return
		}
	}

	var (
		configPath  = flag.String("config", defaultConfigPath, "path to YAML config")
		showVersion = flag.Bool("version", false, "print version and exit")
		logLevel    = flag.String("log-level", "info", "debug | info | warn | error")
		obsAddr     = flag.String("obs-addr", ":9090", "address for /healthz, /readyz, /metrics; empty to disable")
	)
	flag.Parse()

	if *showVersion {
		info := version.Get()
		fmt.Printf("clicktrics %s (commit %s, built %s, %s)\n",
			info.Version, info.Commit, info.BuildDate, info.GoVersion)
		return
	}

	logger := newLogger(*logLevel)
	slog.SetDefault(logger)

	if err := run(*configPath, *obsAddr, logger); err != nil {
		logger.Error("clicktrics exited with error", "err", err)
		os.Exit(1)
	}
}

func run(configPath, obsAddr string, logger *slog.Logger) error {
	hostenv.Init()

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	info := version.Get()
	logger.Info("starting",
		"version", info.Version,
		"commit", info.Commit,
		"build_date", info.BuildDate,
		"go_version", info.GoVersion,
		"host", cfg.Host,
		"exporter", cfg.Exporter.Type,
		"proc_root", hostenv.ProcRoot,
		"sys_root", hostenv.SysRoot,
	)

	// Kick off the background GitHub release checker. It updates
	// clicktrics_update_available + clicktrics_upstream_release_info so
	// dashboards/alerts can notice stale scrapers in the fleet.
	if cfg.UpdateCheck.IsEnabled() {
		go runUpdateChecker(cfg.UpdateCheck.EffectiveInterval(), logger)
	}

	// Observability server first so /healthz responds during startup.
	var obsServer *httpobs.Server
	if obsAddr != "" {
		obsServer = httpobs.New(obsAddr)
		errCh := obsServer.Start()
		go func() {
			if err := <-errCh; err != nil {
				logger.Error("obs server failed", "err", err)
			}
		}()
		logger.Info("obs server listening", "addr", obsAddr)
	}

	exp, err := buildExporter(cfg, logger)
	if err != nil {
		return fmt.Errorf("build exporter: %w", err)
	}
	defer func() {
		if cerr := exp.Close(); cerr != nil {
			logger.Warn("exporter close failed", "err", cerr)
		}
	}()

	collectors := buildCollectors(cfg, logger)
	logger.Info("collectors registered", "count", len(collectors))

	if obsServer != nil {
		obsServer.SetReady(true)
	}
	metrics.Up.Set(1)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runErr := collector.Run(ctx, collectors, exp, collector.RunnerConfig{
		ErrorBudget: cfg.ErrorBudget,
		Logger:      logger,
	})
	metrics.Up.Set(0)

	if obsServer != nil {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = obsServer.Shutdown(sctx)
		scancel()
	}

	if runErr != nil && ctx.Err() == nil {
		return fmt.Errorf("runner: %w", runErr)
	}
	logger.Info("shutdown complete")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func buildExporter(cfg *config.Config, logger *slog.Logger) (exporter.Exporter, error) {
	switch cfg.Exporter.Type {
	case "", "stdout":
		return stdout.New(), nil
	case "clickhouse":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return chexp.New(ctx, chexp.Config{
			DSN:            cfg.Exporter.ClickHouse.DSN,
			FlushInterval:  cfg.Exporter.ClickHouse.FlushInterval,
			SendTimeout:    cfg.Exporter.ClickHouse.SendTimeout,
			QueueSize:      cfg.Exporter.ClickHouse.QueueSize,
			RequiredTables: chexp.DefaultRequiredTables,
		}, logger)
	default:
		return nil, fmt.Errorf("unknown exporter type: %q", cfg.Exporter.Type)
	}
}

// registry maps config keys to constructors. The adapter functions wrap
// each collector's New() into the common ctorFn signature.
var registry = map[string]ctorFn{
	"cpu": func(host string, iv time.Duration) (collector.Collector, error) {
		return cpu.New(host, iv)
	},
	"memory": func(host string, iv time.Duration) (collector.Collector, error) {
		return memory.New(host, iv)
	},
	"pressure": func(host string, iv time.Duration) (collector.Collector, error) {
		return pressure.New(host, iv)
	},
	"sysstats": func(host string, iv time.Duration) (collector.Collector, error) {
		return sysstats.New(host, iv)
	},
	"vmstat": func(host string, iv time.Duration) (collector.Collector, error) {
		return vmstat.New(host, iv)
	},
	"disk": func(host string, iv time.Duration) (collector.Collector, error) {
		return disk.New(host, iv)
	},
	"filesystem": func(host string, iv time.Duration) (collector.Collector, error) {
		return filesystem.New(host, iv)
	},
	"network": func(host string, iv time.Duration) (collector.Collector, error) {
		return network.New(host, iv)
	},
	"network_tcp": func(host string, iv time.Duration) (collector.Collector, error) {
		return nettcp.New(host, iv)
	},
	"sockets": func(host string, iv time.Duration) (collector.Collector, error) {
		return sockets.New(host, iv)
	},
	"conntrack": func(host string, iv time.Duration) (collector.Collector, error) {
		return conntrack.New(host, iv)
	},
	"process": func(host string, iv time.Duration) (collector.Collector, error) {
		return process.New(host, iv)
	},
	"sysinfo": func(host string, iv time.Duration) (collector.Collector, error) {
		return sysinfo.New(host, iv)
	},
	"smart": func(host string, iv time.Duration) (collector.Collector, error) {
		return smart.New(host, iv)
	},
	"msr": func(host string, iv time.Duration) (collector.Collector, error) {
		return cpu.NewPower(host, iv)
	},
}

// buildCollectors instantiates every enabled collector with its configured
// interval. A collector that fails to construct (platform unsupported,
// /proc path missing, etc.) is logged and skipped — the rest continue.
func buildCollectors(cfg *config.Config, logger *slog.Logger) []collector.Collector {
	out := make([]collector.Collector, 0, len(cfg.Collectors))
	for name, cc := range cfg.Collectors {
		if !cc.Enabled {
			continue
		}
		ctor, ok := registry[name]
		if !ok {
			logger.Warn("unknown collector in config; skipping", "collector", name)
			continue
		}
		if cc.Interval <= 0 {
			logger.Warn("collector interval must be > 0; skipping", "collector", name)
			continue
		}
		c, err := ctor(cfg.Host, cc.Interval)
		if err != nil {
			logger.Warn("collector unavailable; skipping",
				"collector", name, "err", err)
			continue
		}
		out = append(out, c)
	}
	return out
}

// migrateMain handles `clicktrics migrate <command>`. It reads the DSN from
// either an explicit --dsn flag or the ClickHouse section of --config, then
// hands off to the migrate package.
//
// Usage:
//
//	clicktrics migrate up                              # apply all pending
//	clicktrics migrate --config=/path/to/config.yaml up
//	clicktrics migrate --dsn=https://u:p@ch/db?secure=true status
//	clicktrics migrate down                            # roll back one
//	clicktrics migrate reset                           # roll back all
func migrateMain(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath,
		"path to YAML config to source the ClickHouse DSN from")
	dsn := fs.String("dsn", "",
		"override ClickHouse DSN (takes precedence over --config)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: clicktrics migrate [flags] <command>\n\n")
		fmt.Fprintf(fs.Output(), "Commands: %v\n\nFlags:\n", migrate.Commands)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one command, got %d args", fs.NArg())
	}
	command := fs.Arg(0)

	resolvedDSN := *dsn
	if resolvedDSN == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return fmt.Errorf("load config %s: %w", *configPath, err)
		}
		resolvedDSN = cfg.Exporter.ClickHouse.DSN
	}
	if resolvedDSN == "" {
		return fmt.Errorf("no ClickHouse DSN found — pass --dsn or set exporter.clickhouse.dsn in the config")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return migrate.Run(ctx, resolvedDSN, command, os.Stdout)
}

// versionMain handles `clicktrics version [--check] [--json]`.
//
//	clicktrics version            # one line of build info
//	clicktrics version --check    # also fetch latest GitHub release, compare
//	clicktrics version --json     # machine-readable output
func versionMain(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	check := fs.Bool("check", false, "query the GitHub API for the latest release and compare")
	asJSON := fs.Bool("json", false, "emit JSON instead of a human line")
	timeout := fs.Duration("timeout", 10*time.Second, "timeout for the GitHub API call")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info := version.Get()

	type output struct {
		version.Info
		LatestVersion string `json:"latest_version,omitempty"`
		LatestURL     string `json:"latest_url,omitempty"`
		Status        string `json:"status,omitempty"`
		Repo          string `json:"github_repo,omitempty"`
		Error         string `json:"error,omitempty"`
	}
	out := output{Info: info, Repo: version.GitHubRepo()}

	if *check {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		rel, err := version.FetchLatest(ctx, out.Repo)
		if err != nil {
			out.Error = err.Error()
			out.Status = version.StatusUnknown.String()
		} else {
			out.LatestVersion = rel.TagName
			out.LatestURL = rel.HTMLURL
			out.Status = version.Compare(info.Version, rel.TagName).String()
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("clicktrics %s\n", info.Version)
	fmt.Printf("  commit     %s\n", info.Commit)
	fmt.Printf("  built      %s\n", info.BuildDate)
	fmt.Printf("  go         %s\n", info.GoVersion)
	if info.Module != "" {
		fmt.Printf("  module     %s\n", info.Module)
	}
	if *check {
		if out.Error != "" {
			fmt.Printf("  upstream   (check failed: %s)\n", out.Error)
		} else {
			fmt.Printf("  upstream   %s  %s\n", out.LatestVersion, out.LatestURL)
			fmt.Printf("  status     %s\n", out.Status)
		}
	}
	return nil
}

// runUpdateChecker polls the GitHub API on the given cadence and keeps the
// clicktrics_update_available / clicktrics_upstream_release_info gauges in
// sync. Logs a single warning when an update is first detected; otherwise
// quiet.
func runUpdateChecker(interval time.Duration, logger *slog.Logger) {
	repo := version.GitHubRepo()
	if repo == "" {
		logger.Debug("update checker disabled: module path is not on github.com")
		return
	}
	info := version.Get()
	warned := false

	check := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rel, err := version.FetchLatest(ctx, repo)
		if err != nil {
			metrics.UpstreamCheckSuccess.Set(0)
			logger.Debug("upstream version check failed", "err", err)
			return
		}
		metrics.UpstreamCheckSuccess.Set(1)
		metrics.UpstreamInfo.Reset()
		value := float64(rel.PublishedAt.Unix())
		if value <= 0 {
			value = 1
		}
		metrics.UpstreamInfo.WithLabelValues(rel.TagName, rel.HTMLURL).Set(value)

		switch version.Compare(info.Version, rel.TagName) {
		case version.StatusOutdated:
			metrics.UpdateAvailable.Set(1)
			if !warned {
				logger.Warn("newer upstream release available",
					"current", info.Version, "latest", rel.TagName, "url", rel.HTMLURL)
				warned = true
			}
		default:
			metrics.UpdateAvailable.Set(0)
			warned = false
		}
	}

	// First check after a short delay so we don't stall startup on a slow
	// api.github.com response.
	time.Sleep(30 * time.Second)
	check()

	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		check()
	}
}
