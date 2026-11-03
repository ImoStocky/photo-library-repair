// Package group aggregates the library's images into near-duplicate
// groups and resolves every group into a plan: which file provides the
// pixels, which capture time the library keeps, which GPS position, and
// which name the stored image gets.
//
// The rules follow docs/identity-decision-graph.md: group membership
// already carries identity (the same sha256, EXIF capture instants within
// one second from the same camera block, or dual 16×16 hash agreement),
// so resolution only settles the plan fields:
//
//   - pixels come from the HEIC file when one exists, else the
//     highest-resolution file, keeping that file's own perceptual hash;
//   - the capture time is the earliest parsable zone-resolved EXIF
//     capture time, otherwise the earliest file creation time, marked as
//     a fallback;
//   - GPS is supplementary: positions agreeing within tolerance collapse
//     to their medoid, positions disagreeing beyond it are a conflict;
//   - a single picture that is undecodable and carries no parsable EXIF
//     capture time cannot be told from a duplicate by any rule and is a
//     conflict;
//   - a group whose only parsable EXIF picture was created more than
//     five seconds before its own capture time, or whose EXIF-less member
//     was created before the group's earliest EXIF capture time, is a
//     conflict;
//   - the stored name is the member name that most resembles an original
//     camera file name.
//
// Conflicts never drop a group: apply materializes them and records the
// reason. Conflicts can still be settled through the Resolver interface
// (Kronk SDK) without touching these rules.
package group

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"photo-library-repair/internal/phash"
)

// Group statuses stored in the library.
const (
	StatusResolved  = "resolved"
	StatusConflict  = "conflict"
	StatusSingleton = "singleton"
)

// captureTolerance is how many seconds a file's creation time may precede
// the EXIF capture time of its own picture before the group conflicts.
const captureTolerance = 5

// gpsTolerance is the coordinate difference (degrees) still considered
// the same position.
const gpsTolerance = 1e-5

// Image is the per-picture view the resolution rules read.
type Image struct {
	ID          string
	Name        string
	SHA256      string
	Phash       string // 32 hex chars, "" when the picture is unhashable
	CreatedAt   int64  // unix seconds, file birth time
	ExifCapture string // raw EXIF wall clock, "" when absent
	ExifOffset  string // normalized offset "+HH:MM" / "Z", "" when unknown
	ExifCamera  string // camera block (EXIF Make + Model), "" when unknown
	Width       int
	Height      int
	Size        int64
	Kind        string // jpeg|png|gif|webp|tiff|heic
	HasGPS      bool
	GPSLat      float64
	GPSLon      float64
}

// Rules is the ResolvedBy value for plans settled by the automatic rules.
const Rules = "rules"

// Plan is the outcome of resolving one group.
type Plan struct {
	Status          string // StatusResolved | StatusConflict | StatusSingleton
	Reason          string
	ResolvedBy      string // Rules, or the name of the resolver that settled a conflict
	PixelImageID    string
	PixelPhash      string // the selected pixel file's own hash, kept as-is
	CaptureImageID  string
	CaptureTime     string // display wall clock of the kept capture
	CaptureInstant  int64  // unix seconds
	CaptureFallback bool   // true when no EXIF was available
	HasGPS          bool
	GPSLat          float64
	GPSLon          float64
	StoredName      string
}

// Conflict is a group the automatic rules cannot resolve.
type Conflict struct {
	Reason string
	Images []Image
}

// Decision is a conflict resolver's answer.
type Decision struct {
	PixelImageID   string
	CaptureImageID string
	StoredName     string
}

// Resolver settles a conflict the automatic rules cannot. Passing nil to
// ResolveGroup leaves conflicts in StatusConflict for later review.
type Resolver interface {
	// Name identifies the resolver; it marks the groups it settles.
	Name() string
	Resolve(c Conflict) (Decision, error)
}

