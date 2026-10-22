// Package exifwrite rewrites the Exif APP1 segment of a JPEG file.
//
// It is the write counterpart of the read-only meta package: it builds a
// little-endian Exif TIFF carrying the capture time, its timezone offset,
// orientation and (optionally) a GPS position, then replaces the first
// Exif APP1 segment of the input, inserting one after the SOI when the
// file has none. The picture data and every other segment are preserved.
package exifwrite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

// Facts are the EXIF facts to write.
type Facts struct {
	Orientation int    // 1-8 per EXIF; 0 omits the orientation tag
	Capture     string // EXIF wall clock "YYYY:MM:DD HH:MM:SS"; "" omits the time tags
	Offset      string // normalized offset "+HH:MM" / "Z"; "" omits the offset tag
	HasGPS      bool
	GPSLat      float64 // signed decimal degrees
	GPSLon      float64
}

// ErrNotJPEG is returned when the input is not a JPEG file.
var ErrNotJPEG = errors.New("not a JPEG file")

// SetJPEG returns data with its Exif APP1 segment replaced by one carrying
// facts. When the file has no Exif APP1, one is inserted after the SOI.
func SetJPEG(data []byte, f Facts) ([]byte, error) {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, ErrNotJPEG
	}
	tiff := buildTIFF(f)
	start, end := locateExifAPP1(data)

	var app1 []byte
	app1 = append(app1, 0xFF, 0xE1)
	var lenField [2]byte
	binary.BigEndian.PutUint16(lenField[:], uint16(2+6+len(tiff)))
	app1 = append(app1, lenField[:]...)
	app1 = append(app1, 'E', 'x', 'i', 'f', 0, 0)
	app1 = append(app1, tiff...)

	out := make([]byte, 0, len(data)+len(tiff))
	if start >= 0 {
		out = append(out, data[:start]...)
		out = append(out, app1...)
		out = append(out, data[end:]...)
		return out, nil
	}
	out = append(out, data[:2]...) // SOI
	out = append(out, app1...)
	out = append(out, data[2:]...)
	return out, nil
}

// locateExifAPP1 returns the [start, end) byte range of the first Exif APP1
// segment, or (-1, -1) when the file carries none.
func locateExifAPP1(data []byte) (int, int) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return -1, -1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return -1, -1
		}
		m := data[i+1]
		switch {
		case m == 0xD9, m == 0xDA: // EOI or SOS: EXIF precedes the scan data
			return -1, -1
		case m == 0xD8 || (m >= 0xD0 && m <= 0xD7): // no length field
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return -1, -1
		}
		if m >= 0xE0 && m <= 0xEF { // APPn
			payload := data[i+4 : i+2+segLen]
			if len(payload) >= 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
				return i, i + 2 + segLen
			}
		}
		i += 2 + segLen
	}
	return -1, -1
}

// TIFF tag ids.
const (
	tagOrientation      = 0x0112
	tagExifPointer      = 0x8769
	tagGPSPointer       = 0x8825
	tagDateTimeOriginal = 0x9003
	tagOffsetTimeOrig   = 0x9011
	tagGPSLatRef        = 0x0001
	tagGPSLat           = 0x0002
	tagGPSLonRef        = 0x0003
	tagGPSLon           = 0x0004
)

// TIFF field types.
const (
	tASCII    = 2
	tSHORT    = 3
	tLONG     = 4
	tRATIONAL = 5
)

type tiffEntry struct {
	tag      int
	typ      int
	ascii    string
	svalue   uint16
	lvalue   uint32
	rational [3][2]uint32 // num, den
}

func (e tiffEntry) count() int {
	switch e.typ {
	case tASCII:
		return len(e.ascii) + 1
	case tRATIONAL:
		return 3
	}
	return 1
}

func (e tiffEntry) body() []byte {
	le := binary.LittleEndian
	switch e.typ {
	case tASCII:
		return append([]byte(e.ascii), 0)
	case tSHORT:
		b := make([]byte, 2)
		le.PutUint16(b, e.svalue)
		return b
	case tLONG:
		b := make([]byte, 4)
		le.PutUint32(b, e.lvalue)
		return b
	case tRATIONAL:
		b := make([]byte, 24)
		for i, r := range e.rational {
			le.PutUint32(b[i*8:], r[0])
			le.PutUint32(b[i*8+4:], r[1])
		}
		return b
	}
	panic("unknown tiffEntry type")
}

