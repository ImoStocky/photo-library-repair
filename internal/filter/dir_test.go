package filter

import (
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
}

func TestFilterDirDropsAndSkipsDenied(t *testing.T) {
	root := t.TempDir()
	writeJPEG(t, filepath.Join(root, "keep.jpg"), 800, 600)
	writeJPEG(t, filepath.Join(root, "strip.jpg"), 4000, 200)

	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: abnormal-aspect-ratio
    match: { min_aspect_ratio: 16 }
    action: drop
`))
	if err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "denied")
	denier := NewDenier(denied, false, nil)
	defer denier.Close()

	rep, err := FilterDir(root, rs, denier, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped["abnormal-aspect-ratio"] != 1 {
		t.Fatalf("dropped=%v", rep.Dropped)
	}
	if len(rep.DropRels) != 1 || rep.DropRels[0] != "strip.jpg" {
		t.Fatalf("DropRels=%v", rep.DropRels)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(denied, "abnormal-aspect-ratio", "strip.jpg")); err != nil {
		t.Fatal(err)
	}

	rep2, err := FilterDir(root, rs, denier, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Dropped["abnormal-aspect-ratio"] != 0 {
		t.Fatalf("second pass should skip denied: %v", rep2.Dropped)
	}
}
