package group

import (
	"photo-library-repair/internal/phash"
)

// DoublePhash refines candidates by intersecting two secondary hashes
// (blockhash 16×16 and double-gradient 16×16) looked up by sha256. The
// primary candidate phash on each Image stays DCT (Aggregate). Two members
// join an identity when sha256 matches or both secondary distances are
// within their radii.
type DoublePhash struct {
	Lookup       ContentPhashLookup
	BlockHamming int // default 24
	GradHamming  int // default 20
}

// Name implements IdentityStrategy.
func (DoublePhash) Name() string { return "double-phash" }

// Refine implements IdentityStrategy.
func (d DoublePhash) Refine(candidates []Image) ([][]Image, error) {
	n := len(candidates)
	if n == 0 {
		return nil, nil
	}
	if n == 1 {
		return [][]Image{{candidates[0]}}, nil
	}
	blockR, gradR := d.BlockHamming, d.GradHamming
	if blockR <= 0 {
		blockR = 24
	}
	if gradR <= 0 {
		gradR = 20
	}

	type pair struct {
		block, grad phash.Hash
		okBlock     bool
		okGrad      bool
	}
	hashes := make([]pair, n)
	for i, im := range candidates {
		if d.Lookup == nil || im.SHA256 == "" {
			continue
		}
		if h, ok, err := d.Lookup.Phash(im.SHA256, phash.AlgBlockhash, phash.Size16); err != nil {
			return nil, err
		} else if ok {
			hashes[i].block, hashes[i].okBlock = h, true
		}
		if h, ok, err := d.Lookup.Phash(im.SHA256, phash.AlgDoubleGradient, phash.Size16); err != nil {
			return nil, err
		} else if ok {
			hashes[i].grad, hashes[i].okGrad = h, true
		}
	}

	uf := newUnionFind(n)
	for i := range n {
		for j := i + 1; j < n; j++ {
			if candidates[i].SHA256 != "" && candidates[i].SHA256 == candidates[j].SHA256 {
				uf.union(i, j)
				continue
			}
			a, b := hashes[i], hashes[j]
			if !a.okBlock || !b.okBlock || !a.okGrad || !b.okGrad {
				continue
			}
			// Min-rotation distances tolerate 90° re-saves and other
			// sizes (slack constants documented in identity_exif.go).
			if phash.MinRotDistance(a.block, b.block, phash.AlgBlockhash) <= blockR+slackBlockhash &&
				phash.MinRotDistance(a.grad, b.grad, phash.AlgDoubleGradient) <= gradR+slackGradient {
				uf.union(i, j)
			}
		}
	}
	return uf.groups(candidates), nil
}