// Aggregate partitions imgs into near-duplicate groups: identical
// sha256 always groups, and perceptual hashes within maxHamming bits
// chain through transitive closure. It returns the imageID -> groupID
// assignment (ids 1..n, in order of first appearance) and the member
// lists per group.
//
// Near-duplicate candidates come from a band index: each hash is split into
// maxHamming+1 contiguous bands, and two hashes at distance maxHamming or
// less must share a whole band (pigeonhole), so only bucket mates are
// exact-tested. Grouping stays exact, without the all-pairs pass. All
// images in one library are assumed to share the same hash length.
func Aggregate(imgs []Image, maxHamming int) (map[string]int64, [][]Image) {
	n := len(imgs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	// Identical sha256 always groups.
	bySha := map[string]int{}
	for i, im := range imgs {
		if im.SHA256 == "" {
			continue
		}
		if first, ok := bySha[im.SHA256]; ok {
			union(first, i)
		} else {
			bySha[im.SHA256] = i
		}
	}
	// Identical phash strings always group (this also covers unparseable
	// hashes, which all parse to zero).
	byPhash := map[string]int{}
	for i, im := range imgs {
		if im.Phash == "" {
			continue
		}
		if first, ok := byPhash[im.Phash]; ok {
			union(first, i)
		} else {
			byPhash[im.Phash] = i
		}
	}

	var hashes []phash.Hash
	bitLen := 0
	for _, im := range imgs {
		h, _ := phash.HashFromHex(im.Phash) // "" fails; hash stays nil
		hashes = append(hashes, h)
		if n := h.BitLen(); n > bitLen {
			bitLen = n
		}
	}
	if maxHamming >= 0 && n > 1 && bitLen > 0 {
		width := min((bitLen+maxHamming)/(maxHamming+1),
			// Band values fit in 64 bits
			64)
		type bandKey struct {
			band  int
			value uint64
		}
		buckets := map[bandKey][]int{}
		for i, im := range imgs {
			if im.Phash == "" {
				continue
			}
			for b := 0; b*width < bitLen; b++ {
				key := bandKey{b, hashes[i].Band(b*width, width)}
				buckets[key] = append(buckets[key], i)
			}
		}
		seen := map[int64]bool{}
		for _, idxs := range buckets {
			for a := range idxs {
				for c := a + 1; c < len(idxs); c++ {
					i, j := idxs[a], idxs[c]
					if i > j {
						i, j = j, i
					}
					pair := int64(i)*int64(n) + int64(j)
					if seen[pair] {
						continue
					}
					seen[pair] = true
					if hashes[i].Hamming(hashes[j]) <= maxHamming {
						union(i, j)
					}
				}
			}
		}
	}

	firstSeen := map[int]int{} // root -> index of first member
	for i := range n {
		r := find(i)
		if _, ok := firstSeen[r]; !ok {
			firstSeen[r] = i
		}
	}
	roots := make([]int, 0, len(firstSeen))
	for r := range firstSeen {
		roots = append(roots, r)
	}
	// Stable group ids: order roots by the first member's position.
	for i := 1; i < len(roots); i++ {
		for j := i; j > 0 && firstSeen[roots[j-1]] > firstSeen[roots[j]]; j-- {
			roots[j-1], roots[j] = roots[j], roots[j-1]
		}
	}

	assign := make(map[string]int64, n)
	groups := make([][]Image, 0, len(roots))
	for gid, r := range roots {
		members := []Image{}
		for i := range n {
			if find(i) == r {
				members = append(members, imgs[i])
				assign[imgs[i].ID] = int64(gid + 1)
			}
		}
		groups = append(groups, members)
	}
	return assign, groups
}

// ResolveGroup applies the resolution rules to one identity group. The
// members' identity was decided when the group was formed (see
// ExifFirst), so resolution only settles the plan fields and surfaces the
// conflicts that remain: GPS positions beyond tolerance, a lone EXIF
// picture created long before its own capture time, and a single
// undecodable picture without EXIF, which no rule could tell apart from
// a duplicate. res may be nil, in which case conflicts stay unresolved.
func ResolveGroup(imgs []Image, res Resolver) Plan {
	if len(imgs) == 0 {
		panic("group: ResolveGroup of an empty group")
	}

	// A missing timezone offset is no information: such members inherit the
	// group's single known offset so wall clocks compare on the same zone
	// (matching the pair rule). Two distinct known offsets stay as they are.
	if zone, ok := groupZone(imgs); ok && zone != "" {
		imgs = inheritZone(imgs, zone)
	}

	plan := Plan{
		ResolvedBy:   Rules,
		PixelImageID: bestPixels(imgs).ID,
	}
	plan.PixelPhash = imgs[indexof(imgs, plan.PixelImageID)].Phash
	plan.StoredName = pickName(imgs, plan.PixelImageID)

	captureIdx, fallback := pickCapture(imgs)
	capIm := imgs[captureIdx]
	plan.CaptureImageID = capIm.ID
	plan.CaptureTime = captureDisplay(capIm)
	plan.CaptureInstant = captureInstant(capIm)
	plan.CaptureFallback = fallback
	if fallback {
		plan.Reason = "no EXIF capture time; earliest file creation time used"
	} else {
		plan.Reason = "earliest EXIF capture time"
	}

	conflictReason := ""
	if len(imgs) == 1 {
		im := imgs[0]
		if im.Phash == "" && !hasEXIFInstant(im) {
			conflictReason = "picture cannot be decoded and carries no EXIF capture time"
		} else {
			plan.Status = StatusSingleton
		}
	} else {
		plan.Status = StatusResolved
		conflictReason = birthTimeDoubt(imgs)
	}

	// GPS is supplementary: members without a position inherit, agreeing
	// positions collapse to their medoid, and a position beyond tolerance
	// of the medoid conflicts. The medoid stays the plan's best effort.
	if g, why := gpsMedoid(imgs); g >= 0 {
		gIm := imgs[g]
		plan.HasGPS, plan.GPSLat, plan.GPSLon = gIm.HasGPS, gIm.GPSLat, gIm.GPSLon
		if conflictReason == "" {
			conflictReason = why
		}
	}

	if conflictReason == "" {
		return plan
	}
	plan.Status = StatusConflict
	plan.Reason = conflictReason
	if res == nil {
		return plan
	}
	d, err := res.Resolve(Conflict{Reason: conflictReason, Images: imgs})
	if err != nil {
		plan.Reason += "; resolver failed: " + err.Error()
		return plan
	}
	// Ignore decisions that reference images outside the group.
	if !containsID(imgs, d.PixelImageID) || !containsID(imgs, d.CaptureImageID) {
		return plan
	}
	plan.Status = StatusResolved
	plan.Reason = "conflict settled by resolver: " + conflictReason
	plan.ResolvedBy = res.Name()
	plan.PixelImageID = d.PixelImageID
	plan.CaptureImageID = d.CaptureImageID
	plan.StoredName = d.StoredName
	plan.PixelPhash = imgs[indexof(imgs, d.PixelImageID)].Phash
	capIm = imgs[indexof(imgs, d.CaptureImageID)]
	plan.CaptureTime = captureDisplay(capIm)
	plan.CaptureInstant = captureInstant(capIm)
	plan.HasGPS, plan.GPSLat, plan.GPSLon = capIm.HasGPS, capIm.GPSLat, capIm.GPSLon
	return plan
}

// pickCapture returns the index of the member that provides the kept
// capture time: the earliest parsable zone-resolved EXIF instant, or the
// earliest file creation time (fallback true) when no EXIF parses.
func pickCapture(imgs []Image) (int, bool) {
	best, bestT := -1, int64(0)
	for i, im := range imgs {
		if im.ExifCapture == "" {
			continue
		}
		t, ok := parseCapture(im.ExifCapture, im.ExifOffset)
		if !ok {
			continue
		}
		if best < 0 || t.Unix() < bestT {
			best, bestT = i, t.Unix()
		}
	}
	if best >= 0 {
		return best, false
	}
	return earliestCreatedAt(imgs), true
}

// exifInstant parses the picture's own zone-resolved EXIF capture instant.
func exifInstant(im Image) (time.Time, bool) {
	if im.ExifCapture == "" {
		return time.Time{}, false
	}
	return parseCapture(im.ExifCapture, im.ExifOffset)
}

// hasEXIFInstant reports whether the picture carries a parsable EXIF
// capture time.
func hasEXIFInstant(im Image) bool {
	_, ok := exifInstant(im)
	return ok
}

// birthTimeDoubt reports the data quality conflict of a group whose file
// creation times disagree with the EXIF capture times: the single parsable
// EXIF picture created more than captureTolerance seconds before its own
// capture time, or a member without any parsable EXIF capture time whose
// file predates the group's earliest EXIF capture time.
func birthTimeDoubt(imgs []Image) string {
	var instants []int64
	for _, im := range imgs {
		if t, ok := exifInstant(im); ok {
			instants = append(instants, t.Unix())
		}
	}
	if len(instants) == 0 {
		return ""
	}
	if len(instants) == 1 {
		for _, im := range imgs {
			if t, ok := exifInstant(im); ok && im.CreatedAt < t.Unix()-captureTolerance {
				return "file creation time precedes its own EXIF capture time by more than 5 seconds"
			}
		}
	}
	earliest := slices.Min(instants)
	for _, im := range imgs {
		if _, ok := exifInstant(im); !ok && im.CreatedAt+captureTolerance < earliest {
			return "a member without EXIF capture time predates the group's earliest EXIF capture time"
		}
	}
	return ""
}

// gpsMedoid returns the index of the group's kept GPS position: the
// medoid of the members that carry one (members without a position
// inherit it). The reason is non-empty when some position lies farther
// than gpsTolerance from the medoid; the medoid index is returned either
// way as the plan's best effort. g is -1 when no member has a position.
func gpsMedoid(imgs []Image) (int, string) {
	gps := make([]int, 0, len(imgs))
	for i, im := range imgs {
		if im.HasGPS {
			gps = append(gps, i)
		}
	}
	if len(gps) == 0 {
		return -1, ""
	}
	best, bestSum := gps[0], 0.0
	for a := range gps {
		sum := 0.0
		for b := range gps {
			sum += gpsDistance(imgs[gps[a]], imgs[gps[b]])
		}
		if a == 0 || sum < bestSum {
			best, bestSum = gps[a], sum
		}
	}
	for _, b := range gps {
		if !gpsClose(imgs[best], imgs[b]) {
			return best, "EXIF GPS positions differ"
		}
	}
	return best, ""
}

// gpsDistance is a plain squared coordinate distance; at these tiny
// tolerances the projection distortion does not matter.
func gpsDistance(a, b Image) float64 {
	dLat, dLon := a.GPSLat-b.GPSLat, a.GPSLon-b.GPSLon
	return dLat*dLat + dLon*dLon
}

func gpsClose(a, b Image) bool {
	return math.Abs(a.GPSLat-b.GPSLat) < gpsTolerance && math.Abs(a.GPSLon-b.GPSLon) < gpsTolerance
}

// bestPixels picks the file that provides the pixels: an HEIC original
// beats every other format, then the most pixels, then the larger file;
// ties keep input order.
func bestPixels(imgs []Image) Image {
	best := 0
	for i := 1; i < len(imgs); i++ {
		a, b := imgs[best], imgs[i]
		ha, hb := a.Kind == "heic", b.Kind == "heic"
		pa, pb := int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height)
		switch {
		case ha != hb:
			if hb {
				best = i
			}
		case pa != pb:
			if pb > pa {
				best = i
			}
		case b.Size > a.Size:
			best = i
		}
	}
	return imgs[best]
}

