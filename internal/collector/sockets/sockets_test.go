package sockets

import (
	"strings"
	"testing"
)

func TestCountStates(t *testing.T) {
	// Excerpted /proc/net/tcp format. Columns: sl local remote state ...
	data := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 ffff 100 0 0 10 0
   1: 0100007F:1F40 0100007F:8A01 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 ffff 100 0 0 10 0
   2: 0100007F:1F40 0100007F:8A02 01 00000000:00000000 00:00000000 00000000     0        0 12347 1 ffff 100 0 0 10 0
   3: 0100007F:1F40 0100007F:8A03 06 00000000:00000000 00:00000000 00000000     0        0 12348 1 ffff 100 0 0 10 0
   4: malformed line
`
	got := countStates(strings.NewReader(data))
	if got["LISTEN"] != 1 {
		t.Errorf("LISTEN = %d, want 1", got["LISTEN"])
	}
	if got["ESTABLISHED"] != 2 {
		t.Errorf("ESTABLISHED = %d, want 2", got["ESTABLISHED"])
	}
	if got["TIME_WAIT"] != 1 {
		t.Errorf("TIME_WAIT = %d, want 1", got["TIME_WAIT"])
	}
}
