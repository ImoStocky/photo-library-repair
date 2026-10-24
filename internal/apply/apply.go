// Package apply copies (or moves, with Options.Move) the planned pixel
// files of every group into a target directory and writes the merged EXIF
// (capture time, offset, GPS) onto the copies.
//
// It is the write-out half of the pipeline: scan/group/plan decide what the
// repaired library looks like, apply materializes it. Conflict groups are
// skipped unless Options.ForceConflicts is set, in which case their
// best-effort plan is applied.
package apply

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/exifwrite"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/store"
)

// Options controls an apply run.
type Options struct {
	Target         string     // directory that receives the final images
	DryRun         bool       // report what would be done without touching the target
	NoEXIF         bool       // copy files without writing merged EXIF
	ForceConflicts bool       // apply conflict groups using their best-effort plan
	Move           bool       // delete the source file once its copy is in place
	EXIF           *exif.Tool // full-data engine; nil uses the built-in writer
}

// FileResult describes one file the run copies (or would copy).
type FileResult struct {
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Renamed bool   `json:"renamed,omitempty"` // the stored name collided and was adjusted
	EXIF    bool   `json:"exif,omitempty"`    // merged EXIF will be (was) written
	Moved   bool   `json:"moved,omitempty"`   // move mode: the source file was deleted
}

// Report summarizes an apply run.
type Report struct {
	DryRun  bool
	Files   []FileResult
	Copied  int
	Moved   int // move mode: files whose source was deleted
	Renamed int
	Skipped int // conflict groups left alone because not forced
	Errors  []string
}

// groupJSON is one plan entry in report.json.
type groupJSON struct {
	ID              int64   `json:"id"`
	StoredName      string  `json:"stored_name"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason,omitempty"`
	CaptureTime     string  `json:"capture_time,omitempty"`
	CaptureFallback bool    `json:"capture_fallback,omitempty"`
	HasGPS          bool    `json:"has_gps,omitempty"`
	GPSLat          float64 `json:"gps_lat,omitzero"`
	GPSLon          float64 `json:"gps_lon,omitzero"`
	PixelSource     string  `json:"pixel_source,omitempty"`
}

// reportJSON is the machine-readable summary written into the target
// directory as report.json after a non-dry-run apply.
type reportJSON struct {
	Generated string       `json:"generated"`
	Target    string       `json:"target"`
	Copied    int          `json:"copied"`
	Moved     int          `json:"moved,omitempty"`
	Renamed   int          `json:"renamed,omitempty"`
	Skipped   int          `json:"skipped,omitempty"`
	Files     []FileResult `json:"files"`
	Groups    []groupJSON  `json:"groups"`
	Errors    []string     `json:"errors,omitempty"`
}

// writeReportJSON writes report.json into target summarizing the run and
// every group's plan (including skipped conflicts).
func writeReportJSON(target string, rep Report, groups []store.Group, byID map[string]store.Image, sources map[int64]string) error {
	rj := reportJSON{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Target:    target,
		Copied:    rep.Copied,
		Moved:     rep.Moved,
		Renamed:   rep.Renamed,
		Skipped:   rep.Skipped,
		Files:     rep.Files,
		Groups:    make([]groupJSON, 0, len(groups)),
		Errors:    rep.Errors,
	}
	ordered := slices.Clone(groups)
	slices.SortFunc(ordered, func(a, b store.Group) int { return cmp.Compare(a.ID, b.ID) })
	for _, g := range ordered {
		ej := groupJSON{
			ID:              g.ID,
			StoredName:      g.StoredName,
			Status:          g.Status,
			Reason:          g.Reason,
			CaptureTime:     g.CaptureTime,
			CaptureFallback: g.CaptureFallback,
			HasGPS:          g.HasGPS,
			GPSLat:          g.GPSLat,
			GPSLon:          g.GPSLon,
		}
		if im, ok := byID[g.PixelImageID]; ok {
			ej.PixelSource = filepath.Join(sources[im.SourceID], im.RelPath)
		}
		rj.Groups = append(rj.Groups, ej)
	}
	data, err := json.MarshalIndent(rj, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(target, "report.json"), data, 0o644)
}

