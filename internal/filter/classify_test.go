package filter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// mapClassifier returns a fixed label per basename (fake model).
type mapClassifier map[string]string

func (m mapClassifier) Classify(_ context.Context, imagePath, _ string, _ []string) (ClassifyResult, error) {
	label := m[filepath.Base(imagePath)]
	if label == "" || label == "none" {
		return ClassifyResult{Raw: `{"label":"none"}`}, nil
	}
	return ClassifyResult{
		Label: label,
		Raw:   fmt.Sprintf(`{"label":%q}`, label),
	}, nil
}

func TestLoadClassifyValidation(t *testing.T) {
	_, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: content-type
    match: { format: jpeg }
    action: classify
`))
	if err == nil {
		t.Fatal("expected error for missing classify block")
	}
	_, err = LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: content-type
    match: { format: jpeg }
    action: classify
    classify:
      prompt: "pick one"
      categories: {}
`))
	if err == nil {
		t.Fatal("expected error for empty categories")
	}
}

func TestParseClassifyLabel(t *testing.T) {
	allowed := map[string]string{"screenshot": "s", "document": "d"}
	if got := ParseClassifyLabel(`{"label":"screenshot"}`, allowed); got != "screenshot" {
		t.Fatalf("got %q", got)
	}
	if got := ParseClassifyLabel(`noise {"label":"none"} trailing`, allowed); got != "" {
		t.Fatalf("none should empty, got %q", got)
	}
	if got := ParseClassifyLabel(`{"label":"Document"}`, allowed); got != "document" {
		t.Fatalf("case fold got %q", got)
	}
	if got := ParseClassifyLabel(`not json`, allowed); got != "" {
		t.Fatalf("garbage got %q", got)
	}
}

func TestFilterDirClassifyFourImages(t *testing.T) {
	root := t.TempDir()
	// Drop candidate (extreme aspect).
	writeJPEG(t, filepath.Join(root, "strip.jpg"), 4000, 200)
	writeJPEG(t, filepath.Join(root, "a.jpg"), 800, 600)
	writeJPEG(t, filepath.Join(root, "b.jpg"), 800, 600)
	writeJPEG(t, filepath.Join(root, "c.jpg"), 800, 600)
	writeJPEG(t, filepath.Join(root, "d.jpg"), 800, 600)

	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: abnormal-aspect-ratio
    match: { min_aspect_ratio: 16 }
    action: drop
  - name: content-type
    match: { format: jpeg }
    action: classify
    classify:
      prompt: |
        Classify this image.
      categories:
        screenshot: "phone or computer screenshot"
        document: "scanned document"
`))
	if err != nil {
		t.Fatal(err)
	}

	denied := filepath.Join(root, "denied")
	denier := NewDenier(denied, false, nil)
	defer denier.Close()
	mover := &ClassMover{Root: root}
	defer mover.Close()

	var (
		mu      sync.Mutex
		batches [][]ClassifyMove
	)
	fake := mapClassifier{
		"a.jpg": "screenshot",
		"b.jpg": "none",
		"c.jpg": "document",
		"d.jpg": "screenshot",
	}

	rep, err := FilterDir(t.Context(), root, rs, denier, DirOpts{
		Workers:    2, // two batches of ~2 for four survivors after drop
		Classifier: fake,
		ClassMover: mover,
		OnClassifyBatch: func(moves []ClassifyMove) error {
			mu.Lock()
			defer mu.Unlock()
			cp := append([]ClassifyMove(nil), moves...)
			batches = append(batches, cp)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped["abnormal-aspect-ratio"] != 1 {
		t.Fatalf("dropped=%v", rep.Dropped)
	}
	if rep.Classified["screenshot"] != 2 || rep.Classified["document"] != 1 {
		t.Fatalf("classified=%v moves=%v", rep.Classified, rep.Moves)
	}
	if _, err := os.Stat(filepath.Join(root, "b.jpg")); err != nil {
		t.Fatal("b.jpg (none) should stay:", err)
	}
	if _, err := os.Stat(filepath.Join(root, "screenshot", "a.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "document", "c.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "screenshot", "d.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(denied, "abnormal-aspect-ratio", "strip.jpg")); err != nil {
		t.Fatal(err)
	}
	if len(batches) == 0 {
		t.Fatal("expected OnClassifyBatch calls")
	}
	// Second pass: category folders skipped, b stays, no more classify moves.
	rep2, err := FilterDir(t.Context(), root, rs, denier, DirOpts{
		Workers:    2,
		Classifier: fake,
		ClassMover: mover,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Moves) != 0 {
		t.Fatalf("second pass moves=%v", rep2.Moves)
	}
}

func TestFlattenClassify(t *testing.T) {
	root := t.TempDir()
	writeJPEG(t, filepath.Join(root, "a.jpg"), 800, 600)
	writeJPEG(t, filepath.Join(root, "b.jpg"), 800, 600)

	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: content-type
    match: { format: jpeg }
    action: classify
    classify:
      prompt: "pick"
      categories:
        screenshot: "ui"
        document: "page"
`))
	if err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "denied")
	denier := NewDenier(denied, false, nil)
	defer denier.Close()
	mover := &ClassMover{Root: root}
	_, err = FilterDir(t.Context(), root, rs, denier, DirOpts{
		Workers:    2,
		Classifier: mapClassifier{"a.jpg": "screenshot", "b.jpg": "document"},
		ClassMover: mover,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mover.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "screenshot", "a.jpg")); err != nil {
		t.Fatal(err)
	}

	var ledger []ClassifyMove
	flat, err := FlattenClassify(root, FlattenOpts{
		OnMoves: func(moves []ClassifyMove) error {
			ledger = append(ledger, moves...)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if flat.Moved != 2 {
		t.Fatalf("moved=%d missing=%d errors=%d", flat.Moved, flat.Missing, flat.Errors)
	}
	if _, err := os.Stat(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "screenshot")); !os.IsNotExist(err) {
		t.Fatalf("screenshot dir should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, classifyManifestName)); !os.IsNotExist(err) {
		t.Fatalf("manifest should be removed: %v", err)
	}
	if len(ledger) != 2 {
		t.Fatalf("ledger moves=%d", len(ledger))
	}
}
