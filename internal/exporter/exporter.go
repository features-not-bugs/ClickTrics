// Package exporter defines the interface collectors use to emit batches.
package exporter

import (
	"context"

	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Exporter receives batches from the collector runner.
//
// Implementations must be safe for concurrent calls from many goroutines
// (the runner launches one goroutine per collector).
type Exporter interface {
	// Send ships a batch. Should return quickly; buffer internally if the
	// underlying sink is slow. Return context.Canceled if ctx is done.
	Send(ctx context.Context, b sample.Batch) error
	// Close flushes any pending state and releases resources.
	Close() error
}
