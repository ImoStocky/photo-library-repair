// Package ingest walks source directories and records every image into the
// library: identity (path, sha256), candidate DCT phash, content-addressed
// extra hashes keyed by sha256, file creation time and EXIF capture time.
//
// Directories that were already recorded as sources are skipped unless
// Options.Force is set; a forced re-scan re-syncs the source, dropping rows
// for files that no longer exist.
//
// A forced re-scan does not re-hash unchanged files: when a file's size and
// modification time match the stored row, its sha256, perceptual hash and
// EXIF facts are reused from the store (the fingerprint cache).
package ingest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"photo-library-repair/internal/fingerprint"
	"photo-library-repair/internal/store"

	"golang.org/x/sys/unix"
)

// Options controls a scan.
type Options struct {
	Dirs    []string
	Force   bool
	Workers int
	MinSize int64 // bytes; 0 accepts everything
}

// Report summarizes a scan.
type Report struct {
	DirsScanned   int
	DirsSkipped   int
	FilesSeen     int
	ImagesAdded   int
	ImagesUpdated int
	ImagesCached  int // fingerprints reused from the store (size+mtime unchanged)
	NonImage      int
	Errors        int
}

// Hasher computes the fingerprint of one file. It is injected so tests do
// not need real images.
type Hasher func(path string, kind string) (fingerprint.Info, error)

type job struct {
	sourceID  int64
	fullPath  string
	relPath   string
	name      string
	size      int64
	mtime     int64 // unix nanoseconds, fingerprint cache key
	kind      string
	createdAt int64
}

// Scan records the images under opts.Dirs into the library.
func Scan(s *store.Store, opts Options, hash Hasher) (*Report, error) {
	if len(opts.Dirs) == 0 {
		return nil, errors.New("no directories to scan")
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	rep := &Report{}
	runStart := time.Now()
	var seq atomic.Int64

	seenInodes := map[uint64]bool{} // (dev, inode) already recorded this run

	for _, dir := range opts.Dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return rep, fmt.Errorf("scan %q: %w", dir, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return rep, fmt.Errorf("scan %q: %w", dir, err)
		}
		if !info.IsDir() {
			return rep, fmt.Errorf("scan %q: not a directory", dir)
		}

		sourceID, exists, err := s.FindSource(abs)
		if err != nil {
			return rep, fmt.Errorf("scan %q: %w", dir, err)
		}
		if exists && !opts.Force {
			rep.DirsSkipped++
			continue
		}
		if !exists {
			sourceID, err = s.AddSource(abs)
			if err != nil {
				return rep, fmt.Errorf("scan %q: %w", dir, err)
			}
		}

		jobs, seen, nonImage, err := collect(abs, opts.MinSize, seenInodes)
		if err != nil {
			return rep, fmt.Errorf("scan %q: %w", dir, err)
		}
		rep.NonImage += nonImage
		for i := range jobs {
			jobs[i].sourceID = sourceID
		}
		rep.DirsScanned++

		// Fingerprint cache: stored rows of a known source, so unchanged
		// files skip the re-hash on a forced re-scan.
		facts := map[string]store.Image{}
		if exists {
			facts, err = s.ImagesBySource(sourceID)
			if err != nil {
				return rep, fmt.Errorf("scan %q: %w", dir, err)
			}
		}

		if err := hashAll(s, jobs, opts.Workers, hash, runStart, &seq, rep, facts); err != nil {
			return rep, fmt.Errorf("scan %q: %w", dir, err)
		}

		if opts.Force {
			if err := dropMissing(s, sourceID, seen); err != nil {
				return rep, fmt.Errorf("scan %q: %w", dir, err)
			}
		}
	}
	return rep, nil
}

// collect walks root and returns the image jobs, the set of relative paths
// that exist, and how many files with an image extension failed the magic
// sniff. Files whose (dev, inode) was already seen in this run (nested
// sources, hard links) are skipped.
func collect(root string, minSize int64, seenInodes map[uint64]bool) ([]job, map[string]bool, int, error) {
	var jobs []job
	seen := map[string]bool{}
	nonImage := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !hasImageExt(d.Name()) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil // vanished or unreadable; skip
		}
		if !fi.Mode().IsRegular() || fi.Size() < minSize {
			return nil
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		inodeKey := uint64(st.Dev)<<32 | uint64(st.Ino)
		if seenInodes[inodeKey] {
			return nil
		}
		seenInodes[inodeKey] = true

		kind, err := sniffKind(path)
		if err != nil || kind == "" {
			nonImage++
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		// Best guess at the picture's birth, used only when EXIF is
		// absent (and for the birth-time doubt). A copy carries the
		// older modification time of its original while its own creation
		// time is the copy moment, so the earlier of the two is closer to
		// the real capture.
		createdAt := fi.ModTime().Unix()
		if bt := birthTime(path, fi.ModTime()); bt > 0 && bt < createdAt {
			createdAt = bt
		}
		jobs = append(jobs, job{
			fullPath:  path,
			relPath:   rel,
			name:      d.Name(),
			size:      fi.Size(),
			mtime:     fi.ModTime().UnixNano(),
			kind:      kind,
			createdAt: createdAt,
		})
		seen[rel] = true
		return nil
	})
	return jobs, seen, nonImage, err
}

