package fingerprint

import (
	"os"
	"path/filepath"
	"testing"

	"photo-library-repair/internal/phash"
	"photo-library-repair/internal/testimg"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestComputeJPEGWithEXIF(t *testing.T) {
	img := testimg.Textured(64, 48, 7)
	data := testimg.MakeEXIFJPEG(img, 90, testimg.EXIF{
		Orientation:        6,
		DateTimeOriginal:   "2024:05:01 12:30:00",
		OffsetTimeOriginal: "+02:00",
		GPS:                true,
	})
	info, err := Compute(writeFile(t, "a.jpg", data), "jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if info.Phash == "" {
		t.Fatal("missing phash")
	}
	h, err := phash.HashFromHex(info.Phash)
	if err != nil || !h.Valid() {
		t.Fatalf("phash %q invalid: %v", info.Phash, err)
	}
	// Orientation 6 swaps the displayed axes: stored 64x48 -> 48x64.
	if info.Width != 48 || info.Height != 64 {
		t.Fatalf("dims = %dx%d, want 48x64", info.Width, info.Height)
	}
	if info.EXIFCapture != "2024:05:01 12:30:00" || info.EXIFOffset != "+02:00" {
		t.Fatalf("capture = %q %q", info.EXIFCapture, info.EXIFOffset)
	}
	if !info.HasGPS || info.GPSLat < 52.52 || info.GPSLon < 13.40 {
		t.Fatalf("gps = %v %v %v", info.HasGPS, info.GPSLat, info.GPSLon)
	}
	if info.Kind != "jpeg" {
		t.Fatalf("kind = %q", info.Kind)
	}
}

func TestComputeJPEGNoExifKeepsStoredDims(t *testing.T) {
	data := testimg.JPEG(testimg.Textured(32, 24, 2), 90)
	info, err := Compute(writeFile(t, "b.jpg", data), "jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 32 || info.Height != 24 {
		t.Fatalf("dims = %dx%d, want 32x24", info.Width, info.Height)
	}
	if info.EXIFCapture != "" || info.HasGPS {
		t.Fatalf("unexpected EXIF facts: %+v", info)
	}
}

func TestComputePNG(t *testing.T) {
	// Reuse the JPEG bytes for nothing; encode a real PNG via the helper.
	data := testimg.PNG(testimg.Textured(20, 16, 3))
	info, err := Compute(writeFile(t, "c.png", data), "png")
	if err != nil {
		t.Fatal(err)
	}
	if info.Phash == "" || info.Width != 20 || info.Height != 16 {
		t.Fatalf("info = %+v", info)
	}
	if info.EXIFCapture != "" {
		t.Fatalf("png must not carry EXIF capture: %q", info.EXIFCapture)
	}
}

func TestComputeUnsupportedKindHasNoPhash(t *testing.T) {
	data := testimg.WebpHeader()
	info, err := Compute(writeFile(t, "d.webp", data), "webp")
	if err != nil {
		t.Fatal(err)
	}
	if info.Phash != "" || info.Width != 0 {
		t.Fatalf("webp must be unhashable, got %+v", info)
	}
}

func TestComputeCorruptJPEGIsError(t *testing.T) {
	p := writeFile(t, "e.jpg", []byte{0xFF, 0xD8, 0xFF, 0x00, 0x01, 0x02})
	if _, err := Compute(p, "jpeg"); err == nil {
		t.Fatal("corrupt jpeg must be an error")
	}
}

// A real HEIC (32x24 fixture encoded with pillow-heif) must hash like the
// standard-library formats.
func TestComputeHEIC(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "fixture.heic"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := Compute(writeFile(t, "a.heic", data), "heic")
	if err != nil {
		t.Fatal(err)
	}
	if info.Phash == "" || info.Width != 32 || info.Height != 24 {
		t.Fatalf("heic must be hashable, got %+v", info)
	}
}

// A HEIC the decoder rejects degrades to an EXIF-only row like a format
// without a decoder, not a file error.
func TestComputeUnreadableHEICDegradesToNoPhash(t *testing.T) {
	b := make([]byte, 64)
	copy(b, "ftypheic")
	info, err := Compute(writeFile(t, "b.heic", b), "heic")
	if err != nil {
		t.Fatal(err)
	}
	if info.Phash != "" || info.Width != 0 {
		t.Fatalf("unreadable heic must be unhashable, got %+v", info)
	}
}
