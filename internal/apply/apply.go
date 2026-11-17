// Package apply copies (or moves, with Options.Move) the planned pixel
// files of every group into a target directory and writes the merged EXIF
// (capture time, offset, GPS) onto the copies.
//
// It is the write-out half of the pipeline: scan/group/plan decide what the
// repaired library looks like, apply materializes it. Conflict groups are
// never dropped: they materialize as a plain copy of their best-effort
// pixel source (no merged EXIF) and the conflict reason is recorded in
// the ledger and report.
//
// Apply keeps a ledger of what it wrote (store.GroupOutput, keyed by
// target directory). Re-applying an unchanged library leaves the target
// untouched instead of duplicating files; a collision prefix (%06d_) is
// assigned once and never chains; a plan whose stored name changed moves
// its output in place; and an output whose pixel source or merged facts
// changed is rewritten under the name it already has. A target that was
// materialized before the ledger existed is adopted from its report.json.
package apply

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/exifwrite"
	"photo-library-repair/internal/filter"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/store"
)

// ErrStrictConflicts means Options.Strict refused to materialize because
// unresolved conflict groups remain in the plan.
var ErrStrictConflicts = errors.New("unresolved conflict groups (--strict)")

// Options controls an apply run.
type Options struct {
	Target   string     // directory that receives the final images
	DryRun   bool       // report what would be done without touching the target
	NoEXIF   bool       // copy files without writing merged EXIF
	Move     bool       // delete the source file once its copy is in place
	Strict   bool       // refuse to write when any StatusConflict remains
	EXIF     *exif.Tool // full-data engine; nil uses the built-in writer
	Identity string     // identity strategy name for report lineage (optional)
	Stages   *StageCounters
	// Filter, when set, evaluates each planned pixel before writing. Drops
	// skip materialization; an existing target file is moved to DeniedDir.
	Filter    *filter.Ruleset
	DeniedDir string            // default: <Target>/denied
	Denier    *filter.Denier    // optional; built from DeniedDir when nil
	ExifLoad  filter.ExifLoader // optional EXIF tag source for rules
	// OnProgress reports (done, total) as each group is processed.
	OnProgress func(done, total int64)
}

// StageCounters are optional pipeline counts mirrored into report.json.
type StageCounters struct {
	ScanCached          int `json:"scan_cached,omitempty"`
	ScanAlreadyKnown    int `json:"scan_already_known,omitempty"`
	ScanAdded           int `json:"scan_added,omitempty"`
	ScanUpdated         int `json:"scan_updated,omitempty"`
	Present             int `json:"present,omitempty"`
	Joined              int `json:"joined,omitempty"`
	New                 int `json:"new,omitempty"`
	Ambiguous           int `json:"ambiguous,omitempty"`
	PartitionSHAHits    int `json:"partition_sha_hits,omitempty"`
	PartitionCandidates int `json:"partition_candidates,omitempty"`
	PartitionSameCalls  int `json:"partition_same_calls,omitempty"`
}

// Per-file actions reported by FileResult.Action.
const (
	ActionNew       = "new"       // the group's output is written for the first time
	ActionUpdated   = "updated"   // the output exists but its content was rewritten
	ActionRenamed   = "renamed"   // the existing output was moved to a new name
	ActionUntouched = "untouched" // the recorded output is already current
	ActionDropped   = "dropped"   // filter rules refused materialization
)

// FileResult describes one file the run placed, would place, or left alone.
type FileResult struct {
	Source  string `json:"source"`
	Dest    string `json:"dest"`
	Group   int64  `json:"group,omitzero"`    // group the output belongs to
	Action  string `json:"action,omitzero"`   // one of the Action* constants
	Renamed bool   `json:"renamed,omitempty"` // the name differs from the plan's stored name
	EXIF    bool   `json:"exif,omitempty"`    // merged EXIF will be (was) written
	Moved   bool   `json:"moved,omitempty"`   // move mode: the source file was deleted
}

