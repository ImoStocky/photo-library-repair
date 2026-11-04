package group

import (
	"photo-library-repair/internal/phash"
)

// LibMember is one existing library image paired with its group id.
// Partition matches incoming members against these.
type LibMember struct {
	Image
	GroupID int64
}

// Match is one incoming member classified against the library. GroupID is
// the library group the member duplicates (present) or joins (matched).
// Ambiguous is true when the member sits at the same (minimal) distance
// from two or more library groups ("matches more than one library group");
// the lowest group id is chosen.
type Match struct {
	Image
	GroupID   int64
	Distance  int
	Ambiguous bool
}

// SameFunc reports whether two pictures are the same image under an
// identity strategy (see ExifFirst.SameFunc).
type SameFunc func(a, b Image) bool

// Partition classifies incoming connected components (already grouped
// among themselves, e.g. by Aggregate) against the library, per the merge
// spec (chapter 16):
//
//   - present: sha256 match, or phash distance 0 with identical size.
//   - matched: distance 1…maxHamming (or 0 with a different size) to a
//     library group; the member joins that group.
//   - unmatched: nothing within maxHamming; the remaining members keep
//     their component shape.
//
// Ties across library groups resolve to the smallest distance; equal
// distances pick the lowest group id and flag the match ambiguous. Library
// groups never merge through an incoming member.
//
// same is an optional identity gate: a library member only takes part in
// the classification when the pair passes the identity rule (a close hash
// alone is not identity, see docs/identity-decision-graph.md). A nil same
// keeps the pure hash classification.
func Partition(components [][]Image, lib []LibMember, maxHamming int, same SameFunc) (present, matched []Match, unmatched [][]Image) {
	libHashes := make([]phash.Hash, len(lib))
	for i := range lib {
		if lib[i].Phash == "" {
			continue
		}
		libHashes[i], _ = phash.HashFromHex(lib[i].Phash) // nil stays "unhashable"
	}
	for _, comp := range components {
		var rest []Image
		for _, im := range comp {
			h := parsePhash(im.Phash)
			m, kind := classify(im, h, lib, libHashes, maxHamming, same)
			switch kind {
			case classPresent:
				present = append(present, m)
			case classMatched:
				matched = append(matched, m)
			default:
				rest = append(rest, im)
			}
		}
		if len(rest) > 0 {
			unmatched = append(unmatched, rest)
		}
	}
	return present, matched, unmatched
}

const (
	classUnmatched = iota
	classPresent
	classMatched
)

// classify sorts one member into present / matched / unmatched. Library
// members are walked in order so ties keep the lowest group id.
func classify(im Image, h phash.Hash, lib []LibMember, libHashes []phash.Hash, maxHamming int, same SameFunc) (Match, int) {
	best := Match{Image: im}
	seen := false
	exact := false // best distance is an exact (present) hit
	for i := range lib {
		if same != nil && !same(im, lib[i].Image) {
			continue // hash-proximate but not the same image under the identity rule
		}
		exactHit := im.SHA256 != "" && im.SHA256 == lib[i].SHA256
		if !exactHit && h != nil && libHashes[i] != nil {
			exactHit = h.Hamming(libHashes[i]) == 0 && sameSize(im, lib[i].Image)
		}
		d := maxHamming + 1
		if exactHit {
			d = 0
		} else if h != nil && libHashes[i] != nil {
			d = h.Hamming(libHashes[i])
		}
		if d > maxHamming && same == nil {
			// Pure-hash mode: the primary radius is the only identity
			// evidence. With a gate, pairs that passed it above stay
			// candidates — a 90° re-save sits far outside the primary
			// bucket yet is the same picture.
			continue
		}
		if !seen || d < best.Distance {
			best, seen = Match{Image: im, GroupID: lib[i].GroupID, Distance: d}, true
			exact = exactHit
			continue
		}
		if d == best.Distance && lib[i].GroupID != best.GroupID {
			best.Ambiguous = true
			if lib[i].GroupID < best.GroupID {
				best.GroupID, exact = lib[i].GroupID, exactHit
			}
		}
	}
	if !seen {
		return Match{}, classUnmatched
	}
	if exact {
		return best, classPresent
	}
	return best, classMatched
}

func sameSize(a, b Image) bool { return a.Width == b.Width && a.Height == b.Height }

// parsePhash decodes a stored hex hash; "" or malformed decodes to nil
// (unhashable), which never takes a distance.
func parsePhash(hex string) phash.Hash {
	if hex == "" {
		return nil
	}
	h, err := phash.HashFromHex(hex)
	if err != nil {
		return nil
	}
	return h
}
