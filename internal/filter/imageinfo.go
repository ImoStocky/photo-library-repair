package filter

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"maps"
	"os"
	"sync"

	_ "github.com/gen2brain/heic"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"photo-library-repair/internal/meta"
)

// ExifLoader loads all EXIF tags for a path once. Missing file EXIF is an
// empty map, not an error.
type ExifLoader func(path string) (map[string]string, error)

// ImageInfo holds the facts filter rules can match.
type ImageInfo struct {
	Path    string // absolute path
	RelPath string // path relative to library root (for path globs + deny layout)
	Size    int64
	Format  string // DecodeConfig name: jpeg, png, ...
	Width   int
	Height  int

	exifOnce   sync.Once
	exifMap    map[string]string
	exifLoader ExifLoader
}

// LoadImageInfo reads size and header dimensions/format for path.
func LoadImageInfo(path, relPath string, exif ExifLoader) (*ImageInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	return &ImageInfo{
		Path:       path,
		RelPath:    relPath,
		Size:       fi.Size(),
		Format:     format,
		Width:      cfg.Width,
		Height:     cfg.Height,
		exifLoader: exif,
	}, nil
}

func (info *ImageInfo) ensureExif() {
	info.exifOnce.Do(func() {
		info.exifMap = map[string]string{}
		if info.exifLoader != nil {
			m, err := info.exifLoader(info.Path)
			if err != nil || m == nil {
				return
			}
			info.exifMap = m
			return
		}
		data, err := os.ReadFile(info.Path)
		if err != nil {
			return
		}
		facts, err := meta.FromJPEG(data)
		if err != nil {
			return
		}
		if facts.Capture != "" {
			info.exifMap["DateTimeOriginal"] = facts.Capture
		}
		if facts.Offset != "" {
			info.exifMap["OffsetTimeOriginal"] = facts.Offset
		}
		if facts.Orientation > 0 {
			info.exifMap["Orientation"] = fmt.Sprintf("%d", facts.Orientation)
		}
		if facts.Camera != "" {
			info.exifMap["Camera"] = facts.Camera
		}
	})
}

// EXIF returns a cached tag value. Missing tags yield ok=false.
func (info *ImageInfo) EXIF(tag string) (string, bool) {
	info.ensureExif()
	v, ok := info.exifMap[tag]
	return v, ok
}

// ExifAll returns a copy of all loaded EXIF tags (lazy-loaded once).
func (info *ImageInfo) ExifAll() map[string]string {
	info.ensureExif()
	out := make(map[string]string, len(info.exifMap))
	maps.Copy(out, info.exifMap)
	return out
}
