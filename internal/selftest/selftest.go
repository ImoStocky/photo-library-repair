// Package selftest runs built-in parity checks over the full pipeline
// (scan -> group -> plan -> apply) on synthetic images, so a fresh checkout
// can be verified without real photos or external tools.
package selftest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"photo-library-repair/internal/apply"
	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/ingest"
	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/store"
	"photo-library-repair/internal/testimg"
)

// maxHamming is the grouping radius the checks use. It is wider than the CLI
// default (12) so quality re-encodings of the same picture always chain,
// which is what the checks want to exercise.
const maxHamming = 16

// conflictHamming is the near-identical conflict limit the checks use; it
// matches the CLI default for --conflict-hamming.
const conflictHamming = 2

// Check is the outcome of one built-in check.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Run executes every built-in check and returns their outcomes.
func Run() []Check {
	return []Check{
		checkScanAndGroup(),
		checkCaptureRules(),
		checkConflicts(),
		checkApply(),
		checkStoreUpsert(),
		checkEXIFEngine(),
		checkCommitRollback(),
	}
}

// env is one self-contained pipeline instance: a temp source directory and
// a throwaway in-memory library. The scan registers the source itself.
type env struct {
	dir string
	s   *store.Store
}

func newEnv() (*env, error) {
	dir, err := os.MkdirTemp("", "plr-selftest-")
	if err != nil {
		return nil, err
	}
	s, err := store.Open(":memory:")
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &env{dir: dir, s: s}, nil
}

func (e *env) close() {
	e.s.Close()
	os.RemoveAll(e.dir)
}

func (e *env) write(rel string, data []byte) error {
	return os.WriteFile(filepath.Join(e.dir, rel), data, 0o644)
}

// scan runs the ingest phase over the temp directory.
func (e *env) scan() (ingest.Report, error) {
	rep, err := ingest.Scan(e.s, ingest.Options{Dirs: []string{e.dir}, Workers: 2}, fingerprint.Compute)
	if err != nil {
		return ingest.Report{}, err
	}
	return *rep, nil
}

// scanForce re-scans the temp directory with Force set: the fingerprint
// cache must reuse the stored rows for unchanged files.
func (e *env) scanForce() (ingest.Report, error) {
	rep, err := ingest.Scan(e.s, ingest.Options{Dirs: []string{e.dir}, Force: true, Workers: 2}, fingerprint.Compute)
	if err != nil {
		return ingest.Report{}, err
	}
	return *rep, nil
}

// groupPlan runs the group and plan phases. It returns the stored plans and
// the member file names per plan id. (The store's group_id column is
// transient: the plan phase's ClearGroups resets it, so membership must be
// tracked from the assignment.)
func (e *env) groupPlan() (plans []store.Group, members map[int64][]string, err error) {
	imgs, err := e.s.AllImages()
	if err != nil {
		return nil, nil, err
	}
	gimgs := make([]group.Image, len(imgs))
	for i, im := range imgs {
		gimgs[i] = group.Image{
			ID:          im.ID,
			Name:        im.Name,
			SHA256:      im.SHA256,
			Phash:       im.Phash,
			CreatedAt:   im.CreatedAt,
			ExifCapture: im.ExifCapture,
			ExifOffset:  im.ExifOffset,
			Width:       im.Width,
			Height:      im.Height,
			Size:        im.Size,
			HasGPS:      im.HasGPS,
			GPSLat:      im.GPSLat,
			GPSLon:      im.GPSLon,
		}
	}
	assign, _ := group.Aggregate(gimgs, maxHamming)
	if err := e.s.SetGroupAssignments(assign); err != nil {
		return nil, nil, err
	}
	if err := e.s.ClearGroups(); err != nil {
		return nil, nil, err
	}
	members = map[int64][]string{}
	for _, im := range gimgs {
		members[assign[im.ID]] = append(members[assign[im.ID]], im.Name)
	}
	for id := range members {
		planMembers := make([]group.Image, 0)
		for _, im := range gimgs {
			if assign[im.ID] == id {
				planMembers = append(planMembers, im)
			}
		}
		p := group.ResolveGroup(planMembers, nil, conflictHamming)
		g := store.Group{
			ID:              id,
			Status:          p.Status,
			Reason:          p.Reason,
			PixelImageID:    p.PixelImageID,
			PixelPhash:      p.PixelPhash,
			CaptureImageID:  p.CaptureImageID,
			CaptureTime:     p.CaptureTime,
			CaptureInstant:  p.CaptureInstant,
			CaptureFallback: p.CaptureFallback,
			HasGPS:          p.HasGPS,
			GPSLat:          p.GPSLat,
			GPSLon:          p.GPSLon,
			StoredName:      p.StoredName,
		}
		if err := e.s.SaveGroup(g); err != nil {
			return nil, nil, err
		}
		plans = append(plans, g)
	}
	return plans, members, nil
}

