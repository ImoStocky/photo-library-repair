package group

import (
	"runtime"
	"sync"
	"sync/atomic"

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
		block, grad         phash.Hash
		blockRots, gradRots []phash.Hash
		okBlock             bool
		okGrad              bool
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
			hashes[i].blockRots = phash.Rotations(h, phash.AlgBlockhash)
		}
		if h, ok, err := d.Lookup.Phash(im.SHA256, phash.AlgDoubleGradient, phash.Size16); err != nil {
			return nil, err
		} else if ok {
			hashes[i].grad, hashes[i].okGrad = h, true
			hashes[i].gradRots = phash.Rotations(h, phash.AlgDoubleGradient)
		}
	}

	uf := newUnionFind(n)
	pairOK := func(i, j int) bool {
		if candidates[i].SHA256 != "" && candidates[i].SHA256 == candidates[j].SHA256 {
			return true
		}
		a, b := hashes[i], hashes[j]
		if !a.okBlock || !b.okBlock || !a.okGrad || !b.okGrad {
			return false
		}
		return phash.MinRotDistanceRots(a.block, b.blockRots) <= blockR+slackBlockhash &&
			phash.MinRotDistanceRots(a.grad, b.gradRots) <= gradR+slackGradient
	}
	if n >= refineParallelMin {
		workers := min(runtime.GOMAXPROCS(0), n)
		edges := make([][]pairEdge, workers)
		var next atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := range workers {
			go func(w int) {
				defer wg.Done()
				var local []pairEdge
				for {
					i := int(next.Add(1) - 1)
					if i >= n {
						break
					}
					for j := i + 1; j < n; j++ {
						if pairOK(i, j) {
							local = append(local, pairEdge{i, j})
						}
					}
				}
				edges[w] = local
			}(w)
		}
		wg.Wait()
		for _, es := range edges {
			unionEdges(uf, es)
		}
	} else {
		for i := range n {
			for j := i + 1; j < n; j++ {
				if pairOK(i, j) {
					uf.union(i, j)
				}
			}
		}
	}
	return uf.groups(candidates), nil
}