// earliestCreatedAt returns the index of the picture with the oldest
// creation time, first in input order on ties.
func earliestCreatedAt(imgs []Image) int {
	best := 0
	for i := 1; i < len(imgs); i++ {
		if imgs[i].CreatedAt < imgs[best].CreatedAt {
			best = i
		}
	}
	return best
}

func indexof(imgs []Image, id string) int {
	for i, im := range imgs {
		if im.ID == id {
			return i
		}
	}
	panic("group: image id " + id + " not in group")
}

func containsID(imgs []Image, id string) bool {
	for _, im := range imgs {
		if im.ID == id {
			return true
		}
	}
	return false
}

// groupZone returns the single timezone offset recorded by the group's EXIF
// members ("" when none is), with ok false when members record distinct
// known offsets.
func groupZone(imgs []Image) (string, bool) {
	zone := ""
	for _, im := range imgs {
		if im.ExifCapture == "" || im.ExifOffset == "" {
			continue
		}
		if zone == "" {
			zone = im.ExifOffset
		} else if im.ExifOffset != zone {
			return "", false
		}
	}
	return zone, true
}

// inheritZone returns the images with the EXIF members' unknown timezone
// offsets filled with zone, copying the slice only when it changes.
func inheritZone(imgs []Image, zone string) []Image {
	if !slices.ContainsFunc(imgs, func(im Image) bool {
		return im.ExifCapture != "" && im.ExifOffset == ""
	}) {
		return imgs
	}
	out := slices.Clone(imgs)
	for i := range out {
		if out[i].ExifCapture != "" && out[i].ExifOffset == "" {
			out[i].ExifOffset = zone
		}
	}
	return out
}

