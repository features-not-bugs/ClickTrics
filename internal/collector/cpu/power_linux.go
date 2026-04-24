//go:build linux

package cpu

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RAPL + package-thermal MSR addresses.
const (
	msrRaplPowerUnit    = 0x606 // MSR_RAPL_POWER_UNIT
	msrPkgEnergyStatus  = 0x611 // MSR_PKG_ENERGY_STATUS
	msrPP0EnergyStatus  = 0x639 // MSR_PP0_ENERGY_STATUS
	msrPP1EnergyStatus  = 0x641 // MSR_PP1_ENERGY_STATUS
	msrDRAMEnergyStatus = 0x619 // MSR_DRAM_ENERGY_STATUS
	msrPkgThermStatus   = 0x1B1 // IA32_PACKAGE_THERM_STATUS
)

// pkgState is retained across ticks: prior energy for power-delta, and the
// running count of thermal throttle events.
type pkgState struct {
	mu             sync.Mutex
	lastEnergy     uint64
	lastEnergyAt   time.Time
	energyUnitJ    float64 // RAPL energy unit in joules (e.g. 61e-6)
	tjMax          float32
	cpu            int // which CPU to read from (one per package)
	throttleEvents uint64
}

// Module-level cache so state persists across Collect calls.
var (
	pkgStateMu sync.Mutex
	pkgs       map[int]*pkgState // package ID → state
)

// NewPower constructs the RAPL/thermal collector and probes for MSR
// availability. Returns an error if /dev/cpu/0/msr is unreadable.
func NewPower(host string, interval time.Duration) (*PowerCollector, error) {
	if _, err := os.Stat("/dev/cpu/0/msr"); err != nil {
		return nil, fmt.Errorf("msr device unavailable (load 'msr' kernel module + CAP_SYS_RAWIO): %w", err)
	}
	if err := initPackages(); err != nil {
		return nil, err
	}
	return &PowerCollector{host: host, interval: interval}, nil
}

// initPackages walks /sys/devices/system/cpu to find one CPU per package
// and initialises RAPL units + TjMax for each.
func initPackages() error {
	pkgStateMu.Lock()
	defer pkgStateMu.Unlock()
	if pkgs != nil {
		return nil
	}
	pkgs = map[int]*pkgState{}

	entries, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/topology/physical_package_id")
	for _, p := range entries {
		data, err := os.ReadFile(p) // #nosec G304 — sysfs glob
		if err != nil {
			continue
		}
		pkgID, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		if _, exists := pkgs[pkgID]; exists {
			continue
		}
		cpuN := cpuIDFromTopologyPath(p)
		if cpuN < 0 {
			continue
		}
		st := &pkgState{cpu: cpuN}
		if err := initPkgState(st); err != nil {
			// Package init failed (likely not Intel RAPL). Skip silently.
			continue
		}
		pkgs[pkgID] = st
	}
	if len(pkgs) == 0 {
		return errors.New("no MSR-readable packages found")
	}
	return nil
}

func initPkgState(st *pkgState) error {
	unit, err := readPkgMSR(st.cpu, msrRaplPowerUnit)
	if err != nil {
		return fmt.Errorf("RAPL_POWER_UNIT: %w", err)
	}
	// Bits 8:12 = energy unit exponent. Unit = 1 / 2^exp joules.
	energyExp := (unit >> 8) & 0x1F
	st.energyUnitJ = 1.0 / float64(uint64(1)<<energyExp)

	tt, err := readPkgMSR(st.cpu, msrTempTarget)
	if err != nil {
		return fmt.Errorf("TEMPERATURE_TARGET: %w", err)
	}
	// Bits 16:23 = TjMax.
	st.tjMax = float32((tt >> 16) & 0xFF)
	return nil
}

