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
	"strconv"
	"strings"
	"time"

	"photo-library-repair/internal/apply"
	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/ingest"
	kronkai "photo-library-repair/internal/kronk"
	"photo-library-repair/internal/phash"
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
	case "commit":
		return runCommit(rest)
	case "rollback":
		return runRollback(rest)
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
               [--photoslibrary-all] [--phash-alg ALG] [--phash-size 8|16|32]
  plr group    [--max-hamming-chunking N] [--identity exif-first|pass|double-phash|vision]
               [--same-image-window DUR] [--db PATH]
  plr plan     [--ai] [--model SOURCE] [--db PATH]
  plr apply    --target DIR [--dry-run] [--no-exif] [--move] [--db PATH]
  plr build    --dir DIR... --target DIR [scan, group, plan and apply flags]
  plr increment --dir DIR... --target DIR [scan, group, plan and apply flags]
  plr commit   [--message TEXT] [--db PATH]
  plr rollback [--to COMMIT_ID] [--db PATH]
  plr report   [--db PATH]
  plr selftest

Commands:
  scan      Walk directories, record every image (sha256, perceptual hash,
            EXIF capture time) into the SQL library. --phash-alg selects
            the hasher: dct (default, legacy 16x8 when --phash-size is
            omitted), or a Czkawka/image_hasher algorithm — mean, median,
            gradient, vert-gradient, double-gradient, blockhash.
            --phash-size 8|16|32 selects a square hash. Directories already
            scanned are skipped unless --force is given. A .photoslibrary
            directory (Apple Photos bundle) anywhere in the tree is scanned
            only inside its originals/ subdirectory, skipping the
            thumbnails and edited derivatives under resources/;
            --photoslibrary-all scans bundles as-is.
  group     Group images: Aggregate candidate buckets by DCT phash
            (--max-hamming-chunking), then refine with --identity.
            exif-first (the approved model, see
            docs/identity-decision-graph.md): the same sha256 is the same
            image; the dual 16x16 block AND gradient hash gate joins
            every pair, and EXIF only splits one the gate joined — two
            camera blocks (Make + Model) that differ at a capture
            instant within --same-image-window. pass = legacy bucket
            grouping; double-phash = blockhash∩double-gradient by sha256;
            vision = Kronk stub.
  plan      Resolve every group: pixels from the HEIC member when one
            exists, else the highest-resolution member; earliest
            zone-resolved EXIF capture time (file creation time as
            flagged fallback); GPS position medoid, camera-style name.
            Remaining conflicts (GPS positions beyond tolerance, undecodable
            pictures without EXIF, doubtful birth times) are recorded and
            never dropped. With --ai the Kronk decision model settles
            conflict groups and marks them resolved_by the model id.
  apply     Copy the planned pixel files into --target and write the merged
            EXIF (capture time, offset, GPS) onto the copies. With --move the
            source file is deleted once its copy is in place. Writes a
            report.json summary into --target. Conflict groups materialize
            as a plain copy of their best-effort pixels and the ledger
            records the conflict reason.
  build     scan + group + plan + apply in one go.
   increment Merge new photos into an existing library: scan the new dirs
             into the same DB, then partition them against the library
             (present = already there, joined = closest group whose pair
             rule (--identity) matches, new = own group; library groups
             never merge
             through incoming images), re-plan and re-apply. On a fresh
             DB it degrades to build.
  commit    Snapshot the current plan table into the commit log with a
            summary and print the per-group diff (new, updated, removed)
            versus the previous commit. build and increment run this
            automatically after the plan phase.
  rollback  Restore the plan table to an earlier commit (default: the
            latest) and record the restore as a new commit. Scanned images
            are untouched; image group assignments that point at groups the
            restore drops are zeroed.
  report    Print a summary of the library, groups, plans and commit log.
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
	photosLibraryAll := fs.Bool("photoslibrary-all", false, ".photoslibrary bundles (Apple Photos) anywhere in the tree: scan whole bundles instead of only originals/")
	phashAlg := fs.String("phash-alg", "dct", "perceptual hash: dct|mean|median|gradient|vert-gradient|double-gradient|blockhash")
	phashSize := fs.Int("phash-size", 0, "square hash size 8, 16, or 32 (0 = legacy 16x8 DCT)")
	fs.Var(&dirs, "dir", "directory to scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "plr scan: at least one --dir is required")
		return 2
	}
	cfg, err := parsePhashConfig(*phashAlg, *phashSize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr scan:", err)
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
	return runScanPhase(s, dirs, *force, *workers, *minSizeKB, *photosLibraryAll, tool, cfg)
}

