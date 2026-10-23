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
  plr apply    --target DIR [--dry-run] [--no-exif] [--force-conflicts] [--db PATH]
  plr build    --dir DIR... --target DIR [scan, group, plan and apply flags]
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
            EXIF (capture time, offset, GPS) onto the copies. Conflict
            groups are skipped unless --force-conflicts is given.
  build     scan + group + plan + apply in one go.
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
	return runScanPhase(s, dirs, *force, *workers, *minSizeKB)
}

// runScanPhase is the scan step, shared by "plr scan" and "plr build".
func runScanPhase(s *store.Store, dirs []string, force bool, workers, minSizeKB int) int {
	rep, err := ingest.Scan(s, ingest.Options{
		Dirs:    dirs,
		Force:   force,
		Workers: workers,
		MinSize: int64(minSizeKB) * 1024,
	}, fingerprint.Compute)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr scan:", err)
		return 1
	}
	fmt.Printf("scanned %d dir(s), skipped %d already scanned\n", rep.DirsScanned, rep.DirsSkipped)
	fmt.Printf("files seen %d: %d added, %d updated, %d non-image, %d errors\n",
		rep.FilesSeen, rep.ImagesAdded, rep.ImagesUpdated, rep.NonImage, rep.Errors)
	return 0
}

func runGroup(args []string) int {
	fs := flag.NewFlagSet("group", flag.ExitOnError)
	maxHamming := fs.Int("max-hamming", 6, "maximum perceptual-hash hamming distance to chain images into one group")
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
	_ = conflictHamming // no rule uses it yet; see docs/mvp-gaps.md
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
	return runPlanPhase(s, res)
}

// runPlanPhase is the plan step, shared by "plr plan" and "plr build".
func runPlanPhase(s *store.Store, res group.Resolver) int {
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
	for _, im := range imgs {
		if im.GroupID == 0 {
			fmt.Println("images without a group; run plr group first")
			return 1
		}
		byGroup[im.GroupID] = append(byGroup[im.GroupID], toGroupImage(im))
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
		p := group.ResolveGroup(byGroup[id], res)
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
	return runApplyPhase(s, apply.Options{
		Target:         *target,
		DryRun:         *dryRun,
		NoEXIF:         *noEXIF,
		ForceConflicts: *forceConflicts,
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
	if rep.DryRun {
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

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	dirs := multiFlag{}
	target := fs.String("target", "", "directory that receives the final images (required)")
	force := fs.Bool("force", false, "re-scan directories that were already scanned")
	maxHamming := fs.Int("max-hamming", 6, "maximum perceptual-hash hamming distance to chain images into one group")
	conflictHamming := fs.Int("conflict-hamming", 2, "perceptual hashes within this distance with identical capture times mark the group as a conflict")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without touching the target")
	noEXIF := fs.Bool("no-exif", false, "copy files without writing merged EXIF")
	forceConflicts := fs.Bool("force-conflicts", false, "apply conflict groups using the best-effort plan")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	workers := fs.Int("workers", 4, "number of hash workers")
	minSizeKB := fs.Int("min-size-kb", 0, "ignore images smaller than this many kilobytes")
	fs.Var(&dirs, "dir", "directory to scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(dirs) == 0 || *target == "" {
		fmt.Fprintln(os.Stderr, "plr build: --dir and --target are required")
		return 2
	}
	_ = conflictHamming // no rule uses it yet; see docs/mvp-gaps.md
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr build:", err)
		return 1
	}
	defer s.Close()
	phases := []struct {
		label string
		fn    func() int
	}{
		{"scan", func() int { return runScanPhase(s, dirs, *force, *workers, *minSizeKB) }},
		{"group", func() int { return runGroupPhase(s, *maxHamming) }},
		{"plan", func() int { return runPlanPhase(s, nil) }},
		{"apply", func() int {
			return runApplyPhase(s, apply.Options{
				Target:         *target,
				DryRun:         *dryRun,
				NoEXIF:         *noEXIF,
				ForceConflicts: *forceConflicts,
			})
		}},
	}
	for _, p := range phases {
		fmt.Printf("== %s\n", p.label)
		if code := p.fn(); code != 0 {
			fmt.Fprintf(os.Stderr, "plr build: %s phase failed\n", p.label)
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
