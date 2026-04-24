// Package pressure reports PSI (Pressure Stall Information) from
// /proc/pressure/{cpu,io,memory}. Kernel 4.20+. On older kernels the
// collector self-disables at startup.
package pressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/prometheus/procfs"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to pressure_stall.
type Row struct {
	Ts          time.Time `ch:"ts"`
	Host        string    `ch:"host"`
	Resource    string    `ch:"resource"`
	SomeAvg10   float32   `ch:"some_avg10"`
	SomeAvg60   float32   `ch:"some_avg60"`
	SomeAvg300  float32   `ch:"some_avg300"`
	SomeTotalUs uint64    `ch:"some_total_us"`
	FullAvg10   float32   `ch:"full_avg10"`
	FullAvg60   float32   `ch:"full_avg60"`
	FullAvg300  float32   `ch:"full_avg300"`
	FullTotalUs uint64    `ch:"full_total_us"`
}

// Collector reads /proc/pressure/{cpu,io,memory} every tick.
type Collector struct {
	host     string
	interval time.Duration
	fs       procfs.FS
}

// New returns a PSI collector. Returns ErrUnsupported if /proc/pressure is
// absent.
func New(host string, interval time.Duration) (*Collector, error) {
	if _, err := os.Stat(hostenv.ProcRoot + "/pressure/cpu"); err != nil {
		return nil, fmt.Errorf("PSI unavailable: %w", err)
	}
	fs, err := procfs.NewFS(hostenv.ProcRoot)
	if err != nil {
		return nil, err
	}
	return &Collector{host: host, interval: interval, fs: fs}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "pressure" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	now := time.Now().UTC()
	rows := make([]Row, 0, 3)
	for _, res := range []string{"cpu", "io", "memory"} {
		psi, err := c.fs.PSIStatsForResource(res)
		if err != nil {
			// memory PSI may be absent on CONFIG_PSI=n kernels; log-and-skip.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("PSI %s: %w", res, err)
		}
		r := Row{Ts: now, Host: c.host, Resource: res}
		if psi.Some != nil {
			r.SomeAvg10 = float32(psi.Some.Avg10)
			r.SomeAvg60 = float32(psi.Some.Avg60)
			r.SomeAvg300 = float32(psi.Some.Avg300)
			r.SomeTotalUs = psi.Some.Total
		}
		if psi.Full != nil {
			r.FullAvg10 = float32(psi.Full.Avg10)
			r.FullAvg60 = float32(psi.Full.Avg60)
			r.FullAvg300 = float32(psi.Full.Avg300)
			r.FullTotalUs = psi.Full.Total
		}
		rows = append(rows, r)
	}
	return &sample.TypedBatch[Row]{TableName: "pressure_stall", Rows: rows}, nil
}
