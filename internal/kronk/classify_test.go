package kronk

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardanlabs/kronk/sdk/kronk/model"

	"photo-library-repair/internal/filter"
)

func TestBuildClassifyMessages(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff} // not a real JPEG; shape-only check
	msgs := buildClassifyMessages("pick a label", jpeg, "filename: a.jpg\ndimensions: 800x600")
	if len(msgs) != 2 {
		t.Fatalf("len(msgs)=%d, want 2 (system + user)", len(msgs))
	}
	if msgs[0]["role"] != model.RoleSystem {
		t.Fatalf("msgs[0].role=%v, want system", msgs[0]["role"])
	}
	if msgs[0]["content"] != "pick a label" {
		t.Fatalf("system content=%v", msgs[0]["content"])
	}
	if msgs[1]["role"] != model.RoleUser {
		t.Fatalf("msgs[1].role=%v, want user", msgs[1]["role"])
	}
	parts, ok := msgs[1]["content"].([]model.D)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content parts=%v, want image+text", msgs[1]["content"])
	}
	if parts[0]["type"] != "image_url" {
		t.Fatalf("part0 type=%v, want image_url", parts[0]["type"])
	}
	img, ok := parts[0]["image_url"].(model.D)
	if !ok {
		t.Fatalf("image_url type=%T", parts[0]["image_url"])
	}
	url, _ := img["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("url=%q, want jpeg data URL", url)
	}
	if parts[1]["type"] != "text" || parts[1]["text"] == "" {
		t.Fatalf("part1=%v, want text sidecar", parts[1])
	}
}

func TestClassifySidecarText(t *testing.T) {
	path := writeTestJPEG(t, 2, 2)
	info, err := filter.LoadImageInfo(path, "2021/trip/IMG_1.jpg", func(string) (map[string]string, error) {
		return map[string]string{
			"DateTimeOriginal": "2021:05:01 12:00:00",
			"Model":            "iPhone 12",
			"Orientation":      "1",
			"IgnoredHugeTag":   strings.Repeat("x", 200),
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info.Width = 4032
	info.Height = 3024
	info.Size = 1234567
	got := classifySidecarText(info)
	for _, want := range []string{
		"filename: 2021/trip/IMG_1.jpg",
		"dimensions: 4032x3024",
		"format: jpeg",
		"bytes: 1234567",
		"DateTimeOriginal=2021:05:01 12:00:00",
		"Model=iPhone 12",
		"Orientation=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sidecar missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "IgnoredHugeTag") {
		t.Fatalf("unexpected unlisted EXIF key in sidecar:\n%s", got)
	}
}

func TestImageJPEGBytesDownscale(t *testing.T) {
	path := writeTestJPEG(t, 2000, 1000)
	data, err := imageJPEGBytes(path, 512)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != 512 || b.Dy() != 256 {
		t.Fatalf("got %dx%d, want 512x256", b.Dx(), b.Dy())
	}
}

func writeTestJPEG(t *testing.T, w, h int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jpg")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return path
}
