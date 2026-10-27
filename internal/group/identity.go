package group

import (
	"photo-library-repair/internal/phash"
)

// IdentityStrategy refines one candidate near-dup cluster into one or more
// identity clusters (Strategy pattern). Candidates come from Aggregate;
// identities then go through ResolveGroup for EXIF / conflict planning.
type IdentityStrategy interface {
	Name() string
	Refine(candidates []Image) ([][]Image, error)
}

// ContentPhashLookup loads a content-addressed perceptual hash by sha256.
// The store's content_phash table is the production implementation.
type ContentPhashLookup interface {
	Phash(sha256 string, alg phash.Alg, size phash.Size) (phash.Hash, bool, error)
}

// PairVerdict is a vision model's same-picture decision.
type PairVerdict int

const (
	PairUnsure PairVerdict = iota
	PairSame
	PairDifferent
)

// VisionDecider decides whether two images depict the same picture
// (DecisionModel identity strategy).
type VisionDecider interface {
	Name() string
	Decide(a, b Image) (PairVerdict, error)
}