// Apply materializes the library's plans into opts.Target. It reads the
// store only; the database is not modified. In move mode the pixel file of
// each successfully placed group is deleted from the source directory.
// After placing files it writes a report.json into opts.Target summarizing
// the run and every group's plan. A non-nil error is returned for fatal
// setup problems (unreadable library, uncreatable target); per-file
// problems are accumulated in Report.Errors.
func Apply(s *store.Store, opts Options) (Report, error) {
	var rep Report
	rep.DryRun = opts.DryRun

	groups, err := s.AllGroups()
	if err != nil {
		return rep, fmt.Errorf("read groups: %w", err)
	}
	images, err := s.AllImages()
	if err != nil {
		return rep, fmt.Errorf("read images: %w", err)
	}
	sources, err := s.Sources()
	if err != nil {
		return rep, fmt.Errorf("read sources: %w", err)
	}
	byID := make(map[string]store.Image, len(images))
	for _, im := range images {
		byID[im.ID] = im
	}
	if len(groups) == 0 {
		return rep, nil
	}

	if !opts.DryRun {
		if err := os.MkdirAll(opts.Target, 0o755); err != nil {
			return rep, fmt.Errorf("create target %q: %w", opts.Target, err)
		}
	}

	reserved := map[string]bool{}
	exists := func(name string) bool {
		if opts.DryRun {
			return false
		}
		_, err := os.Stat(filepath.Join(opts.Target, name))
		return err == nil
	}
	taken := func(name string) bool {
		k := strings.ToLower(name)
		return reserved[k] || exists(name)
	}

	for _, g := range groups {
		if g.Status == group.StatusConflict && !opts.ForceConflicts {
			rep.Skipped++
			continue
		}

		pixel, ok := byID[g.PixelImageID]
		if !ok {
			rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: pixel image %q not in library", g.ID, g.PixelImageID))
			continue
		}
		srcDir, ok := sources[pixel.SourceID]
		if !ok {
			rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: source dir %d not in library", g.ID, pixel.SourceID))
			continue
		}
		srcPath := filepath.Join(srcDir, pixel.RelPath)

		name := uniqueName(g.StoredName, g.ID, taken)
		reserved[strings.ToLower(name)] = true
		destPath := filepath.Join(opts.Target, name)

		// Derive the merged EXIF facts from the plan.
		writeEXIF := false
		var facts exifwrite.Facts
		if pixel.Kind == "jpeg" && !opts.NoEXIF {
			if offsetSec, ok := captureOffsetSec(g.CaptureTime, g.CaptureInstant); ok {
				facts = exifwrite.Facts{
					Capture: exifWallClock(g.CaptureInstant, offsetSec),
					Offset:  offsetString(offsetSec),
					HasGPS:  g.HasGPS,
					GPSLat:  g.GPSLat,
					GPSLon:  g.GPSLon,
				}
				writeEXIF = true
			}
		}

		if opts.DryRun {
			rep.Files = append(rep.Files, FileResult{Source: srcPath, Dest: destPath, Renamed: name != g.StoredName, EXIF: writeEXIF})
			if name != g.StoredName {
				rep.Renamed++
			}
			rep.Copied++
			continue
		}

		if err := place(srcPath, destPath, writeEXIF, facts, opts.EXIF); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: %v", g.ID, err))
			continue
		}
		fr := FileResult{Source: srcPath, Dest: destPath, Renamed: name != g.StoredName, EXIF: writeEXIF}
		// Move mode: the destination is fully in place, so the source can
		// go. If the removal fails, both files remain — the data is safe
		// and the error is reported.
		if opts.Move && !sameFile(srcPath, destPath) {
			if err := os.Remove(srcPath); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: remove source after move: %v", g.ID, err))
			} else {
				fr.Moved = true
				rep.Moved++
			}
		}
		rep.Files = append(rep.Files, fr)
		rep.Copied++
		if name != g.StoredName {
			rep.Renamed++
		}
	}

	if !opts.DryRun {
		if err := writeReportJSON(opts.Target, rep, groups, byID, sources); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("write report.json: %v", err))
		}
	}
	return rep, nil
}

