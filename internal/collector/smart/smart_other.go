//go:build !linux

package smart

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported is returned when SMART collection is attempted on a
// non-Linux platform.
var ErrUnsupported = errors.New("smart: not supported on this platform")

// New returns ErrUnsupported on non-Linux platforms.
func New(_ string, _ time.Duration) (*Collector, error) {
	return nil, ErrUnsupported
}

func collectSMART(_ context.Context, _ string) ([]Row, error) {
	return nil, ErrUnsupported
}
