//go:build darwin

package ingest

import (
	"time"

	"golang.org/x/sys/unix"
)

// birthTime returns the file birth time in unix seconds, or 0 when the
// filesystem does not report one (callers fall back to mtime).
func birthTime(path string, _ time.Time) int64 {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0
	}
	if st.Btim.Sec > 0 {
		return st.Btim.Sec
	}
	return 0
}
