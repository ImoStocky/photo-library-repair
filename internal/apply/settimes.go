package apply

import (
	"os"
	"time"
)

// setCaptureTimes stamps path's filesystem times to the group's planned
// capture instant (unix seconds). atime/mtime via Chtimes; Darwin also
// sets birthtime so Finder Date Created matches. instant <= 0 is a no-op.
func setCaptureTimes(path string, instant int64) error {
	if instant <= 0 {
		return nil
	}
	t := time.Unix(instant, 0)
	if err := os.Chtimes(path, t, t); err != nil {
		return err
	}
	return setBirthTime(path, t)
}
