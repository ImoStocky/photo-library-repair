package cli

import (
	"os"
	"path/filepath"
	"testing"

	"photo-library-repair/internal/testimg"
)

// TestRunBuildEndToEnd smoke-tests the command wiring: flag parsing and the
// scan -> group -> plan -> apply phase order through the public Run entry.
func TestRunBuildEndToEnd(t *testing.T) {
	src := t.TempDir()
	ex := testimg.EXIF{Make: "Canon", Model: "EOS", DateTimeOriginal: "2024:05:01 12:30:00", OffsetTimeOriginal: "+02:00"}
	// c is an unrelated picture captured on a different day, so the
	// timestamp sweep and the identity gate both keep it apart from a/b.
	exOther := testimg.EXIF{Make: "Canon", Model: "EOS", DateTimeOriginal: "2024:05:02 12:30:00", OffsetTimeOriginal: "+02:00"}
	for name, data := range map[string][]byte{
		"a.jpg": testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 1), 95, ex),
		"b.jpg": testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 1), 90, ex), // near-duplicate of a
		// seed 10 is far from seed 1 in the dual 16x16 hashes
		// (measured 128/64 bits; the gate allows 96/28).
		"c.jpg": testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 10), 95, exOther),
	} {
		if err := os.WriteFile(filepath.Join(src, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "out")
	db := filepath.Join(t.TempDir(), "plr.db")

	code := Run([]string{"build", "-dir", src, "--target", target, "--db", db, "--workers", "2"})
	if code != 0 {
		t.Fatalf("build exit code %d (want 0)", code)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	// The 2 groups plus report.json; the near-duplicate pair collapsed to one.
	if len(entries) != 3 {
		t.Fatalf("target entries = %d (want 3: 2 images + report.json)", len(entries))
	}
}

// TestRunIncrementEndToEnd smoke-tests scan → merge-partition → plan → apply
// through the public Run entry on a library that already has groups.
func TestRunIncrementEndToEnd(t *testing.T) {
	src := t.TempDir()
	ex := testimg.EXIF{Make: "Canon", Model: "EOS", DateTimeOriginal: "2024:05:01 12:30:00", OffsetTimeOriginal: "+02:00"}
	exOther := testimg.EXIF{Make: "Canon", Model: "EOS", DateTimeOriginal: "2024:05:02 12:30:00", OffsetTimeOriginal: "+02:00"}
	for name, data := range map[string][]byte{
		"a.jpg": testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 1), 95, ex),
		"b.jpg": testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 1), 90, ex),
		"c.jpg": testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 10), 95, exOther),
	} {
		if err := os.WriteFile(filepath.Join(src, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "out")
	db := filepath.Join(t.TempDir(), "plr.db")
	if code := Run([]string{"build", "-dir", src, "--target", target, "--db", db, "--workers", "2"}); code != 0 {
		t.Fatalf("build exit code %d (want 0)", code)
	}

	inbox := t.TempDir()
	exNew := testimg.EXIF{Make: "Canon", Model: "EOS", DateTimeOriginal: "2024:05:03 12:30:00", OffsetTimeOriginal: "+02:00"}
	if err := os.WriteFile(filepath.Join(inbox, "d.jpg"),
		testimg.MakeEXIFJPEG(testimg.Textured(400, 300, 20), 95, exNew), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"increment", "-dir", inbox, "--target", target, "--db", db, "--workers", "2"}); code != 0 {
		t.Fatalf("increment exit code %d (want 0)", code)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	// Wiring smoke: prior build outputs survive; new file may join or become
	// its own group depending on dual-hash distance (not asserted here).
	if _, err := os.Stat(filepath.Join(target, "report.json")); err != nil {
		t.Fatalf("report.json missing after increment: %v", err)
	}
	if len(entries) < 3 {
		t.Fatalf("target entries = %d (want >= 3 from prior build)", len(entries))
	}
}
