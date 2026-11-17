package filter

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

// DirReport summarizes a FilterDir run against a materialized library.
type DirReport struct {
	Seen     int
	Dropped  map[string]int // rule name → count
	DropRels []string       // library-relative paths that were moved (or dry-run planned)
	Errors   int
}

var libraryExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".tif": true, ".tiff": true,
	".heic": true, ".heif": true,
}

// FilterDir walks a materialized library root, evaluates rs, and moves
// drops via denier. The denied directory (and report.json) are skipped.
func FilterDir(root string, rs *Ruleset, denier *Denier, workers int, exif ExifLoader) (*DirReport, error) {
	if rs == nil {
		return nil, fmt.Errorf("filter rules required")
	}
	if denier == nil {
		return nil, fmt.Errorf("denier required")
	}
	if workers < 1 {
		workers = 1
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := denier.EnsureDir(); err != nil {
		return nil, err
	}

	type item struct {
		path string
		rel  string
		size int64
	}
	var items []item
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if IsUnder(path, denier.DeniedDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if IsUnder(path, denier.DeniedDir) {
			return nil
		}
		if strings.EqualFold(d.Name(), "report.json") {
			return nil
		}
		if !libraryExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return nil
		}
		items = append(items, item{path: path, rel: rel, size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	rep := &DirReport{Seen: len(items), Dropped: map[string]int{}}
	if len(items) == 0 {
		return rep, nil
	}

	type result struct {
		rule string
		rel  string
		drop bool
		err  error
	}
	ch := make(chan result, len(items))
	jobs := make(chan item)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for it := range jobs {
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
	for r := range ch {
		if r.err != nil {
			rep.Errors++
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if r.drop {
			rep.Dropped[r.rule]++
			rep.DropRels = append(rep.DropRels, r.rel)
		}
	}
	return rep, firstErr
}
