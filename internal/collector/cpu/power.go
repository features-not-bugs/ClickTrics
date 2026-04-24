// Per-package CPU power + thermal collection via Intel RAPL MSRs.
//
// Lives in the cpu package alongside the per-core stats collector: MSRs are
// CPU objects and splitting them across packages just obscured that. This
// file holds the platform-agnostic Row + Collector types; platform-specific
// reads are in power_linux.go / power_other.go.
//
// Intel-focused. AMD RAPL uses MSR_CORE_ENERGY_STAT (0xC001029A) and
// related MSRs with a different unit encoding — tracked as roadmap in
// README.

package cpu

import (
	"context"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// PowerRow maps to cpu_power.
type PowerRow struct {
	Ts                    time.Time `ch:"ts"`
	Host                  string    `ch:"host"`
	Package               uint8     `ch:"package"`
	EnergyPkgUj           uint64    `ch:"energy_pkg_uj"`
	EnergyPP0Uj           uint64    `ch:"energy_pp0_uj"`
	EnergyPP1Uj           uint64    `ch:"energy_pp1_uj"`
	EnergyDRAMUj          uint64    `ch:"energy_dram_uj"`
	PowerPkgWatts         float32   `ch:"power_pkg_watts"`
	TempPkgC              float32   `ch:"temp_pkg_c"`
	ThermalThrottleEvents uint64    `ch:"thermal_throttle_events"`
}

// PowerCollector reads RAPL energy + package thermal MSRs for each
// physical CPU package on the host.
type PowerCollector struct {
	host     string
	interval time.Duration
}

// Name implements collector.Collector. Kept as "msr" for backwards
// compatibility with existing configs and dashboards — the identifier
// users see everywhere, not the internal package layout.
func (c *PowerCollector) Name() string { return "msr" }

// Interval implements collector.Collector.
func (c *PowerCollector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector. Delegates to the platform impl.
func (c *PowerCollector) Collect(ctx context.Context) (sample.Batch, error) {
	rows, err := collectPowerMSR(ctx, c.host)
	if err != nil {
		return nil, err
	}
	return &sample.TypedBatch[PowerRow]{TableName: "cpu_power", Rows: rows}, nil
}
