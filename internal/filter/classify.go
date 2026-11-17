package filter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Classifier labels one image. Implementations must treat errors as
// non-matches (caller continues to the next classify rule).
type Classifier interface {
	Classify(ctx context.Context, imagePath, prompt string, labels []string) (ClassifyResult, error)
}

// ClassifyResult is the model answer for one image.
type ClassifyResult struct {
	Label string
	Raw   string
}

// ClassifyMove records one library-relative relocate under a category folder.
type ClassifyMove struct {
	FromRel string
	ToRel   string
	Label   string
	Rule    string
}

// ClassMover moves classified files to <root>/<label>/<rel> and appends
// classify-manifest.jsonl under the library root (not denied/).
type ClassMover struct {
	Root   string
	DryRun bool
	Out    io.Writer

	mu       sync.Mutex
	manifest *os.File
}

// ClassifyManifestEntry is one line of <library>/classify-manifest.jsonl.
type ClassifyManifestEntry struct {
	Action string `json:"action"`
	Src    string `json:"src"`
	Dst    string `json:"dst"`
	Rule   string `json:"rule"`
	Label  string `json:"label"`
	Size   int64  `json:"size"`
	Time   string `json:"time"`
}

// Close flushes the classify manifest.
func (m *ClassMover) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.manifest == nil {
		return nil
	}
	err := m.manifest.Close()
	m.manifest = nil
	return err
}

// Move relocates src into root/label/rel (or dry-run plans it).
func (m *ClassMover) Move(src, rel, rule, label string, size int64) (dst, toRel string, err error) {
	if m == nil {
		return "", "", fmt.Errorf("nil class mover")
	}
	if label == "" {
		return "", "", fmt.Errorf("empty classify label")
	}
	rel = filepath.Clean(rel)
	if rel == "." || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(src)
	}
	toRel = filepath.ToSlash(filepath.Join(label, rel))
	dst = filepath.Join(m.Root, filepath.FromSlash(toRel))
	if m.Out != nil {
		fmt.Fprintf(m.Out, "%s -> %s  rule=%s label=%s\n", src, dst, rule, label)
	}
	if m.DryRun {
		return dst, toRel, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", "", err
	}
	dst, err = uniquePath(dst)
	if err != nil {
		return "", "", err
	}
	// Recompute toRel if uniquePath added a suffix.
	if rel2, rerr := filepath.Rel(m.Root, dst); rerr == nil {
		toRel = filepath.ToSlash(rel2)
	}
	if err := renameOrCopy(nil, src, dst); err != nil {
		return "", "", err
	}
	if err := m.appendManifest(ClassifyManifestEntry{
		Action: ActionClassify,
		Src:    src,
		Dst:    dst,
		Rule:   rule,
		Label:  label,
		Size:   size,
		Time:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return dst, toRel, err
	}
	return dst, toRel, nil
}

func (m *ClassMover) appendManifest(e ClassifyManifestEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.manifest == nil {
		if err := os.MkdirAll(m.Root, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(m.Root, classifyManifestName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		m.manifest = f
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := m.manifest.Write(append(b, '\n')); err != nil {
		return err
	}
	return m.manifest.Sync()
}

// BuildClassifyPrompt appends the label list and a strict JSON instruction.
func BuildClassifyPrompt(rulePrompt string, categories map[string]string, labels []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(rulePrompt))
	b.WriteString("\n\nLabels:\n")
	for _, label := range labels {
		fmt.Fprintf(&b, "- %s: %s\n", label, categories[label])
	}
	b.WriteString("\nReply with JSON only: {\"label\":\"<one label from the list, or none>\"}\n")
	return b.String()
}

// ParseClassifyLabel extracts a label from model text. Unknown / none / empty → "".
func ParseClassifyLabel(raw string, allowed map[string]string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var obj struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return matchCategory(raw, allowed)
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &obj); err != nil {
			return matchCategory(raw, allowed)
		}
	}
	return matchCategory(obj.Label, allowed)
}

func matchCategory(label string, allowed map[string]string) string {
	label = strings.TrimSpace(label)
	if label == "" || strings.EqualFold(label, "none") {
		return ""
	}
	if _, ok := allowed[label]; ok {
		return label
	}
	for k := range allowed {
		if strings.EqualFold(k, label) {
			return k
		}
	}
	return ""
}
