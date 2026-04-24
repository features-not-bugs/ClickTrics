// Package hostenv holds the root paths for /proc and /sys so tests can
// point collectors at fixture trees. At runtime, defaults ("/proc", "/sys")
// are used; in a container with bind-mounted host paths, the operator sets
// PROC_ROOT / SYS_ROOT env vars.
package hostenv

import "os"

// Defaults.
var (
	ProcRoot = "/proc"
	SysRoot  = "/sys"
)

// Init loads overrides from the environment. Call once at startup.
func Init() {
	if v := os.Getenv("PROC_ROOT"); v != "" {
		ProcRoot = v
	}
	if v := os.Getenv("SYS_ROOT"); v != "" {
		SysRoot = v
	}
}
