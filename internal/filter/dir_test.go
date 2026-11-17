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

	var dropCalls int
	rep, err := FilterDir(t.Context(), root, rs, denier, DirOpts{
		Workers: 2,
		OnDropProgress: func(done, total int64) {
			dropCalls++
			if total != 2 {
				t.Fatalf("drop total=%d, want 2", total)
			}
			if done < 1 || done > total {
				t.Fatalf("drop done=%d total=%d", done, total)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dropCalls != 2 {
		t.Fatalf("OnDropProgress calls=%d, want 2", dropCalls)
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

	rep2, err := FilterDir(t.Context(), root, rs, denier, DirOpts{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Dropped["abnormal-aspect-ratio"] != 0 {
		t.Fatalf("second pass should skip denied: %v", rep2.Dropped)
	}
}
