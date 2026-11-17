package filter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	classifyManifestName = "classify-manifest.jsonl"
	denyManifestName     = "manifest.jsonl"
)

// FlattenOpts configures FlattenClassify and RestoreDenied.
type FlattenOpts struct {
	DryRun bool
	Out    io.Writer
	// OnMoves updates the apply ledger after a successful (non-dry-run) flatten.
	// FromRel is the classified path; ToRel is the restored flat path.
	// Not used by RestoreDenied (drop cleared those ledger rows).
	OnMoves func([]ClassifyMove) error
}

// FlattenReport summarizes a flatten or restore-denied run.
type FlattenReport struct {
	Moved   int
	Missing int
	Errors  int
	Moves   []ClassifyMove
}

// FlattenClassify reverses classify-manifest.jsonl moves so files sit at their
// pre-classify paths again (post-apply flat layout). Denied/ is untouched;
// use RestoreDenied for that. After a successful non-dry-run, the manifest is removed.
func FlattenClassify(root string, opts FlattenOpts) (FlattenReport, error) {
	var rep FlattenReport
	root, err := filepath.Abs(root)
	if err != nil {
		return rep, err
	}
	manPath := filepath.Join(root, classifyManifestName)
	entries, err := readClassifyManifest(manPath)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, err
	}
	if len(entries) == 0 {
		return rep, nil
	}

	// Newest first so nested label moves unwind cleanly if they ever appear.
	for _, e := range slices.Backward(entries) {

		if e.Dst == "" || e.Src == "" {
			continue
		}
		if _, err := os.Stat(e.Dst); err != nil {
			if os.IsNotExist(err) {
				rep.Missing++
				continue
			}
			rep.Errors++
			continue
		}
		fromRel, err := relUnder(root, e.Dst)
		if err != nil {
			rep.Errors++
			continue
		}
		toRel, err := relUnder(root, e.Src)
		if err != nil {
			// Src may be outside root in a broken manifest; fall back to basename at root.
			toRel = filepath.Base(e.Src)
		}
		dstRestore := filepath.Join(root, filepath.FromSlash(toRel))
		if opts.Out != nil {
			fmt.Fprintf(opts.Out, "%s -> %s  flatten label=%s\n", e.Dst, dstRestore, e.Label)
		}
		if opts.DryRun {
			rep.Moved++
			rep.Moves = append(rep.Moves, ClassifyMove{
				FromRel: fromRel,
				ToRel:   toRel,
				Label:   e.Label,
				Rule:    e.Rule,
			})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dstRestore), 0o755); err != nil {
			rep.Errors++
			continue
		}
		final, err := uniquePath(dstRestore)
		if err != nil {
			rep.Errors++
			continue
		}
		if err := renameOrCopy(nil, e.Dst, final); err != nil {
			rep.Errors++
			continue
		}
		if rel2, rerr := relUnder(root, final); rerr == nil {
			toRel = rel2
		}
		pruneEmptyParents(e.Dst, root)
		rep.Moved++
		rep.Moves = append(rep.Moves, ClassifyMove{
			FromRel: fromRel,
			ToRel:   toRel,
			Label:   e.Label,
			Rule:    e.Rule,
		})
	}

	if opts.DryRun {
		return rep, nil
	}
	if opts.OnMoves != nil && len(rep.Moves) > 0 {
		if err := opts.OnMoves(rep.Moves); err != nil {
			return rep, err
		}
	}
	if rep.Errors == 0 {
		_ = os.Remove(manPath)
	}
	return rep, nil
}

// RestoreDenied reverses denied-dir/manifest.jsonl moves so dropped files
// return to their pre-drop paths. After a successful non-dry-run, the
// manifest is removed and empty rule folders under denied-dir are pruned.
func RestoreDenied(deniedDir string, opts FlattenOpts) (FlattenReport, error) {
	var rep FlattenReport
	deniedDir, err := filepath.Abs(deniedDir)
	if err != nil {
		return rep, err
	}
	manPath := filepath.Join(deniedDir, denyManifestName)
	entries, err := readDenyManifest(manPath)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, err
	}
	if len(entries) == 0 {
		return rep, nil
	}

	for _, e := range slices.Backward(entries) {
		if e.Dst == "" || e.Src == "" {
			continue
		}
		if _, err := os.Stat(e.Dst); err != nil {
			if os.IsNotExist(err) {
				rep.Missing++
				continue
			}
			rep.Errors++
			continue
		}
		if opts.Out != nil {
			fmt.Fprintf(opts.Out, "%s -> %s  restore-denied rule=%s\n", e.Dst, e.Src, e.Rule)
		}
		if opts.DryRun {
			rep.Moved++
			rep.Moves = append(rep.Moves, ClassifyMove{
				FromRel: e.Dst,
				ToRel:   e.Src,
				Rule:    e.Rule,
			})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(e.Src), 0o755); err != nil {
			rep.Errors++
			continue
		}
		final, err := uniquePath(e.Src)
		if err != nil {
			rep.Errors++
			continue
		}
		if err := renameOrCopy(nil, e.Dst, final); err != nil {
			rep.Errors++
			continue
		}
		pruneEmptyParents(e.Dst, deniedDir)
		rep.Moved++
		rep.Moves = append(rep.Moves, ClassifyMove{
			FromRel: e.Dst,
			ToRel:   final,
			Rule:    e.Rule,
		})
	}

	if opts.DryRun {
		return rep, nil
	}
	if rep.Errors == 0 {
		_ = os.Remove(manPath)
		_ = os.Remove(deniedDir) // only if empty
	}
	return rep, nil
}

func readDenyManifest(path string) ([]ManifestEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ManifestEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ManifestEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func readClassifyManifest(path string) ([]ClassifyManifestEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []ClassifyManifestEntry
	sc := bufio.NewScanner(f)
	// Images paths can be long; raise token limit a bit.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e ClassifyManifestEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

func relUnder(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s not under %s", abs, root)
	}
	return filepath.ToSlash(rel), nil
}

func pruneEmptyParents(filePath, root string) {
	dir := filepath.Dir(filePath)
	root = filepath.Clean(root)
	for {
		if filepath.Clean(dir) == root {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
