package apply

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"photo-library-repair/internal/group"
	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/store"
	"photo-library-repair/internal/testimg"
)

// instant is 2024:05:01 12:30:00 +02:00 = 10:30:00 UTC.
var instant = time.Date(2024, 5, 1, 10, 30, 0, 0, time.UTC).Unix()

func newStore(t *testing.T) (*store.Store, string, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	srcDir := t.TempDir()
	id, err := s.AddSource(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	return s, srcDir, id
}

func addFile(t *testing.T, dir, rel string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func addImage(t *testing.T, s *store.Store, srcID int64, relPath, name, id, kind string, size int64) {
	t.Helper()
	img := store.Image{ID: id, SourceID: srcID, RelPath: relPath, Name: name, Kind: kind, Size: size}
	if _, _, err := s.UpsertImage(img); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCopiesJPEGWithEXIF(t *testing.T) {
	s, srcDir, sid := newStore(t)
	// Source JPEG with its own orientation; the plan re-merges time+GPS.
	addFile(t, srcDir, "IMG_0001.jpg", testimg.MakeEXIFJPEG(testimg.Textured(48, 32, 7), 90, testimg.EXIF{Orientation: 6}))
	addImage(t, s, sid, "IMG_0001.jpg", "IMG_0001.jpg", "a", "jpeg", 100)

	g := store.Group{
		ID: 1, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant,
		HasGPS: true, GPSLat: 52.5204, GPSLon: 13.4055, StoredName: "IMG_0001.jpg",
	}
	if err := s.SaveGroup(g); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if rep.Copied != 1 || !rep.Files[0].EXIF {
		t.Fatalf("rep = %+v", rep)
	}

	out := filepath.Join(target, "IMG_0001.jpg")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	f, err := meta.FromJPEG(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Capture != "2024:05:01 12:30:00" || f.Offset != "+02:00" {
		t.Fatalf("capture = %q %q", f.Capture, f.Offset)
	}
	if f.Orientation != 6 {
		t.Fatalf("orientation = %d, want 6 (preserved from source)", f.Orientation)
	}
	if !f.HasGPS || f.GPSLat < 52.52 || f.GPSLon < 13.40 {
		t.Fatalf("gps = %v %v %v", f.HasGPS, f.GPSLat, f.GPSLon)
	}
}

func TestApplyWritesReportJSON(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addFile(t, srcDir, "b.jpg", testimg.JPEG(testimg.Textured(32, 24, 2), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	addImage(t, s, sid, "b.jpg", "b.jpg", "b", "jpeg", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant,
		HasGPS: true, GPSLat: 52.5204, GPSLon: 13.4055,
		StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(store.Group{ID: 2, Status: group.StatusConflict, PixelImageID: "b",
		Reason: "offsets disagree", CaptureInstant: instant, StoredName: "b.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}

	data, err := os.ReadFile(filepath.Join(target, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rj struct {
		Generated string `json:"generated"`
		Target    string `json:"target"`
		Copied    int    `json:"copied"`
		Files     []struct {
			Source string `json:"source"`
			Dest   string `json:"dest"`
			EXIF   bool   `json:"exif"`
		} `json:"files"`
		Groups []struct {
			ID          int64   `json:"id"`
			StoredName  string  `json:"stored_name"`
			Status      string  `json:"status"`
			Reason      string  `json:"reason"`
			CaptureTime string  `json:"capture_time"`
			HasGPS      bool    `json:"has_gps"`
			GPSLat      float64 `json:"gps_lat"`
			PixelSource string  `json:"pixel_source"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &rj); err != nil {
		t.Fatalf("parse report.json: %v", err)
	}
	if rj.Target != target {
		t.Fatalf("target = %q, want %q", rj.Target, target)
	}
	if rj.Copied != 2 {
		t.Fatalf("copied=%d, want 2 (conflicts materialize too)", rj.Copied)
	}
	if len(rj.Files) != 2 || !rj.Files[0].EXIF || rj.Files[1].EXIF {
		t.Fatalf("files = %+v (want merged EXIF then plain copy)", rj.Files)
	}
	if rj.Files[0].Source != filepath.Join(srcDir, "a.jpg") {
		t.Fatalf("file source = %q", rj.Files[0].Source)
	}
	if len(rj.Groups) != 2 || rj.Groups[0].ID != 1 || rj.Groups[1].ID != 2 {
		t.Fatalf("groups = %+v", rj.Groups)
	}
	g0, g1 := rj.Groups[0], rj.Groups[1]
	if g0.Status != group.StatusResolved || g1.Status != group.StatusConflict {
		t.Fatalf("statuses = %q %q", g0.Status, g1.Status)
	}
	if g0.CaptureTime != "2024:05:01 12:30:00" || !g0.HasGPS || g0.GPSLat != 52.5204 {
		t.Fatalf("resolved facts = %+v", g0)
	}
	if g0.PixelSource != filepath.Join(srcDir, "a.jpg") {
		t.Fatalf("pixel source = %q", g0.PixelSource)
	}
	if g1.Reason != "offsets disagree" {
		t.Fatalf("conflict reason = %q", g1.Reason)
	}
}

func TestApplyDryRunTouchesNothing(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusSingleton, PixelImageID: "a", CaptureInstant: instant, StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, err := Apply(s, Options{Target: target, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 1 {
		t.Fatalf("dry-run should plan 1 file, got %+v", rep)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Fatalf("dry-run must not create files: %v", entries)
	}
}

func TestApplyCopiesConflictsPlainly(t *testing.T) {
	s, srcDir, sid := newStore(t)
	src := addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusConflict, Reason: "offsets disagree", PixelImageID: "a", CaptureInstant: instant, StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 1 {
		t.Fatalf("conflict group must materialize: %+v", rep)
	}
	out := filepath.Join(target, "a.jpg")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("conflict copy missing: %v", err)
	}
	if want, _ := os.ReadFile(src); string(data) != string(want) {
		t.Fatal("conflict output must be a plain copy of the pixel source")
	}
	ledger, err := s.GroupOutputs(target)
	if err != nil {
		t.Fatal(err)
	}
	if ledger[1].Conflict != "offsets disagree" {
		t.Fatalf("ledger conflict = %q, want the plan reason", ledger[1].Conflict)
	}
}

func TestApplyNameCollisionPrefix(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addFile(t, srcDir, "b.jpg", testimg.JPEG(testimg.Textured(32, 24, 2), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	addImage(t, s, sid, "b.jpg", "b.jpg", "b", "jpeg", 100)
	// Two groups resolve to the same stored name.
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a", CaptureInstant: instant, StoredName: "IMG.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(store.Group{ID: 4, Status: group.StatusResolved, PixelImageID: "b", CaptureInstant: instant, StoredName: "IMG.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if _, err := os.Stat(filepath.Join(target, "IMG.jpg")); err != nil {
		t.Fatalf("first name missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "000004_IMG.jpg")); err != nil {
		t.Fatalf("collision must take the group-prefixed name: %v", err)
	}
}

func TestApplyNoEXIFSkipsWriting(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusSingleton, PixelImageID: "a", CaptureInstant: instant, StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, _ := Apply(s, Options{Target: target, NoEXIF: true})
	if rep.Files[0].EXIF {
		t.Fatalf("NoEXIF must not write EXIF: %+v", rep.Files[0])
	}
	// The output should have no readable EXIF capture.
	data, _ := os.ReadFile(filepath.Join(target, "a.jpg"))
	if _, err := meta.FromJPEG(data); err == nil {
		t.Fatal("expected no EXIF in the copy")
	}
}

func TestApplyPNGCopiedWithoutEXIF(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "p.png", testimg.PNG(testimg.Textured(32, 24, 1)))
	addImage(t, s, sid, "p.png", "p.png", "p", "png", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusSingleton, PixelImageID: "p", CaptureInstant: instant, StoredName: "p.png"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	rep, _ := Apply(s, Options{Target: target})
	if rep.Files[0].EXIF {
		t.Fatalf("PNG must not get EXIF: %+v", rep.Files[0])
	}
	src, _ := os.ReadFile(filepath.Join(srcDir, "p.png"))
	dst, _ := os.ReadFile(filepath.Join(target, "p.png"))
	if string(src) != string(dst) {
		t.Fatal("PNG copy must be byte-identical")
	}
}

// addImageSHA records an image with a known pixel sha256, the state a real
// scan produces and what the apply ledger needs to detect staleness.
func addImageSHA(t *testing.T, s *store.Store, srcID int64, relPath, id, kind string, data []byte) {
	t.Helper()
	sum := sha256.Sum256(data)
	img := store.Image{
		ID: id, SourceID: srcID, RelPath: relPath, Name: filepath.Base(relPath),
		Kind: kind, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
	}
	if _, _, err := s.UpsertImage(img); err != nil {
		t.Fatal(err)
	}
}

// fileNames lists the entries of dir except report.json.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != "report.json" {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestApplySecondRunUntouched(t *testing.T) {
	s, srcDir, sid := newStore(t)
	data := testimg.JPEG(testimg.Textured(32, 24, 1), 90)
	addFile(t, srcDir, "a.jpg", data)
	addImageSHA(t, s, sid, "a.jpg", "a", "jpeg", data)
	g := store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant, StoredName: "a.jpg"}
	if err := s.SaveGroup(g); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	if rep, err := Apply(s, Options{Target: target}); err != nil || rep.Copied != 1 {
		t.Fatalf("first apply: rep=%+v err=%v", rep, err)
	}

	// Re-applying the same plan must not touch the target again.
	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 0 || rep.Updated != 0 || rep.Renamed != 0 || rep.Untouched != 1 {
		t.Fatalf("second apply: rep=%+v (want 1 untouched)", rep)
	}
	if names := fileNames(t, target); len(names) != 1 || names[0] != "a.jpg" {
		t.Fatalf("target files = %v, want [a.jpg]", names)
	}

	// A changed plan (new capture time) rewrites in place.
	g.CaptureTime = "2024:05:01 13:30:00"
	g.CaptureInstant = instant + 3600
	if err := s.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if rep, err = Apply(s, Options{Target: target}); err != nil || rep.Copied != 0 || rep.Updated != 1 {
		t.Fatalf("stale facts apply: rep=%+v err=%v (want 1 updated)", rep, err)
	}
	out, err := meta.FromJPEG(mustReadFile(t, filepath.Join(target, "a.jpg")))
	if err != nil {
		t.Fatal(err)
	}
	if out.Capture != "2024:05:01 13:30:00" {
		t.Fatalf("rewritten capture = %q", out.Capture)
	}

	// A changed stored name moves the existing output, it does not copy.
	g.StoredName = "keep.jpg"
	if err := s.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if rep, err = Apply(s, Options{Target: target}); err != nil || rep.Copied != 0 || rep.Renamed != 1 {
		t.Fatalf("rename apply: rep=%+v err=%v (want 1 renamed)", rep, err)
	}
	if names := fileNames(t, target); len(names) != 1 || names[0] != "keep.jpg" {
		t.Fatalf("target files after rename = %v, want [keep.jpg]", names)
	}

	// A changed pixel source (new sha256) rewrites under the recorded name.
	sum := sha256.Sum256([]byte("different pixels"))
	if _, _, err := s.UpsertImage(store.Image{
		ID: "a", SourceID: sid, RelPath: "a.jpg", Name: "a.jpg",
		Kind: "jpeg", Size: 100, SHA256: hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatal(err)
	}
	if rep, err = Apply(s, Options{Target: target}); err != nil || rep.Copied != 0 || rep.Updated != 1 {
		t.Fatalf("stale pixel apply: rep=%+v err=%v (want 1 updated)", rep, err)
	}

	// A deleted output is recreated under the same name.
	if err := os.Remove(filepath.Join(target, "keep.jpg")); err != nil {
		t.Fatal(err)
	}
	if rep, err = Apply(s, Options{Target: target}); err != nil || rep.Updated != 1 {
		t.Fatalf("recreate apply: rep=%+v err=%v (want 1 updated)", rep, err)
	}
	if names := fileNames(t, target); len(names) != 1 || names[0] != "keep.jpg" {
		t.Fatalf("target files after recreate = %v, want [keep.jpg]", names)
	}
}

func TestApplyPrefixStableAcrossRuns(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addFile(t, srcDir, "b.jpg", testimg.JPEG(testimg.Textured(32, 24, 2), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	addImage(t, s, sid, "b.jpg", "b.jpg", "b", "jpeg", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a", CaptureInstant: instant, StoredName: "IMG.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(store.Group{ID: 4, Status: group.StatusResolved, PixelImageID: "b", CaptureInstant: instant, StoredName: "IMG.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	if rep, err := Apply(s, Options{Target: target}); err != nil || rep.Copied != 2 {
		t.Fatalf("first apply: rep=%+v err=%v", rep, err)
	}

	// Re-applying must keep the assigned 000004_ prefix: no chaining, no
	// "(1)" suffix, no duplicates.
	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 0 || rep.Renamed != 0 || rep.Untouched != 2 {
		t.Fatalf("second apply: rep=%+v (want 2 untouched)", rep)
	}
	names := fileNames(t, target)
	want := []string{"000004_IMG.jpg", "IMG.jpg"}
	if len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("target files = %v, want %v", names, want)
	}
}

func TestApplyAdoptsLegacyReport(t *testing.T) {
	s, srcDir, sid := newStore(t)
	addFile(t, srcDir, "a.jpg", testimg.JPEG(testimg.Textured(32, 24, 1), 90))
	addImage(t, s, sid, "a.jpg", "a.jpg", "a", "jpeg", 100)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant, StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}

	// A target materialized by a pre-ledger apply: the file plus a legacy
	// report.json without group ids on the file entries.
	target := t.TempDir()
	prior := []byte("prior content")
	if err := os.WriteFile(filepath.Join(target, "a.jpg"), prior, 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "a.jpg")
	legacy := map[string]any{
		"target": target,
		"files":  []map[string]any{{"source": src, "dest": filepath.Join(target, "a.jpg")}},
		"groups": []map[string]any{{"id": 1, "stored_name": "a.jpg", "status": "resolved", "pixel_source": src}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "report.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 0 || rep.Untouched != 1 {
		t.Fatalf("adopt apply: rep=%+v (want 1 untouched)", rep)
	}
	got, err := os.ReadFile(filepath.Join(target, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(prior) {
		t.Fatal("adopted file was rewritten")
	}

	// The adopted row is now in the ledger: a plan rename moves the file
	// in place instead of writing a duplicate.
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant, StoredName: "renamed.jpg"}); err != nil {
		t.Fatal(err)
	}
	if rep, err = Apply(s, Options{Target: target}); err != nil || rep.Renamed != 1 || rep.Copied != 0 {
		t.Fatalf("rename after adopt: rep=%+v err=%v (want 1 renamed)", rep, err)
	}
	names := fileNames(t, target)
	if len(names) != 1 || names[0] != "renamed.jpg" {
		t.Fatalf("target files after rename = %v, want [renamed.jpg]", names)
	}
}

func TestApplyLedgerSurvivesRenumber(t *testing.T) {
	s, srcDir, sid := newStore(t)
	data := testimg.JPEG(testimg.Textured(32, 24, 1), 90)
	addFile(t, srcDir, "a.jpg", data)
	addImageSHA(t, s, sid, "a.jpg", "a", "jpeg", data)
	if err := s.SaveGroup(store.Group{ID: 1, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant, StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	if rep, err := Apply(s, Options{Target: target}); err != nil || rep.Copied != 1 {
		t.Fatalf("first apply: rep=%+v err=%v", rep, err)
	}

	// A full re-group renumbers: group 1 became group 7 while keeping its
	// pixel image. The ledger row must be picked up, not duplicated.
	key := targetKey(target)
	rows, err := s.GroupOutputs(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroupOutput(key, 7, rows[1]); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroupOutput(key, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearGroups(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(store.Group{ID: 7, Status: group.StatusResolved, PixelImageID: "a",
		CaptureTime: "2024:05:01 12:30:00", CaptureInstant: instant, StoredName: "a.jpg"}); err != nil {
		t.Fatal(err)
	}

	rep, err := Apply(s, Options{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 0 || rep.Untouched != 1 {
		t.Fatalf("renumber apply: rep=%+v (want 1 untouched)", rep)
	}
	if names := fileNames(t, target); len(names) != 1 || names[0] != "a.jpg" {
		t.Fatalf("target files = %v, want [a.jpg]", names)
	}
	rows, err = s.GroupOutputs(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[7]; !ok {
		t.Fatalf("ledger not re-keyed to group 7: %v", rows)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
