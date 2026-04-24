//go:build !linux

package sysinfo

// readLoggedInUsers returns 0 on non-Linux platforms. utmp parsing is
// Linux-specific; darwin/BSD use different formats.
func readLoggedInUsers() uint16 { return 0 }
