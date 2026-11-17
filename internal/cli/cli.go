// Package cli implements the plr command line interface.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"photo-library-repair/internal/apply"
	"photo-library-repair/internal/exif"
	"photo-library-repair/internal/filter"
	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/group"
	"photo-library-repair/internal/ingest"
	kronkai "photo-library-repair/internal/kronk"
	"photo-library-repair/internal/phash"
	"photo-library-repair/internal/progress"
	"photo-library-repair/internal/selftest"
	"photo-library-repair/internal/store"
)

// Run executes the plr command line and returns a process exit code.
func Run(args []string) int {
	fs := flag.NewFlagSet("plr", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cpuProfile := fs.String("cpuprofile", "", "write CPU profile to file")
	memProfile := fs.String("memprofile", "", "write memory profile to file")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			usage(os.Stdout)
			return 0
		}
		return 2
	}
	args = fs.Args()
	stopProfile, err := startProfiles(*cpuProfile, *memProfile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr:", err)
		return 1
	}
	defer stopProfile()

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
	case "filter":
		return runFilter(rest)
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
  plr [-cpuprofile FILE] [-memprofile FILE] <command> ...

  plr scan     --dir DIR... [--force] [--db PATH] [--workers N] [--min-size-kb N]
               [--photoslibrary-all] [--phash-alg ALG] [--phash-size 8|16|32]
  plr group    [--max-hamming-chunking N] [--identity exif-first|pass|double-phash]
               [--same-image-window DUR] [--db PATH]
  plr plan     [--ai] [--model SOURCE] [--db PATH]
  plr apply    --target DIR [--dry-run] [--no-exif] [--move] [--strict] [--db PATH]
               [--filter-rules PATH] [--denied-dir PATH]
  plr filter   --dir DIR --filter-rules PATH [--denied-dir PATH] [--dry-run]
               [--db PATH] [--explain FILE] [--workers N] [--limit N]
               [--no-model] [--model SOURCE]
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
            grouping; double-phash = blockhash∩double-gradient by sha256.
            --identity vision is rejected until a real VisionDecider ships.
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
            records the conflict reason. --strict refuses to write when any
            unresolved conflict remains (default off). Optional
            --filter-rules skips materializing drops and moves an existing
            target file to --denied-dir (default <target>/denied).
  filter    Walk a materialized library (--dir), apply YAML firewall rules,
            and move drops to --denied-dir (default <dir>/denied). Then
            classify survivors (action: classify) with a Kronk vision model
            unless --no-model. Clears drop ledger rows and updates ledger
            paths for classify moves in --db (default plr.db).
            --flatten restores classified files to their pre-classify paths
            via classify-manifest.jsonl (no rules needed). --denied restores
            drops from denied-dir/manifest.jsonl (default <dir>/denied).
            --explain FILE prints per-rule match detail for one file.
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
	return runScanPhaseCounted(s, scanPhaseOpts{
		Dirs:             dirs,
		Force:            *force,
		Workers:          *workers,
		MinSizeKB:        *minSizeKB,
		PhotosLibraryAll: *photosLibraryAll,
		Tool:             tool,
		Cfg:              cfg,
	}, nil)
}

func loadFilterRules(path string) (*filter.Ruleset, error) {
	if path == "" {
		return nil, nil
	}
	return filter.Load(path)
}

func filterExifLoader(tool *exif.Tool) filter.ExifLoader {
	if tool == nil {
		return nil
	}
	return func(path string) (map[string]string, error) {
		m, err := tool.Read(path)
		if err != nil {
			return nil, err
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
		return out, nil
	}
}

func runFilterRestore(dir, dbPath, deniedDir string, dryRun, doFlatten, doDenied bool) int {
	var out io.Writer
	if dryRun {
		out = os.Stdout
	}
	opts := filter.FlattenOpts{DryRun: dryRun, Out: out}
	exit := 0
	if doFlatten {
		if !dryRun {
			s, err := store.Open(dbPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, "plr filter:", err)
				return 1
			}
			defer s.Close()
			opts.OnMoves = func(moves []filter.ClassifyMove) error {
				pm := make([]apply.PathMove, len(moves))
				for i, m := range moves {
					pm[i] = apply.PathMove{From: m.FromRel, To: m.ToRel}
				}
				_, err := apply.UpdateMovedOutputs(s, dir, pm)
				return err
			}
		}
		rep, err := filter.FlattenClassify(dir, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr filter:", err)
			return 1
		}
		fmt.Printf("flatten: moved=%d", rep.Moved)
		if rep.Missing > 0 {
			fmt.Printf(", missing=%d", rep.Missing)
		}
		if rep.Errors > 0 {
			fmt.Printf(", errors=%d", rep.Errors)
			exit = 1
		}
		fmt.Println()
	}
	if doDenied {
		if deniedDir == "" {
			deniedDir = filepath.Join(dir, "denied")
		}
		rep, err := filter.RestoreDenied(deniedDir, filter.FlattenOpts{DryRun: dryRun, Out: out})
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr filter:", err)
			return 1
		}
		fmt.Printf("denied: restored=%d", rep.Moved)
		if rep.Missing > 0 {
			fmt.Printf(", missing=%d", rep.Missing)
		}
		if rep.Errors > 0 {
			fmt.Printf(", errors=%d", rep.Errors)
			exit = 1
		}
		fmt.Println()
	}
	return exit
}

