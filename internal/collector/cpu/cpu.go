// Package cpu reports per-core CPU usage, frequency, and temperature.
//
// Rates are computed from /proc/stat tick deltas between ticks. The first
// Collect after startup returns an empty batch (no prior sample to diff).
// Per-core frequency is read from sysfs; temperature comes from hwmon
// coretemp if available, otherwise zero.
package cpu

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/procfs"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps 1:1 onto cpu_core_stats.
type Row struct {
	Ts           time.Time `ch:"ts"`
	Host         string    `ch:"host"`
	Core         uint16    `ch:"core"`
	UserPct      float32   `ch:"user_pct"`
	NicePct      float32   `ch:"nice_pct"`
	SystemPct    float32   `ch:"system_pct"`
	IdlePct      float32   `ch:"idle_pct"`
	IowaitPct    float32   `ch:"iowait_pct"`
	IrqPct       float32   `ch:"irq_pct"`
	SoftirqPct   float32   `ch:"softirq_pct"`
	StealPct     float32   `ch:"steal_pct"`
	GuestPct     float32   `ch:"guest_pct"`
	GuestNicePct float32   `ch:"guest_nice_pct"`
	FreqHz       uint64    `ch:"freq_hz"`       // core freq while not halted (turbostat Bzy_MHz × 1e6)
	FreqUsedHz   uint64    `ch:"freq_used_hz"`  // time-weighted effective freq incl. halt (Avg_MHz × 1e6)
	TempC        float32   `ch:"temp_c"`
	VoltageV     float32   `ch:"voltage_v"`     // per-core Vcore, Intel only
}

// Collector is stateful: it keeps the previous /proc/stat sample and,
// if MSR is available, per-CPU APERF/MPERF/TSC snapshots.
type Collector struct {
	host     string
	interval time.Duration
	fs       procfs.FS
	prev     map[int64]procfs.CPUStat
	msr      *msrCtx // nil if /dev/cpu/*/msr is unreadable
}

// New returns a CPU collector.
func New(host string, interval time.Duration) (*Collector, error) {
	fs, err := procfs.NewFS(hostenv.ProcRoot)
	if err != nil {
		return nil, fmt.Errorf("procfs.NewFS: %w", err)
	}
	return &Collector{
		host:     host,
		interval: interval,
		fs:       fs,
		msr:      newMsrCtx(),
	}, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "cpu" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	stat, err := c.fs.Stat()
	if err != nil {
		return nil, fmt.Errorf("read /proc/stat: %w", err)
	}
	now := time.Now().UTC()

	// First tick: record and emit nothing.
	if c.prev == nil {
		c.prev = stat.CPU
		return &sample.TypedBatch[Row]{TableName: "cpu_core_stats"}, nil
	}

	// Preferred: per-core freq / avg-freq / temp / voltage via MSR
	// (APERF/MPERF/TSC + IA32_THERM_STATUS + IA32_PERF_STATUS). MSR is the
	// only reliable source on hosts with cpufreq disabled, isolcpus, or
	// custom cstate tuning. Falls back to sysfs/hwmon when MSR is
	// unavailable; voltage stays zero in that case (no sysfs equivalent).
	var coreData map[int64]coreReading
	if c.msr != nil {
		coreData = c.msr.sample(now)
	}
	// Sysfs fallbacks when MSR returned nothing.
	if len(coreData) == 0 {
		coreData = map[int64]coreReading{}
		for core, hz := range readFreqsSysfs() {
			r := coreData[core]
			r.freqHz = hz
			coreData[core] = r
		}
		for core, c := range readCoreTempsHwmon() {
			r := coreData[core]
			r.tempC = c
			coreData[core] = r
		}
	}

	rows := make([]Row, 0, len(stat.CPU))
	for core, cur := range stat.CPU {
		prev, ok := c.prev[core]
		if !ok {
			continue // new core appeared (hot-plug) — wait for baseline
		}
		rows = append(rows, deltaRow(now, c.host, core, prev, cur, coreData[core]))
	}

	c.prev = stat.CPU
	return &sample.TypedBatch[Row]{TableName: "cpu_core_stats", Rows: rows}, nil
}

