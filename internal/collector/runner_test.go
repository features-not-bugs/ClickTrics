package collector_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/collector"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// --- helpers -----------------------------------------------------------------

type fakeCollector struct {
	name     string
	interval time.Duration
	collect  func(ctx context.Context) (sample.Batch, error)
}

func (f *fakeCollector) Name() string                                      { return f.name }
func (f *fakeCollector) Interval() time.Duration                           { return f.interval }
func (f *fakeCollector) Collect(ctx context.Context) (sample.Batch, error) { return f.collect(ctx) }

type fakeExporter struct {
	mu      sync.Mutex
	batches []sample.Batch
	sendErr error
}

func (f *fakeExporter) Send(_ context.Context, b sample.Batch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, b)
	return f.sendErr
}

func (f *fakeExporter) Close() error { return nil }

func (f *fakeExporter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

type fakeBatch struct {
	table string
	rows  []string
}

func (b *fakeBatch) Table() string { return b.table }
func (b *fakeBatch) Len() int      { return len(b.rows) }
func (b *fakeBatch) At(i int) any  { return b.rows[i] }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func runCfg() collector.RunnerConfig {
	return collector.RunnerConfig{Logger: quietLogger()}
}

// --- tests -------------------------------------------------------------------

func TestRun_EmptyCollectors_BlocksUntilCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	exp := &fakeExporter{}

	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx, nil, exp, runCfg()) }()

	select {
	case err := <-done:
		t.Fatalf("Run returned early with %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRun_CollectorTicksAndSends(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := &fakeCollector{
		name:     "test",
		interval: 20 * time.Millisecond,
		collect: func(_ context.Context) (sample.Batch, error) {
			calls.Add(1)
			return &fakeBatch{table: "t", rows: []string{"r"}}, nil
		},
	}
	exp := &fakeExporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	err := collector.Run(ctx, []collector.Collector{c}, exp, runCfg())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	// Initial tick + ~7 ticker fires over 150ms/20ms. Be lenient.
	if got := calls.Load(); got < 3 {
		t.Fatalf("expected at least 3 collect calls, got %d", got)
	}
	if exp.count() < 3 {
		t.Fatalf("expected at least 3 batches delivered, got %d", exp.count())
	}
}

func TestRun_NilAndEmptyBatchesAreNotSent(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := &fakeCollector{
		name:     "nilbatch",
		interval: 10 * time.Millisecond,
		collect: func(_ context.Context) (sample.Batch, error) {
			calls.Add(1)
			if calls.Load()%2 == 0 {
				return nil, nil
			}
			return &fakeBatch{table: "t"}, nil // zero rows
		},
	}
	exp := &fakeExporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = collector.Run(ctx, []collector.Collector{c}, exp, runCfg())

	if exp.count() != 0 {
		t.Fatalf("expected no batches to be sent, got %d", exp.count())
	}
	if calls.Load() < 3 {
		t.Fatalf("collector did not tick enough: %d", calls.Load())
	}
}

func TestRun_ErrorBudgetDisablesFailingCollector(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := &fakeCollector{
		name:     "failing",
		interval: 10 * time.Millisecond,
		collect: func(_ context.Context) (sample.Batch, error) {
			calls.Add(1)
			return nil, errors.New("boom")
		},
	}
	exp := &fakeExporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_ = collector.Run(ctx, []collector.Collector{c}, exp, collector.RunnerConfig{
		ErrorBudget: 3,
		Logger:      quietLogger(),
	})

	// Budget of 3: initial tick + 2 ticker fires (=3 calls) then disabled.
	// The 4th tick check sees fails>=budget and returns.
	// Allow some scheduling slack: must be bounded, not unbounded.
	if got := calls.Load(); got < 3 || got > 6 {
		t.Fatalf("expected ~3 calls before disable, got %d", got)
	}
	if exp.count() != 0 {
		t.Fatalf("no batches should have been sent, got %d", exp.count())
	}
}

func TestRun_SlowCollectorDoesNotBlockSiblings(t *testing.T) {
	t.Parallel()
	var fastCalls atomic.Int64

	slow := &fakeCollector{
		name:     "slow",
		interval: 20 * time.Millisecond,
		collect: func(ctx context.Context) (sample.Batch, error) {
			<-ctx.Done() // always times out
			return nil, ctx.Err()
		},
	}
	fast := &fakeCollector{
		name:     "fast",
		interval: 20 * time.Millisecond,
		collect: func(_ context.Context) (sample.Batch, error) {
			fastCalls.Add(1)
			return &fakeBatch{table: "t", rows: []string{"r"}}, nil
		},
	}

	exp := &fakeExporter{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_ = collector.Run(ctx, []collector.Collector{slow, fast}, exp, collector.RunnerConfig{
		ErrorBudget: 100, // don't disable slow on its per-tick timeouts
		Logger:      quietLogger(),
	})

	if got := fastCalls.Load(); got < 3 {
		t.Fatalf("fast collector did not make progress: %d calls", got)
	}
}

func TestRun_ZeroIntervalSkipsCollector(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	c := &fakeCollector{
		name:     "zero",
		interval: 0,
		collect: func(_ context.Context) (sample.Batch, error) {
			calls.Add(1)
			return nil, nil
		},
	}
	exp := &fakeExporter{}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = collector.Run(ctx, []collector.Collector{c}, exp, runCfg())

	if calls.Load() != 0 {
		t.Fatalf("collector with zero interval should be skipped; got %d calls", calls.Load())
	}
}
