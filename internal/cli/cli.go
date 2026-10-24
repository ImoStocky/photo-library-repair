// Package cli implements the plr command line interface.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"

	"photo-library-repair/internal/apply"
	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/ingest"
	kronkai "photo-library-repair/internal/kronk"
	"photo-library-repair/internal/selftest"
	"photo-library-repair/internal/store"
)

// Run executes the plr command line and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	case "scan":
		return runScan(rest)
	case "group":
		return runGroup(rest)
	case "plan":
		return runPlan(rest)
	case "apply":
		return runApply(rest)
	case "build":
		return runBuild(rest)
	case "increment":
		return runIncrement(rest)
	case "report":
		return runReport(rest)
	case "selftest":
		return runSelftest(rest)
	default:
		fmt.Fprintf(os.Stderr, "plr: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return 2
	}
}

const usageText = `plr - photo library repair

Usage:
  plr scan     --dir DIR... [--force] [--db PATH] [--workers N] [--min-size-kb N]
  plr group    [--max-hamming N] [--db PATH]
  plr plan     [--conflict-hamming N] [--ai] [--model SOURCE] [--db PATH]
  plr apply    --target DIR [--dry-run] [--no-exif] [--move] [--force-conflicts] [--db PATH]
  plr build    --dir DIR... --target DIR [scan, group, plan and apply flags]
  plr increment --dir DIR... --target DIR [scan, group, plan and apply flags]
  plr report   [--db PATH]
  plr selftest

Commands:
  scan      Walk directories, record every image (sha256, 16-byte perceptual
            hash, EXIF capture time) into the SQL library. Directories that
            were already scanned are skipped unless --force is given.
  group     Group images: exact sha256 matches share a group, and images
            whose perceptual hashes differ by at most --max-hamming bits are
            chained into the same group.
  plan      Resolve every group: earliest EXIF capture time (timezone
            aware; file creation time never overrides EXIF), best quality
            pixels, GPS. Groups with near-identical pictures and identical
            capture times are marked as conflicts. With --ai the Kronk
            decision model settles conflict groups and marks them
            resolved_by the model id.
  apply     Copy the planned pixel files into --target and write the merged
            EXIF (capture time, offset, GPS) onto the copies. With --move the
            source file is deleted once its copy is in place. Writes a
            report.json summary into --target. Conflict groups are skipped
            unless --force-conflicts is given.
  build     scan + group + plan + apply in one go.
  increment Merge new photos into an existing library: scan the new dirs
            into the same DB, then re-group (new near-duplicates chain into
            existing groups under the same --max-hamming radius), re-plan
            and re-apply. On a fresh DB it degrades to build.
  report    Print a summary of the library, groups and plans.
  selftest  Run built-in parity checks and print PASS/FAIL.

The library is a SQLite database (default: plr.db in the working directory).
`

func usage(w io.Writer) { io.WriteString(w, usageText) }

func runScan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	dirs := multiFlag{}
	force := fs.Bool("force", false, "re-scan directories that were already scanned")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	workers := fs.Int("workers", 4, "number of hash workers")
	minSizeKB := fs.Int("min-size-kb", 0, "ignore images smaller than this many kilobytes")
	fs.Var(&dirs, "dir", "directory to scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "plr scan: at least one --dir is required")
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr scan:", err)
		return 1
	}
	defer s.Close()
	tool := openEXIF("scan")
	if tool != nil {
		defer tool.Close()
	}
	return runScanPhase(s, dirs, *force, *workers, *minSizeKB, tool)
}

// runScanPhase is the scan step, shared by "plr scan" and "plr build".
func runScanPhase(s *store.Store, dirs []string, force bool, workers, minSizeKB int, tool *exif.Tool) int {
	hasher := fingerprint.Compute
	if tool != nil {
		hasher = fingerprint.WithEXIF(tool)
	}
	rep, err := ingest.Scan(s, ingest.Options{
		Dirs:    dirs,
		Force:   force,
		Workers: workers,
		MinSize: int64(minSizeKB) * 1024,
	}, hasher)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr scan:", err)
		return 1
	}
	fmt.Printf("scanned %d dir(s), skipped %d already scanned\n", rep.DirsScanned, rep.DirsSkipped)
	fmt.Printf("files seen %d: %d added, %d updated, %d cached, %d non-image, %d errors\n",
		rep.FilesSeen, rep.ImagesAdded, rep.ImagesUpdated, rep.ImagesCached, rep.NonImage, rep.Errors)
	return 0
}