// deltaRow computes per-core percentages from tick deltas. Exported-ish name
// is unexported; used by tests via the same package.
func deltaRow(ts time.Time, host string, core int64, prev, cur procfs.CPUStat, hw coreReading) Row {
	d := cpuDelta{
		user:      cur.User - prev.User,
		nice:      cur.Nice - prev.Nice,
		system:    cur.System - prev.System,
		idle:      cur.Idle - prev.Idle,
		iowait:    cur.Iowait - prev.Iowait,
		irq:       cur.IRQ - prev.IRQ,
		softirq:   cur.SoftIRQ - prev.SoftIRQ,
		steal:     cur.Steal - prev.Steal,
		guest:     cur.Guest - prev.Guest,
		guestNice: cur.GuestNice - prev.GuestNice,
	}
	total := d.total()

	r := Row{
		Ts:         ts,
		Host:       host,
		Core:       uint16(core),
		FreqHz:     hw.freqHz,
		FreqUsedHz: hw.freqUsedHz,
		TempC:      hw.tempC,
		VoltageV:   hw.voltageV,
	}
	if total == 0 {
		// Handle idle system; all zeros except idle = 100 feels wrong, leave flat.
		return r
	}
	pct := func(v float64) float32 { return float32(v / total * 100) }
	r.UserPct = pct(d.user)
	r.NicePct = pct(d.nice)
	r.SystemPct = pct(d.system)
	r.IdlePct = pct(d.idle)
	r.IowaitPct = pct(d.iowait)
	r.IrqPct = pct(d.irq)
	r.SoftirqPct = pct(d.softirq)
	r.StealPct = pct(d.steal)
	r.GuestPct = pct(d.guest)
	r.GuestNicePct = pct(d.guestNice)
	return r
}

type cpuDelta struct {
	user, nice, system, idle, iowait, irq, softirq, steal, guest, guestNice float64
}

func (d cpuDelta) total() float64 {
	return d.user + d.nice + d.system + d.idle + d.iowait + d.irq + d.softirq + d.steal + d.guest + d.guestNice
}

// readFreqsSysfs returns core → freq in Hz from cpufreq sysfs. Used as a
// fallback when MSR is unavailable. Returns empty on hosts without cpufreq.
func readFreqsSysfs() map[int64]uint64 {
	out := map[int64]uint64{}
	entries, err := filepath.Glob(filepath.Join(hostenv.SysRoot, "devices", "system", "cpu", "cpu[0-9]*", "cpufreq", "scaling_cur_freq"))
	if err != nil {
		return out
	}
	for _, p := range entries {
		core := coreFromCPUPath(p)
		if core < 0 {
			continue
		}
		data, err := os.ReadFile(p) // #nosec G304 — sysfs glob
		if err != nil {
			continue
		}
		kHz, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			continue
		}
		out[int64(core)] = kHz * 1000
	}
	return out
}

// readCoreTempsHwmon reads hwmon coretemp entries and maps "Core N" labels
// to temperatures. Fallback path when MSR-based temp isn't available.
func readCoreTempsHwmon() map[int64]float32 {
	out := map[int64]float32{}
	hwmons, err := filepath.Glob(filepath.Join(hostenv.SysRoot, "class", "hwmon", "hwmon*"))
	if err != nil {
		return out
	}
	for _, hw := range hwmons {
		labels, _ := filepath.Glob(filepath.Join(hw, "temp*_label"))
		for _, labelPath := range labels {
			data, err := os.ReadFile(labelPath) // #nosec G304 — sysfs glob
			if err != nil {
				continue
			}
			label := strings.TrimSpace(string(data))
			// Expect "Core N" for per-core entries.
			const prefix = "Core "
			if !strings.HasPrefix(label, prefix) {
				continue
			}
			core, err := strconv.Atoi(strings.TrimPrefix(label, prefix))
			if err != nil {
				continue
			}
			// Corresponding temp file: swap "_label" → "_input".
			inputPath := strings.TrimSuffix(labelPath, "_label") + "_input"
			tdata, err := os.ReadFile(inputPath) // #nosec G304
			if err != nil {
				continue
			}
			milliC, err := strconv.ParseInt(strings.TrimSpace(string(tdata)), 10, 64)
			if err != nil {
				continue
			}
			out[int64(core)] = float32(milliC) / 1000
		}
	}
	return out
}

// coreFromCPUPath extracts N from ".../cpuN/cpufreq/scaling_cur_freq".
func coreFromCPUPath(path string) int {
	parts := strings.Split(path, string(filepath.Separator))
	for _, p := range parts {
		if strings.HasPrefix(p, "cpu") && len(p) > 3 {
			if n, err := strconv.Atoi(p[3:]); err == nil {
				return n
			}
		}
	}
	return -1
}

