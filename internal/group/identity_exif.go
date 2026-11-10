package group

import (
	"time"

	"photo-library-repair/internal/phash"
)

// DefaultSameImageWindow is the zone-resolved capture-time gap within
// which a recorded camera-block mismatch splits a pair the hash gate
// joined (docs/identity-decision-graph.md §1.3: two cameras firing at
// one instant are two pictures).
const DefaultSameImageWindow = time.Second

// DefaultExifFirstHamming is the radius each 16×16 secondary hash gets in
// the EXIF-less fallback gate (block AND gradient must both pass).
const DefaultExifFirstHamming = 10

// slackBlockhash / slackGradient are added to the configured secondary
// radii because the hashes are compared min-rotation between pictures of
// any size. Measured same-picture noise (corpus + scratch harness): a
// downscaled re-save moves blockhash ≤6 and double-gradient ≤27 bits; a
// rotated, re-encoded, re-sized save moves blockhash ≤90 (its band medians
// shift under the turn) while double-gradient stays ≤6. The gate is an
// AND, and exports CROPPED from one photo measure 29–30 gradient bits, so
// the gradient radius stops at 28 — it carries the separation (unrelated
// pictures are ≥62 gradient bits apart) and blockhash only vetoes gross
// disagreement.
const (
	slackBlockhash = 86
	slackGradient  = 18
)

// ExifFirst is the identity strategy of docs/identity-decision-graph.md:
// the dual 16×16 gate decides every pair — blockhash AND double-gradient
// must both be within their radii. A capture-time match alone never joins
// a pair. EXIF can only split what the hashes joined: two files that
// record different camera blocks (EXIF Make + Model) and zone-resolved
// capture instants within SameWindow are two cameras firing at one
// instant, not one picture. sha256 matches always merge. Matching is
// transitive.
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
// chunk before Refine, so a pair that passes the hash gate is not lost
// just because the 8×8 bucket radius was too tight to pair them.
func (e ExifFirst) SweepWindow() time.Duration {
	w := e.SameWindow
	if w <= 0 {
		w = DefaultSameImageWindow
	}
	return w
}

// dual holds the two 16×16 secondary hashes of one picture, plus their
// precomputed bit-layout rotations so MinRotDistance does not reallocate
// rotBlockhash90 / rotDoubleGradient90 on every pair.
type dual struct {
	block, grad         phash.Hash
	blockRots, gradRots []phash.Hash
	okBlock             bool
	okGrad              bool
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
		d.blockRots = phash.Rotations(h, phash.AlgBlockhash)
	}
	if h, ok, err := e.Lookup.Phash(sha256, phash.AlgDoubleGradient, phash.Size16); err != nil {
		return d, err
	} else if ok {
		d.grad, d.okGrad = h, true
		d.gradRots = phash.Rotations(h, phash.AlgDoubleGradient)
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

// sameImagePair is the decision graph of docs/identity-decision-graph.md §3:
// the perceptual gate first, then EXIF may split what the hashes joined.
func sameImagePair(a, b Image, da, db dual, winSecs int64, blockR, gradR int) bool {
	if a.SHA256 != "" && a.SHA256 == b.SHA256 {
		return true
	}
	// The phash gate comes first: visual agreement is the only way a pair
	// joins. A shared capture second proves nothing — burst frames are
	// different pictures taken one second apart. Min-rotation distances
	// with the documented slack make the gate tolerate 90° re-saves and
	// resolution changes; unrelated pictures stay far outside even the
	// slackened gradient radius.
	if !(da.okBlock && db.okBlock && da.okGrad && db.okGrad &&
		phash.MinRotDistanceRots(da.block, db.blockRots) <= blockR+slackBlockhash &&
		phash.MinRotDistanceRots(da.grad, db.gradRots) <= gradR+slackGradient) {
		return false
	}
	// EXIF then vetoes what the hashes joined: two recorded cameras firing
	// at one instant are two pictures even when they look identical.
	if ta, tb, ok := pairInstants(a, b); ok {
		d := ta - tb
		if d < 0 {
			d = -d
		}
		if d <= winSecs && cameraConflict(a, b) {
			return false
		}
	}
	return true
}

// cameraConflict reports two recorded camera blocks that differ; an
// unknown (empty) block on either side proves nothing.
func cameraConflict(a, b Image) bool {
	return a.ExifCamera != "" && b.ExifCamera != "" && a.ExifCamera != b.ExifCamera
}

// pairInstants resolves the pair's capture instants. Each member is parsed
// against its own recorded offset; when exactly one side records an offset,
// the other inherits it (a missing zone is no information, and the pair was
// taken in one place) so their wall clocks compare on the same zone. Two
// distinct known offsets, and two unknown ones, are left as they are — the
// first compare as instants, the second compare wall clocks. ok is false
// unless both members carry a parsable EXIF capture time.
func pairInstants(a, b Image) (int64, int64, bool) {
	if a.ExifCapture == "" || b.ExifCapture == "" {
		return 0, 0, false
	}
	offA, offB := a.ExifOffset, b.ExifOffset
	if offA == "" && offB != "" {
		offA = offB
	} else if offB == "" && offA != "" {
		offB = offA
	}
	ta, oka := parseCapture(a.ExifCapture, offA)
	tb, okb := parseCapture(b.ExifCapture, offB)
	if !oka || !okb {
		return 0, 0, false
	}
	return ta.Unix(), tb.Unix(), true
}
