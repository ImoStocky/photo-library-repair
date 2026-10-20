// Package cli implements the plr command line interface.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
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
  scan      Walk directories, record every image (sha256, 16-bit perceptual
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
	_ = force
	_ = db
	_ = workers
	_ = minSizeKB
	return notImplemented("scan")
}

func runGroup(args []string) int {
	fs := flag.NewFlagSet("group", flag.ExitOnError)
	maxHamming := fs.Int("max-hamming", 6, "maximum perceptual-hash hamming distance to chain images into one group")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = maxHamming
	_ = db
	return notImplemented("group")
}

func runPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	conflictHamming := fs.Int("conflict-hamming", 2, "perceptual hashes within this distance with identical capture times mark the group as a conflict")
	db := fs.String("db", "plr.db", "path to the SQLite library")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = conflictHamming
	_ = db
	return notImplemented("plan")
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
	_ = dryRun
	_ = noEXIF
	_ = forceConflicts
	_ = db
	return notImplemented("apply")
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

// multiFlag collects repeated occurrences of a string flag.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