// planFor returns the plan whose member list contains a file named name.
func planFor(plans []store.Group, members map[int64][]string, name string) (store.Group, bool) {
	for _, p := range plans {
		if slices.Contains(members[p.ID], name) {
			return p, true
		}
	}
	return store.Group{}, false
}

// exifJPEG builds one member of a near-duplicate pair: the same 400x300
// picture at quality 95 (hi) or 90 (lo). Same-seed pairs stay within 12
// phash bits, far under maxHamming, while different seeds in one env are at
// least 26 bits apart and the inverted picture is over 100 bits away, so
// unrelated pictures never chain.
func exifJPEG(seed, quality int, capture, offset string, gps bool) []byte {
	ex := testimg.EXIF{
		DateTimeOriginal:   capture,
		OffsetTimeOriginal: offset,
		GPS:                gps,
	}
	return testimg.MakeEXIFJPEG(testimg.Textured(400, 300, seed), quality, ex)
}

func plainJPEG(seed, quality int) []byte {
	return testimg.JPEG(testimg.Textured(400, 300, seed), quality)
}

// inverted returns a JPEG of the negative of the picture in data.
func inverted(data []byte) []byte {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		panic(err) // never happens for selftest input
	}
	bounds := img.Bounds()
	out := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(255 - uint16(r)>>8),
				G: uint8(255 - uint16(g)>>8),
				B: uint8(255 - uint16(b)>>8),
				A: uint8(a >> 8),
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 95}); err != nil {
		panic(err) // never happens for in-memory images
	}
	return buf.Bytes()
}

// checkScanAndGroup verifies that the scan records real images, counts a
// file that only pretends to be a JPEG as non-image, that the group phase
// chains the exact copy and the quality re-encoding into one group while an
// unrelated picture stays apart, and that a forced re-scan of unchanged
// files reuses the stored fingerprints.
func checkScanAndGroup() Check {
	const name = "scan: records images, counts fakes, groups exact and near copies, caches unchanged re-scans"
	e, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer e.close()

	base := exifJPEG(1, 95, "2024:05:01 12:30:00", "+02:00", true)
	files := map[string][]byte{
		"IMG_0001.jpg":       base,
		"copy.jpg":           base, // byte-identical duplicate under a different name
		"IMG_0001_reenc.jpg": exifJPEG(1, 90, "2024:05:01 12:30:00", "+02:00", true),
		"other.jpg":          inverted(base),
		"fake.jpg":           []byte("this is not a JPEG"),
	}
	for rel, data := range files {
		if err := e.write(rel, data); err != nil {
			return Check{Name: name, Detail: fmt.Sprintf("write %s: %v", rel, err)}
		}
	}

	rep, err := e.scan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("scan: %v", err)}
	}
	if rep.FilesSeen != 4 || rep.ImagesAdded != 4 || rep.NonImage != 1 || rep.Errors != 0 {
		return Check{Name: name, Detail: fmt.Sprintf(
			"scan report: seen=%d added=%d non-image=%d errors=%d (want 4/4/1/0)",
			rep.FilesSeen, rep.ImagesAdded, rep.NonImage, rep.Errors)}
	}

	// Forced re-scan of the untouched directory: every file is seen again,
	// none is re-hashed (fingerprint cache).
	rep2, err := e.scanForce()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("rescan: %v", err)}
	}
	if rep2.FilesSeen != 4 || rep2.ImagesCached != 4 || rep2.ImagesAdded != 0 {
		return Check{Name: name, Detail: fmt.Sprintf(
			"rescan report: seen=%d cached=%d added=%d (want 4/4/0)",
			rep2.FilesSeen, rep2.ImagesCached, rep2.ImagesAdded)}
	}

	plans, members, err := e.groupPlan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("group/plan: %v", err)}
	}
	if len(plans) != 2 {
		return Check{Name: name, Detail: fmt.Sprintf("%d groups (want 2)", len(plans))}
	}
	bigPlan, ok := planFor(plans, members, "IMG_0001.jpg")
	if !ok {
		return Check{Name: name, Detail: "no plan for IMG_0001.jpg"}
	}
	smallPlan, ok := planFor(plans, members, "other.jpg")
	if !ok {
		return Check{Name: name, Detail: "no plan for other.jpg"}
	}
	if len(members[bigPlan.ID]) != 3 {
		return Check{Name: name, Detail: fmt.Sprintf("IMG_0001.jpg group has %d members (want 3)", len(members[bigPlan.ID]))}
	}
	if len(members[smallPlan.ID]) != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("other.jpg group has %d members (want 1)", len(members[smallPlan.ID]))}
	}
	if smallPlan.Status != group.StatusSingleton {
		return Check{Name: name, Detail: fmt.Sprintf("other.jpg plan status %q (want %q)", smallPlan.Status, group.StatusSingleton)}
	}
	return Check{Name: name, OK: true}
}

