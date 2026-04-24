// Package smart reports disk health indicators (SMART attributes).
//
// Linux-only. On other platforms the collector's New returns ErrUnsupported.
package smart

import (
	"context"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to smart_stats.
type Row struct {
	Ts                   time.Time `ch:"ts"`
	Host                 string    `ch:"host"`
	Device               string    `ch:"device"`
	Model                string    `ch:"model"`
	Serial               string    `ch:"serial"`
	Firmware             string    `ch:"firmware"`
	TempC                float32   `ch:"temp_c"`
	PowerOnHours         uint64    `ch:"power_on_hours"`
	PowerCycleCount      uint64    `ch:"power_cycle_count"`
	ReallocatedSectors   uint64    `ch:"reallocated_sectors"`
	PendingSectors       uint64    `ch:"pending_sectors"`
	UncorrectableSectors uint64    `ch:"uncorrectable_sectors"`
	CRCErrors            uint64    `ch:"crc_errors"`
	WearLevelingPct      float32   `ch:"wear_leveling_pct"`
	TotalLBAWritten      uint64    `ch:"total_lba_written"`
	TotalLBARead         uint64    `ch:"total_lba_read"`
	HealthOK             uint8     `ch:"health_ok"`
}

// Collector reads SMART attributes for each block device.
type Collector struct {
	host     string
	interval time.Duration
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "smart" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(ctx context.Context) (sample.Batch, error) {
	rows, err := collectSMART(ctx, c.host)
	if err != nil {
		return nil, err
	}
	return &sample.TypedBatch[Row]{TableName: "smart_stats", Rows: rows}, nil
}
