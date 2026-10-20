// Package fingerprint describes the per-file facts computed while scanning.
package fingerprint

// Info is the computed fingerprint of one image file.
type Info struct {
	Phash       string // 4 hex chars (16-bit perceptual hash), "" when unhashable
	Width       int    // displayed width (after EXIF orientation)
	Height      int    // displayed height
	Kind        string // jpeg|png|gif|webp|tiff|heic
	EXIFCapture string // raw EXIF capture wall clock, e.g. "2020:05:01 12:30:00"
	EXIFOffset  string // EXIF timezone offset, e.g. "+05:30" or "Z"; "" when unknown
}
