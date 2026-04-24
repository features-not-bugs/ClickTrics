//go:build linux

package sysinfo

import (
	"encoding/binary"
	"io"
	"os"
)

// utmpUserProcess matches <utmp.h> USER_PROCESS.
const utmpUserProcess = 7

// utmpHeader is the fixed prefix of a utmp record we care about (type + pad
// + pid + line). The rest is skipped; total record size is 384 bytes on
// Linux x86_64 / aarch64.
const (
	utmpRecordSize = 384
	utmpSkipAfter  = utmpRecordSize - 8 // bytes after ut_type+pad+ut_pid
)

type utmpEntry struct {
	Type int16
	_    int16 // padding
	PID  int32
}

// readLoggedInUsers counts USER_PROCESS entries in /var/run/utmp.
// Returns 0 on any error (utmp rotation, permission denied, file missing).
func readLoggedInUsers() uint16 {
	f, err := os.Open("/var/run/utmp")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	var n uint16
	buf := make([]byte, utmpRecordSize)
	for {
		_, err := io.ReadFull(f, buf)
		if err != nil {
			return n
		}
		var e utmpEntry
		if err := binary.Read(byteReader(buf[:8]), binary.LittleEndian, &e); err != nil {
			return n
		}
		if e.Type == utmpUserProcess {
			n++
		}
	}
}

// byteReader is a tiny io.Reader over a byte slice to avoid bytes.NewReader import churn.
type byteReaderT []byte

func byteReader(b []byte) *byteReaderT { r := byteReaderT(b); return &r }
func (r *byteReaderT) Read(p []byte) (int, error) {
	if len(*r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, *r)
	*r = (*r)[n:]
	return n, nil
}