// -----------------------------------------------------------------------------
// MSR-based per-core frequency and temperature.
//
// Why this path exists: on servers with cpufreq disabled, isolcpus pinning, or
// fixed C-states (common in HPC / gaming / low-latency workloads), the sysfs
// `scaling_cur_freq` file is unreliable or absent. MSRs are the authoritative
// source. We follow turbostat's approach:
//
//   TSC (0x10) — monotonic cycle counter at reference frequency
//   MPERF (0xE7) — counts while core is active at reference freq
//   APERF (0xE8) — counts actual cycles (scales with current freq)
//
//   busy_freq_hz = APERF_delta / MPERF_delta * TSC_freq
//   TSC_freq     = TSC_delta / elapsed_seconds
//
// IA32_THERM_STATUS (0x19C) bits 22:16 give the digital readout offset from
// TjMax (MSR 0x1A2, bits 23:16). Temp_C = TjMax - offset.
// -----------------------------------------------------------------------------

const (
	msrTSC          = 0x10
	msrPlatformInfo = 0xCE  // bits 15:8 = max non-turbo ratio (Intel only)
	msrMPERF        = 0xE7
	msrAPERF        = 0xE8
	msrPerfStatus   = 0x198
	msrThermStatus  = 0x19C
	msrTempTarget   = 0x1A2

	// Bus clock for modern Intel (Sandy Bridge onward). Older Nehalem/
	// Westmere used 133.33 MHz but those are out of scope here.
	bclkHz = 100_000_000.0
)

// coreReading is one tick's worth of per-core hardware state. Zero values are
// fine defaults (missing MSR or sysfs falls through to 0 in the database).
type coreReading struct {
	freqHz     uint64  // core frequency while not halted (APERF/MPERF ratio × base_hz)
	freqUsedHz uint64  // time-weighted effective freq (APERF / TSC_delta × TSC_Hz)
	tempC      float32 // core temperature
	voltageV   float32 // core Vcore (Intel IA32_PERF_STATUS)
}

type msrSample struct {
	tsc, mperf, aperf uint64
	ts                time.Time
}

type msrCtx struct {
	fds   map[int]*os.File
	prev  map[int]msrSample
	tjmax float32
	// baseHz is the CPU's nominal base (max non-turbo) frequency, read
	// from MSR_PLATFORM_INFO on Intel. Used directly in the Bzy formula —
	// this is the `has_base_hz` path in turbostat's format_counters().
	// Zero on AMD or if the MSR read fails, in which case we fall back to
	// empirically measured tscHz.
	baseHz float64
	// tscHz is the invariant TSC rate, calibrated once at startup via a
	// sleep-and-measure. Only used when baseHz is unavailable (fallback
	// for the Bzy frequency calculation).
	tscHz float64
}

// newMsrCtx opens /dev/cpu/N/msr for every present CPU. Returns nil if no
// CPU's MSR device is readable (module not loaded, no CAP_SYS_RAWIO, or
// running on a non-Linux host).
func newMsrCtx() *msrCtx {
	paths, _ := filepath.Glob("/dev/cpu/[0-9]*/msr")
	if len(paths) == 0 {
		return nil
	}
	ctx := &msrCtx{
		fds:  make(map[int]*os.File, len(paths)),
		prev: make(map[int]msrSample, len(paths)),
	}
	for _, p := range paths {
		parts := strings.Split(p, string(filepath.Separator))
		if len(parts) < 2 {
			continue
		}
		n, err := strconv.Atoi(parts[len(parts)-2])
		if err != nil {
			continue
		}
		f, err := os.OpenFile(p, os.O_RDONLY, 0) // #nosec G304 — enumerated path
		if err != nil {
			continue
		}
		ctx.fds[n] = f
	}
	if len(ctx.fds) == 0 {
		return nil
	}
	// TjMax is the same across all cores on a package; read once from CPU 0
	// (falling back to any available CPU if 0 isn't online).
	var anyFD *os.File
	for i := 0; i < 4096 && anyFD == nil; i++ {
		anyFD = ctx.fds[i]
	}
	if anyFD != nil {
		if v, err := readMSRAt(anyFD, msrTempTarget); err == nil {
			ctx.tjmax = float32((v >> 16) & 0xFF)
		}
		// MSR_PLATFORM_INFO bits 15:8 → max non-turbo ratio. Intel Core 2nd
		// gen onward; absent on AMD, in which case we fall back to tscHz.
		if v, err := readMSRAt(anyFD, msrPlatformInfo); err == nil {
			if ratio := (v >> 8) & 0xFF; ratio > 0 {
				ctx.baseHz = float64(ratio) * bclkHz
			}
		}
		// Empirical TSC rate — used as fallback for Bzy when baseHz is
		// unavailable, and for sanity checks. TSC is invariant on all
		// in-scope CPUs so one calibration lasts the process lifetime.
		ctx.tscHz = calibrateTSC(anyFD)
	}
	return ctx
}

