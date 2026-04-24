// Package sysinfo reports host-level facts: uptime, logged-in users count,
// file descriptor usage, kernel + OS release strings.
package sysinfo

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/hostenv"
	"github.com/features-not-bugs/clicktrics/internal/sample"
)

// Row maps to system_info.
type Row struct {
	Ts               time.Time `ch:"ts"`
	Host             string    `ch:"host"`
	UptimeSeconds    uint64    `ch:"uptime_seconds"`
	UsersLoggedIn    uint16    `ch:"users_logged_in"`
	FdsAllocated     uint64    `ch:"fds_allocated"`
	FdsMax           uint64    `ch:"fds_max"`
	KernelLogErrRate uint32    `ch:"kernel_log_err_rate"`
	KernelVersion    string    `ch:"kernel_version"`
	OSRelease        string    `ch:"os_release"`
}

// Collector reads uptime/users/FDs and caches kernel/OS labels.
type Collector struct {
	host          string
	interval      time.Duration
	kernelVersion string
	osRelease     string
}

// New constructs the collector and caches immutable labels.
func New(host string, interval time.Duration) (*Collector, error) {
	c := &Collector{host: host, interval: interval}
	c.kernelVersion = readKernelVersion()
	c.osRelease = readOSRelease()
	return c, nil
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return "sysinfo" }

// Interval implements collector.Collector.
func (c *Collector) Interval() time.Duration { return c.interval }

// Collect implements collector.Collector.
func (c *Collector) Collect(_ context.Context) (sample.Batch, error) {
	uptime, err := readUptime()
	if err != nil {
		return nil, fmt.Errorf("read uptime: %w", err)
	}
	fdsAlloc, fdsMax := readFdNr()

	r := Row{
		Ts:            time.Now().UTC(),
		Host:          c.host,
		UptimeSeconds: uptime,
		UsersLoggedIn: readLoggedInUsers(),
		FdsAllocated:  fdsAlloc,
		FdsMax:        fdsMax,
		KernelVersion: c.kernelVersion,
		OSRelease:     c.osRelease,
	}
	return &sample.TypedBatch[Row]{TableName: "system_info", Rows: []Row{r}}, nil
}

func readUptime() (uint64, error) {
	data, err := os.ReadFile(filepath.Join(hostenv.ProcRoot, "uptime")) // #nosec G304
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty uptime")
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return uint64(f), nil
}

func readFdNr() (alloc, maxFds uint64) {
	data, err := os.ReadFile(filepath.Join(hostenv.ProcRoot, "sys", "fs", "file-nr")) // #nosec G304
	if err != nil {
		return 0, 0
	}
	// Format: "allocated free_but_in_use max"
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0
	}
	alloc, _ = strconv.ParseUint(fields[0], 10, 64)
	maxFds, _ = strconv.ParseUint(fields[2], 10, 64)
	return alloc, maxFds
}

func readKernelVersion() string {
	data, err := os.ReadFile(filepath.Join(hostenv.ProcRoot, "version")) // #nosec G304
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		return fields[2]
	}
	return strings.TrimSpace(string(data))
}

func readOSRelease() string {
	f, err := os.Open("/etc/os-release") // #nosec G304 — fixed well-known path
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	var name, version string
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "NAME=") {
			name = strings.Trim(strings.TrimPrefix(line, "NAME="), `"`)
		}
		if strings.HasPrefix(line, "VERSION_ID=") {
			version = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
		}
	}
	if name != "" && version != "" {
		return name + " " + version
	}
	return name
}