func runGroup(args []string) int {
	fs := flag.NewFlagSet("group", flag.ExitOnError)
	maxHamming := fs.Int("max-hamming", 12, "maximum perceptual-hash hamming distance to chain images into one group")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	defer s.Close()
	return runGroupPhase(s, *maxHamming)
}

// runGroupPhase is the group step, shared by "plr group" and "plr build".
func runGroupPhase(s *store.Store, maxHamming int) int {
	imgs, err := s.AllImages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	if len(imgs) == 0 {
		fmt.Println("no images recorded; run plr scan first")
		return 0
	}
	assign, groups := group.Aggregate(toGroupImages(imgs), maxHamming)
	if err := s.SetGroupAssignments(assign); err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	multi := 0
	for _, g := range groups {
		if len(g) > 1 {
			multi++
		}
	}
	fmt.Printf("%d images -> %d groups (%d with duplicates)\n", len(imgs), len(groups), multi)
	return 0
}

func runPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	conflictHamming := fs.Int("conflict-hamming", 2, "perceptual hashes within this distance with identical capture times mark the group as a conflict")
	ai := fs.Bool("ai", false, "settle conflict groups with the Kronk decision model")
	modelSource := fs.String("model", kronkai.DefaultModel, "Kronk decision model source for --ai")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	defer s.Close()
	var res group.Resolver
	if *ai {
		var close func()
		res, close, err = kronkai.New(context.Background(), *modelSource)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr plan:", err)
			return 1
		}
		defer close()
		fmt.Printf("decision model %s loaded; settling conflicts\n", res.Name())
	}
	return runPlanPhase(s, res, *conflictHamming)
}

// runPlanPhase is the plan step, shared by "plr plan" and "plr build".
func runPlanPhase(s *store.Store, res group.Resolver, conflictHamming int) int {
	imgs, err := s.AllImages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	if len(imgs) == 0 {
		fmt.Println("no images recorded; run plr scan first")
		return 0
	}
	byGroup := map[int64][]group.Image{}
	assign := make(map[string]int64, len(imgs))
	for _, im := range imgs {
		if im.GroupID == 0 {
			fmt.Println("images without a group; run plr group first")
			return 1
		}
		byGroup[im.GroupID] = append(byGroup[im.GroupID], toGroupImage(im))
		assign[im.ID] = im.GroupID
	}
	if err := s.ClearGroups(); err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	counts := map[string]int{}
	ids := make([]int64, 0, len(byGroup))
	for id := range byGroup {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	aiSettled := 0
	for _, id := range ids {
		p := group.ResolveGroup(byGroup[id], res, conflictHamming)
		if p.ResolvedBy != group.Rules {
			aiSettled++
		}
		g := store.Group{
			ID:              id,
			Status:          p.Status,
			Reason:          p.Reason,
			ResolvedBy:      p.ResolvedBy,
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
		if err := s.SaveGroup(g); err != nil {
			fmt.Fprintln(os.Stderr, "plr plan:", err)
			return 1
		}
		counts[p.Status]++
	}
	// Re-apply the assignments: ClearGroups above zeroes images.group_id.
	if err := s.SetGroupAssignments(assign); err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	fmt.Printf("%d groups: %d resolved, %d conflicts, %d singletons\n",
		len(ids), counts[group.StatusResolved], counts[group.StatusConflict], counts[group.StatusSingleton])
	if res != nil {
		fmt.Printf("%d conflict group(s) settled by %s\n", aiSettled, res.Name())
	}
	return 0
}

func runApply(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	target := fs.String("target", "", "directory that receives the final images (required)")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without touching the target")
	noEXIF := fs.Bool("no-exif", false, "copy files without writing merged EXIF")
	move := fs.Bool("move", false, "delete each source file once its copy is in place in --target")
	forceConflicts := fs.Bool("force-conflicts", false, "apply conflict groups using the best-effort plan")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "plr apply: --target is required")
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr apply:", err)
		return 1
	}
	defer s.Close()
	tool := openEXIF("apply")
	if tool != nil {
		defer tool.Close()
	}
	return runApplyPhase(s, apply.Options{
		Target:         *target,
		DryRun:         *dryRun,
		NoEXIF:         *noEXIF,
		Move:           *move,
		ForceConflicts: *forceConflicts,
		EXIF:           tool,
	})
}

