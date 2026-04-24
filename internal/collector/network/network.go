// Package network reports per-interface cumulative counters + link state.
package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/procfs"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to network_interface.
type Row struct {
	Ts           time.Time `ch:"ts"`
	Host         string    `ch:"host"`
	Iface        string    `ch:"iface"`
	RxBytes      uint64    `ch:"rx_bytes"`
	RxPackets    uint64    `ch:"rx_packets"`
	RxErrors     uint64    `ch:"rx_errors"`
	RxDropped    uint64    `ch:"rx_dropped"`
	RxFifo       uint64    `ch:"rx_fifo"`
	RxFrame      uint64    `ch:"rx_frame"`
	RxCompressed uint64    `ch:"rx_compressed"`
	RxMulticast  uint64    `ch:"rx_multicast"`
	TxBytes      uint64    `ch:"tx_bytes"`
	TxPackets    uint64    `ch:"tx_packets"`
	TxErrors     uint64    `ch:"tx_errors"`
	TxDropped    uint64    `ch:"tx_dropped"`
	TxFifo       uint64    `ch:"tx_fifo"`
	TxCollisions uint64    `ch:"tx_collisions"`
	TxCarrier    uint64    `ch:"tx_carrier"`
	TxCompressed uint64    `ch:"tx_compressed"`
	SpeedMbps    int32     `ch:"speed_mbps"`
	LinkUp       uint8     `ch:"link_up"`
	Duplex       string    `ch:"duplex"`
}

// Collector reads /proc/net/dev + /sys/class/net/<iface>/{speed,operstate,duplex}.
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
func (c *Collector) Name() string { return "network" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	nd, err := c.fs.NetDev()
	if err != nil {
		return nil, fmt.Errorf("netdev: %w", err)
	}
	now := time.Now().UTC()
	rows := make([]Row, 0, len(nd))

	for name, s := range nd {
		if name == "lo" {
			continue
		}
		speed, duplex, up := ifaceState(name)
		rows = append(rows, Row{
			Ts:           now,
			Host:         c.host,
			Iface:        name,
			RxBytes:      s.RxBytes,
			RxPackets:    s.RxPackets,
			RxErrors:     s.RxErrors,
			RxDropped:    s.RxDropped,
			RxFifo:       s.RxFIFO,
			RxFrame:      s.RxFrame,
			RxCompressed: s.RxCompressed,
			RxMulticast:  s.RxMulticast,
			TxBytes:      s.TxBytes,
			TxPackets:    s.TxPackets,
			TxErrors:     s.TxErrors,
			TxDropped:    s.TxDropped,
			TxFifo:       s.TxFIFO,
			TxCollisions: s.TxCollisions,
			TxCarrier:    s.TxCarrier,
			TxCompressed: s.TxCompressed,
			SpeedMbps:    speed,
			LinkUp:       up,
			Duplex:       duplex,
		})
	}
	return &sample.TypedBatch[Row]{TableName: "network_interface", Rows: rows}, nil
}

// ifaceState reads speed, duplex, and operstate from /sys/class/net/<iface>.
// Missing values are -1 / "" / 0 so the row is still emittable.
func ifaceState(iface string) (speedMbps int32, duplex string, linkUp uint8) {
	base := filepath.Join(hostenv.SysRoot, "class", "net", iface)
	speedMbps = -1
	if b, err := os.ReadFile(filepath.Join(base, "speed")); err == nil { // #nosec G304 — sysfs
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 32); err == nil {
			speedMbps = int32(n)
		}
	}
	if b, err := os.ReadFile(filepath.Join(base, "duplex")); err == nil { // #nosec G304
		duplex = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(base, "operstate")); err == nil { // #nosec G304
		if strings.TrimSpace(string(b)) == "up" {
			linkUp = 1
		}
	}
	return
}
