// Package sockets reports socket state distributions by parsing
// /proc/net/{tcp,tcp6,udp,udp6}.
//
// Parsing /proc/net/tcp* is O(sockets); on hosts with millions of sockets
// netlink SOCK_DIAG is faster, but /proc is simpler and sufficient for most
// deployments. Consider a lower interval (10s+) for high-socket hosts.
package sockets

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to network_socket_states.
type Row struct {
	Ts     time.Time `ch:"ts"`
	Host   string    `ch:"host"`
	Family string    `ch:"family"`
	State  string    `ch:"state"`
	Count  uint32    `ch:"count"`
}

// stateNames maps hex state codes from /proc/net/tcp to strings.
var stateNames = map[string]string{
	"01": "ESTABLISHED",
	"02": "SYN_SENT",
	"03": "SYN_RECV",
	"04": "FIN_WAIT1",
	"05": "FIN_WAIT2",
	"06": "TIME_WAIT",
	"07": "CLOSE",
	"08": "CLOSE_WAIT",
	"09": "LAST_ACK",
	"0A": "LISTEN",
	"0B": "CLOSING",
	"0C": "NEW_SYN_RECV",
}

// Collector counts sockets by (family, state).
type Collector struct {
	host     string
	interval time.Duration
}

// New constructs the collector.
func New(host string, interval time.Duration) (*Collector, error) {
	return &Collector{host: host, interval: interval}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "sockets" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	now := time.Now().UTC()
	rows := make([]Row, 0, 24)

	type src struct{ path, family string }
	sources := []src{
		{filepath.Join(hostenv.ProcRoot, "net", "tcp"), "tcp4"},
		{filepath.Join(hostenv.ProcRoot, "net", "tcp6"), "tcp6"},
		{filepath.Join(hostenv.ProcRoot, "net", "udp"), "udp4"},
		{filepath.Join(hostenv.ProcRoot, "net", "udp6"), "udp6"},
	}

	for _, s := range sources {
		f, err := os.Open(s.path) // #nosec G304 — fixed proc path
		if err != nil {
			continue
		}
		counts := countStates(f)
		_ = f.Close()
		for state, n := range counts {
			rows = append(rows, Row{
				Ts:     now,
				Host:   c.host,
				Family: s.family,
				State:  state,
				Count:  n,
			})
		}
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("no socket data read")
	}
	return &sample.TypedBatch[Row]{TableName: "network_socket_states", Rows: rows}, nil
}

// countStates parses /proc/net/tcp-formatted input and returns state→count.
func countStates(r io.Reader) map[string]uint32 {
	counts := map[string]uint32{}
	s := bufio.NewScanner(r)
	// Skip header.
	if !s.Scan() {
		return counts
	}
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 4 {
			continue
		}
		state := strings.ToUpper(fields[3])
		name, ok := stateNames[state]
		if !ok {
			name = "UNKNOWN_" + state
		}
		counts[name]++
	}
	return counts
}