// checkCaptureRules verifies that a group with agreeing EXIF keeps the
// earliest capture time and the GPS position, and that a group without EXIF
// falls back to the earliest file creation time.
func checkCaptureRules() Check {
	const name = "plan: keeps earliest EXIF capture and GPS, falls back without EXIF"
	e, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer e.close()

	files := map[string][]byte{
		"IMG_E1.jpg": exifJPEG(4, 95, "2024:05:01 12:30:00", "+02:00", true),
		"IMG_E2.jpg": exifJPEG(4, 90, "2024:05:01 12:35:00", "+02:00", true),
		"IMG_F1.jpg": plainJPEG(5, 95),
		"IMG_F2.jpg": plainJPEG(5, 90),
	}
	for rel, data := range files {
		if err := e.write(rel, data); err != nil {
			return Check{Name: name, Detail: fmt.Sprintf("write %s: %v", rel, err)}
		}
	}
	if _, err := e.scan(); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("scan: %v", err)}
	}
	plans, members, err := e.groupPlan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("group/plan: %v", err)}
	}
	if len(plans) != 2 {
		return Check{Name: name, Detail: fmt.Sprintf("%d groups (want 2)", len(plans))}
	}

	exifPlan, ok := planFor(plans, members, "IMG_E1.jpg")
	if !ok {
		return Check{Name: name, Detail: "no plan for IMG_E1.jpg"}
	}
	if exifPlan.Status != group.StatusResolved {
		return Check{Name: name, Detail: fmt.Sprintf("EXIF group status %q (want %q): %s", exifPlan.Status, group.StatusResolved, exifPlan.Reason)}
	}
	if exifPlan.CaptureTime != "2024:05:01 12:30:00" {
		return Check{Name: name, Detail: fmt.Sprintf("kept capture %q (want 2024:05:01 12:30:00)", exifPlan.CaptureTime)}
	}
	if exifPlan.CaptureFallback {
		return Check{Name: name, Detail: "EXIF group marked as fallback (want false)"}
	}
	if !exifPlan.HasGPS || math.Abs(exifPlan.GPSLat-52.5204) > 1e-3 || math.Abs(exifPlan.GPSLon-13.4055) > 1e-3 {
		return Check{Name: name, Detail: fmt.Sprintf("GPS %v %v,%v (want 52.5204, 13.4055)", exifPlan.HasGPS, exifPlan.GPSLat, exifPlan.GPSLon)}
	}

	fallbackPlan, ok := planFor(plans, members, "IMG_F1.jpg")
	if !ok {
		return Check{Name: name, Detail: "no plan for IMG_F1.jpg"}
	}
	if fallbackPlan.Status != group.StatusResolved || !fallbackPlan.CaptureFallback {
		return Check{Name: name, Detail: fmt.Sprintf("no-EXIF group status %q fallback %v (want resolved/true)", fallbackPlan.Status, fallbackPlan.CaptureFallback)}
	}
	if fallbackPlan.CaptureInstant <= 0 {
		return Check{Name: name, Detail: "fallback capture instant is zero"}
	}
	return Check{Name: name, OK: true}
}

