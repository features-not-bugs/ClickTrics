// Package process walks /proc/[pid]/ and emits per-PID rows plus a state
// rollup. All processes are emitted — cardinality is high, keep
// process_stats TTL tight in ClickHouse.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/prometheus/procfs"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to process_stats.
type Row struct {
	Ts           time.Time `ch:"ts"`
	Host         string    `ch:"host"`
	Pid          uint32    `ch:"pid"`
	Ppid         uint32    `ch:"ppid"`
	UID          uint32    `ch:"uid"`
	GID          uint32    `ch:"gid"`
	Comm         string    `ch:"comm"`
	Cmdline      string    `ch:"cmdline"`
	State        string    `ch:"state"`
	Nice         int8      `ch:"nice"`
	Priority     int16     `ch:"priority"`
	Threads      uint32    `ch:"threads"`
	Fds          uint32    `ch:"fds"`
	CPUUserPct   float32   `ch:"cpu_user_pct"`
	CPUSystemPct float32   `ch:"cpu_system_pct"`
	RSSBytes     uint64    `ch:"rss_bytes"`
	VSZBytes     uint64    `ch:"vsz_bytes"`
	Minflt       uint64    `ch:"minflt"`
	Majflt       uint64    `ch:"majflt"`
	ReadBytes    uint64    `ch:"read_bytes"`
	WriteBytes   uint64    `ch:"write_bytes"`
	StartTime    uint64    `ch:"start_time"`
}

// SummaryRow maps to process_summary.
type SummaryRow struct {
	Ts        time.Time `ch:"ts"`
	Host      string    `ch:"host"`
	Total     uint32    `ch:"total"`
	Running   uint32    `ch:"running"`
	Sleeping  uint32    `ch:"sleeping"`
	DiskSleep uint32    `ch:"disk_sleep"`
	Stopped   uint32    `ch:"stopped"`
	Zombie    uint32    `ch:"zombie"`
	Idle      uint32    `ch:"idle"`
}

// Tracked state from prior tick for CPU rate calculation.
type prior struct {
	utime, stime uint
	ts           time.Time
}

// Collector walks /proc/[pid]/.
type Collector struct {
	host     string
	interval time.Duration
	fs       procfs.FS
	prev     map[int]prior
	pageSize uint64
	hz       uint64
}

// New constructs the collector.
func New(host string, interval time.Duration) (*Collector, error) {
	fs, err := procfs.NewFS(hostenv.ProcRoot)
	if err != nil {
		return nil, fmt.Errorf("procfs.NewFS: %w", err)
	}
	return &Collector{
		host:     host,
		interval: interval,
		fs:       fs,
		prev:     map[int]prior{},
		pageSize: uint64(os.Getpagesize()),
		// SC_CLK_TCK is almost always 100 on Linux. Not worth linking to libc.
		hz: 100,
	}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "process" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector. Returns a sample.MultiBatch with
// rows for both process_stats and process_summary; the ClickHouse exporter
// fans out each sub-batch to its target table.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	all, err := c.fs.AllProcs()
	if err != nil {
		return nil, fmt.Errorf("AllProcs: %w", err)
	}
	now := time.Now().UTC()

	rows := make([]Row, 0, len(all))
	var summary SummaryRow
	summary.Ts = now
	summary.Host = c.host

	cur := make(map[int]prior, len(all))

	for _, p := range all {
		// Most /proc/[pid]/* reads can fail with ENOENT if the process exited
		// mid-walk; treat those as skips, not errors.
		stat, err := p.Stat()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			continue
		}

		status, _ := p.NewStatus()
		cmdline, _ := p.CmdLine()
		fdLen, _ := p.FileDescriptorsLen()
		io, _ := p.IO()

		summary.Total++
		switch stat.State {
		case "R":
			summary.Running++
		case "S":
			summary.Sleeping++
		case "D":
			summary.DiskSleep++
		case "T", "t":
			summary.Stopped++
		case "Z":
			summary.Zombie++
		case "I":
			summary.Idle++
		}

		var userPct, sysPct float32
		prevP, havePrev := c.prev[p.PID]
		cur[p.PID] = prior{utime: stat.UTime, stime: stat.STime, ts: now}
		if havePrev {
			elapsedSec := now.Sub(prevP.ts).Seconds()
			if elapsedSec > 0 {
				uDelta := float64(stat.UTime - prevP.utime)
				sDelta := float64(stat.STime - prevP.stime)
				userPct = float32(uDelta / float64(c.hz) / elapsedSec * 100)
				sysPct = float32(sDelta / float64(c.hz) / elapsedSec * 100)
			}
		}

		rows = append(rows, Row{
			Ts:           now,
			Host:         c.host,
			Pid:          uint32(p.PID),
			Ppid:         uint32(stat.PPID),
			UID:          uint32(status.UIDs[0]),
			GID:          uint32(status.GIDs[0]),
			Comm:         stat.Comm,
			Cmdline:      strings.Join(cmdline, " "),
			State:        stat.State,
			Nice:         int8(stat.Nice),
			Priority:     int16(stat.Priority),
			Threads:      uint32(stat.NumThreads),
			Fds:          uint32(fdLen),
			CPUUserPct:   userPct,
			CPUSystemPct: sysPct,
			RSSBytes:     uint64(stat.RSS) * c.pageSize,
			VSZBytes:     uint64(stat.VSize),
			Minflt:       uint64(stat.MinFlt),
			Majflt:       uint64(stat.MajFlt),
			ReadBytes:    io.ReadBytes,
			WriteBytes:   io.WriteBytes,
			StartTime:    stat.Starttime,
		})
	}
	c.prev = cur

	return &sample.MultiBatch{
		Batches: []sample.Batch{
			&sample.TypedBatch[Row]{TableName: "process_stats", Rows: rows},
			&sample.TypedBatch[SummaryRow]{TableName: "process_summary", Rows: []SummaryRow{summary}},
		},
	}, nil
}
