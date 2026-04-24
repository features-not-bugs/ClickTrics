package vmstat

import (
	"strings"
	"testing"
)

func TestParseVmstat(t *testing.T) {
	input := `pgfault 1234
pgmajfault 56
pswpin 1
pswpout 2
pgscan_direct_dma 10
pgscan_direct_normal 20
pgsteal_kswapd_dma 5
oom_kill 7
ignored_line_without_number text
malformed
`
	m := parseVmstat(strings.NewReader(input))

	if m["pgfault"] != 1234 {
		t.Errorf("pgfault=%d", m["pgfault"])
	}
	if m["pgmajfault"] != 56 {
		t.Errorf("pgmajfault=%d", m["pgmajfault"])
	}
	if _, ok := m["malformed"]; ok {
		t.Errorf("malformed line should be skipped")
	}
	if _, ok := m["ignored_line_without_number"]; ok {
		t.Errorf("non-numeric values should be skipped")
	}
	// pgscan_direct: sum of _dma + _normal = 30
	var sum uint64
	for k, v := range m {
		if strings.HasPrefix(k, "pgscan_direct") {
			sum += v
		}
	}
	if sum != 30 {
		t.Errorf("pgscan_direct sum = %d, want 30", sum)
	}
}