// hashAll runs sha256 + fingerprint over the jobs with a worker pool and
// upserts the results. Files whose (size, mtime) match the stored facts
// (the cache, keyed per source) are not re-read or re-hashed.
func hashAll(s *store.Store, jobs []job, workers int, hash Hasher, runStart time.Time, seq *atomic.Int64, rep *Report, facts map[string]store.Image) error {
	jobsCh := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	firstErr := error(nil)

	for range workers {
		wg.Go(func() {
			for j := range jobsCh {
				rep.FilesSeen++
				if stored, ok := facts[j.relPath]; ok && stored.Size == j.size && stored.MTime == j.mtime {
					// Unchanged since the last scan: reuse the stored
					// sha256/phash/EXIF facts and only refresh the row.
					stored.Name = j.name
					if _, _, err := s.UpsertImage(stored); err != nil {
						mu.Lock()
						rep.Errors++
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
					} else {
						mu.Lock()
						rep.ImagesCached++
						mu.Unlock()
					}
					continue
				}
				sum, err := sha256File(j.fullPath)
				if err != nil {
					mu.Lock()
					rep.Errors++
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				info := fingerprint.Info{Kind: j.kind}
				if info, err = hash(j.fullPath, j.kind); err != nil {
					// Unhashable pictures are still collected; they end
					// up as singleton groups.
					info = fingerprint.Info{Kind: j.kind}
				}
				img := store.Image{
					ID:          store.NewID(runStart, int(seq.Add(1))),
					RelPath:     j.relPath,
					Name:        j.name,
					SHA256:      sum,
					Phash:       info.Phash,
					CreatedAt:   j.createdAt,
					ExifCapture: info.EXIFCapture,
					ExifOffset:  info.EXIFOffset,
					ExifCamera:  info.EXIFCamera,
					Width:       info.Width,
					Height:      info.Height,
					Kind:        j.kind,
					Size:        j.size,
					MTime:       j.mtime,
					HasGPS:      info.HasGPS,
					GPSLat:      info.GPSLat,
					GPSLon:      info.GPSLon,
				}
				img.SourceID = j.sourceID
				_, created, err := s.UpsertImage(img)
				if err == nil && sum != "" {
					for _, eh := range info.ExtraHashes {
						if eh.Hex == "" {
							continue
						}
						if putErr := s.PutContentPhash(store.ContentPhash{
							SHA256: sum,
							Alg:    string(eh.Alg),
							Size:   int(eh.Size),
							Hex:    eh.Hex,
						}); putErr != nil {
							err = putErr
							break
						}
					}
				}
				mu.Lock()
				if err != nil {
					rep.Errors++
					if firstErr == nil {
						firstErr = err
					}
				} else if created {
					rep.ImagesAdded++
				} else {
					rep.ImagesUpdated++
				}
				mu.Unlock()
			}
		})
	}

	for _, j := range jobs {
		jobsCh <- j
	}
	close(jobsCh)
	wg.Wait()
	return firstErr
}

// dropMissing removes rows of a force-re-scanned source whose files are gone.
func dropMissing(s *store.Store, sourceID int64, seen map[string]bool) error {
	tx, err := s.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TEMP TABLE plr_seen (rel_path TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	for rel := range seen {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO plr_seen (rel_path) VALUES (?)`, rel); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM images WHERE source_id = ? AND rel_path NOT IN (SELECT rel_path FROM plr_seen)`, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE plr_seen`); err != nil {
		return err
	}
	return tx.Commit()
}

// birthTime returns the file birth time in unix seconds, or 0 when the
// filesystem does not report one (callers fall back to mtime).
func birthTime(path string, fallback time.Time) int64 {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &stx); err == nil {
		return stx.Btime.Sec
	}
	return 0
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// sniffKind identifies the image format from the first bytes of the file.
func sniffKind(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [16]byte
	n, _ := io.ReadFull(f, head[:])
	return Kind(head[:n]), nil
}

// Kind identifies an image format from its header bytes.
// It returns "" when the bytes do not look like a supported image.
func Kind(header []byte) string {
	switch {
	case len(header) >= 3 && header[0] == 0xFF && header[1] == 0xD8 && header[2] == 0xFF:
		return "jpeg"
	case len(header) >= 8 && bytes.Equal(header[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png"
	case len(header) >= 6 && (bytes.Equal(header[:6], []byte("GIF87a")) || bytes.Equal(header[:6], []byte("GIF89a"))):
		return "gif"
	case len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")):
		return "webp"
	case len(header) >= 4 && (bytes.Equal(header[:4], []byte{0x49, 0x49, 0x2A, 0x00}) || bytes.Equal(header[:4], []byte{0x4D, 0x4D, 0x00, 0x2A})):
		return "tiff"
	case len(header) >= 12 && bytes.Equal(header[4:8], []byte("ftyp")):
		switch string(header[8:12]) {
		case "heic", "heif", "heim", "heis", "hevc", "hevx", "mif1":
			return "heic"
		}
	}
	return ""
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".tif": true, ".tiff": true,
	".heic": true, ".heif": true,
}

func hasImageExt(name string) bool {
	_, ok := imageExts[strings.ToLower(filepath.Ext(name))]
	return ok
}
