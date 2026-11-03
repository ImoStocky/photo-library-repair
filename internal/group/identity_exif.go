package group

import (
	"time"

	"photo-library-repair/internal/phash"
)

// DefaultSameImageWindow is the zone-resolved capture-time gap within
// which two EXIF pictures are the same image (docs/identity-decision-graph.md
// §1: a scene cannot meaningfully change in one second).
const DefaultSameImageWindow = time.Second

// DefaultExifFirstHamming is the radius each 16×16 secondary hash gets in
// the EXIF-less fallback gate (block AND gradient must both pass).
const DefaultExifFirstHamming = 10

// ExifFirst is the identity strategy of docs/identity-decision-graph.md:
// a pair whose zone-resolved EXIF capture instants differ by at most
// SameWindow is the same image unless both files record camera blocks
// (EXIF Make + Model) that differ — then they are two cameras firing at
// one instant. Any other pair, including two EXIF pictures further apart
// than the window, is decided by the dual 16×16 gate: blockhash AND
// double-gradient must both be within their radii. sha256 matches always
// merge. Matching is transitive.
type ExifFirst struct {
	Lookup       ContentPhashLookup
	SameWindow   time.Duration // default DefaultSameImageWindow
	BlockHamming int           // default DefaultExifFirstHamming
	GradHamming  int           // default DefaultExifFirstHamming
}

// Name implements IdentityStrategy.
func (ExifFirst) Name() string { return "exif-first" }

func (e ExifFirst) winSecs() int64 {
	w := e.SameWindow
	if w <= 0 {
		w = DefaultSameImageWindow
	}
	return int64(w / time.Second)
}

func (e ExifFirst) blockR() int {
	if e.BlockHamming <= 0 {
		return DefaultExifFirstHamming
	}
	return e.BlockHamming
}

func (e ExifFirst) gradR() int {
	if e.GradHamming <= 0 {
		return DefaultExifFirstHamming
	}
	return e.GradHamming
}

// SweepWindow implements the Pipeline candidate-recall hook: EXIF-bearing
// members within the same-image window of each other are pulled into one
// chunk before Refine, so the EXIF rule stays exact even when the 8×8
// bucket radius was too tight to pair them.
func (e ExifFirst) SweepWindow() time.Duration {
	w := e.SameWindow
	if w <= 0 {
		w = DefaultSameImageWindow
	}
	return w
}

// dual holds the two 16×16 secondary hashes of one picture.
type dual struct {
	block, grad phash.Hash
	okBlock     bool
	okGrad      bool
}

func (e ExifFirst) load(sha256 string) (dual, error) {
	var d dual
	if e.Lookup == nil || sha256 == "" {
		return d, nil
	}
	if h, ok, err := e.Lookup.Phash(sha256, phash.AlgBlockhash, phash.Size16); err != nil {
		return d, err
	} else if ok {
		d.block, d.okBlock = h, true
	}
	if h, ok, err := e.Lookup.Phash(sha256, phash.AlgDoubleGradient, phash.Size16); err != nil {
		return d, err
	} else if ok {
		d.grad, d.okGrad = h, true
	}
	return d, nil
}

// Refine implements IdentityStrategy: pairwise same-image edges over the
// candidates plus their transitive closure.
func (e ExifFirst) Refine(candidates []Image) ([][]Image, error) {
	n := len(candidates)
	if n == 0 {
		return nil, nil
	}
	if n == 1 {
		return [][]Image{{candidates[0]}}, nil
	}
	hashes := make([]dual, n)
	for i, im := range candidates {
		d, err := e.load(im.SHA256)
		if err != nil {
			return nil, err
		}
		hashes[i] = d
	}
	uf := newUnionFind(n)
	win, blockR, gradR := e.winSecs(), e.blockR(), e.gradR()
	for i := range n {
		for j := i + 1; j < n; j++ {
			if sameImagePair(candidates[i], candidates[j], hashes[i], hashes[j], win, blockR, gradR) {
				uf.union(i, j)
			}
		}
	}
	return uf.groups(candidates), nil
}

// SameFunc returns the pair rule for the incremental merge (Partition):
// proximity in the candidate hash alone never joins a library group, the
// pair must also be the same image here. Secondary hash lookups are
// cached per sha256; a failed lookup reads as "no hash", which keeps the
// pair apart rather than merging on weak evidence.
func (e ExifFirst) SameFunc() SameFunc {
	cache := map[string]dual{}
	get := func(sha256 string) dual {
		if d, ok := cache[sha256]; ok {
			return d
		}
		d, _ := e.load(sha256)
		cache[sha256] = d
		return d
	}
	win, blockR, gradR := e.winSecs(), e.blockR(), e.gradR()
	return func(a, b Image) bool {
		return sameImagePair(a, b, get(a.SHA256), get(b.SHA256), win, blockR, gradR)
	}
}

// sameImagePair is the decision graph of docs/identity-decision-graph.md §3.
func sameImagePair(a, b Image, da, db dual, winSecs int64, blockR, gradR int) bool {
	if a.SHA256 != "" && a.SHA256 == b.SHA256 {
		return true
	}
	if ta, tb, ok := pairInstants(a, b); ok {
		d := ta - tb
		if d < 0 {
			d = -d
		}
		if d <= winSecs {
			// Same moment: only recorded cameras that disagree can tell
			// the two files apart (two cameras firing at one instant).
			return !cameraConflict(a, b)
		}
		// Further apart is no veto: the hash gate below decides.
	}
	return da.okBlock && db.okBlock && da.okGrad && db.okGrad &&
		da.block.Hamming(db.block) <= blockR &&
		da.grad.Hamming(db.grad) <= gradR
}

// cameraConflict reports two recorded camera blocks that differ; an
// unknown (empty) block on either side proves nothing.
func cameraConflict(a, b Image) bool {
	return a.ExifCamera != "" && b.ExifCamera != "" && a.ExifCamera != b.ExifCamera
}

// pairInstants resolves the pair's capture instants: every member is
// parsed against its own recorded offset — an unknown offset compares its
// wall clock, no zone is ever borrowed from the partner — so two known
// offsets compare as instants. ok is false unless both members carry a
// parsable EXIF capture time.
func pairInstants(a, b Image) (int64, int64, bool) {
	if a.ExifCapture == "" || b.ExifCapture == "" {
		return 0, 0, false
	}
	ta, oka := parseCapture(a.ExifCapture, a.ExifOffset)
	tb, okb := parseCapture(b.ExifCapture, b.ExifOffset)
	if !oka || !okb {
		return 0, 0, false
	}
	return ta.Unix(), tb.Unix(), true
}
