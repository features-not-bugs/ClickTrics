package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/exporter"
	"github.com/features-not-bugs/clicktrics/internal/metrics"
)

// collectTimeoutRatio caps each Collect at this fraction of its Interval so a
// wedged collector can't lag past its next tick.
const collectTimeoutRatio = 0.9

// defaultErrorBudget: consecutive failures tolerated before a collector is
// disabled for the remainder of the process lifetime.
const defaultErrorBudget = 10

// RunnerConfig configures Run.
type RunnerConfig struct {
	// ErrorBudget is the number of consecutive failures a collector may
	// incur before it is disabled. Zero defaults to defaultErrorBudget.
	ErrorBudget int
	// Logger is the structured logger for runner + collector events.
	// Zero defaults to slog.Default().
	Logger *slog.Logger
}

// Run blocks until ctx is cancelled, ticking every collector on its own
// cadence. Individual collector failures are logged and do not terminate
// siblings. Returns ctx.Err() once ctx is done and all collectors have drained.
func Run(ctx context.Context, collectors []Collector, exp exporter.Exporter, cfg RunnerConfig) error {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ErrorBudget == 0 {
		cfg.ErrorBudget = defaultErrorBudget
	}

	if len(collectors) == 0 {
		cfg.Logger.Warn("no collectors enabled; runner idling until shutdown")
	}

	var wg sync.WaitGroup
	for _, c := range collectors {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCollector(ctx, c, exp, cfg)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

func runCollector(ctx context.Context, c Collector, exp exporter.Exporter, cfg RunnerConfig) {
	log := cfg.Logger.With("collector", c.Name())

	interval := c.Interval()
	if interval <= 0 {
		log.Error("collector interval must be > 0; skipping", "interval", interval)
		return
	}
	timeout := time.Duration(float64(interval) * collectTimeoutRatio)
	if timeout <= 0 {
		timeout = interval
	}

	var fails int
	tick := func() {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		t0 := time.Now()
		batch, err := c.Collect(cctx)
		metrics.CollectorDuration.WithLabelValues(c.Name()).Observe(time.Since(t0).Seconds())
		if err != nil {
			fails++
			metrics.CollectorErrors.WithLabelValues(c.Name()).Inc()
			log.Warn("collect failed", "err", err, "consecutive_fails", fails)
			return
		}
		fails = 0
		metrics.CollectorRuns.WithLabelValues(c.Name()).Inc()

		if batch == nil || batch.Len() == 0 {
			return
		}
		metrics.CollectorRowsEmitted.WithLabelValues(c.Name(), batch.Table()).Add(float64(batch.Len()))

		if err := exp.Send(ctx, batch); err != nil {
			log.Warn("export failed", "err", err, "table", batch.Table(), "rows", batch.Len())
		}
	}

	// Initial tick so dashboards populate without waiting a full interval.
	tick()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if fails >= cfg.ErrorBudget {
				log.Error("collector disabled after exhausting error budget",
					"budget", cfg.ErrorBudget)
				return
			}
			tick()
		}
	}
}
