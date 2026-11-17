package filter

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// 100×100 lossy WebP (PIL-generated) for DecodeConfig tests.
var tinyWebP = []byte{
	0x52, 0x49, 0x46, 0x46, 0x56, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50, 0x38, 0x20,
	0x4a, 0x00, 0x00, 0x00, 0xb0, 0x05, 0x00, 0x9d, 0x01, 0x2a, 0x64, 0x00, 0x64, 0x00, 0x3e, 0x6d,
	0x36, 0x99, 0x49, 0xa4, 0x23, 0x22, 0xa1, 0x20, 0xa8, 0x00, 0x80, 0x0d, 0x89, 0x69, 0x6e, 0xe1,
	0x73, 0xe0, 0x00, 0x63, 0x6b, 0xa9, 0xc5, 0x08, 0xd9, 0x60, 0x1a, 0xea, 0x6f, 0x71, 0x17, 0x96,
	0x01, 0xae, 0xa6, 0xf7, 0x11, 0x79, 0x60, 0x1a, 0xe0, 0x00, 0x00, 0xfe, 0xff, 0x5a, 0x49, 0xf9,
	0xd8, 0xf7, 0x07, 0xfd, 0x07, 0xc3, 0x4d, 0x0d, 0x77, 0x10, 0x00, 0x00, 0x00, 0x00,
}

func TestDenyMoveLayoutAndCollision(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "lib", "2021", "trip")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "IMG_1.jpg")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "lib", "denied")
	d := NewDenier(denied, false, nil)
	defer d.Close()

	dst, err := d.Drop(src, filepath.Join("2021", "trip", "IMG_1.jpg"), "clutter", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(denied, "clutter", "2021", "trip", "IMG_1.jpg")
	if dst != want {
		t.Fatalf("dst=%s want %s", dst, want)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source should be gone")
	}

	src2 := filepath.Join(root, "lib", "IMG_1.jpg")
	if err := os.WriteFile(src2, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst2, err := d.Drop(src2, filepath.Join("2021", "trip", "IMG_1.jpg"), "clutter", 5)
	if err != nil {
		t.Fatal(err)
	}
	want2 := filepath.Join(denied, "clutter", "2021", "trip", "IMG_1 (1).jpg")
	if dst2 != want2 {
		t.Fatalf("collision dst=%s want %s", dst2, want2)
	}

	man, err := os.ReadFile(filepath.Join(denied, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(man), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("manifest lines=%d", len(lines))
	}
	var e ManifestEntry
	if err := json.Unmarshal(lines[0], &e); err != nil {
		t.Fatal(err)
	}
	if e.Rule != "clutter" || e.Dst != want {
		t.Fatalf("manifest %+v", e)
	}
}

func TestDenyDryRun(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.png")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	d := NewDenier(filepath.Join(root, "denied"), true, &buf)
	if _, err := d.Drop(src, "a.png", "miniature", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("dry-run must leave source")
	}
	if !bytes.Contains(buf.Bytes(), []byte("rule=miniature")) {
		t.Fatalf("buf=%s", buf.String())
	}
}

func TestRestoreDenied(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "lib")
	srcDir := filepath.Join(lib, "2021", "trip")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "IMG_1.jpg")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(lib, "denied")
	d := NewDenier(denied, false, nil)
	if _, err := d.Drop(src, filepath.Join("2021", "trip", "IMG_1.jpg"), "clutter", 5); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	rep, err := RestoreDenied(denied, FlattenOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moved != 1 || rep.Errors != 0 {
		t.Fatalf("moved=%d missing=%d errors=%d", rep.Moved, rep.Missing, rep.Errors)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(denied, denyManifestName)); !os.IsNotExist(err) {
		t.Fatalf("manifest should be removed: %v", err)
	}
	if _, err := os.Stat(denied); !os.IsNotExist(err) {
		t.Fatalf("empty denied dir should be removed: %v", err)
	}
}

func TestDenyEXDEVFallback(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.jpg")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "denied")
	d := NewDenier(denied, false, nil)
	d.rename = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	defer d.Close()
	dst, err := d.Drop(src, "a.jpg", "x", 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source should be removed after copy")
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "payload" {
		t.Fatalf("dst=%v data=%q", err, b)
	}
}

func TestAcceptanceExampleConfig(t *testing.T) {
	root := t.TempDir()
	writePNG := func(name string, w, h int) string {
		p := filepath.Join(root, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		img.Set(0, 0, color.RGBA{1, 2, 3, 255})
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	writeJPEG := func(name string, w, h int) string {
		p := filepath.Join(root, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
		return p
	}

	strip := writeJPEG("strip.jpg", 4000, 200)
	mini := writeJPEG("mini.jpg", 400, 400)
	smallPNG := writePNG("small.png", 64, 64)
	keepPNG := writePNG("keep.png", 400, 400)
	{
		f, err := os.OpenFile(keepPNG, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(bytes.Repeat([]byte{0}, 60*1024)); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}
	keepJPEG := writeJPEG("keep.jpg", 3000, 2000)
	webpPath := filepath.Join(root, "tiny.webp")
	if err := os.WriteFile(webpPath, tinyWebP, 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: abnormal-aspect-ratio
    match: { min_aspect_ratio: 16 }
    action: drop
  - name: miniature
    match: { format: jpeg, aspect_ratio: 1, max_width: 499, max_height: 499 }
    action: drop
  - name: clutter
    match:
      any:
        - all: [ { format: png }, { max_file_size: 50KB } ]
        - all: [ { max_width: 127 }, { max_height: 127 } ]
    action: drop
`))
	if err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "denied")
	d := NewDenier(denied, false, nil)
	defer d.Close()

	check := func(path, wantRule string, wantDrop bool) {
		t.Helper()
		info, err := LoadImageInfo(path, filepath.Base(path), nil)
		if err != nil {
			t.Fatal(path, err)
		}
		dec := rs.Evaluate(info)
		if wantDrop {
			if dec.Action != ActionDrop || dec.Rule != wantRule {
				t.Fatalf("%s: %+v want drop %s (format=%s %dx%d size=%d)", path, dec, wantRule, info.Format, info.Width, info.Height, info.Size)
			}
			if _, err := d.Drop(path, filepath.Base(path), dec.Rule, info.Size); err != nil {
				t.Fatal(err)
			}
			dst := filepath.Join(denied, wantRule, filepath.Base(path))
			if _, err := os.Stat(dst); err != nil {
				t.Fatal(err)
			}
		} else if dec.Action != ActionAccept {
			t.Fatalf("%s should accept: %+v", path, dec)
		}
	}
	check(strip, "abnormal-aspect-ratio", true)
	check(mini, "miniature", true)
	check(smallPNG, "clutter", true)
	check(webpPath, "clutter", true)
	check(keepPNG, "", false)
	check(keepJPEG, "", false)
}