// checkConflicts verifies that disagreements the automatic rules cannot
// settle (differing timezone offsets, one member with GPS and one without,
// near-identical pictures with identical capture times) are surfaced as
// conflicts with a reason.
func checkConflicts() Check {
	const name = "plan: flags offset, GPS and near-identical pairs as conflicts"
	e, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer e.close()

	files := map[string][]byte{
		"IMG_C1.jpg": exifJPEG(4, 95, "2024:05:01 12:30:00", "+02:00", false),
		"IMG_C2.jpg": exifJPEG(4, 90, "2024:05:01 12:30:00", "+05:00", false),
		"IMG_D1.jpg": exifJPEG(5, 95, "2024:05:01 12:30:00", "+02:00", true),
		"IMG_D2.jpg": exifJPEG(5, 90, "2024:05:01 12:30:00", "+02:00", false),
		"IMG_E1.jpg": exifJPEG(6, 95, "2024:05:01 12:30:00", "+02:00", true),
		"IMG_E2.jpg": exifJPEG(6, 95, "2024:05:01 12:30:00", "+02:00", true), // byte-identical, same capture
	}
	for rel, data := range files {
		if err := e.write(rel, data); err != nil {
			return Check{Name: name, Detail: fmt.Sprintf("write %s: %v", rel, err)}
		}
	}
	if _, err := e.scan(); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("scan: %v", err)}
	}
	plans, members, err := e.groupPlan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("group/plan: %v", err)}
	}
	if len(plans) != 3 {
		return Check{Name: name, Detail: fmt.Sprintf("%d groups (want 3)", len(plans))}
	}

	offPlan, ok := planFor(plans, members, "IMG_C1.jpg") // offset group
	if !ok || offPlan.Status != group.StatusConflict {
		return Check{Name: name, Detail: fmt.Sprintf("offset group: status %q reason %q (want conflict)", offPlan.Status, offPlan.Reason)}
	}
	if offPlan.Reason == "" {
		return Check{Name: name, Detail: "offset conflict has no reason"}
	}
	gpsPlan, ok := planFor(plans, members, "IMG_D1.jpg")
	if !ok || gpsPlan.Status != group.StatusConflict {
		return Check{Name: name, Detail: fmt.Sprintf("GPS group: status %q reason %q (want conflict)", gpsPlan.Status, gpsPlan.Reason)}
	}
	nearPlan, ok := planFor(plans, members, "IMG_E1.jpg")
	if !ok || nearPlan.Status != group.StatusConflict {
		return Check{Name: name, Detail: fmt.Sprintf("near-identical group: status %q reason %q (want conflict)", nearPlan.Status, nearPlan.Reason)}
	}
	if !strings.Contains(nearPlan.Reason, "near-identical") {
		return Check{Name: name, Detail: fmt.Sprintf("near-identical reason %q", nearPlan.Reason)}
	}
	return Check{Name: name, OK: true}
}

