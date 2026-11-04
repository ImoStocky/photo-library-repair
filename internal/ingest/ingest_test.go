package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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

func TestScanBirthGuessIsEarliestOfBornAndMod(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "copy.jpg")
	write(t, path, jpegBytes())
	// A copied file keeps its original (older) modification time while its
	// own creation time is the copy moment; the birth guess is the earlier
	// of the two, so it lands on the modification time here.
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Scan(s, Options{Dirs: []string{root}, Workers: 1}, fakeHasher("jpeg")); err != nil {
		t.Fatal(err)
	}
	images, _ := s.AllImages()
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	if got := images[0].CreatedAt; got != old.Unix() {
		t.Fatalf("CreatedAt = %d, want %d (earlier mod time, not the later copy moment)", got, old.Unix())
	}
}

// A byte-identical copy arriving at a new path is already-processed
// content: the scan neither records it nor spends a hash on it.
func TestScanSkipsKnownContent(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "a.jpg"), jpegBytes())
	inc := t.TempDir()
	write(t, filepath.Join(inc, "a_copy.jpg"), jpegBytes())
	write(t, filepath.Join(inc, "b.jpg"), append(jpegBytes(), 0x01))

	hashed := &atomic.Int64{}
	inner := fakeHasher("jpeg")
	hash := func(path, kind string) (fingerprint.Info, error) {
		hashed.Add(1)
		return inner(path, kind)
	}
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := Scan(s, Options{Dirs: []string{src}, Workers: 1}, hash); err != nil {
		t.Fatal(err)
	}
	rep, err := Scan(s, Options{Dirs: []string{inc}, Workers: 1}, hash)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ImagesSkipped != 1 || rep.ImagesAdded != 1 {
		t.Fatalf("incoming scan: skipped=%d added=%d, want 1/1", rep.ImagesSkipped, rep.ImagesAdded)
	}
	if got := hashed.Load(); got != 2 { // a.jpg, b.jpg; never a_copy.jpg
		t.Fatalf("hasher calls = %d, want 2 (skipped files must not hash)", got)
	}
	images, _ := s.AllImages()
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2 (copy must not be recorded)", len(images))
	}
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

// countingHasher counts its calls and gives every call a distinct phash,
// so a test can tell re-hashed files from cached ones.
func countingHasher(calls *int32) Hasher {
	return func(path string, k string) (fingerprint.Info, error) {
		n := atomic.AddInt32(calls, 1)
		return fingerprint.Info{
			Phash:       fmt.Sprintf("%032x", n),
			Width:       100,
			Height:      50,
			Kind:        k,
			EXIFCapture: "2020:01:01 12:00:00",
			EXIFOffset:  "+02:00",
		}, nil
	}
}

func TestScanForceReusesUnchangedFingerprints(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.jpg")
	b := filepath.Join(root, "b.png")
	write(t, a, jpegBytes())
	write(t, b, pngBytes())

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var calls int32
	rep, err := Scan(s, Options{Dirs: []string{root}, Workers: 2}, countingHasher(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if rep.ImagesAdded != 2 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("first scan = %+v calls=%d, want 2 added and 2 calls", rep, calls)
	}
	images, _ := s.AllImages()
	if len(images) != 2 || images[0].MTime == 0 || images[1].MTime == 0 {
		t.Fatalf("mtime not stored: %+v", images)
	}
	phashB := images[1].Phash

	// Forced re-scan of the untouched directory: both files keep their
	// stored fingerprints and the hasher is not called again.
	rep, err = Scan(s, Options{Dirs: []string{root}, Force: true, Workers: 2}, countingHasher(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesSeen != 2 || rep.ImagesCached != 2 || rep.ImagesAdded != 0 || rep.ImagesUpdated != 0 {
		t.Fatalf("force scan (unchanged) = %+v, want seen 2 cached 2", rep)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("hasher called %d times, want 2 (unchanged files must not be re-hashed)", calls)
	}

	// Change one file: only it is re-hashed, the other stays cached.
	write(t, a, append(jpegBytes(), 0x01, 0x02))
	rep, err = Scan(s, Options{Dirs: []string{root}, Force: true, Workers: 2}, countingHasher(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesSeen != 2 || rep.ImagesCached != 1 || rep.ImagesUpdated != 1 {
		t.Fatalf("force scan (one changed) = %+v, want seen 2 cached 1 updated 1", rep)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("hasher called %d times, want 3", calls)
	}
	images, _ = s.AllImages()
	if images[1].Phash != phashB {
		t.Fatalf("cached file lost its phash (was %q, now %q)", phashB, images[1].Phash)
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
	// ftyp box size varies with the compatible-brands list (the corpus
	// files carry 0x34); sniffing must depend on the brand, not the size.
	b := make([]byte, 20)
	b[0], b[1], b[2], b[3] = 0, 0, 0, 0x34
	copy(b[4:8], "ftyp")
	copy(b[8:12], brand)
	return b
}
