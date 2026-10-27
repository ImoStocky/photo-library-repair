package group

// PassThrough leaves Aggregate candidates unchanged (legacy grouping).
type PassThrough struct{}

// Name implements IdentityStrategy.
func (PassThrough) Name() string { return "pass" }

// Refine implements IdentityStrategy.
func (PassThrough) Refine(candidates []Image) ([][]Image, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	out := make([]Image, len(candidates))
	copy(out, candidates)
	return [][]Image{out}, nil
}