// parseCapture turns an EXIF wall clock (YYYY:MM:DD HH:MM:SS) plus its
// normalized offset into a UTC instant. An empty offset is treated as UTC.
func parseCapture(wall, offset string) (time.Time, bool) {
	t, err := time.Parse("2006:01:02 15:04:05", strings.TrimSpace(wall))
	if err != nil {
		return time.Time{}, false
	}
	t = t.UTC()
	if offset == "" || offset == "Z" {
		return t, true
	}
	sign := 1
	if offset[0] == '-' {
		sign = -1
	}
	body := strings.ReplaceAll(offset[1:], ":", "")
	if len(body) == 2 {
		body += "00"
	}
	if len(body) != 4 {
		return time.Time{}, false
	}
	hh, err1 := strconv.Atoi(body[:2])
	mm, err2 := strconv.Atoi(body[2:])
	if err1 != nil || err2 != nil || hh > 14 || mm > 59 {
		return time.Time{}, false
	}
	return t.Add(-time.Duration(sign*(hh*3600+mm*60)) * time.Second), true
}

// captureDisplay renders the kept capture time for display.
func captureDisplay(im Image) string {
	if im.ExifCapture != "" {
		return im.ExifCapture
	}
	return time.Unix(im.CreatedAt, 0).UTC().Format("2006-01-02 15:04:05")
}

