package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/store"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeHasher(kind string) Hasher {
	return func(path string, k string) (fingerprint.Info, error) {
		return fingerprint.Info{
			Phash:       "abcd",
			Width:       100,
			Height:      50,
			Kind:        k,
			EXIFCapture: "2020:01:01 12:00:00",
			EXIFOffset:  "+02:00",
		}, nil
	}
}

func jpegBytes() []byte {
	b := make([]byte, 32)
	b[0], b[1], b[2] = 0xFF, 0xD8, 0xFF
	return b
}

func pngBytes() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 16)...)
}

func TestScanAndSkipAndForce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.jpg"), jpegBytes())
	write(t, filepath.Join(root, "sub", "b.png"), pngBytes())
	write(t, filepath.Join(root, "notes.txt"), []byte("hello"))

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rep, err := Scan(s, Options{Dirs: []string{root}, Workers: 2}, fakeHasher("jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.DirsScanned != 1 || rep.ImagesAdded != 2 || rep.NonImage != 0 || rep.Errors != 0 {
		t.Fatalf("first scan = %+v", rep)
	}
	images, _ := s.AllImages()
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}
	for _, img := range images {
		if img.Phash != "abcd" || img.ExifCapture != "2020:01:01 12:00:00" || img.CreatedAt <= 0 {
			t.Fatalf("image = %+v", img)
		}
	}

	// Second scan without --force skips the directory.
	rep, err = Scan(s, Options{Dirs: []string{root}, Workers: 2}, fakeHasher("jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.DirsSkipped != 1 || rep.DirsScanned != 0 {
		t.Fatalf("second scan = %+v", rep)
	}

	// Delete one file, force re-scan: the row must disappear.
	if err := os.Remove(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	rep, err = Scan(s, Options{Dirs: []string{root}, Force: true, Workers: 2}, fakeHasher("jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.DirsScanned != 1 {
		t.Fatalf("force scan = %+v", rep)
	}
	images, _ = s.AllImages()
	if len(images) != 1 || images[0].RelPath != filepath.Join("sub", "b.png") {
		t.Fatalf("after force = %+v", images)
	}
}

func TestScanNestedDirsDedupeByInode(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.jpg"), jpegBytes())
	write(t, filepath.Join(root, "sub", "b.jpg"), jpegBytes())
	// A nested source pointing at the same tree must not double-record.
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rep, err := Scan(s, Options{
		Dirs:    []string{root, filepath.Join(root, "sub")},
		Workers: 1,
	}, fakeHasher("jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.ImagesAdded != 2 {
		t.Fatalf("nested scan added %d images, want 2 (report %+v)", rep.ImagesAdded, rep)
	}
}

func TestKind(t *testing.T) {
	cases := []struct {
		name   string
		header []byte
		want   string
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, "jpeg"},
		{"png", pngBytes(), "png"},
		{"gif", []byte("GIF89a" + "xxxx"), "gif"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBPxxxx")...), "webp"},
		{"tiff-le", []byte{0x49, 0x49, 0x2A, 0x00}, "tiff"},
		{"tiff-be", []byte{0x4D, 0x4D, 0x00, 0x2A}, "tiff"},
		{"heic", heicBytes("heic"), "heic"},
		{"heif", heicBytes("heif"), "heic"},
		{"mp4", heicBytes("mp42"), ""},
		{"text", []byte("hello world!"), ""},
	}
	for _, tc := range cases {
		if got := Kind(tc.header); got != tc.want {
			t.Errorf("Kind(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func heicBytes(brand string) []byte {
	b := make([]byte, 16)
	b[0], b[1], b[2], b[3] = 0, 0, 0, 32
	copy(b[4:8], "ftyp")
	copy(b[8:12], brand)
	return b
}
