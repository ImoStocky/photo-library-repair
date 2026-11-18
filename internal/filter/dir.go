package filter

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// DirReport summarizes a FilterDir run against a materialized library.
type DirReport struct {
	Seen       int
	Dropped    map[string]int // rule name → count
	DropRels   []string       // library-relative paths that were moved (or dry-run planned)
	Classified map[string]int // label → count
	Moves      []ClassifyMove // classify relocates (including dry-run)
	Errors     int
}

// DirOpts configures FilterDir workers, EXIF, and the optional classify phase.
type DirOpts struct {
	Workers         int
	Limit           int // max images to process after walk; 0 = no limit
	Exif            ExifLoader
	Classifier      Classifier // nil skips classify (e.g. --no-model)
	ClassMover      *ClassMover
	OnClassifyBatch func([]ClassifyMove) error // called after each finished batch of moves
	// OnDropProgress reports (done, total) as each image finishes the drop/accept phase.
	OnDropProgress func(done, total int64)
	// OnClassifyProgress reports (done, total) as each survivor finishes classify.
	OnClassifyProgress func(done, total int64)
}

var libraryExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".tif": true, ".tiff": true,
	".heic": true, ".heif": true,
}

type walkItem struct {
	path string
	rel  string
	size int64
}

// FilterDir walks a materialized library root: phase 1 applies drop/accept/tag;
// phase 2 classifies survivors in batches (when Classifier is set). Denied and
// category folders are skipped on the walk.
func FilterDir(ctx context.Context, root string, rs *Ruleset, denier *Denier, opts DirOpts) (*DirReport, error) {
	if rs == nil {
		return nil, fmt.Errorf("filter rules required")
	}
	if denier == nil {
		return nil, fmt.Errorf("denier required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workers := max(opts.Workers, 1)
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := denier.EnsureDir(); err != nil {
		return nil, err
	}

	skipDirs := rs.CategoryDirs()
	if skipDirs == nil {
		skipDirs = map[string]bool{}
	}

	var items []walkItem
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if IsUnder(path, denier.DeniedDir) {
				return filepath.SkipDir
			}
			base := d.Name()
			if skipDirs[base] {
				return filepath.SkipDir
			}
			return nil
		}
		if IsUnder(path, denier.DeniedDir) {
			return nil
		}
		if strings.EqualFold(d.Name(), "report.json") ||
			strings.EqualFold(d.Name(), classifyManifestName) ||
			strings.EqualFold(d.Name(), "manifest.jsonl") {
			return nil
		}
		if !libraryExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		// Already under a category folder (nested walk edge): skip.
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return nil
		}
		if underCategory(rel, skipDirs) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		items = append(items, walkItem{path: path, rel: rel, size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if opts.Limit > 0 && len(items) > opts.Limit {
		items = items[:opts.Limit]
	}

	rep := &DirReport{
		Seen:       len(items),
		Dropped:    map[string]int{},
		Classified: map[string]int{},
	}
	if len(items) == 0 {
		return rep, nil
	}

	dropped := make(map[string]bool)
	if err := phaseDrop(ctx, items, rs, denier, opts.Exif, workers, rep, dropped, opts.OnDropProgress); err != nil {
		return rep, err
	}
	if opts.Classifier == nil || !rs.HasClassify || opts.ClassMover == nil {
		return rep, nil
	}

	var survivors []walkItem
	for _, it := range items {
		if !dropped[it.rel] {
			survivors = append(survivors, it)
		}
	}
	if err := phaseClassify(ctx, survivors, rs, opts, workers, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

func underCategory(rel string, cats map[string]bool) bool {
	if len(cats) == 0 {
		return false
	}
	first, _, ok := strings.Cut(filepath.ToSlash(rel), "/")
	return ok && cats[first]
}

func phaseDrop(ctx context.Context, items []walkItem, rs *Ruleset, denier *Denier, exif ExifLoader, workers int, rep *DirReport, dropped map[string]bool, onProgress func(done, total int64)) error {
	type result struct {
		rule string
		rel  string
		drop bool
		err  error
	}
	ch := make(chan result, len(items))
	jobs := make(chan walkItem)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for it := range jobs {
				if err := ctx.Err(); err != nil {
					ch <- result{err: err}
					continue
				}
				info, err := LoadImageInfo(it.path, it.rel, exif)
				if err != nil {
					ch <- result{err: err}
					continue
				}
				dec := rs.Evaluate(info)
				if dec.Action != ActionDrop {
					ch <- result{}
					continue
				}
				if _, err := denier.Drop(it.path, it.rel, dec.Rule, it.size); err != nil {
					ch <- result{err: err}
					continue
				}
				ch <- result{drop: true, rule: dec.Rule, rel: it.rel}
			}
		})
	}
	go func() {
		for _, it := range items {
			jobs <- it
		}
		close(jobs)
		wg.Wait()
		close(ch)
	}()

	var firstErr error
	total := int64(len(items))
	var done int64
	for r := range ch {
		done++
		if onProgress != nil {
			onProgress(done, total)
		}
		if r.err != nil {
			rep.Errors++
			// Only abort the whole run on cancellation; per-file decode/IO
			// errors are counted so classify can still run on survivors.
			if firstErr == nil && ctx.Err() != nil {
				firstErr = ctx.Err()
			}
			continue
		}
		if r.drop {
			rep.Dropped[r.rule]++
			rep.DropRels = append(rep.DropRels, r.rel)
			dropped[r.rel] = true
		}
	}
	return firstErr
}

func phaseClassify(ctx context.Context, items []walkItem, rs *Ruleset, opts DirOpts, workers int, rep *DirReport) error {
	batchSize := max(workers, 1)
	total := int64(len(items))
	var done atomic.Int64
	onItem := func() {
		if opts.OnClassifyProgress == nil {
			return
		}
		opts.OnClassifyProgress(done.Add(1), total)
	}
	var firstErr error
	for i := 0; i < len(items); i += batchSize {
		if err := ctx.Err(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		end := min(i+batchSize, len(items))
		batch := items[i:end]
		moves, errs := classifyBatch(ctx, batch, rs, opts, onItem)
		rep.Errors += errs
		for _, mv := range moves {
			rep.Classified[mv.Label]++
			rep.Moves = append(rep.Moves, mv)
		}
		if len(moves) > 0 && opts.OnClassifyBatch != nil {
			if err := opts.OnClassifyBatch(moves); err != nil {
				rep.Errors++
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func classifyBatch(ctx context.Context, batch []walkItem, rs *Ruleset, opts DirOpts, onItem func()) ([]ClassifyMove, int) {
	type result struct {
		mv  *ClassifyMove
		err error
	}
	ch := make(chan result, len(batch))
	var wg sync.WaitGroup
	for _, it := range batch {
		wg.Go(func() {
			mv, err := classifyOne(ctx, it, rs, opts)
			ch <- result{mv: mv, err: err}
		})
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	var moves []ClassifyMove
	errs := 0
	for r := range ch {
		if onItem != nil {
			onItem()
		}
		if r.err != nil {
			errs++
			continue
		}
		if r.mv != nil {
			moves = append(moves, *r.mv)
		}
	}
	return moves, errs
}

func classifyOne(ctx context.Context, it walkItem, rs *Ruleset, opts DirOpts) (*ClassifyMove, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := LoadImageInfo(it.path, it.rel, opts.Exif)
	if err != nil {
		return nil, err
	}
	for _, r := range rs.Rules {
		if r.Action != ActionClassify {
			continue
		}
		if !r.Match(info) {
			continue
		}
		prompt := BuildClassifyPrompt(r.Prompt, r.Categories, r.Labels)
		res, err := opts.Classifier.Classify(ctx, info, prompt, r.Labels)
		if err != nil {
			// Model error ≠ match; try next classify rule.
			continue
		}
		label := ParseClassifyLabel(res.Raw, r.Categories)
		if label == "" {
			label = matchCategory(res.Label, r.Categories)
		}
		if label == "" {
			continue
		}
		_, toRel, err := opts.ClassMover.Move(it.path, it.rel, r.Name, label, it.size)
		if err != nil {
			return nil, err
		}
		return &ClassifyMove{FromRel: it.rel, ToRel: toRel, Label: label, Rule: r.Name}, nil
	}
	return nil, nil
}
