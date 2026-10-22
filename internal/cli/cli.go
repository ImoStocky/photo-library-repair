// Package cli implements the plr command line interface.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"photo-library-repair/internal/apply"
	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/ingest"
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
  plr plan     [--conflict-hamming N] [--db PATH]
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
            capture times are marked as conflicts for later human/model
            review.
  apply     Copy the planned pixel files into --target and write the merged
            EXIF (capture time, offset, GPS) onto the copies. Conflict
            groups are skipped unless --force-conflicts is given.
  build     scan + group + plan + apply in one go.
  report    Print a summary of the library, groups and plans.
  selftest  Run built-in parity checks and print PASS/FAIL.

The library is a SQLite database (default: plr.db in the working directory).
`

func usage(w io.Writer) { io.WriteString(w, usageText) }

func notImplemented(cmd string) int {
	fmt.Fprintf(os.Stderr, "plr: %s is not implemented yet\n", cmd)
	return 1
}

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
	rep, err := ingest.Scan(s, ingest.Options{
		Dirs:    dirs,
		Force:   *force,
		Workers: *workers,
		MinSize: int64(*minSizeKB) * 1024,
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
	imgs, err := s.AllImages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	if len(imgs) == 0 {
		fmt.Println("no images recorded; run plr scan first")
		return 0
	}
	gimgs := toGroupImages(imgs)
	assign, groups := group.Aggregate(gimgs, *maxHamming)
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
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = conflictHamming
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	defer s.Close()
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
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		p := group.ResolveGroup(byGroup[id], nil)
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
		if err := s.SaveGroup(g); err != nil {
			fmt.Fprintln(os.Stderr, "plr plan:", err)
			return 1
		}
		counts[p.Status]++
	}
	fmt.Printf("%d groups: %d resolved, %d conflicts, %d singletons\n",
		len(ids), counts[group.StatusResolved], counts[group.StatusConflict], counts[group.StatusSingleton])
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
	rep, err := apply.Apply(s, apply.Options{
		Target:         *target,
		DryRun:         *dryRun,
		NoEXIF:         *noEXIF,
		ForceConflicts: *forceConflicts,
	})
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
	_ = force
	_ = maxHamming
	_ = conflictHamming
	_ = dryRun
	_ = noEXIF
	_ = forceConflicts
	_ = db
	_ = workers
	_ = minSizeKB
	return notImplemented("build")
}

func runReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = db
	return notImplemented("report")
}

func runSelftest(args []string) int {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return notImplemented("selftest")
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