// checkApply verifies that the write-out phase copies the chosen pixels with
// merged EXIF, leaves conflict groups alone, honors --dry-run, applies
// conflicts when forced, and renames stored names that already exist.
func checkApply() Check {
	const name = "apply: writes merged EXIF + report.json, skips conflicts, honors dry-run, moves sources, renames collisions"
	e, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer e.close()

	files := map[string][]byte{
		"IMG_A1.jpg": exifJPEG(4, 95, "2024:05:01 12:30:00", "+02:00", true),
		"IMG_A2.jpg": exifJPEG(4, 90, "2024:05:01 12:35:00", "+02:00", true),
		"IMG_B1.jpg": exifJPEG(5, 95, "2024:05:01 12:30:00", "+02:00", false),
		"IMG_B2.jpg": exifJPEG(5, 90, "2024:05:01 12:30:00", "+05:00", false),
	}
	for rel, data := range files {
		if err := e.write(rel, data); err != nil {
			return Check{Name: name, Detail: fmt.Sprintf("write %s: %v", rel, err)}
		}
	}
	if _, err := e.scan(); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("scan: %v", err)}
	}
	plans, members, err := e.groupPlan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("group/plan: %v", err)}
	}
	resolved, ok := planFor(plans, members, "IMG_A1.jpg")
	if !ok || resolved.Status != group.StatusResolved {
		return Check{Name: name, Detail: fmt.Sprintf("setup: resolved group missing (status %q)", resolved.Status)}
	}

	target, err := os.MkdirTemp("", "plr-selftest-target-")
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer os.RemoveAll(target)

	rep, err := apply.Apply(e.s, apply.Options{Target: target})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("apply: %v", err)}
	}
	if len(rep.Errors) != 0 {
		return Check{Name: name, Detail: fmt.Sprintf("apply errors: %v", rep.Errors)}
	}
	if rep.Copied != 1 || rep.Skipped != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("copied=%d skipped=%d (want 1/1)", rep.Copied, rep.Skipped)}
	}
	out := filepath.Join(target, resolved.StoredName)
	facts, err := meta.FromJPEG(mustRead(out))
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("read output %s: %v", out, err)}
	}
	if facts.Capture != "2024:05:01 12:30:00" || facts.Offset != "+02:00" || !facts.HasGPS {
		return Check{Name: name, Detail: fmt.Sprintf("merged EXIF %q %q gps=%v (want 2024:05:01 12:30:00 +02:00 gps)",
			facts.Capture, facts.Offset, facts.HasGPS)}
	}

	data, err := os.ReadFile(filepath.Join(target, "report.json"))
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("report.json missing: %v", err)}
	}
	var rj struct {
		Generated string `json:"generated"`
		Copied    int    `json:"copied"`
		Skipped   int    `json:"skipped"`
		Files     []struct {
			Source string `json:"source"`
			Dest   string `json:"dest"`
			EXIF   bool   `json:"exif"`
		} `json:"files"`
		Groups []struct {
			Status string `json:"status"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &rj); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("parse report.json: %v", err)}
	}
	if rj.Copied != 1 || rj.Skipped != 1 || len(rj.Groups) != 2 {
		return Check{Name: name, Detail: fmt.Sprintf("report.json copied=%d skipped=%d groups=%d (want 1/1/2)",
			rj.Copied, rj.Skipped, len(rj.Groups))}
	}
	if len(rj.Files) != 1 || !rj.Files[0].EXIF || rj.Files[0].Dest != out {
		return Check{Name: name, Detail: fmt.Sprintf("report.json files = %+v", rj.Files)}
	}

	dryTarget := filepath.Join(target, "dry") // must not be created
	rep, err = apply.Apply(e.s, apply.Options{Target: dryTarget, DryRun: true})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("dry-run: %v", err)}
	}
	if _, err := os.Stat(dryTarget); !os.IsNotExist(err) {
		return Check{Name: name, Detail: fmt.Sprintf("dry-run created target directory %s", dryTarget)}
	}
	if rep.Copied != 1 || rep.Skipped != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("dry-run copied=%d skipped=%d (want 1/1)", rep.Copied, rep.Skipped)}
	}

	forceTarget := filepath.Join(target, "forced")
	rep, err = apply.Apply(e.s, apply.Options{Target: forceTarget, ForceConflicts: true})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("force: %v", err)}
	}
	if len(rep.Errors) != 0 {
		return Check{Name: name, Detail: fmt.Sprintf("force errors: %v", rep.Errors)}
	}
	if rep.Copied != 2 || rep.Skipped != 0 {
		return Check{Name: name, Detail: fmt.Sprintf("force copied=%d skipped=%d (want 2/0)", rep.Copied, rep.Skipped)}
	}

	collideTarget := filepath.Join(target, "collide")
	if err := os.MkdirAll(collideTarget, 0o755); err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	if err := os.WriteFile(filepath.Join(collideTarget, resolved.StoredName), []byte("occupied"), 0o644); err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	rep, err = apply.Apply(e.s, apply.Options{Target: collideTarget})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("collision: %v", err)}
	}
	want := fmt.Sprintf("%06d_%s", resolved.ID, resolved.StoredName)
	if rep.Renamed != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("renamed=%d (want 1)", rep.Renamed)}
	}
	if _, err := os.Stat(filepath.Join(collideTarget, want)); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("renamed output missing: %v", err)}
	}

	moveTarget := filepath.Join(target, "moved")
	rep, err = apply.Apply(e.s, apply.Options{Target: moveTarget, Move: true})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("move: %v", err)}
	}
	if len(rep.Errors) != 0 {
		return Check{Name: name, Detail: fmt.Sprintf("move errors: %v", rep.Errors)}
	}
	if rep.Copied != 1 || rep.Moved != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("move copied=%d moved=%d (want 1/1)", rep.Copied, rep.Moved)}
	}
	if _, err := os.Stat(filepath.Join(moveTarget, resolved.StoredName)); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("moved output missing: %v", err)}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "IMG_A1.jpg")); !os.IsNotExist(err) {
		return Check{Name: name, Detail: "move mode kept the source file"}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "IMG_B1.jpg")); err != nil {
		return Check{Name: name, Detail: "move mode touched a skipped conflict group"}
	}

	moveDryTarget := filepath.Join(target, "moved-dry")
	rep, err = apply.Apply(e.s, apply.Options{Target: moveDryTarget, DryRun: true, Move: true})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("dry-run move: %v", err)}
	}
	if rep.Copied != 1 || rep.Moved != 0 {
		return Check{Name: name, Detail: fmt.Sprintf("dry-run move copied=%d moved=%d (want 1/0)", rep.Copied, rep.Moved)}
	}
	if _, err := os.Stat(moveDryTarget); !os.IsNotExist(err) {
		return Check{Name: name, Detail: "dry-run move created the target directory"}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "IMG_A2.jpg")); err != nil {
		return Check{Name: name, Detail: "dry-run move deleted a source file"}
	}

	// Moving into the source directory: the existing file occupies its own
	// name, so the copy is renamed in place and the original removed — no
	// data loss. (The sameFile guard keeps a true dest == src rewrite safe
	// should the collision logic ever change.)
	same, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer same.close()
	if err := same.write("solo.jpg", exifJPEG(6, 95, "2024:05:01 12:30:00", "+02:00", true)); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("write solo: %v", err)}
	}
	if _, err := same.scan(); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("same scan: %v", err)}
	}
	samePlans, _, err := same.groupPlan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("same group/plan: %v", err)}
	}
	if len(samePlans) != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("want 1 group, got %d", len(samePlans))}
	}
	rep, err = apply.Apply(same.s, apply.Options{Target: same.dir, Move: true})
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("same-file move: %v", err)}
	}
	if len(rep.Errors) != 0 {
		return Check{Name: name, Detail: fmt.Sprintf("same-file move errors: %v", rep.Errors)}
	}
	if rep.Copied != 1 || rep.Moved != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("same-file move copied=%d moved=%d (want 1/1)", rep.Copied, rep.Moved)}
	}
	wantSame := fmt.Sprintf("%06d_solo.jpg", samePlans[0].ID)
	if _, err := os.Stat(filepath.Join(same.dir, wantSame)); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("in-place renamed copy missing: %v", err)}
	}
	if _, err := os.Stat(filepath.Join(same.dir, "solo.jpg")); !os.IsNotExist(err) {
		return Check{Name: name, Detail: "original source not removed in same-dir move"}
	}
	return Check{Name: name, OK: true}
}

func mustRead(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err) // the file was created moments before
	}
	return data
}

// checkCommitRollback verifies the commit model: each commit snapshots the
// plan table together with a per-group diff, and a rollback restores an
// earlier snapshot (recorded as a new commit) while scanned images stay
// untouched.
func checkCommitRollback() Check {
	const name = "store: commit log records plan diffs, rollback restores an earlier commit"
	e, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer e.close()

	files := map[string][]byte{
		"IMG_A1.jpg": exifJPEG(4, 95, "2024:05:01 12:30:00", "+02:00", true),
		"IMG_A2.jpg": exifJPEG(4, 90, "2024:05:01 12:35:00", "+02:00", true),
		"IMG_B1.jpg": exifJPEG(5, 95, "2024:05:01 12:30:00", "+02:00", false),
		"IMG_B2.jpg": exifJPEG(5, 90, "2024:05:01 12:30:00", "+05:00", false),
	}
	for rel, data := range files {
		if err := e.write(rel, data); err != nil {
			return Check{Name: name, Detail: fmt.Sprintf("write %s: %v", rel, err)}
		}
	}
	if _, err := e.scan(); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("scan: %v", err)}
	}
	plans, _, err := e.groupPlan()
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("group/plan: %v", err)}
	}
	if len(plans) != 2 {
		return Check{Name: name, Detail: fmt.Sprintf("%d groups (want 2)", len(plans))}
	}
	baseline := map[int64]store.Group{}
	for _, p := range plans {
		baseline[p.ID] = p
	}

	// First commit: both groups are new.
	res, err := e.s.Commit("baseline")
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("commit: %v", err)}
	}
	if res.Commit.ID != 1 || len(res.Actions) != 2 {
		return Check{Name: name, Detail: fmt.Sprintf("first commit = %+v (%d actions, want 2 new)", res.Commit, len(res.Actions))}
	}

	// Mutate the conflict group and commit: exactly one updated action.
	for _, p := range plans {
		if p.Status == group.StatusConflict {
			p.Status = group.StatusResolved
			if err := e.s.SaveGroup(p); err != nil {
				return Check{Name: name, Detail: err.Error()}
			}
		}
	}
	res, err = e.s.Commit("settle")
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("commit: %v", err)}
	}
	if res.Commit.ID != 2 || len(res.Actions) != 1 || res.Actions[0].Action != store.ActionUpdated {
		return Check{Name: name, Detail: fmt.Sprintf("second commit = %+v actions %+v (want one updated)", res.Commit, res.Actions)}
	}

	// Roll back to the baseline: the restore is recorded as commit 3 and the
	// plans are back to the baseline rows.
	res, err = e.s.Rollback(1)
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("rollback: %v", err)}
	}
	if res.Commit.ID != 3 || res.Commit.Summary != "rollback to commit 1" {
		return Check{Name: name, Detail: fmt.Sprintf("rollback commit = %+v", res.Commit)}
	}
	groups, err := e.s.AllGroups()
	if err != nil || len(groups) != 2 {
		return Check{Name: name, Detail: fmt.Sprintf("groups after rollback = (%d, %v), want the 2 baseline plans", len(groups), err)}
	}
	for _, g := range groups {
		if want, ok := baseline[g.ID]; !ok || g != want {
			return Check{Name: name, Detail: fmt.Sprintf("group %d after rollback = %+v (want %+v)", g.ID, g, want)}
		}
	}
	// Scanned images are untouched by the rollback.
	images, err := e.s.AllImages()
	if err != nil || len(images) != 4 {
		return Check{Name: name, Detail: fmt.Sprintf("images after rollback = (%d, %v), want 4", len(images), err)}
	}
	commits, err := e.s.Commits()
	if err != nil || len(commits) != 3 {
		return Check{Name: name, Detail: fmt.Sprintf("commits = (%d, %v), want 3", len(commits), err)}
	}
	return Check{Name: name, OK: true}
}

// checkStoreUpsert verifies that re-scanning the same file keeps the image
// id and does not duplicate the row.
func checkStoreUpsert() Check {
	const name = "store: upsert keeps id, group assignment and row count"
	e, err := newEnv()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer e.close()

	if err := e.write("a.jpg", plainJPEG(11, 95)); err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	if _, err := e.scan(); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("scan: %v", err)}
	}
	images, err := e.s.AllImages()
	if err != nil || len(images) != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("first scan: %d images, err %v (want 1)", len(images), err)}
	}
	orig := images[0]

	if _, err := e.scan(); err != nil { // non-forced re-scan skips the directory
		return Check{Name: name, Detail: fmt.Sprintf("re-scan: %v", err)}
	}
	if _, _, err := e.s.UpsertImage(orig); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("upsert: %v", err)}
	}
	images, err = e.s.AllImages()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	if len(images) != 1 {
		return Check{Name: name, Detail: fmt.Sprintf("row count %d (want 1)", len(images))}
	}
	if images[0].ID != orig.ID {
		return Check{Name: name, Detail: fmt.Sprintf("id changed from %s to %s", orig.ID, images[0].ID)}
	}
	return Check{Name: name, OK: true}
}

// checkEXIFEngine verifies that the full-data EXIF engine reads every tag
// of a file, writes merged facts to a new file, and preserves the tags the
// pipeline does not use. It mirrors the apply flow: the source is never
// rewritten in place.
func checkEXIFEngine() Check {
	const name = "exif engine: full read, merged write, preserves other tags"
	tool, err := exif.New()
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer tool.Close()

	dir, err := os.MkdirTemp("", "plr-selftest-exif-")
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "rich.jpg")
	if err := os.WriteFile(src, exifJPEG(7, 95, "2024:05:01 12:30:00", "+02:00", true), 0o644); err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	f, err := tool.Facts(src)
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("read: %v", err)}
	}
	if f.Capture != "2024:05:01 12:30:00" || f.Offset != "+02:00" {
		return Check{Name: name, Detail: fmt.Sprintf("facts %q %q (want 2024:05:01 12:30:00 +02:00)", f.Capture, f.Offset)}
	}
	if !f.HasGPS || math.Abs(f.GPSLat-52.5204) > 1e-3 || math.Abs(f.GPSLon-13.4055) > 1e-3 {
		return Check{Name: name, Detail: fmt.Sprintf("gps %v %v,%v (want 52.5204, 13.4055)", f.HasGPS, f.GPSLat, f.GPSLon)}
	}
	if f.Tags < 10 {
		return Check{Name: name, Detail: fmt.Sprintf("full read saw %d tags (want at least 10)", f.Tags)}
	}

	// A copy that carries extra tags the pipeline does not know.
	rich := filepath.Join(dir, "rich-tagged.jpg")
	extra := map[string]any{
		"Title":            "Selftest picture",
		"Author":           "Selftest",
		"Copyright":        "(c) plr",
		"ImageDescription": "kept on write",
	}
	if err := tool.SetTags(src, rich, extra); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("tag copy: %v", err)}
	}
	m, err := tool.Read(rich)
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("read copy: %v", err)}
	}
	for k, want := range extra {
		if got := fmt.Sprint(m[k]); got != fmt.Sprint(want) {
			return Check{Name: name, Detail: fmt.Sprintf("tag %s = %q after copy (want %q)", k, got, want)}
		}
	}

	// The merged write: new capture facts, GPS from the read, a fresh file.
	out := filepath.Join(dir, "merged.jpg")
	if err := tool.WriteFacts(rich, out, exif.Facts{
		Capture: "2024:05:01 13:45:00",
		Offset:  "+03:00",
		HasGPS:  true,
		GPSLat:  f.GPSLat,
		GPSLon:  f.GPSLon,
	}); err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("write: %v", err)}
	}
	f2, err := tool.Facts(out)
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("re-read: %v", err)}
	}
	if f2.Capture != "2024:05:01 13:45:00" || f2.Offset != "+03:00" {
		return Check{Name: name, Detail: fmt.Sprintf("merged facts %q %q (want 2024:05:01 13:45:00 +03:00)", f2.Capture, f2.Offset)}
	}
	if !f2.HasGPS || math.Abs(f2.GPSLat-f.GPSLat) > 1e-3 || math.Abs(f2.GPSLon-f.GPSLon) > 1e-3 {
		return Check{Name: name, Detail: fmt.Sprintf("merged gps %v %v,%v (want %v,%v)", f2.HasGPS, f2.GPSLat, f2.GPSLon, f.GPSLat, f.GPSLon)}
	}
	m, err = tool.Read(out)
	if err != nil {
		return Check{Name: name, Detail: fmt.Sprintf("re-read tags: %v", err)}
	}
	for k, want := range extra {
		if got := fmt.Sprint(m[k]); got != fmt.Sprint(want) {
			return Check{Name: name, Detail: fmt.Sprintf("tag %s = %q after merged write (want %q)", k, got, want)}
		}
	}
	return Check{Name: name, OK: true}
}
