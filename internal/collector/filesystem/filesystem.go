// Package filesystem reports per-mount capacity via statfs.
//
// Mount discovery uses /proc/self/mountinfo; virtual filesystems (tmpfs,
// overlay, cgroup, proc, sys, etc.) are filtered by default.
package filesystem

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/procfs"
	"golang.org/x/sys/unix"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to filesystem_stats.
type Row struct {
	Ts                 time.Time `ch:"ts"`
	Host               string    `ch:"host"`
	MountPoint         string    `ch:"mount_point"`
	Fstype             string    `ch:"fstype"`
	Device             string    `ch:"device"`
	SizeTotalBytes     uint64    `ch:"size_total_bytes"`
	SizeUsedBytes      uint64    `ch:"size_used_bytes"`
	SizeFreeBytes      uint64    `ch:"size_free_bytes"`
	SizeAvailableBytes uint64    `ch:"size_available_bytes"`
	InodesTotal        uint64    `ch:"inodes_total"`
	InodesUsed         uint64    `ch:"inodes_used"`
	InodesFree         uint64    `ch:"inodes_free"`
	ReadOnly           uint8     `ch:"read_only"`
}

// Filesystem types we skip unconditionally.
var skipFstypes = map[string]struct{}{
	"tmpfs":           {},
	"devtmpfs":        {},
	"devpts":          {},
	"proc":            {},
	"sysfs":           {},
	"cgroup":          {},
	"cgroup2":         {},
	"overlay":         {},
	"squashfs":        {},
	"fuse.gvfsd-fuse": {},
	"autofs":          {},
	"mqueue":          {},
	"securityfs":      {},
	"pstore":          {},
	"tracefs":         {},
	"debugfs":         {},
	"binfmt_misc":     {},
	"bpf":             {},
	"configfs":        {},
	"fusectl":         {},
	"hugetlbfs":       {},
	"nsfs":            {},
	"ramfs":           {},
	"rpc_pipefs":      {},
	"selinuxfs":       {},
}

// Collector reads /proc/self/mountinfo + statfs.
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
func (c *Collector) Name() string { return "filesystem" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	self, err := c.fs.Self()
	if err != nil {
		return nil, fmt.Errorf("procfs.Self: %w", err)
	}
	mounts, err := self.MountInfo()
	if err != nil {
		return nil, fmt.Errorf("mountinfo: %w", err)
	}
	now := time.Now().UTC()
	rows := make([]Row, 0, len(mounts))

	for _, m := range mounts {
		if _, skip := skipFstypes[m.FSType]; skip {
			continue
		}
		var s unix.Statfs_t
		if err := unix.Statfs(m.MountPoint, &s); err != nil {
			// Mount may have gone away (autofs, network FS) — quietly skip.
			continue
		}
		bsize := uint64(s.Bsize)
		total := s.Blocks * bsize
		free := s.Bfree * bsize
		avail := s.Bavail * bsize
		used := total - free

		var ro uint8
		if _, roOpt := m.Options["ro"]; roOpt {
			ro = 1
		}

		rows = append(rows, Row{
			Ts:                 now,
			Host:               c.host,
			MountPoint:         m.MountPoint,
			Fstype:             m.FSType,
			Device:             m.Source,
			SizeTotalBytes:     total,
			SizeUsedBytes:      used,
			SizeFreeBytes:      free,
			SizeAvailableBytes: avail,
			InodesTotal:        s.Files,
			InodesUsed:         s.Files - s.Ffree,
			InodesFree:         s.Ffree,
			ReadOnly:           ro,
		})
	}
	return &sample.TypedBatch[Row]{TableName: "filesystem_stats", Rows: rows}, nil
}