// captureInstant is the unix instant of the picture's capture time.
func captureInstant(im Image) int64 {
	if im.ExifCapture != "" {
		if t, ok := parseCapture(im.ExifCapture, im.ExifOffset); ok {
			return t.Unix()
		}
	}
	return im.CreatedAt
}

var (
	copySuffixRe   = regexp.MustCompile(`\(\d+\)$`)
	timestampRe    = regexp.MustCompile(`^\d{4}[-_./]\d{2}[-_./]\d{2}`)
	cameraPrefixRe = regexp.MustCompile(`^(IMG|DSC|PANO|DCIM|DCI_|DCIM_|SANYO|P10[0-9]{2}|R0[0-9]{3})`)
)

// nameScore rates how much a file name resembles an original camera
// name; higher is better.
func nameScore(name string) int {
	base := strings.TrimSuffix(name, extOf(name))
	score := 0
	if copySuffixRe.MatchString(base) {
		score -= 3
	}
	if strings.Contains(strings.ToLower(base), "copy") {
		score -= 2
	}
	if timestampRe.MatchString(base) {
		score -= 2
	}
	if cameraPrefixRe.MatchString(base) {
		score += 1
	}
	return score
}

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}

// pickName chooses the member name that most resembles an original file
// name, breaking ties in favor of the pixel file's name, then
// lexicographically for determinism.
func pickName(imgs []Image, pixelID string) string {
	best := 0
	for i := 1; i < len(imgs); i++ {
		a, b := imgs[best], imgs[i]
		sa, sb := nameScore(a.Name), nameScore(b.Name)
		if sb > sa {
			best = i
			continue
		}
		if sb < sa {
			continue
		}
		if a.ID == pixelID && b.ID != pixelID {
			continue
		}
		if b.ID == pixelID && a.ID != pixelID {
			best = i
			continue
		}
		if b.Name < a.Name {
			best = i
		}
	}
	return imgs[best].Name
}
