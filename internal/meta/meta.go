// Package meta reads the EXIF facts this tool needs directly from JPEG
// bytes, without external tools or third-party dependencies.
//
// It understands the small part of the TIFF-in-JPEG structure that carries
// the capture time, its timezone offset, GPS coordinates and orientation:
// IFD0 (orientation, DateTime, OffsetTime, IFD pointers), the ExifIFD
// (DateTimeOriginal/-Digitized, OffsetTime*) and the GPSIFD.
package meta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
)

// Facts are the EXIF facts of one image.
type Facts struct {
	Capture     string  // raw EXIF wall clock, e.g. "2020:05:01 12:30:00"
	Offset      string  // timezone offset, "+05:30" or "Z"; "" when unknown
	GPSLat      float64 // signed degrees, 0 when absent
	GPSLon      float64
	HasGPS      bool
	Orientation int // 1-8 per EXIF, 0 when the tag is absent
}

// ErrNoEXIF is returned when the file carries no readable EXIF block.
var ErrNoEXIF = errors.New("no EXIF data")

// FromJPEG reads the EXIF facts from JPEG bytes. Callers that only want the
// facts can ignore the returned error (ErrNoEXIF means "plain picture").
func FromJPEG(data []byte) (Facts, error) {
	tiff, err := exifTIFF(data)
	if err != nil {
		return Facts{}, err
	}
	return fromTIFF(tiff)
}

// exifTIFF walks the JPEG marker segments and returns the TIFF payload of
// the first Exif APP1 segment.
func exifTIFF(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, ErrNoEXIF
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return nil, ErrNoEXIF
		}
		m := data[i+1]
		switch {
		case m == 0xD9, m == 0xDA: // EOI or SOS: EXIF precedes the scan data
			return nil, ErrNoEXIF
		case m == 0xD8 || (m >= 0xD0 && m <= 0xD7): // no length field
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return nil, ErrNoEXIF
		}
		if m >= 0xE0 && m <= 0xEF { // APPn
			payload := data[i+4 : i+2+segLen]
			if len(payload) >= 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
				return payload[6:], nil
			}
		}
		i += 2 + segLen
	}
	return nil, ErrNoEXIF
}

// TIFF tag ids.
const (
	tagOrientation      = 0x0112
	tagDateTime         = 0x0132
	tagExifPointer      = 0x8769
	tagGPSPointer       = 0x8825
	tagDateTimeOriginal = 0x9003
	tagDateTimeDig      = 0x9004
	tagOffsetTime       = 0x9010
	tagOffsetTimeOrig   = 0x9011
	tagOffsetTimeDig    = 0x9012
	tagGPSLatRef        = 0x0001
	tagGPSLat           = 0x0002
	tagGPSLonRef        = 0x0003
	tagGPSLon           = 0x0004
)

// TIFF field types.
const (
	tASCII     = 2
	tSHORT     = 3
	tLONG      = 4
	tRATIONAL  = 5
	tSRATIONAL = 10
)

// entry is one resolved IFD record.
type entry struct {
	tag       int
	str       string    // tASCII
	num       int       // first tSHORT/tLONG value
	rationals []float64 // tRATIONAL/tSRATIONAL
}

// entries is a resolved IFD record list.
type entries []entry

func typeSize(typ int) int {
	switch typ {
	case tASCII:
		return 1
	case tSHORT:
		return 2
	case tLONG:
		return 4
	case tRATIONAL, tSRATIONAL:
		return 8
	}
	return 0
}

// readIFD decodes the IFD at off into resolved entries. A zero offset (no
// pointer) or out-of-bounds records yield nil.
func readIFD(d []byte, off int, bo binary.ByteOrder) entries {
	if off == 0 || off < 0 || off+2 > len(d) {
		return nil
	}
	n := int(bo.Uint16(d[off : off+2]))
	off += 2
	if off+n*12 > len(d) {
		return nil
	}
	var out entries
	for k := range n {
		e := d[off+k*12 : off+k*12+12]
		tag := int(bo.Uint16(e[0:2]))
		typ := int(bo.Uint16(e[2:4]))
		count := int(bo.Uint32(e[4:8]))
		size := typeSize(typ) * count
		if size <= 0 {
			continue
		}
		var val []byte
		switch {
		case size <= 4:
			val = append([]byte(nil), e[8:8+size]...)
		default:
			voff := int(bo.Uint32(e[8:12]))
			if voff+size <= len(d) {
				val = append([]byte(nil), d[voff:voff+size]...)
			}
		}
		if val == nil {
			continue
		}
		rec := entry{tag: tag}
		switch typ {
		case tASCII:
			rec.str = strings.TrimRight(string(val), "\x00 ")
		case tSHORT:
			rec.num = int(bo.Uint16(val[0:2]))
		case tLONG:
			rec.num = int(bo.Uint32(val[0:4]))
		case tRATIONAL:
			for r := 0; r*8+8 <= len(val); r++ {
				num := bo.Uint32(val[r*8 : r*8+4])
				den := bo.Uint32(val[r*8+4 : r*8+8])
				if den == 0 {
					rec.rationals = append(rec.rationals, math.NaN())
				} else {
					rec.rationals = append(rec.rationals, float64(num)/float64(den))
				}
			}
		case tSRATIONAL:
			for r := 0; r*8+8 <= len(val); r++ {
				num := int32(bo.Uint32(val[r*8 : r*8+4]))
				den := int32(bo.Uint32(val[r*8+4 : r*8+8]))
				if den == 0 {
					rec.rationals = append(rec.rationals, math.NaN())
				} else {
					rec.rationals = append(rec.rationals, float64(num)/float64(den))
				}
			}
		}
		out = append(out, rec)
	}
	return out
}

