//go:build linux

package ingest

import (
	"time"

	"golang.org/x/sys/unix"
)

// birthTime returns the file birth time in unix seconds, or 0 when the
// filesystem does not report one (callers fall back to mtime).
func birthTime(path string, _ time.Time) int64 {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &stx); err == nil {
		return stx.Btime.Sec
	}
	return 0
}