// cpuIDFromTopologyPath extracts N from ".../cpuN/topology/..." — similar
// to coreFromCPUPath but this path layout has "topology" not "cpufreq".
func cpuIDFromTopologyPath(path string) int {
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

// readPkgMSR opens /dev/cpu/<cpu>/msr and reads 8 bytes at offset. Named
// `Pkg` to disambiguate from readMSRAt in cpu.go which takes an already-
// open file handle (used for the hot-path per-core reads).
func readPkgMSR(cpu int, offset int64) (uint64, error) {
	f, err := os.Open(fmt.Sprintf("/dev/cpu/%d/msr", cpu)) // #nosec G304 — controlled path
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var buf [8]byte
	if _, err := f.ReadAt(buf[:], offset); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// writePkgMSR writes 8 bytes at offset. Used to clear the sticky thermal
// log bit after counting a throttle event.
func writePkgMSR(cpu int, offset int64, val uint64) error {
	f, err := os.OpenFile(fmt.Sprintf("/dev/cpu/%d/msr", cpu), os.O_WRONLY, 0) // #nosec G304
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], val)
	_, err = f.WriteAt(buf[:], offset)
	return err
}

// collectPowerMSR reads energy + thermal MSRs for all known packages.
func collectPowerMSR(_ context.Context, host string) ([]PowerRow, error) {
	pkgStateMu.Lock()
	if pkgs == nil {
		pkgStateMu.Unlock()
		return nil, errors.New("msr not initialised")
	}
	ids := make([]int, 0, len(pkgs))
	for id := range pkgs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	pkgStateMu.Unlock()

	now := time.Now().UTC()
	rows := make([]PowerRow, 0, len(ids))

	for _, id := range ids {
		pkgStateMu.Lock()
		st := pkgs[id]
		pkgStateMu.Unlock()

		rows = append(rows, readPackage(st, host, id, now))
	}
	return rows, nil
}

func readPackage(st *pkgState, host string, pkgID int, now time.Time) PowerRow {
	st.mu.Lock()
	defer st.mu.Unlock()

	r := PowerRow{Ts: now, Host: host, Package: uint8(pkgID)}

	// Energy counters (32-bit, wrap on Intel).
	if v, err := readPkgMSR(st.cpu, msrPkgEnergyStatus); err == nil {
		r.EnergyPkgUj = uint64(float64(v) * st.energyUnitJ * 1e6)
		if !st.lastEnergyAt.IsZero() {
			dt := now.Sub(st.lastEnergyAt).Seconds()
			if dt > 0 {
				delta := v - st.lastEnergy
				if v < st.lastEnergy { // actual wrap
					delta = v + (^uint64(0) - st.lastEnergy) + 1
				}
				r.PowerPkgWatts = float32(float64(delta) * st.energyUnitJ / dt)
			}
		}
		st.lastEnergy = v
		st.lastEnergyAt = now
	}
	if v, err := readPkgMSR(st.cpu, msrPP0EnergyStatus); err == nil {
		r.EnergyPP0Uj = uint64(float64(v) * st.energyUnitJ * 1e6)
	}
	if v, err := readPkgMSR(st.cpu, msrPP1EnergyStatus); err == nil {
		r.EnergyPP1Uj = uint64(float64(v) * st.energyUnitJ * 1e6)
	}
	if v, err := readPkgMSR(st.cpu, msrDRAMEnergyStatus); err == nil {
		r.EnergyDRAMUj = uint64(float64(v) * st.energyUnitJ * 1e6)
	}

	// Package thermal status: bits 16:22 = digital readout (offset from TjMax).
	if v, err := readPkgMSR(st.cpu, msrPkgThermStatus); err == nil {
		offset := float32((v >> 16) & 0x7F)
		r.TempPkgC = st.tjMax - offset

		// Bit 1 = thermal log (sticky). Detect, count, clear.
		if v&(1<<1) != 0 {
			st.throttleEvents++
			cleared := v &^ (1 << 1)
			_ = writePkgMSR(st.cpu, msrPkgThermStatus, cleared)
		}
	}
	r.ThermalThrottleEvents = st.throttleEvents
	return r
}
