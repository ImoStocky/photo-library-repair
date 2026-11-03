// Package exif is the full-data EXIF engine: the real ExifTool, embedded
// as WebAssembly (pure Go, no system dependencies, no cgo). It reads every
// available metadata tag of a file and writes the merged facts while
// preserving all other tags — including ones the pipeline does not use
// (title, description, artist, copyright, camera fields, ...).
//
// The engine is best-effort at startup: when New fails, callers fall back
// to the built-in minimal reader (meta) and writer (exifwrite), which
// cover the four pipeline facts.
package exif

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	exiftool "github.com/yashikota/exiftool-go/pkg/exiftool"

	"photo-library-repair/internal/meta"
)

// Facts are the pipeline EXIF facts, derived from a full tag set.
type Facts struct {
	Capture     string  // EXIF wall clock, e.g. "2020:05:01 12:30:00"
	Offset      string  // timezone offset, "+05:30" or "Z"; "" when unknown
	GPSLat      float64 // signed degrees, 0 when absent
	GPSLon      float64
	HasGPS      bool
	Orientation int    // 1-8 per EXIF, 0 when the tag is absent
	Camera      string // camera block (Make + Model), "" when both tags are absent
	Tags        int    // how many tags the engine read (full data)
}

// Tool is one embedded ExifTool instance. Methods are safe for concurrent
// use: calls are serialized internally.
type Tool struct {
	et *exiftool.ExifTool
}

// New starts the engine (WASM runtime plus Perl interpreter). The cost is
// paid once per process.
func New() (*Tool, error) {
	et, err := exiftool.New()
	if err != nil {
		return nil, fmt.Errorf("exiftool engine: %w", err)
	}
	return &Tool{et: et}, nil
}

// Close releases the engine resources.
func (t *Tool) Close() { t.et.Close() }

// Version returns the embedded ExifTool version.
func (t *Tool) Version() (string, error) { return t.et.Version() }

// Read returns every metadata tag ExifTool can extract from path. Keys are
// bare tag names; a tag present in more than one section appears as
// "Tag (1)" for the extra occurrence.
func (t *Tool) Read(path string) (map[string]any, error) {
	return t.et.ReadMetadata(path)
}

// SetTags writes src to dst (in place when dst is empty) with the given
// tags set. Every other metadata tag in src is preserved.
func (t *Tool) SetTags(src, dst string, tags map[string]any) error {
	return t.et.WriteMetadata(src, dst, tags)
}

// Facts reads path and derives the pipeline facts from the full tag set.
func (t *Tool) Facts(path string) (Facts, error) {
	m, err := t.Read(path)
	if err != nil {
		return Facts{}, err
	}
	return derive(m), nil
}

// WriteFacts writes src to dst (in place when dst is empty) with f
// applied. Every other metadata tag in src is preserved; a zero
// Orientation leaves the file's own orientation tag untouched.
func (t *Tool) WriteFacts(src, dst string, f Facts) error {
	tags := map[string]any{}
	if f.Capture != "" {
		tags["DateTimeOriginal"] = f.Capture
	}
	if f.Offset != "" {
		tags["OffsetTimeOriginal"] = writeOffset(f.Offset)
	}
	if f.HasGPS {
		tags["GPSLatitudeRef"] = hemisphere(f.GPSLat, true)
		tags["GPSLatitude"] = dmsString(f.GPSLat)
		tags["GPSLongitudeRef"] = hemisphere(f.GPSLon, false)
		tags["GPSLongitude"] = dmsString(f.GPSLon)
	}
	if f.Orientation > 0 {
		tags["Orientation"] = strconv.Itoa(f.Orientation)
	}
	if len(tags) == 0 {
		return nil
	}
	return t.et.WriteMetadata(src, dst, tags)
}

// derive maps the engine's full tag set to the pipeline facts.
func derive(m map[string]any) Facts {
	f := Facts{Tags: len(m)}
	f.Camera = meta.CameraBlock(strOf(m["Make"]), strOf(m["Model"]))
	f.Capture, f.Offset = captureTime(m)
	f.Orientation = intValue(m["Orientation"])
	f.GPSLat, f.GPSLon, f.HasGPS = gpsPosition(m)
	return f
}

