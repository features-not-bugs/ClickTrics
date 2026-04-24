//go:build !linux

package cpu

import (
	"context"
	"errors"
	"time"
)

// ErrMSRUnsupported is returned on non-Linux platforms.
var ErrMSRUnsupported = errors.New("msr: not supported on this platform")

// NewPower returns ErrMSRUnsupported on non-Linux platforms.
func NewPower(_ string, _ time.Duration) (*PowerCollector, error) {
	return nil, ErrMSRUnsupported
}

func collectPowerMSR(_ context.Context, _ string) ([]PowerRow, error) {
	return nil, ErrMSRUnsupported
}