func runFilterExplain(rulesPath, file string) int {
	rs, err := filter.Load(rulesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr filter:", err)
		return 2
	}
	info, err := filter.LoadImageInfo(file, file, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr filter:", err)
		return 1
	}
	dec, steps := rs.Explain(info)
	fmt.Printf("file %s  format=%s %dx%d size=%d\n", file, info.Format, info.Width, info.Height, info.Size)
	for _, s := range steps {
		fmt.Printf("  rule=%s matched=%v action=%s %s\n", s.Rule, s.Matched, s.Action, s.Detail)
	}
	fmt.Printf("decision action=%s rule=%s tags=%v\n", dec.Action, dec.Rule, dec.Tags)
	return 0
}

func runFilter(args []string) int {
	fs := flag.NewFlagSet("filter", flag.ExitOnError)
	dir := fs.String("dir", "", "materialized library directory to filter")
	filterRules := fs.String("filter-rules", "", "YAML firewall rules (required unless --flatten/--denied)")
	deniedDir := fs.String("denied-dir", "", "where dropped images are moved (default: <dir>/denied)")
	dryRun := fs.Bool("dry-run", false, "print planned drops/classify without moving files")
	db := fs.String("db", "plr.db", "SQLite library whose apply ledger is updated for moves")
	explain := fs.String("explain", "", "print per-rule match detail for one file and exit")
	flatten := fs.Bool("flatten", false, "restore classified files to flat pre-classify paths (classify-manifest.jsonl)")
	restoreDenied := fs.Bool("denied", false, "restore dropped files from denied-dir/manifest.jsonl")
	workers := fs.Int("workers", 4, "parallel filter/classify workers (also classify batch size)")
	limit := fs.Int("limit", 0, "max images to process after walk (0 = all)")
	noModel := fs.Bool("no-model", false, "skip classify rules (drop/accept/tag only)")
	modelSource := fs.String("model", kronkai.DefaultVisionModel, "Kronk vision model for classify rules")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *explain != "" {
		if *filterRules == "" {
			fmt.Fprintln(os.Stderr, "plr filter: --explain requires --filter-rules")
			return 2
		}
		return runFilterExplain(*filterRules, *explain)
	}
	if *flatten || *restoreDenied {
		if *dir == "" {
			fmt.Fprintln(os.Stderr, "plr filter: --flatten/--denied requires --dir")
			return 2
		}
		return runFilterRestore(*dir, *db, *deniedDir, *dryRun, *flatten, *restoreDenied)
	}
	if *dir == "" || *filterRules == "" {
		fmt.Fprintln(os.Stderr, "plr filter: --dir and --filter-rules are required")
		return 2
	}
	rs, err := loadFilterRules(*filterRules)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr filter:", err)
		return 2
	}
	denied := *deniedDir
	if denied == "" {
		denied = filepath.Join(*dir, "denied")
	}
	var out io.Writer
	if *dryRun {
		out = os.Stdout
	}
	denier := filter.NewDenier(denied, *dryRun, out)
	defer denier.Close()
	tool := openEXIF("filter")
	if tool != nil {
		defer tool.Close()
	}
	var exifLoad filter.ExifLoader
	if rs.NeedsEXIF {
		exifLoad = filterExifLoader(tool)
	}

	opts := filter.DirOpts{Workers: *workers, Limit: *limit, Exif: exifLoad}
	dropProg := progress.New("filter")
	defer dropProg.Stop()
	opts.OnDropProgress = func(_, total int64) { dropProg.SetTotal(total); dropProg.Add(1) }

	ctx := context.Background()
	var classProg *progress.Reporter
	if rs.HasClassify && !*noModel {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		vc, closeFn, err := kronkai.NewVisionClassifier(ctx, *modelSource)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr filter:", err)
			return 1
		}
		defer closeFn()
		opts.Classifier = vc
		opts.ClassMover = &filter.ClassMover{Root: *dir, DryRun: *dryRun, Out: out}
		defer opts.ClassMover.Close()
		fmt.Printf("vision model %s loaded; classify enabled\n", vc.Name())

		classProg = progress.New("classify")
		defer classProg.Stop()
		opts.OnClassifyProgress = func(_, total int64) { classProg.SetTotal(total); classProg.Add(1) }

		if !*dryRun {
			s, err := store.Open(*db)
			if err != nil {
				fmt.Fprintln(os.Stderr, "plr filter:", err)
				return 1
			}
			defer s.Close()
			opts.OnClassifyBatch = func(moves []filter.ClassifyMove) error {
				pm := make([]apply.PathMove, len(moves))
				for i, m := range moves {
					pm[i] = apply.PathMove{From: m.FromRel, To: m.ToRel}
				}
				_, err := apply.UpdateMovedOutputs(s, *dir, pm)
				return err
			}
		}
	}

	rep, err := filter.FilterDir(ctx, *dir, rs, denier, opts)
	dropProg.Stop()
	if classProg != nil {
		classProg.Stop()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr filter:", err)
		return 1
	}
	cleared := 0
	if !*dryRun && len(rep.DropRels) > 0 {
		s, err := store.Open(*db)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr filter:", err)
			return 1
		}
		cleared, err = apply.ClearDroppedOutputs(s, *dir, rep.DropRels)
		s.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr filter: ledger:", err)
			return 1
		}
	}
	fmt.Printf("filter: seen %d", rep.Seen)
	if len(rep.Dropped) > 0 {
		keys := make([]string, 0, len(rep.Dropped))
		for k := range rep.Dropped {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf(", dropped")
		for _, k := range keys {
			fmt.Printf(" %s=%d", k, rep.Dropped[k])
		}
	}
	if len(rep.Classified) > 0 {
		keys := make([]string, 0, len(rep.Classified))
		for k := range rep.Classified {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf(", classified")
		for _, k := range keys {
			fmt.Printf(" %s=%d", k, rep.Classified[k])
		}
	}
	if cleared > 0 {
		fmt.Printf(", ledger_cleared=%d", cleared)
	}
	if rep.Errors > 0 {
		fmt.Printf(", errors=%d", rep.Errors)
	}
	fmt.Println()
	if rep.Errors > 0 {
		return 1
	}
	return 0
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

type scanPhaseOpts struct {
	Dirs             []string
	Force            bool
	Workers          int
	MinSizeKB        int
	PhotosLibraryAll bool
	Tool             *exif.Tool
	Cfg              phash.Config
}

// runScanPhaseCounted is the scan step, shared by "plr scan" and "plr build".
func runScanPhaseCounted(s *store.Store, opts scanPhaseOpts, stages *apply.StageCounters) int {
	hasher := fingerprint.Hasher(opts.Cfg, opts.Tool)
	prog := progress.New("scan")
	defer prog.Stop()
	rep, err := ingest.Scan(s, ingest.Options{
		Dirs:             opts.Dirs,
		Force:            opts.Force,
		Workers:          opts.Workers,
		MinSize:          int64(opts.MinSizeKB) * 1024,
		PhotosLibraryAll: opts.PhotosLibraryAll,
		OnProgress:       func(done, total int64) { prog.SetTotal(total); prog.Add(1) },
	}, hasher)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr scan:", err)
		return 1
	}
	prog.Stop()
	if stages != nil {
		stages.ScanCached = rep.ImagesCached
		stages.ScanAlreadyKnown = rep.ImagesSkipped
		stages.ScanAdded = rep.ImagesAdded
		stages.ScanUpdated = rep.ImagesUpdated
	}
	fmt.Printf("scanned %d dir(s), skipped %d already scanned\n", rep.DirsScanned, rep.DirsSkipped)
	fmt.Printf("files seen %d: %d added, %d updated, %d cached, %d already-known, %d non-image, %d errors\n",
		rep.FilesSeen, rep.ImagesAdded, rep.ImagesUpdated, rep.ImagesCached, rep.ImagesSkipped, rep.NonImage, rep.Errors)
	return 0
}

func runGroup(args []string) int {
	fs := flag.NewFlagSet("group", flag.ExitOnError)
	maxHamming := fs.Int("max-hamming-chunking", 20, "maximum DCT phash hamming distance for candidate buckets")
	identity := fs.String("identity", "exif-first", "identity strategy: exif-first, pass, or double-phash")
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
	t0 := time.Now()
	assign, groups, err := pipe.Run(toGroupImages(imgs))
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "plr: group Pipeline %s (%d images)\n",
		time.Since(t0).Round(time.Millisecond), len(imgs))
	if err := s.SetGroupAssignments(assign); err != nil {
		fmt.Fprintln(os.Stderr, "plr group:", err)
		return 1
	}
	if err := s.DeletePlans(); err != nil {
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
func runIncrementGroupPhaseCounted(s *store.Store, opts groupPhaseOpts, before map[string]bool, stages *apply.StageCounters) int {
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
		switch {
		case !before[im.ID]:
			// Added by this scan.
			incoming = append(incoming, toGroupImage(im))
		case im.GroupID == 0:
			// Already in the library but never grouped (e.g. a prior
			// increment crashed after scan). Treat as incoming so a
			// retry finishes the merge instead of dropping them.
			incoming = append(incoming, toGroupImage(im))
		default:
			lib = append(lib, group.LibMember{Image: toGroupImage(im), GroupID: im.GroupID})
		}
	}
	if len(lib) == 0 {
		// Nothing grouped yet (fresh or never-grouped library): build.
		return runGroupPhase(s, opts)
	}
	tAgg := time.Now()
	_, comps := group.Aggregate(incoming, opts.MaxHamming)
	fmt.Fprintf(os.Stderr, "plr: increment Aggregate %s (%d incoming)\n",
		time.Since(tAgg).Round(time.Millisecond), len(incoming))
	comps = group.SweepCandidates(strat, comps)
	tRef := time.Now()
	refined := make([][]group.Image, 0, len(comps))
	for _, c := range comps {
		parts, err := strat.Refine(c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr increment:", err)
			return 1
		}
		refined = append(refined, parts...)
	}
	fmt.Fprintf(os.Stderr, "plr: increment Refine %s (%d components)\n",
		time.Since(tRef).Round(time.Millisecond), len(comps))
	var pstats group.PartitionStats
	tPart := time.Now()
	present, matched, unmatched := group.PartitionWithStats(refined, lib, opts.MaxHamming, sameGate(strat), &pstats)
	fmt.Fprintf(os.Stderr, "plr: increment Partition %s (incoming=%d sha_hits=%d candidates=%d same_calls=%d lib=%d)\n",
		time.Since(tPart).Round(time.Millisecond),
		pstats.Incoming, pstats.SHAHits, pstats.Candidates, pstats.SameCalls, pstats.LibMembers)
	assign := make(map[string]int64, len(incoming))
	ambiguous := 0
	ambGroups := map[int64]bool{}
	for _, p := range present {
		assign[p.ID] = p.GroupID
		if p.Ambiguous {
			ambiguous++
			ambGroups[p.GroupID] = true
		}
	}
	for _, m := range matched {
		assign[m.ID] = m.GroupID
		if m.Ambiguous {
			ambiguous++
			ambGroups[m.GroupID] = true
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
	if err := s.DeletePlans(); err != nil {
		fmt.Fprintln(os.Stderr, "plr increment:", err)
		return 1
	}
	ambIDs := make([]int64, 0, len(ambGroups))
	for id := range ambGroups {
		ambIDs = append(ambIDs, id)
	}
	slices.Sort(ambIDs)
	if err := s.ReplaceAmbiguousGroups(ambIDs); err != nil {
		fmt.Fprintln(os.Stderr, "plr increment:", err)
		return 1
	}
	if stages != nil {
		stages.Present = len(present)
		stages.Joined = len(matched)
		stages.New = len(unmatched)
		stages.Ambiguous = ambiguous
		stages.PartitionSHAHits = pstats.SHAHits
		stages.PartitionCandidates = pstats.Candidates
		stages.PartitionSameCalls = pstats.SameCalls
	}
	fmt.Printf("increment: %d incoming -> %d present, %d joined, %d new (%d ambiguous) [identity=%s]\n",
		len(incoming), len(present), len(matched), len(unmatched), ambiguous, strat.Name())
	if ambiguous > 0 {
		fmt.Printf("warning: %d incoming images match more than one library group; joined the lowest group id (plan conflict ambiguous_merge)\n", ambiguous)
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
		return nil, fmt.Errorf("--identity vision is not implemented")
	default:
		return nil, fmt.Errorf("unknown --identity %q (want exif-first, pass, or double-phash)", opts.Identity)
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
		// Ctrl-C cancels model download and in-flight decision calls.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		res, close, err = kronkai.New(ctx, *modelSource)
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
	ambiguous, err := s.AmbiguousGroups()
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	counts := map[string]int{}
	ids := make([]int64, 0, len(byGroup))
	for id := range byGroup {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	prog := progress.New("plan")
	prog.SetTotal(int64(len(ids)))
	defer prog.Stop()
	aiSettled := 0
	plans := make([]store.Group, 0, len(ids))
	for _, id := range ids {
		p := group.ResolveGroup(byGroup[id], res)
		if ambiguous[id] {
			p = group.MarkAmbiguousMerge(p)
		}
		if p.ResolvedBy != group.Rules {
			aiSettled++
		}
		plans = append(plans, store.Group{
			ID:              id,
			Status:          p.Status,
			Reason:          p.Reason,
			ConflictKind:    p.Kind,
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
		})
		counts[p.Status]++
		prog.Add(1)
	}
	if err := s.ReplacePlans(plans, assign); err != nil {
		fmt.Fprintln(os.Stderr, "plr plan:", err)
		return 1
	}
	if err := s.ClearAmbiguousGroups(); err != nil {
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
	strict := fs.Bool("strict", false, "refuse to materialize when unresolved conflict groups remain")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	filterRules := fs.String("filter-rules", "", "YAML firewall rules; omit to disable filtering")
	deniedDir := fs.String("denied-dir", "", "where filter drops are moved (default: <target>/denied)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "plr apply: --target is required")
		return 2
	}
	rs, err := loadFilterRules(*filterRules)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr apply:", err)
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
	var exifLoad filter.ExifLoader
	if rs != nil && rs.NeedsEXIF {
		exifLoad = filterExifLoader(tool)
	}
	return runApplyPhase(s, apply.Options{
		Target:    *target,
		DryRun:    *dryRun,
		NoEXIF:    *noEXIF,
		Move:      *move,
		Strict:    *strict,
		EXIF:      tool,
		Filter:    rs,
		DeniedDir: *deniedDir,
		ExifLoad:  exifLoad,
	})
}

// runApplyPhase is the apply step, shared by "plr apply" and "plr build".
func runApplyPhase(s *store.Store, opts apply.Options) int {
	prog := progress.New("apply")
	defer prog.Stop()
	opts.OnProgress = func(done, total int64) { prog.SetTotal(total); prog.Add(1) }
	rep, err := apply.Apply(s, opts)
	prog.Stop()
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
	if len(rep.FilterDropped) > 0 {
		keys := make([]string, 0, len(rep.FilterDropped))
		for k := range rep.FilterDropped {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("filter dropped:")
		for _, k := range keys {
			fmt.Printf(" %s=%d", k, rep.FilterDropped[k])
		}
		fmt.Println()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "plr apply:", err)
		return 1
	}
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
		case apply.ActionDropped:
			return "would drop"
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
	case apply.ActionDropped:
		return "dropped"
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
	identity := fs.String("identity", "exif-first", "identity strategy: exif-first, pass, or double-phash")
	sameWindowStr := fs.String("same-image-window", "1s", "exif-first: zone-resolved capture-time gap that means the same image; a bare number is seconds")
	blockH := fs.Int("identity-block-hamming", 0, "dual-hash gate: max blockhash-16 distance (0 = strategy default: 10 exif-first, 24 double-phash)")
	gradH := fs.Int("identity-grad-hamming", 0, "dual-hash gate: max double-gradient-16 distance (0 = strategy default: 10 exif-first, 20 double-phash)")
	dryRun := fs.Bool("dry-run", false, "report what would be copied / filter-dropped without touching files")
	noEXIF := fs.Bool("no-exif", false, "copy files without writing merged EXIF")
	move := fs.Bool("move", false, "delete each source file once its copy is in place in --target")
	strict := fs.Bool("strict", false, "refuse to materialize when unresolved conflict groups remain")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	workers := fs.Int("workers", 4, "number of hash workers")
	minSizeKB := fs.Int("min-size-kb", 0, "ignore images smaller than this many kilobytes")
	photosLibraryAll := fs.Bool("photoslibrary-all", false, ".photoslibrary bundles (Apple Photos) anywhere in the tree: scan whole bundles instead of only originals/")
	phashAlg := fs.String("phash-alg", "dct", "candidate perceptual hash (default dct)")
	phashSize := fs.Int("phash-size", 0, "square DCT size 8, 16, or 32 (0 = legacy 16x8 DCT)")
	filterRules := fs.String("filter-rules", "", "YAML firewall rules applied during apply; omit to disable")
	deniedDir := fs.String("denied-dir", "", "where filter drops are moved (default: <target>/denied)")
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
	rs, err := loadFilterRules(*filterRules)
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
	stages := &apply.StageCounters{}
	identityName := *identity
	if identityName == "" {
		identityName = "exif-first"
	}
	phases := []struct {
		label string
		fn    func() int
	}{
		{"scan", func() int {
			return runScanPhaseCounted(s, scanPhaseOpts{
				Dirs:             dirs,
				Force:            *force,
				Workers:          *workers,
				MinSizeKB:        *minSizeKB,
				PhotosLibraryAll: *photosLibraryAll,
				Tool:             tool,
				Cfg:              cfg,
			}, stages)
		}},
		{"group", func() int {
			opts := groupPhaseOpts{
				MaxHamming: *maxHamming,
				Identity:   *identity,
				SameWindow: sameWindow,
				BlockH:     *blockH,
				GradH:      *gradH,
			}
			if cmd == "increment" && len(libSnapshot) > 0 {
				return runIncrementGroupPhaseCounted(s, opts, libSnapshot, stages)
			}
			return runGroupPhase(s, opts)
		}},
		{"plan", func() int { return runPlanPhase(s, nil) }},
		{"commit", func() int { return runCommitPhase(s, cmd) }},
		{"apply", func() int {
			var exifLoad filter.ExifLoader
			if rs != nil && rs.NeedsEXIF {
				exifLoad = filterExifLoader(tool)
			}
			return runApplyPhase(s, apply.Options{
				Target:    *target,
				DryRun:    *dryRun,
				NoEXIF:    *noEXIF,
				Move:      *move,
				Strict:    *strict,
				Identity:  identityName,
				Stages:    stages,
				EXIF:      tool,
				Filter:    rs,
				DeniedDir: *deniedDir,
				ExifLoad:  exifLoad,
			})
		}},
	}
	for _, p := range phases {
		fmt.Printf("== %s\n", p.label)
		start := time.Now()
		if code := p.fn(); code != 0 {
			fmt.Fprintf(os.Stderr, "plr %s: %s phase failed\n", cmd, p.label)
			return code
		}
		fmt.Fprintf(os.Stderr, "plr: %s phase %s\n", p.label, time.Since(start).Round(time.Millisecond))
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
			if g.ConflictKind != "" {
				fmt.Printf("  #%d [%s] %s\n", g.ID, g.ConflictKind, g.Reason)
			} else {
				fmt.Printf("  #%d %s\n", g.ID, g.Reason)
			}
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

// startProfiles enables optional CPU and heap profiles. The returned
// stop function flushes both; it is safe to call when neither path was set.
func startProfiles(cpuPath, memPath string) (func(), error) {
	var cpuFile *os.File
	if cpuPath != "" {
		f, err := os.Create(cpuPath)
		if err != nil {
			return nil, fmt.Errorf("cpuprofile: %w", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			return nil, fmt.Errorf("cpuprofile: %w", err)
		}
		cpuFile = f
	}
	return func() {
		if cpuFile != nil {
			pprof.StopCPUProfile()
			cpuFile.Close()
		}
		if memPath == "" {
			return
		}
		f, err := os.Create(memPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "plr: memprofile:", err)
			return
		}
		defer f.Close()
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "plr: memprofile:", err)
		}
	}, nil
}

// multiFlag collects repeated occurrences of a string flag.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
