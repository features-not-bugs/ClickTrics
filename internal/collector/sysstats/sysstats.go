// Package sysstats reports system-wide CPU aggregates: load averages,
// context switches, interrupts, run queue length, fork count, boot time.
// Data comes from /proc/stat + /proc/loadavg.
package sysstats

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/procfs"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to cpu_system_stats.
type Row struct {
	Ts              time.Time `ch:"ts"`
	Host            string    `ch:"host"`
	Load1           float32   `ch:"load1"`
	Load5           float32   `ch:"load5"`
	Load15          float32   `ch:"load15"`
	ProcsRunning    uint32    `ch:"procs_running"`
	ProcsBlocked    uint32    `ch:"procs_blocked"`
	ContextSwitches uint64    `ch:"context_switches"`
	Interrupts      uint64    `ch:"interrupts"`
	Softirqs        uint64    `ch:"softirqs"`
	Forks           uint64    `ch:"forks"`
	BootTime        uint64    `ch:"boot_time"`
}

// Collector reads /proc/stat and /proc/loadavg.
type Collector struct {
	host     string
	interval time.Duration
	fs       procfs.FS
}

// New constructs the collector.
func New(host string, interval time.Duration) (*Collector, error) {
	fs, err := procfs.NewFS(hostenv.ProcRoot)
	if err != nil {
		return nil, fmt.Errorf("procfs.NewFS: %w", err)
	}
	return &Collector{host: host, interval: interval, fs: fs}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "sysstats" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	stat, err := c.fs.Stat()
	if err != nil {
		return nil, fmt.Errorf("read /proc/stat: %w", err)
	}
	la, err := c.fs.LoadAvg()
	if err != nil {
		return nil, fmt.Errorf("read /proc/loadavg: %w", err)
	}

	r := Row{
		Ts:              time.Now().UTC(),
		Host:            c.host,
		Load1:           float32(la.Load1),
		Load5:           float32(la.Load5),
		Load15:          float32(la.Load15),
		ProcsRunning:    uint32(stat.ProcessesRunning),
		ProcsBlocked:    uint32(stat.ProcessesBlocked),
		ContextSwitches: stat.ContextSwitches,
		Interrupts:      stat.IRQTotal,
		Softirqs:        stat.SoftIRQTotal,
		Forks:           stat.ProcessCreated,
		BootTime:        stat.BootTime,
	}
	return &sample.TypedBatch[Row]{TableName: "cpu_system_stats", Rows: []Row{r}}, nil
}
