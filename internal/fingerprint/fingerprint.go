// Package fingerprint computes the per-file facts recorded while scanning:
// the perceptual hash, the displayed dimensions, and the EXIF capture facts.
//
// JPEG, PNG and GIF are decoded with the standard library. HEIC is decoded
// with gen2brain/heic (Rust decoder as embedded WASM, CGO-free; a system
// libheif is used automatically when present). WebP and TIFF have no
// decoder in this prototype, so files of those kinds are recorded without a
// perceptual hash and end up in singleton groups.
package fingerprint

import (
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"

	"github.com/gen2brain/heic"

	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/phash"
)

// errNoDecoder marks formats this build cannot decode to pixels.
var errNoDecoder = errors.New("no decoder for this format")

// ExtraHash is one additional perceptual hash computed in the same decode
// pass as Info.Phash (the candidate DCT). Ingest stores these under sha256.
type ExtraHash struct {
	Alg  phash.Alg
	Size phash.Size // 0 = legacy DCT 16×8
	Hex  string
}

// Info is the computed fingerprint of one image file.
type Info struct {
	Phash       string // candidate DCT hex (primary phash), "" when unhashable
	ExtraHashes []ExtraHash
	Width       int    // displayed width (after EXIF orientation)
	Height      int    // displayed height
	Kind        string // jpeg|png|gif|webp|tiff|heic
	EXIFCapture string // raw EXIF capture wall clock, e.g. "2020-05-01 12:30:00"
	EXIFOffset  string // EXIF timezone offset, e.g. "+05:30" or "Z"; "" when unknown
	EXIFCamera  string // camera block (EXIF Make + Model), "" when unknown
	HasGPS      bool   // the image carries a usable GPS position
	GPSLat      float64
	GPSLon      float64
}

// Compute reads path and derives its fingerprint with the built-in
// decoders, the minimal JPEG EXIF reader, and the default (legacy 16×8
// DCT) perceptual hash. Formats without a decoder (webp, tiff) and HEIC
// files the HEIC decoder rejects yield an Info with an empty Phash and no
// error; corrupt pictures of decodable formats are an error.
func Compute(path, kind string) (Info, error) {
	return compute(path, kind, nil, phash.Config{})
}

// WithEXIF returns a hasher that reads the EXIF facts through the
// full-data engine, which sees every available tag; the pipeline
// consumes the standard four. A nil engine falls back to the built-in
// reader. Uses the default perceptual-hash config.
func WithEXIF(t *exif.Tool) func(path, kind string) (Info, error) {
	return Hasher(phash.Config{}, t)
}

// Hasher returns a fingerprint function that uses cfg for the perceptual
// hash and optional t for full-data EXIF.
func Hasher(cfg phash.Config, t *exif.Tool) func(path, kind string) (Info, error) {
	return func(path, kind string) (Info, error) {
		return compute(path, kind, t, cfg)
	}
}

// IdentityExtras are the secondary hashes written into content_phash on
// every successful decode so DoublePhash identity can run without
// re-opening the file. Candidate Phash stays DCT (cfg).
var IdentityExtras = []phash.Config{
	{Alg: phash.AlgBlockhash, Size: phash.Size16},
	{Alg: phash.AlgDoubleGradient, Size: phash.Size16},
}