// parseDuration parses a duration that may also be given as bare seconds
// ("10" = "10s"), as the spec's max_capture_disagreement accepts.
func parseDuration(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(strings.TrimSpace(s))
}

// parsePhashConfig builds a phash.Config from CLI flags.
func parsePhashConfig(alg string, size int) (phash.Config, error) {
	a, err := phash.ParseAlg(alg)
	if err != nil {
		return phash.Config{}, err
	}
	cfg := phash.Config{Alg: a, Size: phash.Size(size)}
	return cfg.Normalize()
}

// runScanPhase is the scan step, shared by "plr scan" and "plr build".
func runScanPhase(s *store.Store, dirs []string, force bool, workers, minSizeKB int, photosLibraryAll bool, tool *exif.Tool, cfg phash.Config) int {
	hasher := fingerprint.Hasher(cfg, tool)
	rep, err := ingest.Scan(s, ingest.Options{
		Dirs:             dirs,
		Force:            force,
		Workers:          workers,
		MinSize:          int64(minSizeKB) * 1024,
		PhotosLibraryAll: photosLibraryAll,
	}, hasher)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr scan:", err)
		return 1
	}
	fmt.Printf("scanned %d dir(s), skipped %d already scanned\n", rep.DirsScanned, rep.DirsSkipped)
	fmt.Printf("files seen %d: %d added, %d updated, %d cached, %d already-known, %d non-image, %d errors\n",
		rep.FilesSeen, rep.ImagesAdded, rep.ImagesUpdated, rep.ImagesCached, rep.ImagesSkipped, rep.NonImage, rep.Errors)
	return 0
}

