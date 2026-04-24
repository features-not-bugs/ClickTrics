// Package collector defines the Collector contract and the runner that
// ticks every registered collector on its own cadence.
package collector

import (
	"context"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Collector gathers metrics for one subsystem on a fixed cadence.
//
// Implementations must be safe to Collect sequentially (the runner calls
// Collect serially for a given collector) but need not be safe for
// concurrent Collect calls.
type Collector interface {
	// Name is a stable identifier used in logs and config (e.g. "cpu").
	Name() string
	// Interval is how often Collect should be invoked. Must be > 0.
	Interval() time.Duration
	// Collect returns a batch for this tick. A nil batch or zero-row batch
	// is treated as a no-op (not an error). ctx carries a per-tick timeout.
	Collect(ctx context.Context) (sample.Batch, error)
}