func compute(path, kind string, t *exif.Tool, cfg phash.Config) (Info, error) {
	info := Info{Kind: kind}

	img, err := decodeFile(path, kind)
	if err != nil {
		if errors.Is(err, errNoDecoder) {
			// Still try EXIF for jpeg/heic so unhashable rows can carry capture facts.
			if kind == "jpeg" || kind == "heic" {
				applyEXIF(&info, path, t)
			}
			return info, nil
		}
		return info, fmt.Errorf("decode %s: %w", path, err)
	}
	cfg, err = cfg.Normalize()
	if err != nil {
		return info, err
	}
	b := img.Bounds()
	info.Width, info.Height = b.Dx(), b.Dy()
	// Reported dimensions are the stored pixels above; hashes are computed
	// on the stored pixels and compared under phash.MinRotDistance, so 90°
	// re-saves of one picture match without a canonical rotation.
	h, err := phash.ComputeWith(img, cfg)
	if err != nil {
		return info, fmt.Errorf("hash %s: %w", path, err)
	}
	info.Phash = h.Hex()
	info.ExtraHashes = append(info.ExtraHashes, ExtraHash{
		Alg: cfg.Alg, Size: cfg.Size, Hex: info.Phash,
	})
	for _, extra := range IdentityExtras {
		if extra.Alg == cfg.Alg && extra.Size == cfg.Size {
			continue
		}
		eh, err := phash.ComputeWith(img, extra)
		if err != nil {
			continue // secondary hashes are best-effort
		}
		info.ExtraHashes = append(info.ExtraHashes, ExtraHash{
			Alg: extra.Alg, Size: extra.Size, Hex: eh.Hex(),
		})
	}
	img = nil // allow GC before any JPEG meta re-read

	// EXIF facts need the full-data engine for HEIC; without it the
	// built-in reader (JPEG-only) below simply fails and the file keeps
	// no capture facts, which is the correct degradation.
	if kind == "jpeg" || kind == "heic" {
		applyEXIF(&info, path, t)
	}
	return info, nil
}

func applyEXIF(info *Info, path string, t *exif.Tool) {
	f, ok := exifFacts(path, t)
	if !ok {
		return
	}
	info.EXIFCapture = f.Capture
	info.EXIFOffset = f.Offset
	info.EXIFCamera = f.Camera
	info.HasGPS = f.HasGPS
	info.GPSLat = f.GPSLat
	info.GPSLon = f.GPSLon
	// Orientations 5-8 are the transposed ones: the displayed
	// axes swap relative to the stored pixels.
	if f.Orientation >= 5 && f.Orientation <= 8 && info.Width > 0 {
		info.Width, info.Height = info.Height, info.Width
	}
}

// exifFacts derives EXIF facts, preferring the full-data engine and
// falling back to a JPEG-only re-read (after decode, so peak RAM is not
// file bytes + decoded image together).
func exifFacts(path string, t *exif.Tool) (meta.Facts, bool) {
	if t != nil {
		if f, err := t.Facts(path); err == nil {
			return meta.Facts{
				Capture:     f.Capture,
				Offset:      f.Offset,
				Camera:      f.Camera,
				GPSLat:      f.GPSLat,
				GPSLon:      f.GPSLon,
				HasGPS:      f.HasGPS,
				Orientation: f.Orientation,
			}, true
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return meta.Facts{}, false
	}
	f, err := meta.FromJPEG(data)
	return f, err == nil
}

// decodeFile opens path and decodes from the file handle so we do not keep
// a whole-file []byte beside the decoded image. Decoders may still buffer
// internally (HEIC WASM especially).
func decodeFile(path, kind string) (image.Image, error) {
	switch kind {
	case "jpeg", "png", "gif", "heic":
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return decode(f, kind)
	default:
		// shortcut: webp/tiff have no decoder, so they are recorded
		// unhashable and never group with re-encodes of other formats;
		// add a CGO-free decoder dependency when that under-grouping bites.
		return nil, errNoDecoder
	}
}

// decode dispatches on the image kind.
func decode(r io.Reader, kind string) (image.Image, error) {
	switch kind {
	case "jpeg":
		return jpeg.Decode(r)
	case "png":
		return png.Decode(r)
	case "gif":
		return gif.Decode(r)
	case "heic":
		// Decode failures degrade to EXIF-only rows (errNoDecoder): the
		// young WASM decoder does not cover every HEIF variant, and one
		// unreadable HEIC must not error its file out of the library.
		img, err := heic.Decode(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNoDecoder, err)
		}
		return img, nil
	default:
		return nil, errNoDecoder
	}
}
