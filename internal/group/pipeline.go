package group

import (
	"cmp"
	"slices"
	"time"
)

// Pipeline is the Chain: Aggregate (candidates) → IdentityStrategy →
// assignment map for the store. ResolveGroup / plan stays a separate phase.
type Pipeline struct {
	CandidateRadius int
	Identity        IdentityStrategy
}

// timestampSweeper is implemented by identity strategies that need
// candidate recall beyond the hash buckets: pictures captured within
// SweepWindow of each other must reach Refine as one chunk even when
// their bucket hashes drifted past the radius
// (docs/identity-decision-graph.md §8).
type timestampSweeper interface {
	SweepWindow() time.Duration
}

// Run partitions imgs into identity groups and returns imageID → groupID
// (1..n in order of first appearance) plus the member lists.
func (p Pipeline) Run(imgs []Image) (map[string]int64, [][]Image, error) {
	identity := p.Identity
	if identity == nil {
		identity = PassThrough{}
	}
	_, candidates := Aggregate(imgs, p.CandidateRadius)
	candidates = SweepCandidates(identity, candidates)
	var identities [][]Image
	for _, c := range candidates {
		parts, err := identity.Refine(c)
		if err != nil {
			return nil, nil, err
		}
		identities = append(identities, parts...)
	}
	assign := map[string]int64{}
	var gid int64
	ordered := make([][]Image, 0, len(identities))
	for _, members := range identities {
		if len(members) == 0 {
			continue
		}
		gid++
		for _, im := range members {
			assign[im.ID] = gid
		}
		ordered = append(ordered, members)
	}
	return assign, ordered, nil
}

// SweepCandidates merges candidate chunks whose members share a capture
// moment when the identity strategy asks for it (timestampSweeper); the
// chunks are returned unchanged otherwise. Pipeline.Run applies it
// internally; the increment partition calls it explicitly.
func SweepCandidates(identity IdentityStrategy, chunks [][]Image) [][]Image {
	if sw, ok := identity.(timestampSweeper); ok {
		return sweepTimestamps(chunks, sw.SweepWindow())
	}
	return chunks
}

// sweepTimestamps merges candidate chunks that share a capture moment:
// every EXIF-bearing picture is timed once per timezone interpretation
// (members with a known offset keep their true instants, an unknown
// offset is read in turn as UTC and each known zone), the timings are
// sorted per pass, and pictures within window of each other merge their
// chunks. Merging only widens what Refine compares; the pairwise identity
// rule stays authoritative, so a zone mismatch or a burst split reappears
// inside Refine while a true same-moment pair is never missed by the
// buckets.
func sweepTimestamps(chunks [][]Image, window time.Duration) [][]Image {
	flat := []Image{}
	timed := 0
	ranges := make([][2]int, 0, len(chunks))
	for _, c := range chunks {
		start := len(flat)
		for _, im := range c {
			if im.ExifCapture != "" {
				timed++
			}
			flat = append(flat, im)
		}
		ranges = append(ranges, [2]int{start, len(flat)})
	}
	if timed < 2 || window <= 0 {
		return chunks
	}
	secs := int64(window / time.Second)
	zones := []string{""}
	known := map[string]bool{"": true}
	for _, im := range flat {
		if im.ExifCapture != "" && im.ExifOffset != "" && !known[im.ExifOffset] {
			known[im.ExifOffset] = true
			zones = append(zones, im.ExifOffset)
		}
	}
	type at struct {
		idx int
		sec int64
	}
	uf := newUnionFind(len(flat))
	for _, r := range ranges { // chunks merge, they never split
		for i := r[0] + 1; i < r[1]; i++ {
			uf.union(r[0], i)
		}
	}
	// In each pass a member with an unknown offset is read as one of the
	// candidate zones while members with a known offset keep their true
	// instants, so every zone interpretation gets its chance to pair.
	for _, zone := range zones {
		ts := make([]at, 0, timed)
		for i, im := range flat {
			if im.ExifCapture == "" {
				continue
			}
			z := im.ExifOffset
			if z == "" {
				z = zone
			}
			if t, ok := parseCapture(im.ExifCapture, z); ok {
				ts = append(ts, at{i, t.Unix()})
			}
		}
		slices.SortFunc(ts, func(a, b at) int { return cmp.Compare(a.sec, b.sec) })
		for a := range ts {
			for b := a + 1; b < len(ts); b++ {
				if ts[b].sec-ts[a].sec > secs {
					break
				}
				uf.union(ts[a].idx, ts[b].idx)
			}
		}
	}
	return uf.groups(flat)
}
