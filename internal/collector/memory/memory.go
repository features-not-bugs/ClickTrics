// Package memory reports /proc/meminfo snapshots.
package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/procfs"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps 1:1 onto the memory_stats table.
type Row struct {
	Ts                time.Time `ch:"ts"`
	Host              string    `ch:"host"`
	MemTotalBytes     uint64    `ch:"mem_total_bytes"`
	MemFreeBytes      uint64    `ch:"mem_free_bytes"`
	MemAvailableBytes uint64    `ch:"mem_available_bytes"`
	MemUsedBytes      uint64    `ch:"mem_used_bytes"`
	BuffersBytes      uint64    `ch:"buffers_bytes"`
	CachedBytes       uint64    `ch:"cached_bytes"`
	SlabBytes         uint64    `ch:"slab_bytes"`
	SReclaimableBytes uint64    `ch:"sreclaimable_bytes"`
	SUnreclaimBytes   uint64    `ch:"sunreclaim_bytes"`
	DirtyBytes        uint64    `ch:"dirty_bytes"`
	WritebackBytes    uint64    `ch:"writeback_bytes"`
	AnonPagesBytes    uint64    `ch:"anon_pages_bytes"`
	MappedBytes       uint64    `ch:"mapped_bytes"`
	ShmemBytes        uint64    `ch:"shmem_bytes"`
	SwapTotalBytes    uint64    `ch:"swap_total_bytes"`
	SwapFreeBytes     uint64    `ch:"swap_free_bytes"`
	SwapCachedBytes   uint64    `ch:"swap_cached_bytes"`
	HugePagesTotal    uint64    `ch:"huge_pages_total"`
	HugePagesFree     uint64    `ch:"huge_pages_free"`
	HugePageSizeBytes uint64    `ch:"huge_page_size_bytes"`
}

// Collector reads /proc/meminfo.
type Collector struct {
	host     string
	interval time.Duration
	fs       procfs.FS
}

// New returns a memory collector.
func New(host string, interval time.Duration) (*Collector, error) {
	fs, err := procfs.NewFS(hostenv.ProcRoot)
	if err != nil {
		return nil, fmt.Errorf("procfs.NewFS: %w", err)
	}
	return &Collector{host: host, interval: interval, fs: fs}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "memory" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	m, err := c.fs.Meminfo()
	if err != nil {
		return nil, fmt.Errorf("read meminfo: %w", err)
	}
	now := time.Now().UTC()

	// meminfo fields are *uint64 (kB). deref returns bytes.
	kb := func(p *uint64) uint64 {
		if p == nil {
			return 0
		}
		return *p * 1024
	}

	row := Row{
		Ts:                now,
		Host:              c.host,
		MemTotalBytes:     kb(m.MemTotal),
		MemFreeBytes:      kb(m.MemFree),
		MemAvailableBytes: kb(m.MemAvailable),
		BuffersBytes:      kb(m.Buffers),
		CachedBytes:       kb(m.Cached),
		SlabBytes:         kb(m.Slab),
		SReclaimableBytes: kb(m.SReclaimable),
		SUnreclaimBytes:   kb(m.SUnreclaim),
		DirtyBytes:        kb(m.Dirty),
		WritebackBytes:    kb(m.Writeback),
		AnonPagesBytes:    kb(m.AnonPages),
		MappedBytes:       kb(m.Mapped),
		ShmemBytes:        kb(m.Shmem),
		SwapTotalBytes:    kb(m.SwapTotal),
		SwapFreeBytes:     kb(m.SwapFree),
		SwapCachedBytes:   kb(m.SwapCached),
		HugePageSizeBytes: kb(m.Hugepagesize),
	}
	// HugePagesTotal/Free are counts, not kB.
	if m.HugePagesTotal != nil {
		row.HugePagesTotal = *m.HugePagesTotal
	}
	if m.HugePagesFree != nil {
		row.HugePagesFree = *m.HugePagesFree
	}
	// MemUsed = Total - Free - Buffers - Cached (classic formula).
	row.MemUsedBytes = row.MemTotalBytes - row.MemFreeBytes - row.BuffersBytes - row.CachedBytes

	return &sample.TypedBatch[Row]{TableName: "memory_stats", Rows: []Row{row}}, nil
}
