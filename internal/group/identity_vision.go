package group

// UnsureDecider always returns PairUnsure (Vision stub until Kronk pairwise
// identity is wired).
type UnsureDecider struct{}

// Name implements VisionDecider.
func (UnsureDecider) Name() string { return "unsure" }

// Decide implements VisionDecider.
func (UnsureDecider) Decide(a, b Image) (PairVerdict, error) {
	return PairUnsure, nil
}

// DecisionModel refines candidates with pairwise vision decisions.
// Same → union; Different / Unsure → no edge. If no Same (or sha) edges
// appear, the Aggregate candidate is kept intact so EXIF ResolveGroup can
// still see the cluster (stub-friendly).
type DecisionModel struct {
	Decider VisionDecider
}

// Name implements IdentityStrategy.
func (DecisionModel) Name() string { return "vision" }

// Refine implements IdentityStrategy.
func (d DecisionModel) Refine(candidates []Image) ([][]Image, error) {
	n := len(candidates)
	if n == 0 {
		return nil, nil
	}
	if n == 1 || d.Decider == nil {
		out := make([]Image, len(candidates))
		copy(out, candidates)
		return [][]Image{out}, nil
	}
	uf := newUnionFind(n)
	anySame := false
	for i := range n {
		for j := i + 1; j < n; j++ {
			if candidates[i].SHA256 != "" && candidates[i].SHA256 == candidates[j].SHA256 {
				uf.union(i, j)
				anySame = true
				continue
			}
			v, err := d.Decider.Decide(candidates[i], candidates[j])
			if err != nil {
				return nil, err
			}
			if v == PairSame {
				uf.union(i, j)
				anySame = true
			}
		}
	}
	if !anySame {
		out := make([]Image, len(candidates))
		copy(out, candidates)
		return [][]Image{out}, nil
	}
	return uf.groups(candidates), nil
}
