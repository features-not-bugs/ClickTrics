//go:build linux

package smart

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/anatol/smart.go"
)

// New constructs a SMART collector. Availability of individual devices is
// probed per Collect — callers don't need to special-case "no disks".
func New(host string, interval time.Duration) (*Collector, error) {
	return &Collector{host: host, interval: interval}, nil
}

// collectSMART enumerates /dev/sd* + /dev/nvme*n1 and reads SMART.
func collectSMART(_ context.Context, host string) ([]Row, error) {
	now := time.Now().UTC()
	rows := make([]Row, 0, 4)

	patterns := []string{"/dev/sd[a-z]", "/dev/nvme[0-9]n1"}
	var devices []string
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		devices = append(devices, matches...)
	}

	for _, dev := range devices {
		r, err := readOne(dev)
		if err != nil {
			continue
		}
		r.Ts = now
		r.Host = host
		r.Device = strings.TrimPrefix(dev, "/dev/")
		rows = append(rows, r)
	}
	return rows, nil
}

func readOne(path string) (Row, error) {
	d, err := smart.Open(path)
	if err != nil {
		return Row{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = d.Close() }()

	var r Row
	r.HealthOK = 1 // assume healthy unless a signal says otherwise

	switch dev := d.(type) {
	case *smart.SataDevice:
		return readSATA(dev, r)
	case *smart.NVMeDevice:
		return readNVMe(dev, r)
	default:
		// SCSI/SAS and unknown types: return what we have (device + zeros).
		return r, nil
	}
}

func readSATA(d *smart.SataDevice, r Row) (Row, error) {
	id, err := d.Identify()
	if err == nil {
		r.Model = cleanASCII(id.ModelNumberRaw[:])
		r.Serial = cleanASCII(id.SerialNumberRaw[:])
		r.Firmware = cleanASCII(id.FirmwareRevisionRaw[:])
	}
	page, err := d.ReadSMARTData()
	if err != nil {
		return r, err
	}
	// Attrs is map[uint8]AtaSmartAttr; iterate and cherry-pick known IDs.
	for _, a := range page.Attrs {
		switch a.Id {
		case 5: // Reallocated_Sector_Ct
			r.ReallocatedSectors = a.ValueRaw
		case 9: // Power_On_Hours
			r.PowerOnHours = a.ValueRaw & 0xFFFFFFFF
		case 12: // Power_Cycle_Count
			r.PowerCycleCount = a.ValueRaw
		case 194: // Temperature_Celsius
			r.TempC = float32(a.ValueRaw & 0xFF)
		case 197: // Current_Pending_Sector
			r.PendingSectors = a.ValueRaw
		case 198: // Offline_Uncorrectable
			r.UncorrectableSectors = a.ValueRaw
		case 199: // UDMA_CRC_Error_Count
			r.CRCErrors = a.ValueRaw
		case 177, 231: // Wear_Leveling_Count / SSD_Life_Left
			r.WearLevelingPct = float32(a.Current)
		case 241: // Total_LBAs_Written
			r.TotalLBAWritten = a.ValueRaw
		case 242: // Total_LBAs_Read
			r.TotalLBARead = a.ValueRaw
		}
	}
	return r, nil
}

func readNVMe(d *smart.NVMeDevice, r Row) (Row, error) {
	// Identify returns (controller, namespaces, err) — we only need controller.
	id, _, err := d.Identify()
	if err == nil {
		r.Model = cleanASCII(id.ModelNumberRaw[:])
		r.Serial = cleanASCII(id.SerialNumberRaw[:])
		r.Firmware = cleanASCII(id.FirmwareRevRaw[:])
	}
	log, err := d.ReadSMART()
	if err != nil {
		return r, err
	}
	r.TempC = float32(log.Temperature) - 273 // Kelvin → Celsius
	r.PowerOnHours = log.PowerOnHours.Val[0]
	r.PowerCycleCount = log.PowerCycles.Val[0]
	// NVMe data-units are in 512k (1000×512 bytes) blocks per spec. We store
	// the raw count; query layer multiplies.
	r.TotalLBAWritten = log.DataUnitsWritten.Val[0]
	r.TotalLBARead = log.DataUnitsRead.Val[0]
	r.WearLevelingPct = float32(100 - int(log.PercentUsed))
	if log.CritWarning != 0 {
		r.HealthOK = 0
	}
	return r, nil
}

// cleanASCII trims trailing spaces/nulls from fixed-length ATA/NVMe strings.
func cleanASCII(b []byte) string {
	return strings.TrimRight(strings.TrimSpace(string(b)), "\x00 ")
}
