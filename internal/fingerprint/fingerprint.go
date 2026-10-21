// Package fingerprint computes the per-file facts recorded while scanning:
// the 128-bit perceptual hash, the displayed dimensions, and the EXIF
// capture facts.
//
// JPEG, PNG and GIF are decoded with the standard library. WebP, TIFF and
// HEIF have no pure-Go decoder in this prototype, so files of those kinds
// are recorded without a perceptual hash and end up in singleton groups.
// When libheif development headers are available, HEIF decoding can be
// added behind a build tag without changing this interface.
package fingerprint

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"

	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/phash"
)

// errNoDecoder marks formats this build cannot decode to pixels.
var errNoDecoder = errors.New("no decoder for this format")

// Info is the computed fingerprint of one image file.
type Info struct {
	Phash       string // 32 hex chars (16-byte perceptual hash), "" when unhashable
	Width       int    // displayed width (after EXIF orientation)
	Height      int    // displayed height
	Kind        string // jpeg|png|gif|webp|tiff|heic
	EXIFCapture string // raw EXIF capture wall clock, e.g. "2020-05-01 12:30:00"
	EXIFOffset  string // EXIF timezone offset, e.g. "+05:30" or "Z"; "" when unknown
	HasGPS      bool   // the image carries a usable GPS position
	GPSLat      float64
	GPSLon      float64
}

// Compute reads path and derives its fingerprint. Formats without a
// decoder (webp, tiff, heic) yield an Info with an empty Phash and no
// error; corrupt pictures of decodable formats are an error.
func Compute(path, kind string) (Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	info := Info{Kind: kind}

	img, err := decode(data, kind)
	if err != nil {
		if errors.Is(err, errNoDecoder) {
			return info, nil
		}
		return info, fmt.Errorf("decode %s: %w", path, err)
	}
	h, err := phash.Compute(img)
	if err != nil {
		return info, fmt.Errorf("hash %s: %w", path, err)
	}
	b := img.Bounds()
	info.Phash = h.Hex()
	info.Width, info.Height = b.Dx(), b.Dy()

	if kind == "jpeg" {
		f, err := meta.FromJPEG(data)
		if err == nil {
			info.EXIFCapture = f.Capture
			info.EXIFOffset = f.Offset
			info.HasGPS = f.HasGPS
			info.GPSLat = f.GPSLat
			info.GPSLon = f.GPSLon
			// Orientations 5-8 are the transposed ones: the displayed
			// axes swap relative to the stored pixels.
			if f.Orientation >= 5 && f.Orientation <= 8 {
				info.Width, info.Height = info.Height, info.Width
			}
		}
	}
	return info, nil
}

// decode dispatches on the image kind.
func decode(data []byte, kind string) (image.Image, error) {
	switch kind {
	case "jpeg":
		return jpeg.Decode(bytes.NewReader(data))
	case "png":
		return png.Decode(bytes.NewReader(data))
	case "gif":
		return gif.Decode(bytes.NewReader(data))
	default:
		return nil, errNoDecoder
	}
}