func runGroup(args []string) int {
	fs := flag.NewFlagSet("group", flag.ExitOnError)
	maxHamming := fs.Int("max-hamming-chunking", 20, "maximum DCT phash hamming distance for candidate buckets")
	identity := fs.String("identity", "exif-first", "identity strategy: exif-first, pass, double-phash, or vision")
	sameWindowStr := fs.String("same-image-window", "1s", "exif-first: zone-resolved capture-time gap that means the same image; a bare number is seconds")
	blockH := fs.Int("identity-block-hamming", 0, "dual-hash gate: max blockhash-16 distance (0 = strategy default: 10 exif-first, 24 double-phash)")
	gradH := fs.Int("identity-grad-hamming", 0, "dual-hash gate: max double-gradient-16 distance (0 = strategy default: 10 exif-first, 20 double-phash)")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sameWindow, err := parseDuration(*sameWindowStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plr group: invalid --same-image-window %q: %v\n", *sameWindowStr, err)
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	defer s.Close()
	return runGroupPhase(s, groupPhaseOpts{
		MaxHamming: *maxHamming,
		Identity:   *identity,
		SameWindow: sameWindow,
		BlockH:     *blockH,
		GradH:      *gradH,
	})
}

type groupPhaseOpts struct {
	MaxHamming int
	Identity   string
	SameWindow time.Duration
	BlockH     int
	GradH      int
}

// runGroupPhase is the group step, shared by "plr group" and "plr build".
func runGroupPhase(s *store.Store, opts groupPhaseOpts) int {
	imgs, err := s.AllImages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	if len(imgs) == 0 {
		fmt.Println("no images recorded; run plr scan first")
		return 0
	}
	strat, err := identityStrategy(s, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 2
	}
	pipe := group.Pipeline{CandidateRadius: opts.MaxHamming, Identity: strat}
	assign, groups, err := pipe.Run(toGroupImages(imgs))
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
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
	fmt.Printf("%d images -> %d groups (%d with duplicates) [identity=%s]\n",
		len(imgs), len(groups), multi, strat.Name())
	return 0
}

// runIncrementGroupPhase is the S6 merge partition for "plr increment":
// incoming images are grouped among themselves and then classified
// against the library instead of re-aggregating the whole library.
// Present members join the group they duplicate, matched members join the
// closest library group, and unmatched members form new groups. Library
// groups never chain into each other through incoming images.
func runIncrementGroupPhase(s *store.Store, opts groupPhaseOpts, before map[string]bool) int {
	strat, err := identityStrategy(s, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr increment:", err)
		return 2
	}
	imgs, err := s.AllImages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr increment:", err)
		return 1
	}
	var lib []group.LibMember
	var incoming []group.Image
	for _, im := range imgs {
		if before[im.ID] {
			if im.GroupID != 0 {
				lib = append(lib, group.LibMember{Image: toGroupImage(im), GroupID: im.GroupID})
			}
		} else {
			incoming = append(incoming, toGroupImage(im))
		}
	}
	if len(lib) == 0 {
		// Nothing grouped yet (fresh or never-grouped library): build.
		return runGroupPhase(s, opts)
	}
	_, comps := group.Aggregate(incoming, opts.MaxHamming)
	comps = group.SweepCandidates(strat, comps)
	refined := make([][]group.Image, 0, len(comps))
	for _, c := range comps {
		parts, err := strat.Refine(c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr increment:", err)
			return 1
		}
		refined = append(refined, parts...)
	}
	present, matched, unmatched := group.Partition(refined, lib, opts.MaxHamming, sameGate(strat))
	assign := make(map[string]int64, len(incoming))
	ambiguous := 0
	for _, p := range present {
		assign[p.ID] = p.GroupID
		if p.Ambiguous {
			ambiguous++
		}
	}
	for _, m := range matched {
		assign[m.ID] = m.GroupID
		if m.Ambiguous {
			ambiguous++
		}
	}
	next := int64(0)
	for _, m := range lib {
		if m.GroupID > next {
			next = m.GroupID
		}
	}
	for _, comp := range unmatched {
		next++
		for _, im := range comp {
			assign[im.ID] = next
		}
	}
	if err := s.SetGroupAssignments(assign); err != nil {
		fmt.Fprintln(os.Stderr, "plr increment:", err)
		return 1
	}
	fmt.Printf("increment: %d incoming -> %d present, %d joined, %d new (%d ambiguous) [identity=%s]\n",
		len(incoming), len(present), len(matched), len(unmatched), ambiguous, strat.Name())
	if ambiguous > 0 {
		fmt.Printf("warning: %d incoming images match more than one library group; joined the lowest group id\n", ambiguous)
	}
	return 0
}

func identityStrategy(s *store.Store, opts groupPhaseOpts) (group.IdentityStrategy, error) {
	switch opts.Identity {
	case "", "exif-first":
		return group.ExifFirst{
			Lookup:       storePhashLookup{s},
			SameWindow:   opts.SameWindow,
			BlockHamming: opts.BlockH,
			GradHamming:  opts.GradH,
		}, nil
	case "pass":
		return group.PassThrough{}, nil
	case "double-phash":
		return group.DoublePhash{
			Lookup:       storePhashLookup{s},
			BlockHamming: opts.BlockH,
			GradHamming:  opts.GradH,
		}, nil
	case "vision":
		return group.DecisionModel{Decider: group.UnsureDecider{}}, nil
	default:
		return nil, fmt.Errorf("unknown --identity %q (want exif-first, pass, double-phash, or vision)", opts.Identity)
	}
}

// sameGate exposes an identity strategy's pair rule for the incremental
// merge (group.Partition); strategies without one keep the pure hash
// classification.
func sameGate(strat group.IdentityStrategy) group.SameFunc {
	if g, ok := strat.(interface{ SameFunc() group.SameFunc }); ok {
		return g.SameFunc()
	}
	return nil
}

// storePhashLookup adapts store.GetContentPhash to group.ContentPhashLookup.
type storePhashLookup struct{ s *store.Store }

func (l storePhashLookup) Phash(sha256 string, alg phash.Alg, size phash.Size) (phash.Hash, bool, error) {
	hex, ok, err := l.s.GetContentPhash(sha256, string(alg), int(size))
	if err != nil || !ok {
		return nil, ok, err
	}
	h, err := phash.HashFromHex(hex)
	if err != nil {
		return nil, false, err
	}
	return h, true, nil
}

func runPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
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
		Target: *target,
		DryRun: *dryRun,
		NoEXIF: *noEXIF,
		Move:   *move,
		EXIF:   tool,
	})
}

// runApplyPhase is the apply step, shared by "plr apply" and "plr build".
func runApplyPhase(s *store.Store, opts apply.Options) int {
	rep, err := apply.Apply(s, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr apply:", err)
		return 1
	}
	for _, f := range rep.Files {
		line := fmt.Sprintf("%s %s -> %s", applyVerb(f.Action, opts), f.Source, f.Dest)
		if f.EXIF {
			line += " (EXIF)"
		}
		fmt.Println(line)
	}
	for _, e := range rep.Errors {
		fmt.Fprintln(os.Stderr, "plr apply:", e)
	}
	fmt.Printf("apply: %d new, %d updated, %d renamed, %d unchanged\n",
		rep.Copied, rep.Updated, rep.Renamed, rep.Untouched)
	if len(rep.Errors) > 0 {
		return 1
	}
	return 0
}

// applyVerb renders the per-file action for the terminal, in the tense the
// options ask for.
func applyVerb(action string, opts apply.Options) string {
	if opts.DryRun {
		switch action {
		case apply.ActionUntouched:
			return "keep"
		case apply.ActionUpdated:
			return "would rewrite"
		case apply.ActionRenamed:
			return "would rename"
		default: // new
			if opts.Move {
				return "would move"
			}
			return "would copy"
		}
	}
	switch action {
	case apply.ActionUntouched:
		return "unchanged"
	case apply.ActionUpdated:
		return "rewrote"
	case apply.ActionRenamed:
		return "renamed"
	default: // new
		if opts.Move {
			return "moved"
		}
		return "copied"
	}
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
// --max-hamming-chunking radius), re-plan and re-apply. On a fresh DB it degrades
// to build.
func runIncrement(args []string) int { return runPipeline("increment", args) }

func runPipeline(cmd string, args []string) int {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dirs := multiFlag{}
	target := fs.String("target", "", "directory that receives the final images (required)")
	force := fs.Bool("force", false, "re-scan directories that were already scanned")
	maxHamming := fs.Int("max-hamming-chunking", 20, "maximum DCT phash hamming distance for candidate buckets")
	identity := fs.String("identity", "exif-first", "identity strategy: exif-first, pass, double-phash, or vision")
	sameWindowStr := fs.String("same-image-window", "1s", "exif-first: zone-resolved capture-time gap that means the same image; a bare number is seconds")
	blockH := fs.Int("identity-block-hamming", 0, "dual-hash gate: max blockhash-16 distance (0 = strategy default: 10 exif-first, 24 double-phash)")
	gradH := fs.Int("identity-grad-hamming", 0, "dual-hash gate: max double-gradient-16 distance (0 = strategy default: 10 exif-first, 20 double-phash)")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without touching the target")
	noEXIF := fs.Bool("no-exif", false, "copy files without writing merged EXIF")
	move := fs.Bool("move", false, "delete each source file once its copy is in place in --target")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	workers := fs.Int("workers", 4, "number of hash workers")
	minSizeKB := fs.Int("min-size-kb", 0, "ignore images smaller than this many kilobytes")
	photosLibraryAll := fs.Bool("photoslibrary-all", false, ".photoslibrary bundles (Apple Photos) anywhere in the tree: scan whole bundles instead of only originals/")
	phashAlg := fs.String("phash-alg", "dct", "candidate perceptual hash (default dct)")
	phashSize := fs.Int("phash-size", 0, "square DCT size 8, 16, or 32 (0 = legacy 16x8 DCT)")
	fs.Var(&dirs, "dir", "directory to scan (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(dirs) == 0 || *target == "" {
		fmt.Fprintf(os.Stderr, "plr %s: --dir and --target are required\n", cmd)
		return 2
	}
	cfg, err := parsePhashConfig(*phashAlg, *phashSize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr "+cmd+":", err)
		return 2
	}
	sameWindow, err := parseDuration(*sameWindowStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plr %s: invalid --same-image-window %q: %v\n", cmd, *sameWindowStr, err)
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
	// "increment" partitions incoming images against the library as it
	// was BEFORE this scan (S6 merge partition), so snapshot the ids now.
	var libSnapshot map[string]bool
	if cmd == "increment" {
		imgs, err := s.AllImages()
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr increment:", err)
			return 1
		}
		libSnapshot = make(map[string]bool, len(imgs))
		for _, im := range imgs {
			libSnapshot[im.ID] = true
		}
	}
	phases := []struct {
		label string
		fn    func() int
	}{
		{"scan", func() int { return runScanPhase(s, dirs, *force, *workers, *minSizeKB, *photosLibraryAll, tool, cfg) }},
		{"group", func() int {
			opts := groupPhaseOpts{
				MaxHamming: *maxHamming,
				Identity:   *identity,
				SameWindow: sameWindow,
				BlockH:     *blockH,
				GradH:      *gradH,
			}
			if cmd == "increment" && len(libSnapshot) > 0 {
				return runIncrementGroupPhase(s, opts, libSnapshot)
			}
			return runGroupPhase(s, opts)
		}},
		{"plan", func() int { return runPlanPhase(s, nil) }},
		{"commit", func() int { return runCommitPhase(s, cmd) }},
		{"apply", func() int {
			return runApplyPhase(s, apply.Options{
				Target: *target,
				DryRun: *dryRun,
				NoEXIF: *noEXIF,
				Move:   *move,
				EXIF:   tool,
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

func runCommit(args []string) int {
	fs := flag.NewFlagSet("commit", flag.ExitOnError)
	message := fs.String("message", "plan", "commit summary")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr commit:", err)
		return 1
	}
	defer s.Close()
	return runCommitPhase(s, *message)
}

// runCommitPhase snapshots the current plans as a commit and prints the
// diff; build and increment use it after the plan phase.
func runCommitPhase(s *store.Store, message string) int {
	res, err := s.Commit(message)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr commit:", err)
		return 1
	}
	printCommit(res)
	return 0
}

func runRollback(args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	to := fs.Int64("to", 0, "commit id to restore (default: the latest commit)")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr rollback:", err)
		return 1
	}
	defer s.Close()
	target := *to
	if target == 0 {
		commits, err := s.Commits()
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr rollback:", err)
			return 1
		}
		if len(commits) == 0 {
			fmt.Fprintln(os.Stderr, "plr rollback: the commit log is empty; nothing to roll back to")
			return 1
		}
		target = commits[len(commits)-1].ID
	}
	res, err := s.Rollback(target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr rollback:", err)
		return 1
	}
	printCommit(res)
	return 0
}

// printCommit prints a commit id, summary, and one line per group action.
func printCommit(res store.CommitResult) {
	counts := map[string]int{}
	for _, a := range res.Actions {
		counts[a.Action]++
	}
	fmt.Printf("commit %d %q: %d new, %d updated, %d removed\n",
		res.Commit.ID, res.Commit.Summary,
		counts[store.ActionNew], counts[store.ActionUpdated], counts[store.ActionRemoved])
	for _, a := range res.Actions {
		switch a.Action {
		case store.ActionNew:
			fmt.Printf("  group %d: new %q\n", a.GroupID, a.NewName)
		case store.ActionRemoved:
			fmt.Printf("  group %d: removed %q\n", a.GroupID, a.PrevName)
		default:
			fmt.Printf("  group %d: updated %q -> %q\n", a.GroupID, a.PrevName, a.NewName)
		}
	}
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

	commits, err := s.Commits()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr report:", err)
		return 1
	}
	if len(commits) > 0 {
		start := max(len(commits)-5, 0)
		fmt.Println("\ncommits:")
		for _, c := range commits[start:] {
			actions, err := s.CommitActions(c.ID)
			if err != nil {
				fmt.Fprintln(os.Stderr, "plr report:", err)
				return 1
			}
			actionCounts := map[string]int{}
			for _, a := range actions {
				actionCounts[a.Action]++
			}
			fmt.Printf("  #%d %s %q (%d new, %d updated, %d removed)\n",
				c.ID, time.Unix(c.At, 0).Format("2006-01-02 15:04:05"), c.Summary,
				actionCounts[store.ActionNew], actionCounts[store.ActionUpdated], actionCounts[store.ActionRemoved])
		}
		if start > 0 {
			fmt.Printf("  ... %d older commit(s)\n", start)
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
		ExifCamera:  im.ExifCamera,
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
