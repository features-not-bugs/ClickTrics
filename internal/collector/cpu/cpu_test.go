package cpu

import (
	"testing"
	"time"

	"github.com/prometheus/procfs"
)

func TestDeltaRow_Percentages(t *testing.T) {
	prev := procfs.CPUStat{
		User:      100,
		Nice:      0,
		System:    50,
		Idle:      800,
		Iowait:    10,
		IRQ:       5,
		SoftIRQ:   5,
		Steal:     0,
		Guest:     0,
		GuestNice: 0,
	}
	cur := procfs.CPUStat{
		User:      150, // +50
		Nice:      0,
		System:    75,  // +25
		Idle:      900, // +100
		Iowait:    15,  // +5
		IRQ:       10,  // +5
		SoftIRQ:   10,  // +5
		Steal:     10,  // +10
		Guest:     0,
		GuestNice: 0,
	}
	// deltas sum: 50+25+100+5+5+5+10 = 200

	hw := coreReading{
		freqHz:     3_200_000_000, // core freq while not halted (Bzy)
		freqUsedHz: 2_400_000_000, // time-weighted used freq (Avg)
		tempC:      42.5,
		voltageV:   1.15,
	}
	r := deltaRow(time.Unix(0, 0), "h", 3, prev, cur, hw)

	if r.Core != 3 {
		t.Errorf("Core = %d, want 3", r.Core)
	}
	if r.Host != "h" {
		t.Errorf("Host = %q", r.Host)
	}
	if r.FreqHz != 3_200_000_000 {
		t.Errorf("FreqHz = %d", r.FreqHz)
	}
	if r.FreqUsedHz != 2_400_000_000 {
		t.Errorf("FreqUsedHz = %d", r.FreqUsedHz)
	}
	if r.TempC != 42.5 {
		t.Errorf("TempC = %v", r.TempC)
	}
	if !approxEq(r.VoltageV, 1.15) {
		t.Errorf("VoltageV = %v, want 1.15", r.VoltageV)
	}

	// 50/200 = 25%
	if !approxEq(r.UserPct, 25) {
		t.Errorf("UserPct = %v, want 25", r.UserPct)
	}
	// 100/200 = 50%
	if !approxEq(r.IdlePct, 50) {
		t.Errorf("IdlePct = %v, want 50", r.IdlePct)
	}
	// 10/200 = 5%
	if !approxEq(r.StealPct, 5) {
		t.Errorf("StealPct = %v, want 5", r.StealPct)
	}

	sum := r.UserPct + r.NicePct + r.SystemPct + r.IdlePct + r.IowaitPct +
		r.IrqPct + r.SoftirqPct + r.StealPct + r.GuestPct + r.GuestNicePct
	if !approxEq(sum, 100) {
		t.Errorf("sum of percentages = %v, want 100", sum)
	}
}

func TestDeltaRow_ZeroTotalDoesNotDivideByZero(t *testing.T) {
	// No change between samples — all deltas zero.
	s := procfs.CPUStat{User: 1, Idle: 1}
	r := deltaRow(time.Unix(0, 0), "h", 0, s, s, coreReading{})
	if r.UserPct != 0 || r.IdlePct != 0 {
		t.Fatalf("expected zero percentages on zero delta, got %+v", r)
	}
}

func TestCoreFromCPUPath(t *testing.T) {
	cases := map[string]int{
		"/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq":  0,
		"/sys/devices/system/cpu/cpu12/cpufreq/scaling_cur_freq": 12,
		"/sys/devices/system/cpu/cpufreq/scaling_cur_freq":       -1, // no cpuN
		"": -1,
	}
	for in, want := range cases {
		if got := coreFromCPUPath(in); got != want {
			t.Errorf("coreFromCPUPath(%q) = %d, want %d", in, got, want)
		}
	}
}

func approxEq(a, b float32) bool {
	const eps = 0.001
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
