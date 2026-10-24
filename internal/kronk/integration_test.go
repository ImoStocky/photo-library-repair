//go:build kronkai

// Package kronk integration test: loads the real decision model and settles
// a synthetic conflict. Excluded from default runs because it loads a
// ~0.8B model. Enable with: go test -tags kronkai ./internal/kronk/
package kronk

import (
	"testing"

	"photo-library-repair/internal/group"
)

func TestResolveWithRealModel(t *testing.T) {
	res, close, err := New(t.Context(), DefaultModel)
	if err != nil {
		t.Skipf("decision model unavailable: %v", err)
	}
	defer close()

	c := testConflict()
	d, err := res.Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if d.PixelImageID != "a" && d.PixelImageID != "b" {
		t.Fatalf("pixel %q is not a member", d.PixelImageID)
	}
	if d.CaptureImageID != "a" && d.CaptureImageID != "b" {
		t.Fatalf("capture %q is not a member", d.CaptureImageID)
	}
	if d.StoredName == "" {
		t.Fatal("no name chosen")
	}

	p := group.ResolveGroup(c.Images, res, 2)
	if p.Status != group.StatusResolved || p.ResolvedBy == "" {
		t.Fatalf("plan = %+v", p)
	}
	t.Logf("model %s settled the conflict: %+v", res.Name(), d)
}