// captureTime applies the standard priority DateTimeOriginal > DateTime >
// DateTimeDigitized, pairing each with its own OffsetTime tag.
func captureTime(m map[string]any) (string, string) {
	if ts := strOf(m["DateTimeOriginal"]); ts != "" {
		return meta.CleanTime(ts), meta.NormalizeOffset(strOf(m["OffsetTimeOriginal"]))
	}
	if ts := strOf(m["DateTime"]); ts != "" {
		return meta.CleanTime(ts), meta.NormalizeOffset(strOf(m["OffsetTime"]))
	}
	if ts := strOf(m["DateTimeDigitized"]); ts != "" {
		return meta.CleanTime(ts), meta.NormalizeOffset(strOf(m["OffsetTimeDigitized"]))
	}
	return "", ""
}

// strOf renders a tag value as a string; numbers come back without a
// decimal point.
func strOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// intValue parses a tag value as an integer; anything unusable is 0.
func intValue(v any) int {
	n, err := strconv.Atoi(strOf(v))
	if err != nil {
		return 0
	}
	return n
}

// gpsPosition converts ExifTool's DMS GPS values ("52 deg 31' 13.3\" N")
// to signed decimal degrees.
func gpsPosition(m map[string]any) (float64, float64, bool) {
	lat, latOK := dmsValue(m["GPSLatitude"], strOf(m["GPSLatitudeRef"]))
	lon, lonOK := dmsValue(m["GPSLongitude"], strOf(m["GPSLongitudeRef"]))
	if !latOK || !lonOK {
		return 0, 0, false
	}
	return lat, lon, true
}

// dmsValue converts one GPS coordinate to signed decimal degrees. The
// refFallback supplies the hemisphere when the value is a bare number.
func dmsValue(v any, refFallback string) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return signed(x, refFallback), true
	case string:
		s := strings.TrimSpace(x)
		if d, err := strconv.ParseFloat(s, 64); err == nil {
			return signed(d, refFallback), true
		}
		deg, hemi, ok := parseDMS(s)
		if !ok {
			return 0, false
		}
		return signed(deg, hemi), true
	}
	return 0, false
}

// parseDMS parses "52 deg 31' 13.30\" N" (the bare "52 31 13.30" spelling
// is tolerated too) into decimal degrees and the hemisphere's first letter.
func parseDMS(s string) (deg float64, hemi string, ok bool) {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, "", false
	}
	whole, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, "", false
	}
	i := 1
	if fields[i] == "deg" {
		i++
	}
	if len(fields) < i+2 {
		return 0, "", false
	}
	min, err := strconv.ParseFloat(strings.TrimRight(fields[i], "'"), 64)
	if err != nil {
		return 0, "", false
	}
	sec, err := strconv.ParseFloat(strings.TrimRight(fields[i+1], `"`), 64)
	if err != nil {
		return 0, "", false
	}
	if len(fields) > i+2 {
		hemi = strings.ToUpper(fields[i+2])
	}
	if whole < 0 || min < 0 || sec < 0 {
		return 0, "", false
	}
	return whole + min/60 + sec/3600, hemi, true
}

// signed applies the hemisphere (from the DMS spelling or the ref tag) to
// a coordinate.
func signed(d float64, ref string) float64 {
	if strings.Contains(ref, "S") || strings.Contains(ref, "W") {
		return -d
	}
	return d
}

// writeOffset renders the canonical offset the way ExifTool writes it:
// "Z" becomes "+00:00".
func writeOffset(o string) string {
	if o == "Z" {
		return "+00:00"
	}
	return o
}

// hemisphere renders the N/S/E/W hemisphere of a decimal degree.
func hemisphere(deg float64, isLat bool) string {
	neg := deg < 0
	if isLat {
		if neg {
			return "S"
		}
		return "N"
	}
	if neg {
		return "W"
	}
	return "E"
}

// dmsString renders decimal degrees in ExifTool's "deg min sec" spelling,
// with the seconds at two decimals.
func dmsString(deg float64) string {
	if deg < 0 {
		deg = -deg
	}
	d := int(math.Floor(deg))
	rem := (deg - float64(d)) * 60
	m := int(math.Floor(rem))
	s := (rem - float64(m)) * 60
	if s >= 59.995 { // rounding to two decimals would carry into 60
		s = 0
		m++
		if m == 60 {
			m = 0
			d++
		}
	}
	return fmt.Sprintf("%d %d %.2f", d, m, s)
}
