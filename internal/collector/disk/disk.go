// Package disk reports per-device cumulative I/O counters from /proc/diskstats.
package disk

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/prometheus/procfs/blockdevice"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to disk_io.
type Row struct {
	Ts               time.Time `ch:"ts"`
	Host             string    `ch:"host"`
	Device           string    `ch:"device"`
	Reads            uint64    `ch:"reads"`
	ReadsMerged      uint64    `ch:"reads_merged"`
	ReadSectors      uint64    `ch:"read_sectors"`
	ReadTimeMs       uint64    `ch:"read_time_ms"`
	Writes           uint64    `ch:"writes"`
	WritesMerged     uint64    `ch:"writes_merged"`
	WriteSectors     uint64    `ch:"write_sectors"`
	WriteTimeMs      uint64    `ch:"write_time_ms"`
	IOInProgress     uint32    `ch:"io_in_progress"`
	IOTimeMs         uint64    `ch:"io_time_ms"`
	WeightedIOTimeMs uint64    `ch:"weighted_io_time_ms"`
	Discards         uint64    `ch:"discards"`
	DiscardSectors   uint64    `ch:"discard_sectors"`
	DiscardTimeMs    uint64    `ch:"discard_time_ms"`
}

// skipPattern filters virtual devices (loop, ram, dm-*) that skew dashboards.
var skipPattern = regexp.MustCompile(`^(loop|ram|sr|md|dm-|zram)`)

// Collector reads /proc/diskstats.
type Collector struct {
	host     string
	interval time.Duration
	fs       blockdevice.FS
}

// New constructs the collector.
func New(host string, interval time.Duration) (*Collector, error) {
	fs, err := blockdevice.NewFS(hostenv.ProcRoot, hostenv.SysRoot)
	if err != nil {
		return nil, fmt.Errorf("blockdevice.NewFS: %w", err)
	}
	return &Collector{host: host, interval: interval, fs: fs}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "disk" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	stats, err := c.fs.ProcDiskstats()
	if err != nil {
		return nil, fmt.Errorf("diskstats: %w", err)
	}
	now := time.Now().UTC()
	rows := make([]Row, 0, len(stats))
	for _, s := range stats {
		if skipPattern.MatchString(s.DeviceName) {
			continue
		}
		rows = append(rows, Row{
			Ts:               now,
			Host:             c.host,
			Device:           s.DeviceName,
			Reads:            s.ReadIOs,
			ReadsMerged:      s.ReadMerges,
			ReadSectors:      s.ReadSectors,
			ReadTimeMs:       s.ReadTicks,
			Writes:           s.WriteIOs,
			WritesMerged:     s.WriteMerges,
			WriteSectors:     s.WriteSectors,
			WriteTimeMs:      s.WriteTicks,
			IOInProgress:     uint32(s.IOsInProgress),
			IOTimeMs:         s.IOsTotalTicks,
			WeightedIOTimeMs: s.WeightedIOTicks,
			Discards:         s.DiscardIOs,
			DiscardSectors:   s.DiscardSectors,
			DiscardTimeMs:    s.DiscardTicks,
		})
	}
	return &sample.TypedBatch[Row]{TableName: "disk_io", Rows: rows}, nil
}