// buildTIFF assembles a little-endian Exif TIFF from facts. IFD sections are
// written contiguously and every out-of-line value goes into one value area
// at the end, so the offsets can be precomputed.
func buildTIFF(f Facts) []byte {
	le := binary.LittleEndian

	var ifd0 []tiffEntry
	if f.Orientation > 0 {
		ifd0 = append(ifd0, tiffEntry{tag: tagOrientation, typ: tSHORT, svalue: uint16(f.Orientation)})
	}
	exifPtrIdx := len(ifd0)
	ifd0 = append(ifd0, tiffEntry{tag: tagExifPointer, typ: tLONG})
	var gpsPtrIdx int
	if f.HasGPS {
		gpsPtrIdx = len(ifd0)
		ifd0 = append(ifd0, tiffEntry{tag: tagGPSPointer, typ: tLONG})
	}

	var exifIFD []tiffEntry
	if f.Capture != "" {
		exifIFD = append(exifIFD, tiffEntry{tag: tagDateTimeOriginal, typ: tASCII, ascii: f.Capture})
	}
	if f.Offset != "" {
		exifIFD = append(exifIFD, tiffEntry{tag: tagOffsetTimeOrig, typ: tASCII, ascii: f.Offset})
	}

	var gpsIFD []tiffEntry
	if f.HasGPS {
		latRef, lat := gpsDMS(f.GPSLat, true)
		lonRef, lon := gpsDMS(f.GPSLon, false)
		gpsIFD = append(gpsIFD,
			tiffEntry{tag: tagGPSLatRef, typ: tASCII, ascii: latRef},
			tiffEntry{tag: tagGPSLat, typ: tRATIONAL, rational: lat},
			tiffEntry{tag: tagGPSLonRef, typ: tASCII, ascii: lonRef},
			tiffEntry{tag: tagGPSLon, typ: tRATIONAL, rational: lon},
		)
	}

	ifdSize := func(n int) int { return 2 + n*12 + 4 }
	exifOff := 8 + ifdSize(len(ifd0))
	gpsOff := exifOff + ifdSize(len(exifIFD))
	valOff := gpsOff + ifdSize(len(gpsIFD))

	groups := [][]tiffEntry{ifd0, exifIFD, gpsIFD}

	valAt := make([][]int, len(groups))
	next := valOff
	for g, group := range groups {
		valAt[g] = make([]int, len(group))
		for i := range group {
			if len(group[i].body()) > 4 {
				valAt[g][i] = next
				next += len(group[i].body())
			}
		}
	}
	if f.HasGPS {
		ifd0[gpsPtrIdx].lvalue = uint32(gpsOff)
	}
	ifd0[exifPtrIdx].lvalue = uint32(exifOff)

	data := []byte{'I', 'I', 0x2A, 0x00, 0, 0, 0, 0}
	le.PutUint32(data[4:8], 8)

	writeIFD := func(g int) {
		group := groups[g]
		var b [2]byte
		le.PutUint16(b[:], uint16(len(group)))
		data = append(data, b[:]...)
		for i, e := range group {
			body := e.body()
			var eb [12]byte
			le.PutUint16(eb[0:2], uint16(e.tag))
			le.PutUint16(eb[2:4], uint16(e.typ))
			le.PutUint32(eb[4:8], uint32(e.count()))
			if len(body) <= 4 {
				copy(eb[8:12], body)
			} else {
				le.PutUint32(eb[8:12], uint32(valAt[g][i]))
			}
			data = append(data, eb[:]...)
		}
		data = append(data, 0, 0, 0, 0) // next IFD: none
	}
	writeIFD(0)
	writeIFD(1)
	if f.HasGPS {
		writeIFD(2)
	}

	for len(data) < valOff {
		data = append(data, 0)
	}
	for _, group := range groups {
		for _, e := range group {
			if body := e.body(); len(body) > 4 {
				data = append(data, body...)
			}
		}
	}
	return data
}

// gpsDMS converts a signed decimal degree into its hemisphere reference and
// degrees/minutes/seconds rationals. Seconds use a denominator of 100, which
// keeps round-trip error well under the group GPS tolerance.
func gpsDMS(deg float64, isLat bool) (string, [3][2]uint32) {
	neg := deg < 0
	if neg {
		deg = -deg
	}
	ref := "E"
	if isLat {
		ref = "N"
	}
	if neg {
		if isLat {
			ref = "S"
		} else {
			ref = "W"
		}
	}
	d := uint32(math.Floor(deg))
	remMin := (deg - float64(d)) * 60
	m := uint32(math.Floor(remMin))
	sNum := uint32(math.Round((remMin - float64(m)) * 60 * 100))
	if sNum == 100 {
		sNum = 0
		m++
		if m == 60 {
			m = 0
			d++
		}
	}
	return ref, [3][2]uint32{{d, 1}, {m, 1}, {sNum, 100}}
}
