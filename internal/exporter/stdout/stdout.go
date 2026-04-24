// Package stdout provides a JSON-lines Exporter for bring-up and debugging.
// One line per row: {"table":"...","row":{...}}.
package stdout

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Exporter writes rows as JSON lines to an io.Writer (stdout by default).
type Exporter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// New returns an Exporter writing to os.Stdout.
func New() *Exporter { return NewWithWriter(os.Stdout) }

// NewWithWriter returns an Exporter writing to w. Useful for tests.
func NewWithWriter(w io.Writer) *Exporter {
	return &Exporter{enc: json.NewEncoder(w)}
}

type line struct {
	Table string `json:"table"`
	Row   any    `json:"row"`
}

// Send implements exporter.Exporter.
func (e *Exporter) Send(ctx context.Context, b sample.Batch) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := 0; i < b.Len(); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.enc.Encode(line{Table: b.Table(), Row: b.At(i)}); err != nil {
			return err
		}
	}
	return nil
}

// Close implements exporter.Exporter. No-op for stdout.
func (e *Exporter) Close() error { return nil }
