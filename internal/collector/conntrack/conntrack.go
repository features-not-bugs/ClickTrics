// Package conntrack reports netfilter conntrack table utilization:
// /proc/sys/net/netfilter/nf_conntrack_count and nf_conntrack_max.
//
// Approaching nf_conntrack_max silently drops new connections — this is one
// of the top causes of mystery network errors under load.
package conntrack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to network_conntrack.
type Row struct {
	Ts    time.Time `ch:"ts"`
	Host  string    `ch:"host"`
	Count uint32    `ch:"count"`
	Max   uint32    `ch:"max"`
}

// Collector reads conntrack count/max.
type Collector struct {
	host     string
	interval time.Duration
}

// New constructs the collector. Returns an error if netfilter isn't loaded.
func New(host string, interval time.Duration) (*Collector, error) {
	if _, err := os.Stat(filepath.Join(hostenv.ProcRoot, "sys", "net", "netfilter", "nf_conntrack_count")); err != nil {
		return nil, fmt.Errorf("conntrack unavailable: %w", err)
	}
	return &Collector{host: host, interval: interval}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "conntrack" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	read := func(name string) (uint32, error) {
		p := filepath.Join(hostenv.ProcRoot, "sys", "net", "netfilter", name)
		data, err := os.ReadFile(p) // #nosec G304 — fixed proc path
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
		if err != nil {
			return 0, err
		}
		return uint32(n), nil
	}

	count, err := read("nf_conntrack_count")
	if err != nil {
		return nil, fmt.Errorf("read count: %w", err)
	}
	maxV, err := read("nf_conntrack_max")
	if err != nil {
		return nil, fmt.Errorf("read max: %w", err)
	}

	r := Row{Ts: time.Now().UTC(), Host: c.host, Count: count, Max: maxV}
	return &sample.TypedBatch[Row]{TableName: "network_conntrack", Rows: []Row{r}}, nil
}
