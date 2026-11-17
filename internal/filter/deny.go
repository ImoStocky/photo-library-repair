package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// renameFunc is os.Rename by default; tests inject failures (EXDEV).
type renameFunc func(oldpath, newpath string) error

// Denier moves dropped files into denied-dir/<rule>/<relpath> and appends
// one JSONL manifest line per move.
type Denier struct {
	DeniedDir string
	DryRun    bool
	Out       io.Writer // dry-run / decision lines; may be nil

	mu       sync.Mutex
	manifest *os.File
	rename   renameFunc
}

// ManifestEntry is one line of denied-dir/manifest.jsonl.
type ManifestEntry struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Rule string `json:"rule"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}

// NewDenier prepares a denier. The denied directory is created on first move
// (or immediately when not dry-run so the walk can exclude it).
func NewDenier(deniedDir string, dryRun bool, out io.Writer) *Denier {
	return &Denier{
		DeniedDir: deniedDir,
		DryRun:    dryRun,
		Out:       out,
		rename:    os.Rename,
	}
}

// EnsureDir creates the denied directory (0755) so scans can skip it.
func (d *Denier) EnsureDir() error {
	if d == nil || d.DeniedDir == "" {
		return nil
	}
	return os.MkdirAll(d.DeniedDir, 0o755)
}

// Close flushes the manifest file.
func (d *Denier) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.manifest == nil {
		return nil
	}
	err := d.manifest.Close()
	d.manifest = nil
	return err
}

// Drop moves (or dry-run plans) src into denied/<rule>/<rel>.
func (d *Denier) Drop(src, rel, rule string, size int64) (dst string, err error) {
	if d == nil {
		return "", errors.New("nil denier")
	}
	if rule == "" {
		rule = DefaultRuleName
	}
	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(src)
	}
	dst = filepath.Join(d.DeniedDir, rule, rel)
	if d.Out != nil {
		fmt.Fprintf(d.Out, "%s -> %s  rule=%s\n", src, dst, rule)
	}
	if d.DryRun {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	dst, err = uniquePath(dst)
	if err != nil {
		return "", err
	}
	if err := d.moveFile(src, dst); err != nil {
		return "", err
	}
	if err := d.appendManifest(ManifestEntry{
		Src:  src,
		Dst:  dst,
		Rule: rule,
		Size: size,
		Time: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return dst, err
	}
	return dst, nil
}

func (d *Denier) moveFile(src, dst string) error {
	rename := d.rename
	if rename == nil {
		rename = os.Rename
	}
	err := rename(src, dst)
	if err == nil {
		return nil
	}
	if !isEXDEV(err) {
		return err
	}
	// Cross-device: copy, fsync, verify size, remove original.
	return copyVerifyRemove(src, dst)
}

func isEXDEV(err error) bool {
	if linkErr, ok := errors.AsType[*os.LinkError](err); ok {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}

func copyVerifyRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	fi, err := in.Stat()
	if err != nil {
		in.Close()
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		in.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	in.Close()
	if copyErr != nil {
		out.Close()
		os.Remove(dst)
		return copyErr
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	st, err := os.Stat(dst)
	if err != nil {
		os.Remove(dst)
		return err
	}
	if st.Size() != fi.Size() {
		os.Remove(dst)
		return fmt.Errorf("copy size mismatch: got %d want %d", st.Size(), fi.Size())
	}
	return os.Remove(src)
}

func uniquePath(path string) (string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	} else if err != nil {
		return "", err
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i < 10_000; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many collisions for %s", path)
}

func (d *Denier) appendManifest(e ManifestEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.manifest == nil {
		if err := os.MkdirAll(d.DeniedDir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(d.DeniedDir, "manifest.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		d.manifest = f
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := d.manifest.Write(append(b, '\n')); err != nil {
		return err
	}
	return d.manifest.Sync()
}

// IsUnder reports whether path is the denied dir or a file inside it.
func IsUnder(path, deniedDir string) bool {
	if deniedDir == "" {
		return false
	}
	absPath, err1 := filepath.Abs(path)
	absDenied, err2 := filepath.Abs(deniedDir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDenied, absPath)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}
