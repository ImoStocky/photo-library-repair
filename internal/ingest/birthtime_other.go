//go:build !linux && !darwin

package ingest

import "time"

// birthTime returns 0 when birth time is unavailable on this platform
// (callers fall back to mtime).
func birthTime(_ string, _ time.Time) int64 {
	return 0
}
