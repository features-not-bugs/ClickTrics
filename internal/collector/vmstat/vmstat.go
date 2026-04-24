// Package vmstat reports /proc/vmstat cumulative counters.
package vmstat

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to memory_vmstat. Values are cumulative; compute rates at query
// time via runningDifference / delta.
type Row struct {
	Ts            time.Time `ch:"ts"`
	Host          string    `ch:"host"`
	Pgfault       uint64    `ch:"pgfault"`
	Pgmajfault    uint64    `ch:"pgmajfault"`
	Pswpin        uint64    `ch:"pswpin"`
	Pswpout       uint64    `ch:"pswpout"`
	PgscanDirect  uint64    `ch:"pgscan_direct"`
	PgscanKswapd  uint64    `ch:"pgscan_kswapd"`
	PgstealDirect uint64    `ch:"pgsteal_direct"`
	PgstealKswapd uint64    `ch:"pgsteal_kswapd"`
	Allocstall    uint64    `ch:"allocstall"`
	OomKill       uint64    `ch:"oom_kill"`
	ThpFaultAlloc uint64    `ch:"thp_fault_alloc"`
	CompactStall  uint64    `ch:"compact_stall"`
}

// Collector reads /proc/vmstat.
type Collector struct {
	host     string
	interval time.Duration
	path     string
}

// New constructs the collector.
func New(host string, interval time.Duration) (*Collector, error) {
	return &Collector{
		host:     host,
		interval: interval,
		path:     filepath.Join(hostenv.ProcRoot, "vmstat"),
	}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "vmstat" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	data, err := os.ReadFile(c.path) // #nosec G304 — known path under ProcRoot
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", c.path, err)
	}
	m := parseVmstat(bytes.NewReader(data))
	// pgscan_direct / pgsteal_* are sums across zones:
	// kernels expose pgscan_direct_dma, pgscan_direct_normal, etc. Sum them.
	sum := func(prefix string) uint64 {
		var total uint64
		for k, v := range m {
			if strings.HasPrefix(k, prefix) {
				total += v
			}
		}
		return total
	}

	r := Row{
		Ts:            time.Now().UTC(),
		Host:          c.host,
		Pgfault:       m["pgfault"],
		Pgmajfault:    m["pgmajfault"],
		Pswpin:        m["pswpin"],
		Pswpout:       m["pswpout"],
		PgscanDirect:  sum("pgscan_direct"),
		PgscanKswapd:  sum("pgscan_kswapd"),
		PgstealDirect: sum("pgsteal_direct"),
		PgstealKswapd: sum("pgsteal_kswapd"),
		Allocstall:    sum("allocstall"),
		OomKill:       m["oom_kill"],
		ThpFaultAlloc: m["thp_fault_alloc"],
		CompactStall:  m["compact_stall"],
	}
	return &sample.TypedBatch[Row]{TableName: "memory_vmstat", Rows: []Row{r}}, nil
}

// parseVmstat consumes a /proc/vmstat-format stream. Format is "key value\n"
// pairs; unknown keys are ignored.
func parseVmstat(r io.Reader) map[string]uint64 {
	m := make(map[string]uint64, 256)
	s := bufio.NewScanner(r)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		m[fields[0]] = v
	}
	return m
}