// Report summarizes an apply run. Copied, Updated and Renamed count files
// the run acted on; Untouched counts outputs the ledger proved current.
type Report struct {
	DryRun        bool
	Files         []FileResult
	Copied        int            // new outputs placed
	Updated       int            // existing outputs rewritten
	Moved         int            // move mode: files whose source was deleted
	Renamed       int            // outputs moved to a new name (incl. first-time prefixed placements)
	Untouched     int            // outputs left in place by the ledger
	FilterDropped map[string]int // rule name → count
	Errors        []string
}

func (rep *Report) count(action string, renamed bool) {
	switch action {
	case ActionUntouched:
		rep.Untouched++
	case ActionUpdated:
		rep.Updated++
	case ActionRenamed:
		rep.Renamed++
	case ActionDropped:
		// counted in FilterDropped
	default:
		rep.Copied++
		if renamed {
			rep.Renamed++
		}
	}
}

// groupJSON is one plan entry in report.json.
type groupJSON struct {
	ID              int64    `json:"id"`
	StoredName      string   `json:"stored_name"`
	Status          string   `json:"status"`
	ConflictKind    string   `json:"conflict_kind,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	CaptureTime     string   `json:"capture_time,omitempty"`
	CaptureFallback bool     `json:"capture_fallback,omitempty"`
	HasGPS          bool     `json:"has_gps,omitempty"`
	GPSLat          float64  `json:"gps_lat,omitzero"`
	GPSLon          float64  `json:"gps_lon,omitzero"`
	PixelImageID    string   `json:"pixel_image_id,omitempty"`
	PixelSource     string   `json:"pixel_source,omitempty"`
	Members         []string `json:"members,omitempty"`
}

// reportJSON is the machine-readable summary written into the target
// directory as report.json after a non-dry-run apply.
type reportJSON struct {
	Generated string         `json:"generated"`
	Target    string         `json:"target"`
	CommitID  int64          `json:"commit_id,omitzero"`
	Identity  string         `json:"identity,omitempty"`
	Stages    *StageCounters `json:"stages,omitempty"`
	Copied    int            `json:"copied"`
	Updated   int            `json:"updated,omitempty"`
	Untouched int            `json:"untouched,omitempty"`
	Moved     int            `json:"moved,omitempty"`
	Renamed   int            `json:"renamed,omitempty"`
	Files     []FileResult   `json:"files"`
	Groups    []groupJSON    `json:"groups"`
	Errors    []string       `json:"errors,omitempty"`
}

// writeReportJSON writes report.json into target summarizing the run and
// every group's plan (conflict groups included, with their reason).
func writeReportJSON(target string, rep Report, groups []store.Group, byID map[string]store.Image, sources map[int64]string, opts Options, commitID int64, membersByGroup map[int64][]string) error {
	rj := reportJSON{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Target:    target,
		CommitID:  commitID,
		Identity:  opts.Identity,
		Stages:    opts.Stages,
		Copied:    rep.Copied,
		Updated:   rep.Updated,
		Untouched: rep.Untouched,
		Moved:     rep.Moved,
		Renamed:   rep.Renamed,
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
			ConflictKind:    g.ConflictKind,
			Reason:          g.Reason,
			CaptureTime:     g.CaptureTime,
			CaptureFallback: g.CaptureFallback,
			HasGPS:          g.HasGPS,
			GPSLat:          g.GPSLat,
			GPSLon:          g.GPSLon,
			PixelImageID:    g.PixelImageID,
			Members:         membersByGroup[g.ID],
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

// targetKey normalizes the target path so that the same directory shares
// one ledger no matter how it was spelled on the command line.
func targetKey(target string) string {
	if abs, err := filepath.Abs(target); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(target)
}

// ClearDroppedOutputs removes apply-ledger rows whose Name matches a path
// plr filter moved out of the target (match on the relative path or its
// base name). Same end state as an apply-time filter drop.
func ClearDroppedOutputs(s *store.Store, target string, rels []string) (int, error) {
	if s == nil || len(rels) == 0 {
		return 0, nil
	}
	want := make(map[string]struct{}, len(rels)*2)
	for _, rel := range rels {
		rel = filepath.Clean(rel)
		want[strings.ToLower(rel)] = struct{}{}
		want[strings.ToLower(filepath.Base(rel))] = struct{}{}
	}
	key := targetKey(target)
	ledger, err := s.GroupOutputs(key)
	if err != nil {
		return 0, err
	}
	n := 0
	for gid, row := range ledger {
		name := strings.ToLower(row.Name)
		if _, ok := want[name]; !ok {
			continue
		}
		if err := s.DeleteGroupOutput(key, gid); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// adoptLedger seeds the ledger from a report.json left by a pre-ledger
// apply: prior file names are adopted without content fingerprints (empty
// SHA256/Facts), so an upgrade never re-duplicates an existing target.
// Legacy reports have no group id on file entries; they are matched back
// through the group's pixel source path.
func adoptLedger(target string, groups []store.Group, byID map[string]store.Image, sources map[int64]string) map[int64]store.GroupOutput {
	data, err := os.ReadFile(filepath.Join(target, "report.json"))
	if err != nil {
		return nil
	}
	var rj reportJSON
	if err := json.Unmarshal(data, &rj); err != nil {
		return nil
	}
	byPixel := make(map[string]int64, len(groups))
	pixelOf := make(map[int64]string, len(groups))
	for _, g := range groups {
		if im, ok := byID[g.PixelImageID]; ok {
			byPixel[filepath.Join(sources[im.SourceID], im.RelPath)] = g.ID
			pixelOf[g.ID] = g.PixelImageID
		}
	}
	out := make(map[int64]store.GroupOutput)
	for _, f := range rj.Files {
		gid := f.Group
		if gid == 0 {
			gid = byPixel[f.Source]
		}
		if gid == 0 || f.Dest == "" {
			continue
		}
		out[gid] = store.GroupOutput{Name: filepath.Base(f.Dest), PixelID: pixelOf[gid]}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stale reports whether the recorded output no longer matches what the
// plan asks for now. Recorded empty values are unknown (adopted rows) and
// never count as stale.
func stale(prev store.GroupOutput, pixelSHA, factsSig string) bool {
	return (prev.SHA256 != "" && prev.SHA256 != pixelSHA) ||
		(prev.Facts != "" && prev.Facts != factsSig)
}

// planFacts derives the merged EXIF facts the plan writes for one group.
// CaptureFallback never invents DateTimeOriginal/offset (file birth is not
// authoritative EXIF); GPS from the plan may still be written.
func planFacts(g store.Group, pixel store.Image, opts Options) (bool, exifwrite.Facts) {
	if pixel.Kind != "jpeg" || opts.NoEXIF {
		return false, exifwrite.Facts{}
	}
	f := exifwrite.Facts{
		HasGPS: g.HasGPS,
		GPSLat: g.GPSLat,
		GPSLon: g.GPSLon,
	}
	if !g.CaptureFallback {
		offsetSec, ok := captureOffsetSec(g.CaptureTime, g.CaptureInstant)
		if !ok {
			if !g.HasGPS {
				return false, exifwrite.Facts{}
			}
			return true, f
		}
		f.Capture = exifWallClock(g.CaptureInstant, offsetSec)
		f.Offset = offsetString(offsetSec)
	} else if !g.HasGPS {
		return false, exifwrite.Facts{}
	}
	return true, f
}

// factsSignature fingerprints the merged EXIF facts for the ledger.
func factsSignature(writeEXIF bool, f exifwrite.Facts) string {
	if !writeEXIF {
		return "plain"
	}
	sig := "exif:" + f.Capture + "|" + f.Offset
	if f.HasGPS {
		sig += fmt.Sprintf("|%.6f,%.6f", f.GPSLat, f.GPSLon)
	}
	return sig
}

// planName decides which file name the group's output carries. A recorded
// name sticks forever — the collision prefix is assigned once and never
// chains — and only moves when the plan's stored name changed, in which
// case the output moves to the new stored name (or its prefixed form).
// taken reports names spoken for by another group or a foreign file.
func planName(g store.Group, prev store.GroupOutput, hadPrev bool, taken func(string) bool) string {
	base := g.StoredName
	prefixed := fmt.Sprintf("%06d_%s", g.ID, base)
	if !hadPrev {
		return uniqueName(base, g.ID, taken)
	}
	switch {
	case prev.Name == base, prev.Name == prefixed:
		return prev.Name
	case prev.Prefixed && !taken(prefixed):
		return prefixed // stored name changed: move on, same prefix
	case !prev.Prefixed && !taken(base):
		return base // stored name changed into a free name
	case !taken(prefixed):
		return prefixed
	default:
		return prev.Name
	}
}

// Apply materializes the library's plans into opts.Target. Besides the
// files it reads the apply ledger from the library (keyed by target
// directory) and updates it; report.json is rewritten as a manifest of
// every group. In move mode the pixel file of each newly placed group is
// deleted from the source directory; untouched groups never touch their
// sources. A non-nil error is returned for fatal setup problems
// (unreadable library, uncreatable target); per-file problems are
// accumulated in Report.Errors.
func Apply(s *store.Store, opts Options) (Report, error) {
	var rep Report
	rep.DryRun = opts.DryRun
	if opts.Filter != nil {
		rep.FilterDropped = map[string]int{}
	}

	var denier *filter.Denier
	if opts.Filter != nil {
		deniedDir := opts.DeniedDir
		if deniedDir == "" {
			deniedDir = filepath.Join(opts.Target, "denied")
		}
		if opts.Denier != nil {
			denier = opts.Denier
		} else {
			var out io.Writer
			if opts.DryRun {
				out = os.Stdout
			}
			denier = filter.NewDenier(deniedDir, opts.DryRun, out)
		}
		if err := denier.EnsureDir(); err != nil {
			return rep, fmt.Errorf("denied dir: %w", err)
		}
		defer denier.Close()
	}

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
	membersByGroup := map[int64][]string{}
	for _, im := range images {
		byID[im.ID] = im
		if im.GroupID != 0 {
			membersByGroup[im.GroupID] = append(membersByGroup[im.GroupID],
				filepath.Join(sources[im.SourceID], im.RelPath))
		}
	}
	if len(groups) == 0 {
		return rep, nil
	}

	strictN := 0
	if opts.Strict {
		for _, g := range groups {
			if g.Status == group.StatusConflict {
				strictN++
			}
		}
		if strictN > 0 && !opts.DryRun {
			return rep, fmt.Errorf("%w: %d unresolved", ErrStrictConflicts, strictN)
		}
	}

	key := targetKey(opts.Target)
	ledger, err := s.GroupOutputs(key)
	if err != nil {
		return rep, fmt.Errorf("read apply ledger: %w", err)
	}
	if len(ledger) == 0 {
		ledger = adoptLedger(opts.Target, groups, byID, sources)
		if !opts.DryRun {
			for gid, row := range ledger {
				if err := s.SaveGroupOutput(key, gid, row); err != nil {
					return rep, fmt.Errorf("save adopted ledger row %d: %w", gid, err)
				}
			}
		}
	}
	// owner maps lower-cased names to the group whose ledger row claims
	// them; pixelOwner lets a renumbered group (a full re-group renumbers)
	// pick up the ledger row written for its own pixel file.
	owner := make(map[string]int64, len(ledger))
	pixelOwner := make(map[string]int64, len(ledger))
	for gid, row := range ledger {
		owner[strings.ToLower(row.Name)] = gid
		if row.PixelID != "" {
			pixelOwner[row.PixelID] = gid
		}
	}

	if !opts.DryRun {
		if err := os.MkdirAll(opts.Target, 0o755); err != nil {
			return rep, fmt.Errorf("create target %q: %w", opts.Target, err)
		}
		// Leftover place() temps from a killed prior run; ignore remove errors.
		if matches, err := filepath.Glob(filepath.Join(opts.Target, "*.tmp")); err == nil {
			for _, p := range matches {
				_ = os.Remove(p)
			}
		}
	}

	reserved := map[string]int64{} // names claimed during this run
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(opts.Target, name))
		return err == nil
	}

	for i, g := range groups {
		if opts.OnProgress != nil {
			opts.OnProgress(int64(i+1), int64(len(groups)))
		}
		// Conflict groups still materialize (plain copy, no merged EXIF);
		// the ledger records why the plan is only best effort. A filter
		// drop may still move the file afterward, same as any other group.
		conflict := g.Status == group.StatusConflict

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

		writeEXIF, facts := planFacts(g, pixel, opts)
		if conflict {
			writeEXIF, facts = false, exifwrite.Facts{}
		}
		sig := factsSignature(writeEXIF, facts)

		prev, hadPrev := ledger[g.ID]
		orphan := int64(0)
		if !hadPrev && g.PixelImageID != "" {
			if gid, ok := pixelOwner[g.PixelImageID]; ok && gid != g.ID {
				prev, hadPrev, orphan = ledger[gid], true, gid
			}
		}

		// taken reports whether name is spoken for by anything that is
		// not this group's own recorded output.
		taken := func(name string) bool {
			k := strings.ToLower(name)
			if o, ok := reserved[k]; ok {
				return o != g.ID
			}
			if o, ok := owner[k]; ok {
				return o != g.ID
			}
			return exists(name) // unclaimed file on disk belongs to someone else
		}

		name := planName(g, prev, hadPrev, taken)
		prefixed := name != g.StoredName
		destPath := filepath.Join(opts.Target, name)
		action := ActionNew
		if hadPrev {
			switch {
			case name != prev.Name:
				action = ActionRenamed
			case stale(prev, pixel.SHA256, sig) || !exists(name):
				action = ActionUpdated
			default:
				action = ActionUntouched
			}
		}

		if opts.Filter != nil {
			info, ferr := filter.LoadImageInfo(srcPath, pixel.RelPath, opts.ExifLoad)
			if ferr != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: filter: %v", g.ID, ferr))
				continue
			}
			dec := opts.Filter.Evaluate(info)
			if dec.Action == filter.ActionDrop {
				fr := FileResult{
					Source: srcPath,
					Dest:   destPath,
					Group:  g.ID,
					Action: ActionDropped,
				}
				// Remove this group's current library file (ledger name),
				// not only planName — an unclaimed same-name file may force
				// a prefix that does not exist on disk yet.
				removeName := name
				if hadPrev {
					removeName = prev.Name
				}
				removePath := filepath.Join(opts.Target, removeName)
				if _, err := os.Stat(removePath); err == nil {
					if _, err := denier.Drop(removePath, removeName, dec.Rule, pixel.Size); err != nil {
						rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: deny existing: %v", g.ID, err))
						continue
					}
				}
				if !opts.DryRun && hadPrev {
					if err := s.DeleteGroupOutput(key, g.ID); err != nil {
						rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: drop ledger: %v", g.ID, err))
					}
					delete(ledger, g.ID)
					delete(owner, strings.ToLower(prev.Name))
					if prev.PixelID != "" {
						delete(pixelOwner, prev.PixelID)
					}
				}
				rep.FilterDropped[dec.Rule]++
				rep.Files = append(rep.Files, fr)
				rep.count(ActionDropped, false)
				continue
			}
		}

		reserved[strings.ToLower(name)] = g.ID

		fr := FileResult{
			Source:  srcPath,
			Dest:    destPath,
			Group:   g.ID,
			Action:  action,
			Renamed: prefixed,
			EXIF:    writeEXIF,
		}
		if opts.DryRun {
			rep.Files = append(rep.Files, fr)
			rep.count(action, fr.Renamed)
			continue
		}

		var placeErr error
		switch action {
		case ActionUntouched:
			// File stays; the ledger row is refreshed below (adopted rows
			// start out without fingerprints).
		case ActionUpdated:
			placeErr = place(srcPath, destPath, writeEXIF, facts, opts.EXIF)
		case ActionRenamed:
			prevPath := filepath.Join(opts.Target, prev.Name)
			if exists(prev.Name) && !stale(prev, pixel.SHA256, sig) {
				placeErr = os.Rename(prevPath, destPath) // content still current
			} else {
				placeErr = place(srcPath, destPath, writeEXIF, facts, opts.EXIF)
				if placeErr == nil {
					// Free the old name; a leftover is reported, not fatal,
					// the new file is already in place.
					if err := os.Remove(prevPath); err != nil {
						rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: remove old name %s: %v", g.ID, prev.Name, err))
					}
				}
			}
		case ActionNew:
			placeErr = place(srcPath, destPath, writeEXIF, facts, opts.EXIF)
		}
		if placeErr != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: %v", g.ID, placeErr))
			continue
		}
		// Stamp FS times from the planned capture (earliest EXIF in the
		// group, else earliest file creation). Untouched outputs are
		// included so a re-apply backfills an existing library.
		if err := setCaptureTimes(destPath, g.CaptureInstant); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: set capture times: %v", g.ID, err))
		}

		// Move mode: the destination is fully in place, so the source can
		// go. If the removal fails, both files remain — the data is safe
		// and the error is reported. Untouched outputs leave sources alone.
		if opts.Move && action != ActionUntouched && !sameFile(srcPath, destPath) {
			if err := os.Remove(srcPath); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: remove source after move: %v", g.ID, err))
			} else {
				fr.Moved = true
				rep.Moved++
			}
		}
		rep.Files = append(rep.Files, fr)
		rep.count(action, fr.Renamed)

		row := store.GroupOutput{
			Name:      name,
			PixelID:   g.PixelImageID,
			SHA256:    pixel.SHA256,
			Facts:     sig,
			Prefixed:  prefixed,
			WrittenAt: time.Now().Unix(),
		}
		if conflict {
			row.Conflict = g.Reason
		}
		if err := s.SaveGroupOutput(key, g.ID, row); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: save apply ledger: %v", g.ID, err))
			continue
		}
		owner[strings.ToLower(name)] = g.ID
		if row.PixelID != "" {
			pixelOwner[row.PixelID] = g.ID
		}
		if orphan != 0 {
			if oldRow, ok := ledger[orphan]; ok {
				if owner[strings.ToLower(oldRow.Name)] == orphan {
					delete(owner, strings.ToLower(oldRow.Name))
				}
				if oldRow.PixelID != "" && pixelOwner[oldRow.PixelID] == orphan {
					delete(pixelOwner, oldRow.PixelID)
				}
			}
			delete(ledger, orphan)
			if err := s.DeleteGroupOutput(key, orphan); err != nil {
				rep.Errors = append(rep.Errors, fmt.Sprintf("group %d: drop adopted ledger row %d: %v", g.ID, orphan, err))
			}
		}
	}

	var commitID int64
	if commits, err := s.Commits(); err == nil && len(commits) > 0 {
		commitID = commits[len(commits)-1].ID
	}
	if !opts.DryRun {
		if err := writeReportJSON(opts.Target, rep, groups, byID, sources, opts, commitID, membersByGroup); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("write report.json: %v", err))
		}
	}
	if strictN > 0 {
		return rep, fmt.Errorf("%w: %d unresolved", ErrStrictConflicts, strictN)
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