func (es entries) ascii(tag int) string {
	for _, e := range es {
		if e.tag == tag {
			return e.str
		}
	}
	return ""
}

func (es entries) short(tag int) int {
	for _, e := range es {
		if e.tag == tag {
			return e.num
		}
	}
	return 0
}

func (es entries) long(tag int) int {
	for _, e := range es {
		if e.tag == tag {
			return e.num
		}
	}
	return 0
}

func (es entries) rationals(tag int) []float64 {
	for _, e := range es {
		if e.tag == tag {
			return e.rationals
		}
	}
	return nil
}

func fromTIFF(d []byte) (Facts, error) {
	if len(d) < 8 {
		return Facts{}, ErrNoEXIF
	}
	var bo binary.ByteOrder
	switch {
	case bytes.Equal(d[:2], []byte("II")):
		bo = binary.LittleEndian
	case bytes.Equal(d[:2], []byte("MM")):
		bo = binary.BigEndian
	default:
		return Facts{}, ErrNoEXIF
	}
	if bo.Uint16(d[2:4]) != 0x002A {
		return Facts{}, ErrNoEXIF
	}
	ifd0 := readIFD(d, int(bo.Uint32(d[4:8])), bo)
	if ifd0 == nil {
		return Facts{}, ErrNoEXIF
	}

	f := Facts{Orientation: ifd0.short(tagOrientation)}
	f.Capture, f.Offset = captureTime(readIFD(d, ifd0.long(tagExifPointer), bo), ifd0)
	f.GPSLat, f.GPSLon, f.HasGPS = gpsPosition(readIFD(d, ifd0.long(tagGPSPointer), bo))
	return f, nil
}

// captureTime applies the standard priority DateTimeOriginal > DateTime >
// DateTimeDigitized, pairing each with its own OffsetTime tag.
func captureTime(exif, ifd0 entries) (string, string) {
	if ts := exif.ascii(tagDateTimeOriginal); ts != "" {
		return CleanTime(ts), NormalizeOffset(exif.ascii(tagOffsetTimeOrig))
	}
	if ts := ifd0.ascii(tagDateTime); ts != "" {
		return CleanTime(ts), NormalizeOffset(ifd0.ascii(tagOffsetTime))
	}
	if ts := exif.ascii(tagDateTimeDig); ts != "" {
		return CleanTime(ts), NormalizeOffset(exif.ascii(tagOffsetTimeDig))
	}
	return "", ""
}

// CleanTime trims the string and drops fractional seconds, which some
// cameras append.
func CleanTime(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// NormalizeOffset maps the EXIF offset spellings to "+HH:MM" or "Z" and
// returns "" when the value is unusable.
func NormalizeOffset(s string) string {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return ""
	case "Z", "z", "UTC", "GMT":
		return "Z"
	}
	if s[0] != '+' && s[0] != '-' {
		return ""
	}
	body := strings.ReplaceAll(s[1:], ":", "")
	if len(body) == 2 {
		body += "00"
	}
	if len(body) != 4 {
		return ""
	}
	hh, err1 := strconv.Atoi(body[:2])
	mm, err2 := strconv.Atoi(body[2:])
	if err1 != nil || err2 != nil || hh > 14 || mm > 59 {
		return ""
	}
	if s[0] == '-' && hh == 0 && mm == 0 {
		return "Z"
	}
	return s[0:1] + body[:2] + ":" + body[2:]
}

// gpsPosition converts degrees/minutes/seconds rationals plus the N/S/E/W
// references into signed decimal degrees.
func gpsPosition(gps entries) (lat, lon float64, ok bool) {
	latRef := gps.ascii(tagGPSLatRef)
	lonRef := gps.ascii(tagGPSLonRef)
	latR := gps.rationals(tagGPSLat)
	lonR := gps.rationals(tagGPSLon)
	if latRef == "" || lonRef == "" || len(latR) != 3 || len(lonR) != 3 {
		return 0, 0, false
	}
	for _, v := range append(append([]float64{}, latR...), lonR...) {
		if math.IsNaN(v) || v < 0 {
			return 0, 0, false
		}
	}
	lat = latR[0] + latR[1]/60 + latR[2]/3600
	lon = lonR[0] + lonR[1]/60 + lonR[2]/3600
	if latRef == "S" {
		lat = -lat
	}
	if lonRef == "W" {
		lon = -lon
	}
	return lat, lon, true
}