// place copies src to dest, writing merged EXIF when requested. The write
// goes to a temporary file that is renamed into place, so the target never
// holds a partial file. With the full-data engine, every metadata tag in
// the source survives; the built-in writer only carries the merged facts.
func place(src, dest string, writeEXIF bool, facts exifwrite.Facts, tool *exif.Tool) error {
	if !writeEXIF {
		return copyFile(src, dest)
	}
	tmp := dest + ".tmp"
	if tool != nil {
		// Full-data write: everything in src survives and the merged
		// facts are overwritten. The zero Orientation leaves the
		// source's own orientation tag untouched.
		if err := tool.WriteFacts(src, tmp, exif.Facts{
			Capture: facts.Capture,
			Offset:  facts.Offset,
			HasGPS:  facts.HasGPS,
			GPSLat:  facts.GPSLat,
			GPSLon:  facts.GPSLon,
		}); err != nil {
			os.Remove(tmp)
			return err
		}
	} else {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		orig, _ := meta.FromJPEG(data) // preserve the source orientation
		facts.Orientation = orig.Orientation
		out, err := exifwrite.SetJPEG(data, facts)
		if err != nil {
			return err
		}
		if err := os.WriteFile(tmp, out, 0o644); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// sameFile reports whether a and b refer to the same file (symlinks
// resolved). Move mode uses it as a safety net: a true dest == src must
// never be followed by a source deletion. Today the name-collision logic
// already renames in that case, so the guard is defensive.
func sameFile(a, b string) bool {
	fi, err := os.Stat(a)
	if err != nil {
		return false
	}
	fj, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fi, fj)
}

func copyFile(src, dest string) error {
	tmp := dest + ".tmp"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// uniqueName returns base, or a collision-free variant when taken reports the
// name is already used. Collisions first take the group-prefixed form
// (matching the 000001_IMG.jpg convention), then a numeric suffix.
func uniqueName(base string, groupID int64, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	prefixed := fmt.Sprintf("%06d_%s", groupID, base)
	if !taken(prefixed) {
		return prefixed
	}
	stem, ext := splitExt(prefixed)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !taken(cand) {
			return cand
		}
	}
}

func splitExt(name string) (string, string) {
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i:]
}

var wallClockLayouts = []string{"2006:01:02 15:04:05", "2006-01-02 15:04:05"}

// captureOffsetSec recovers the capture timezone offset (seconds) from the
// plan's display wall clock and UTC instant. The wall clock parsed as UTC
// minus the instant is exactly the offset that was applied at plan time.
func captureOffsetSec(captureTime string, instant int64) (int, bool) {
	if captureTime == "" || instant <= 0 {
		return 0, false
	}
	for _, layout := range wallClockLayouts {
		if t, err := time.Parse(layout, captureTime); err == nil {
			return int(t.Unix() - instant), true
		}
	}
	return 0, false
}

// exifWallClock renders the EXIF DateTimeOriginal for the UTC instant in the
// given offset's zone.
func exifWallClock(instant int64, offsetSec int) string {
	t := time.Unix(instant, 0).Add(time.Duration(offsetSec) * time.Second)
	return t.UTC().Format("2006:01:02 15:04:05")
}

// offsetString renders an offset in seconds as "+HH:MM", "-HH:MM" or "Z".
func offsetString(sec int) string {
	if sec == 0 {
		return "Z"
	}
	sign := "+"
	if sec < 0 {
		sign = "-"
		sec = -sec
	}
	return fmt.Sprintf("%s%02d:%02d", sign, sec/3600, (sec%3600)/60)
}
