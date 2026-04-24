// Package nettcp reports TCP-wide cumulative counters from /proc/net/snmp
// and /proc/net/netstat.
package nettcp

import (
	"bufio"
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

// Row maps to network_tcp.
type Row struct {
	Ts                  time.Time `ch:"ts"`
	Host                string    `ch:"host"`
	ActiveOpens         uint64    `ch:"active_opens"`
	PassiveOpens        uint64    `ch:"passive_opens"`
	AttemptFails        uint64    `ch:"attempt_fails"`
	EstabResets         uint64    `ch:"estab_resets"`
	CurrEstab           uint64    `ch:"curr_estab"`
	InSegs              uint64    `ch:"in_segs"`
	OutSegs             uint64    `ch:"out_segs"`
	RetransSegs         uint64    `ch:"retrans_segs"`
	InErrs              uint64    `ch:"in_errs"`
	OutRsts             uint64    `ch:"out_rsts"`
	SyncookiesSent      uint64    `ch:"syncookies_sent"`
	SyncookiesRecv      uint64    `ch:"syncookies_recv"`
	ListenDrops         uint64    `ch:"listen_drops"`
	ListenOverflows     uint64    `ch:"listen_overflows"`
	TCPLostRetransmit   uint64    `ch:"tcp_lost_retransmit"`
	TCPFastRetrans      uint64    `ch:"tcp_fast_retrans"`
	TCPSlowStartRetrans uint64    `ch:"tcp_slow_start_retrans"`
}

// Collector reads /proc/net/snmp and /proc/net/netstat.
type Collector struct {
	host     string
	interval time.Duration
	snmpPath string
	netPath  string
}

// New constructs the collector.
func New(host string, interval time.Duration) (*Collector, error) {
	return &Collector{
		host:     host,
		interval: interval,
		snmpPath: filepath.Join(hostenv.ProcRoot, "net", "snmp"),
		netPath:  filepath.Join(hostenv.ProcRoot, "net", "netstat"),
	}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "network_tcp" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	snmp, err := readProtoPairs(c.snmpPath)
	if err != nil {
		return nil, fmt.Errorf("snmp: %w", err)
	}
	netstat, err := readProtoPairs(c.netPath)
	if err != nil {
		return nil, fmt.Errorf("netstat: %w", err)
	}

	tcp := snmp["Tcp"]
	tcpExt := netstat["TcpExt"]

	r := Row{
		Ts:                  time.Now().UTC(),
		Host:                c.host,
		ActiveOpens:         tcp["ActiveOpens"],
		PassiveOpens:        tcp["PassiveOpens"],
		AttemptFails:        tcp["AttemptFails"],
		EstabResets:         tcp["EstabResets"],
		CurrEstab:           tcp["CurrEstab"],
		InSegs:              tcp["InSegs"],
		OutSegs:             tcp["OutSegs"],
		RetransSegs:         tcp["RetransSegs"],
		InErrs:              tcp["InErrs"],
		OutRsts:             tcp["OutRsts"],
		SyncookiesSent:      tcpExt["SyncookiesSent"],
		SyncookiesRecv:      tcpExt["SyncookiesRecv"],
		ListenDrops:         tcpExt["ListenDrops"],
		ListenOverflows:     tcpExt["ListenOverflows"],
		TCPLostRetransmit:   tcpExt["TCPLostRetransmit"],
		TCPFastRetrans:      tcpExt["TCPFastRetrans"],
		TCPSlowStartRetrans: tcpExt["TCPSlowStartRetrans"],
	}
	return &sample.TypedBatch[Row]{TableName: "network_tcp", Rows: []Row{r}}, nil
}

// readProtoPairs parses /proc/net/snmp or /proc/net/netstat format:
//
//	<Proto>: Field1 Field2 ...
//	<Proto>: val1   val2   ...
//
// Returns proto → field → value.
func readProtoPairs(path string) (map[string]map[string]uint64, error) {
	f, err := os.Open(path) // #nosec G304 — fixed /proc path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseProtoPairs(f)
}

func parseProtoPairs(r io.Reader) (map[string]map[string]uint64, error) {
	out := map[string]map[string]uint64{}
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1<<16), 1<<20) // netstat lines can exceed default 64k
	type pending struct {
		proto  string
		fields []string
	}
	var headers []pending
	for s.Scan() {
		line := s.Text()
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		proto := line[:colon]
		rest := strings.TrimSpace(line[colon+1:])
		tokens := strings.Fields(rest)

		// Heuristic: if tokens are all numbers, it's a data line; else header.
		if _, err := strconv.ParseUint(tokens[0], 10, 64); err != nil {
			headers = append(headers, pending{proto: proto, fields: tokens})
			continue
		}

		// Match data to last header for this proto.
		var hdr *pending
		for i := len(headers) - 1; i >= 0; i-- {
			if headers[i].proto == proto {
				hdr = &headers[i]
				break
			}
		}
		if hdr == nil || len(tokens) != len(hdr.fields) {
			continue
		}
		m := out[proto]
		if m == nil {
			m = map[string]uint64{}
			out[proto] = m
		}
		for i, t := range tokens {
			v, err := strconv.ParseUint(t, 10, 64)
			if err != nil {
				continue
			}
			m[hdr.fields[i]] = v
		}
	}
	return out, s.Err()
}
