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

// PartitionStats holds optional counters filled by Partition when non-nil
// (P5 observability). Safe for single-threaded Partition; parallel
// Partition accumulates under its own lock.
type PartitionStats struct {
	Incoming    int
	SHAHits     int
	Candidates  int // sum of candidate-list lengths considered
	SameCalls   int
	LibMembers  int
	BandLookups int
}

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
//
// Candidates come from a sha256 map plus a primary-phash band index (same
// pigeonhole banding as Aggregate), so SameFunc is only invoked on a short
// shortlist — not on every library member.
func Partition(components [][]Image, lib []LibMember, maxHamming int, same SameFunc) (present, matched []Match, unmatched [][]Image) {
	return partition(components, lib, maxHamming, same, nil)
}

func partition(components [][]Image, lib []LibMember, maxHamming int, same SameFunc, stats *PartitionStats) (present, matched []Match, unmatched [][]Image) {
	idx := buildLibIndex(lib, maxHamming)
	if stats != nil {
		stats.LibMembers = len(lib)
	}
	for _, comp := range components {
		var rest []Image
		for _, im := range comp {
			h := parsePhash(im.Phash)
			m, kind := classifyIndexed(im, h, idx, maxHamming, same, stats)
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

type bandKey struct {
	band  int
	value uint64
}

// libIndex is the sha256 + primary-phash band lookup used by classify.
type libIndex struct {
	lib        []LibMember
	hashes     []phash.Hash
	bySHA      map[string][]int
	buckets    map[bandKey][]int
	width      int
	bitLen     int
	maxHamming int
}

func bandWidth(bitLen, maxHamming int) int {
	if bitLen <= 0 {
		return 0
	}
	return min((bitLen+maxHamming)/(maxHamming+1), 64)
}

func buildLibIndex(lib []LibMember, maxHamming int) *libIndex {
	idx := &libIndex{
		lib:        lib,
		hashes:     make([]phash.Hash, len(lib)),
		bySHA:      map[string][]int{},
		buckets:    map[bandKey][]int{},
		maxHamming: maxHamming,
	}
	bitLen := 0
	for i := range lib {
		if lib[i].SHA256 != "" {
			idx.bySHA[lib[i].SHA256] = append(idx.bySHA[lib[i].SHA256], i)
		}
		if lib[i].Phash == "" {
			continue
		}
		h, err := phash.HashFromHex(lib[i].Phash)
		if err != nil {
			continue
		}
		idx.hashes[i] = h
		if n := h.BitLen(); n > bitLen {
			bitLen = n
		}
	}
	idx.bitLen = bitLen
	idx.width = bandWidth(bitLen, maxHamming)
	if idx.width <= 0 || maxHamming < 0 {
		return idx
	}
	// Band near-dupe search uses one representative per group (largest
	// pixels) so |candidates| tracks group count, not member count.
	// sha256 present hits still see every member via bySHA.
	for _, i := range groupRepresentatives(lib) {
		h := idx.hashes[i]
		if h == nil {
			continue
		}
		for b := 0; b*idx.width < bitLen; b++ {
			key := bandKey{b, h.Band(b*idx.width, idx.width)}
			idx.buckets[key] = append(idx.buckets[key], i)
		}
	}
	return idx
}

// groupRepresentatives returns one lib index per GroupID: the member with
// the largest pixel area (then stable sha256 / id ties).
func groupRepresentatives(lib []LibMember) []int {
	best := map[int64]int{}
	for i, m := range lib {
		if m.GroupID == 0 {
			continue
		}
		j, ok := best[m.GroupID]
		if !ok || betterRep(m, lib[j]) {
			best[m.GroupID] = i
		}
	}
	out := make([]int, 0, len(best))
	for _, i := range best {
		out = append(out, i)
	}
	return out
}

func betterRep(a, b LibMember) bool {
	aa, bb := a.Width*a.Height, b.Width*b.Height
	if aa != bb {
		return aa > bb
	}
	if a.SHA256 != b.SHA256 {
		return a.SHA256 < b.SHA256
	}
	return a.ID < b.ID
}

// candidates returns unique library indices that share a phash band with h
// (pigeonhole: any pair within maxHamming must share a whole band).
func (idx *libIndex) candidates(h phash.Hash, stats *PartitionStats) []int {
	if h == nil || idx.width <= 0 {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for b := 0; b*idx.width < idx.bitLen; b++ {
		key := bandKey{b, h.Band(b*idx.width, idx.width)}
		if stats != nil {
			stats.BandLookups++
		}
		for _, i := range idx.buckets[key] {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	return out
}

// classifyIndexed sorts one member into present / matched / unmatched using
// the library index. sha256 hits short-circuit; otherwise only band-mates
// are considered (and SameFunc runs only on that shortlist).
func classifyIndexed(im Image, h phash.Hash, idx *libIndex, maxHamming int, same SameFunc, stats *PartitionStats) (Match, int) {
	if stats != nil {
		stats.Incoming++
	}
	// Exact content already in the library: present without the gate.
	if im.SHA256 != "" {
		if hits := idx.bySHA[im.SHA256]; len(hits) > 0 {
			if stats != nil {
				stats.SHAHits++
			}
			best := Match{Image: im, GroupID: idx.lib[hits[0]].GroupID, Distance: 0}
			for _, i := range hits[1:] {
				gid := idx.lib[i].GroupID
				if gid != best.GroupID {
					best.Ambiguous = true
					if gid < best.GroupID {
						best.GroupID = gid
					}
				}
			}
			return best, classPresent
		}
	}

	cands := idx.candidates(h, stats)
	if stats != nil {
		stats.Candidates += len(cands)
	}
	best := Match{Image: im}
	seen := false
	exact := false
	for _, i := range cands {
		lh := idx.hashes[i]
		// Cheap primary distance before the expensive identity gate.
		exactHit := false
		d := maxHamming + 1
		if h != nil && lh != nil {
			d = h.Hamming(lh)
			exactHit = d == 0 && sameSize(im, idx.lib[i].Image)
		}
		if d > maxHamming {
			// Outside the primary radius: with a gate, a 90° re-save can
			// still be the same picture, but it will not share a primary
			// band — band shortlisting already dropped those. Skip.
			continue
		}
		if same != nil {
			if stats != nil {
				stats.SameCalls++
			}
			if !same(im, idx.lib[i].Image) {
				continue
			}
		}
		if !seen || d < best.Distance {
			best, seen = Match{Image: im, GroupID: idx.lib[i].GroupID, Distance: d}, true
			exact = exactHit
			continue
		}
		if d == best.Distance && idx.lib[i].GroupID != best.GroupID {
			best.Ambiguous = true
			if idx.lib[i].GroupID < best.GroupID {
				best.GroupID, exact = idx.lib[i].GroupID, exactHit
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