// runApplyPhase is the apply step, shared by "plr apply" and "plr build".
func runApplyPhase(s *store.Store, opts apply.Options) int {
	rep, err := apply.Apply(s, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr apply:", err)
		return 1
	}
	verb := "copied"
	switch {
	case opts.Move && !opts.DryRun:
		verb = "moved"
	case opts.Move:
		verb = "would move"
	case opts.DryRun:
		verb = "would copy"
	}
	for _, f := range rep.Files {
		line := fmt.Sprintf("%s %s -> %s", verb, f.Source, f.Dest)
		if f.EXIF {
			line += " (EXIF)"
		}
		fmt.Println(line)
	}
	for _, e := range rep.Errors {
		fmt.Fprintln(os.Stderr, "plr apply:", e)
	}
	fmt.Printf("%s: %d file(s)%s, %d conflict group(s) skipped\n",
		verb, rep.Copied, renamedSuffix(rep.Renamed), rep.Skipped)
	if len(rep.Errors) > 0 {
		return 1
	}
	return 0
}

func renamedSuffix(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d renamed)", n)
}

// openEXIF starts the full-data EXIF engine (the embedded ExifTool). When
// it cannot be started, the pipeline continues with the built-in minimal
// reader/writer and the failure is reported.
func openEXIF(cmd string) *exif.Tool {
	tool, err := exif.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "plr %s: EXIF engine unavailable (%v); built-in EXIF reader/writer only\n", cmd, err)
		return nil
	}
	ver, _ := tool.Version()
	fmt.Printf("exif engine: ExifTool %s (embedded, full metadata)\n", ver)
	return tool
}

func runBuild(args []string) int { return runPipeline("build", args) }

// runIncrement merges new photos into an existing library: scan the new
// directories into the same DB (upserts keep existing ids), then re-group
// (new near-duplicates chain into existing groups under the same
// --max-hamming radius), re-plan and re-apply. On a fresh DB it degrades
// to build.
func runIncrement(args []string) int { return runPipeline("increment", args) }

func runPipeline(cmd string, args []string) int {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dirs := multiFlag{}
	target := fs.String("target", "", "directory that receives the final images (required)")
	force := fs.Bool("force", false, "re-scan directories that were already scanned")
	maxHamming := fs.Int("max-hamming", 12, "maximum perceptual-hash hamming distance to chain images into one group")
	conflictHamming := fs.Int("conflict-hamming", 2, "perceptual hashes within this distance with identical capture times mark the group as a conflict")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without touching the target")
	noEXIF := fs.Bool("no-exif", false, "copy files without writing merged EXIF")
	move := fs.Bool("move", false, "delete each source file once its copy is in place in --target")
	forceConflicts := fs.Bool("force-conflicts", false, "apply conflict groups using the best-effort plan")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	workers := fs.Int("workers", 4, "number of hash workers")
	minSizeKB := fs.Int("min-size-kb", 0, "ignore images smaller than this many kilobytes")
	fs.Var(&dirs, "dir", "directory to scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(dirs) == 0 || *target == "" {
		fmt.Fprintf(os.Stderr, "plr %s: --dir and --target are required\n", cmd)
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr "+cmd+":", err)
		return 1
	}
	defer s.Close()
	tool := openEXIF(cmd)
	if tool != nil {
		defer tool.Close()
	}
	phases := []struct {
		label string
		fn    func() int
	}{
		{"scan", func() int { return runScanPhase(s, dirs, *force, *workers, *minSizeKB, tool) }},
		{"group", func() int { return runGroupPhase(s, *maxHamming) }},
		{"plan", func() int { return runPlanPhase(s, nil, *conflictHamming) }},
		{"apply", func() int {
			return runApplyPhase(s, apply.Options{
				Target:         *target,
				DryRun:         *dryRun,
				NoEXIF:         *noEXIF,
				Move:           *move,
				ForceConflicts: *forceConflicts,
				EXIF:           tool,
			})
		}},
	}
	for _, p := range phases {
		fmt.Printf("== %s\n", p.label)
		if code := p.fn(); code != 0 {
			fmt.Fprintf(os.Stderr, "plr %s: %s phase failed\n", cmd, p.label)
			return code
		}
	}
	return 0
}

func runReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr report:", err)
		return 1
	}
	defer s.Close()

	sources, err := s.Sources()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr report:", err)
		return 1
	}
	images, err := s.AllImages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr report:", err)
		return 1
	}
	groups, err := s.AllGroups()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr report:", err)
		return 1
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	byID := make(map[string]store.Image, len(images))
	for _, im := range images {
		byID[im.ID] = im
	}
	counts := map[string]int{}
	for _, g := range groups {
		counts[g.Status]++
	}

	fmt.Printf("library %s\n", *db)
	dirList := make([]string, 0, len(sources))
	for _, d := range sources {
		dirList = append(dirList, d)
	}
	sort.Strings(dirList)
	fmt.Printf("  sources: %d\n", len(dirList))
	for _, d := range dirList {
		fmt.Printf("    - %s\n", d)
	}
	fmt.Printf("  images:  %d\n", len(images))
	fmt.Printf("  groups:  %d (%d resolved, %d conflicts, %d singletons)\n",
		len(groups), counts[group.StatusResolved], counts[group.StatusConflict], counts[group.StatusSingleton])
	if len(groups) == 0 {
		fmt.Println("no plans; run plr scan, plr group and plr plan first")
		return 0
	}

	fmt.Println("\nplans:")
	for _, g := range groups {
		line := fmt.Sprintf("  #%d %-28s %-10s", g.ID, g.StoredName, g.Status)
		if g.CaptureTime != "" {
			line += "  capture " + g.CaptureTime
			if g.CaptureFallback {
				line += " (file time)"
			}
		}
		if g.HasGPS {
			line += fmt.Sprintf("  gps %.5f,%.5f", g.GPSLat, g.GPSLon)
		}
		if im, ok := byID[g.PixelImageID]; ok {
			line += "  pixels " + im.Name
		}
		fmt.Println(line)
	}

	conflicts := make([]store.Group, 0)
	for _, g := range groups {
		if g.Status == group.StatusConflict {
			conflicts = append(conflicts, g)
		}
	}
	if len(conflicts) > 0 {
		fmt.Println("\nconflicts:")
		for _, g := range conflicts {
			fmt.Printf("  #%d %s\n", g.ID, g.Reason)
		}
	}
	return 0
}

func runSelftest(args []string) int {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	checks := selftest.Run()
	failed := 0
	for _, c := range checks {
		if c.OK {
			fmt.Printf("PASS %s\n", c.Name)
		} else {
			failed++
			fmt.Printf("FAIL %s: %s\n", c.Name, c.Detail)
		}
	}
	fmt.Printf("%d/%d checks passed\n", len(checks)-failed, len(checks))
	if failed > 0 {
		return 1
	}
	return 0
}

func toGroupImages(imgs []store.Image) []group.Image {
	out := make([]group.Image, len(imgs))
	for i := range imgs {
		out[i] = toGroupImage(imgs[i])
	}
	return out
}

func toGroupImage(im store.Image) group.Image {
	return group.Image{
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
		Kind:        im.Kind,
		HasGPS:      im.HasGPS,
		GPSLat:      im.GPSLat,
		GPSLon:      im.GPSLon,
	}
}

// multiFlag collects repeated occurrences of a string flag.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