// calibrateTSC returns the TSC tick rate in Hz by diffing two reads bracketed
// by a known sleep. 100ms is enough to average out any short stall in the
// syscall/read path while keeping startup latency tolerable.
func calibrateTSC(f *os.File) float64 {
	start, err := readMSRAt(f, msrTSC)
	if err != nil {
		return 0
	}
	t0 := time.Now()
	time.Sleep(100 * time.Millisecond)
	end, err := readMSRAt(f, msrTSC)
	if err != nil || end <= start {
		return 0
	}
	elapsed := time.Since(t0).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(end-start) / elapsed
}

func readMSRAt(f *os.File, offset int64) (uint64, error) {
	var buf [8]byte
	if _, err := f.ReadAt(buf[:], offset); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// sample reads TSC / MPERF / APERF / THERM_STATUS / PERF_STATUS from every
// opened CPU. Temperature and voltage are point-in-time; frequencies require
// a prior sample to diff against and stay zero on the first call per core.
//
// Each CPU's timestamp is captured immediately before its TSC read, so the
// elapsed-time denominator used in `tscHz = dt/elapsed` matches the actual
// interval between this CPU's MSR reads — not loop-start wall time. This
// matters on hosts with many CPUs where sequential MSR reads span a few ms
// and Go's randomized map iteration order would otherwise jitter elapsed
// between samples (visible as noise on the frequency chart).
func (m *msrCtx) sample(_ time.Time) map[int64]coreReading {
	out := make(map[int64]coreReading, len(m.fds))

	type reading struct {
		s           msrSample
		therm       uint64
		perfStatus  uint64
	}
	curr := make(map[int]reading, len(m.fds))

	// Iterate in deterministic CPU order so the per-CPU read offset is
	// consistent across samples — reducing jitter further.
	cpus := make([]int, 0, len(m.fds))
	for cpu := range m.fds {
		cpus = append(cpus, cpu)
	}
	sort.Ints(cpus)

	for _, cpu := range cpus {
		f := m.fds[cpu]
		var r reading
		// Stamp ts against the TSC read itself, not the outer loop start.
		r.s.ts = time.Now().UTC()
		var err error
		if r.s.tsc, err = readMSRAt(f, msrTSC); err != nil {
			continue
		}
		if r.s.mperf, err = readMSRAt(f, msrMPERF); err != nil {
			continue
		}
		if r.s.aperf, err = readMSRAt(f, msrAPERF); err != nil {
			continue
		}
		r.therm, _ = readMSRAt(f, msrThermStatus)
		r.perfStatus, _ = readMSRAt(f, msrPerfStatus)
		curr[cpu] = r
	}

	for cpu, r := range curr {
		cr := coreReading{}

		// Temperature (snapshot).
		if m.tjmax > 0 {
			offset := float32((r.therm >> 16) & 0x7F)
			cr.tempC = m.tjmax - offset
		}

		// Vcore from IA32_PERF_STATUS bits 47:32, encoded as volts × 8192.
		// Intel-only. AMD leaves this field zero, which is fine — the
		// dashboard filters `voltage_v > 0`.
		if v := uint16((r.perfStatus >> 32) & 0xFFFF); v > 0 {
			cr.voltageV = float32(v) / 8192.0
		}

		// Frequencies need a prior sample to diff against. Formulas match
		// turbostat's format_counters() exactly:
		//
		//   Avg_Hz = APERF_delta / elapsed                         [line 3401]
		//   Bzy_Hz = base_hz * APERF_delta / MPERF_delta           [line 3408]
		//          = (TSC_delta/elapsed) * APERF / MPERF           [line 3410, fallback]
		prev, ok := m.prev[cpu]
		m.prev[cpu] = r.s
		if ok {
			elapsed := r.s.ts.Sub(prev.ts).Seconds()
			da := r.s.aperf - prev.aperf
			dm := r.s.mperf - prev.mperf

			// Used (turbostat Avg_MHz): APERF counts cycles that actually
			// happened. Per-second rate of APERF directly IS the effective
			// frequency over the interval — no TSC, no base_hz needed.
			// This is the time-weighted average including halted cycles.
			if elapsed > 0 {
				cr.freqUsedHz = uint64(float64(da) / elapsed)
			}

			// Core freq (turbostat Bzy_MHz): APERF/MPERF ratio scaled to the
			// nominal base clock — the frequency the core was running at
			// while not halted. Using base_hz (MSR_PLATFORM_INFO) is
			// turbostat's preferred path; tscHz is a reasonable fallback on
			// AMD and pre-Sandy-Bridge Intel.
			if dm > 0 {
				ref := m.baseHz
				if ref == 0 {
					ref = m.tscHz
				}
				if ref > 0 {
					cr.freqHz = uint64(ref * float64(da) / float64(dm))
				}
			}
		}

		out[int64(cpu)] = cr
	}
	return out
}
