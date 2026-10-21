// Package testimg builds synthetic images and JPEG files with
// hand-crafted EXIF blocks. It exists so the test suites of the other
// packages do not duplicate TIFF assembly code.
package testimg

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
)

// Textured returns a synthetic photo-like RGBA image with energy spread
// over low and mid frequencies.
func Textured(w, h, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x), float64(y)
			v := 128.0 +
				40.0*math.Sin(fx*0.05+float64(seed)) +
				25.0*math.Sin(fy*0.03+1) +
				15.0*math.Sin((fx+fy)*0.02+2) +
				10.0*math.Sin(fx*0.15)*math.Sin(fy*0.11)
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			img.Set(x, y, color.RGBA{uint8(v), uint8(v * 0.8), uint8(v * 0.6), 255})
		}
	}
	return img
}

// EXIF describes the tags written by MakeEXIFJPEG.
type EXIF struct {
	Orientation        uint16 // 0 = omit the tag
	DateTimeOriginal   string // raw EXIF string, "" = omit
	OffsetTimeOriginal string // "" = omit
	DateTime           string // IFD0 fallback, "" = omit
	OffsetTime         string // IFD0 fallback, "" = omit
	GPS                bool   // write a fixed coordinate (52.5204N, 13.4055E)
}

// GPS values written when EXIF.GPS is set, in degrees/minutes/seconds.
const (
	gpsLatD, gpsLatM, gpsLatN, gpsLatDen = 52, 31, 133, 10 // 52°31'13.3" N
	gpsLonD, gpsLonM, gpsLonN, gpsLonDen = 13, 24, 198, 10 // 13°24'19.8" E
)

// JPEG encodes img with the given quality (1-100).
func JPEG(img image.Image, quality int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		panic(err) // never happens for in-memory images
	}
	return buf.Bytes()
}

// PNG encodes img.
func PNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // never happens for in-memory images
	}
	return buf.Bytes()
}

// WebpHeader returns bytes that sniff as a WebP container. This build has
// no WebP decoder, so tests use it to exercise the unhashable path.
func WebpHeader() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00,
		'W', 'E', 'B', 'P', 'V', 'P', '8', ' ',
		0, 0, 0, 0,
	}
}

// MakeEXIFJPEG encodes img and splices a little-endian Exif APP1 segment
// after the SOI marker, carrying exactly the tags set in ex.
func MakeEXIFJPEG(img image.Image, quality int, ex EXIF) []byte {
	tiff := buildTIFF(ex)
	encoded := JPEG(img, quality)

	app1 := make([]byte, 0, 4+6+len(tiff))
	app1 = append(app1, 0xFF, 0xE1)
	var lenField [2]byte
	binary.BigEndian.PutUint16(lenField[:], uint16(2+6+len(tiff)))
	app1 = append(app1, lenField[:]...)
	app1 = append(app1, 'E', 'x', 'i', 'f', 0, 0)
	app1 = append(app1, tiff...)

	out := make([]byte, 0, len(encoded)+len(app1))
	out = append(out, encoded[:2]...) // SOI
	out = append(out, app1...)
	out = append(out, encoded[2:]...)
	return out
}

// tiff entry types
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

// buildTIFF assembles a little-endian TIFF structure with IFD0, an ExifIFD
// and (optionally) a GPSIFD. All IFD sections are written contiguously and
// every out-of-line value goes into one value area at the end, so the
// offsets can be precomputed.
func buildTIFF(ex EXIF) []byte {
	le := binary.LittleEndian

	var ifd0 []tiffEntry
	if ex.Orientation > 0 {
		ifd0 = append(ifd0, tiffEntry{tag: 0x0112, typ: tSHORT, svalue: ex.Orientation})
	}
	if ex.DateTime != "" {
		ifd0 = append(ifd0, tiffEntry{tag: 0x0132, typ: tASCII, ascii: ex.DateTime})
	}
	exifPtrIdx := len(ifd0)
	ifd0 = append(ifd0, tiffEntry{tag: 0x8769, typ: tLONG})
	var gpsPtrIdx int
	if ex.GPS {
		gpsPtrIdx = len(ifd0)
		ifd0 = append(ifd0, tiffEntry{tag: 0x8825, typ: tLONG})
	}
	if ex.OffsetTime != "" {
		// 0x9010 sorts after the IFD pointers.
		ifd0 = append(ifd0, tiffEntry{tag: 0x9010, typ: tASCII, ascii: ex.OffsetTime})
	}

	var exifIFD []tiffEntry
	if ex.DateTimeOriginal != "" {
		exifIFD = append(exifIFD, tiffEntry{tag: 0x9003, typ: tASCII, ascii: ex.DateTimeOriginal})
	}
	if ex.OffsetTimeOriginal != "" {
		exifIFD = append(exifIFD, tiffEntry{tag: 0x9011, typ: tASCII, ascii: ex.OffsetTimeOriginal})
	}

	var gpsIFD []tiffEntry
	if ex.GPS {
		gpsIFD = append(gpsIFD,
			tiffEntry{tag: 0x0001, typ: tASCII, ascii: "N"},
			tiffEntry{tag: 0x0002, typ: tRATIONAL, rational: [3][2]uint32{
				{gpsLatD, 1}, {gpsLatM, 1}, {gpsLatN, gpsLatDen}}},
			tiffEntry{tag: 0x0003, typ: tASCII, ascii: "E"},
			tiffEntry{tag: 0x0004, typ: tRATIONAL, rational: [3][2]uint32{
				{gpsLonD, 1}, {gpsLonM, 1}, {gpsLonN, gpsLonDen}}},
		)
	}

	ifdSize := func(n int) int { return 2 + n*12 + 4 }
	exifOff := 8 + ifdSize(len(ifd0))
	gpsOff := exifOff + ifdSize(len(exifIFD))
	valOff := gpsOff + ifdSize(len(gpsIFD))

	groups := [][]tiffEntry{ifd0, exifIFD, gpsIFD}

	// Value offsets: the value area at valOff holds the out-of-line bodies
	// in group order, entry order.
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
	if ex.GPS {
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
	if ex.GPS {
		writeIFD(2)
	}

	for len(data) < valOff {
		data = append(data, 0)
	}
	for _, group := range groups {
		for _, e := range group {
			body := e.body()
			if len(body) > 4 {
				data = append(data, body...)
			}
		}
	}
	return data
}
