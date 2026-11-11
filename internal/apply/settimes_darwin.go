//go:build darwin

package apply

import (
	"encoding/binary"
	"time"

	"golang.org/x/sys/unix"
)

// setBirthTime sets the file birth time (Finder Date Created) via setattrlist.
func setBirthTime(path string, t time.Time) error {
	var attrlist unix.Attrlist
	attrlist.Bitmapcount = unix.ATTR_BIT_MAP_COUNT
	attrlist.Commonattr = unix.ATTR_CMN_CRTIME

	// timespec: int64 sec + int64 nsec, little-endian on Darwin.
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[0:8], uint64(t.Unix()))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(t.Nanosecond()))
	return unix.Setattrlist(path, &attrlist, buf, 0)
}
