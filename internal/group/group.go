// Package group aggregates the library's images into near-duplicate
// groups and resolves every group into a plan: which file provides the
// pixels, which capture time the library keeps, which GPS position, and
// which name the stored image gets.
//
// The rules follow the library design:
//
//   - pixels come from the highest-resolution file, keeping that file's
//     own perceptual hash;
//   - the capture time is the earliest EXIF capture time when the group
//     carries EXIF (time-zone aware), otherwise the earliest file
//     creation time, marked as a fallback;
//   - a single EXIF picture whose file existed more than five seconds
//     before its own capture time is a conflict;
//   - several EXIF pictures agree when only their capture times differ;
//     any other EXIF disagreement (offsets, GPS) is a conflict;
//   - the stored name is the member name that most resembles an original
//     camera file name.
//
// Conflicts are surfaced through the Resolver interface so a later phase
// (Kronk SDK) can settle them without touching these rules.
package group

import (
	"fmt"
	"math"
	"regexp"
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
	Width       int
	Height      int
	Size        int64
	HasGPS      bool
	GPSLat      float64
	GPSLon      float64
}

// Plan is the outcome of resolving one group.
type Plan struct {
	Status          string // StatusResolved | StatusConflict | StatusSingleton
	Reason          string
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

// Resolver settles a conflict the automatic rules cannot. Phase 5 ships
// no implementation; passing nil to ResolveGroup leaves conflicts in
// StatusConflict for later review.
type Resolver interface {
	Resolve(c Conflict) (Decision, error)
}

// Aggregate partitions imgs into near-duplicate groups: identical
// sha256 always groups, and perceptual hashes within maxHamming bits
// chain through transitive closure. It returns the imageID -> groupID
// assignment (ids 1..n, in order of first appearance) and the member
// lists per group.
func Aggregate(imgs []Image, maxHamming int) (map[string]int64, [][]Image) {
	n := len(imgs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
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

	var hashes []phash.Hash
	for _, im := range imgs {
		h, _ := phash.HashFromHex(im.Phash) // "" fails; hash stays zero
		hashes = append(hashes, h)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if imgs[i].SHA256 != "" && imgs[i].SHA256 == imgs[j].SHA256 {
				union(i, j)
				continue
			}
			if imgs[i].Phash != "" && imgs[i].Phash == imgs[j].Phash {
				union(i, j)
				continue
			}
			if imgs[i].Phash != "" && imgs[j].Phash != "" &&
				hashes[i].Hamming(hashes[j]) <= maxHamming {
				union(i, j)
			}
		}
	}

	firstSeen := map[int]int{} // root -> index of first member
	for i := 0; i < n; i++ {
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
		for i := 0; i < n; i++ {
			if find(i) == r {
				members = append(members, imgs[i])
				assign[imgs[i].ID] = int64(gid + 1)
			}
		}
		groups = append(groups, members)
	}
	return assign, groups
}

// ResolveGroup applies the resolution rules to one group of pictures.
// res may be nil, in which case conflicts stay unresolved.
func ResolveGroup(imgs []Image, res Resolver) Plan {
	if len(imgs) == 0 {
		panic("group: ResolveGroup of an empty group")
	}
	if len(imgs) == 1 {
		im := imgs[0]
		return Plan{
			Status:          StatusSingleton,
			PixelImageID:    im.ID,
			PixelPhash:      im.Phash,
			CaptureImageID:  im.ID,
			CaptureTime:     captureDisplay(im),
			CaptureInstant:  captureInstant(im),
			CaptureFallback: im.ExifCapture == "",
			HasGPS:          im.HasGPS,
			GPSLat:          im.GPSLat,
			GPSLon:          im.GPSLon,
			StoredName:      im.Name,
		}
	}

	var exif []Image
	for _, im := range imgs {
		if im.ExifCapture != "" {
			exif = append(exif, im)
		}
	}

	captureIdx, reason, conflictReason, fallback := 0, "", "", false
	switch {
	case len(exif) == 0:
		captureIdx = earliestCreatedAt(imgs)
		fallback = true
		reason = "no EXIF capture time; earliest file creation time used"
	case len(exif) == 1:
		im := exif[0]
		t, ok := parseCapture(im.ExifCapture, im.ExifOffset)
		if !ok {
			conflictReason = fmt.Sprintf("unparsable EXIF capture time %q", im.ExifCapture)
			break
		}
		if im.CreatedAt < t.Unix()-captureTolerance {
			conflictReason = "file creation time precedes its own EXIF capture time by more than 5 seconds"
			break
		}
		captureIdx = indexof(imgs, im.ID)
		reason = "earliest EXIF capture time"
	default:
		captureIdx, conflictReason = resolveMultipleEXIF(imgs, exif)
		if conflictReason == "" {
			reason = "earliest EXIF capture time; EXIF data agrees"
		}
	}

	plan := Plan{
		Status:          StatusResolved,
		Reason:          reason,
		PixelImageID:    bestPixels(imgs).ID,
		CaptureImageID:  imgs[captureIdx].ID,
		CaptureFallback: fallback,
	}
	plan.PixelPhash = imgs[indexof(imgs, plan.PixelImageID)].Phash
	capIm := imgs[captureIdx]
	plan.CaptureTime = captureDisplay(capIm)
	plan.CaptureInstant = captureInstant(capIm)
	plan.HasGPS, plan.GPSLat, plan.GPSLon = capIm.HasGPS, capIm.GPSLat, capIm.GPSLon
	plan.StoredName = pickName(imgs, plan.PixelImageID)

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

// resolveMultipleEXIF applies the agreement rules to several EXIF
// pictures and returns the capture index, or a non-empty conflict reason.
func resolveMultipleEXIF(imgs, exif []Image) (int, string) {
	type parsed struct {
		idx     int
		instant int64
	}
	var all []parsed
	for i, im := range exif {
		t, ok := parseCapture(im.ExifCapture, im.ExifOffset)
		if !ok {
			return 0, fmt.Sprintf("unparsable EXIF capture time %q", im.ExifCapture)
		}
		all = append(all, parsed{idx: i, instant: t.Unix()})
	}
	// Everything but the capture time must agree.
	for i := 1; i < len(exif); i++ {
		if exif[i].ExifOffset != exif[0].ExifOffset {
			return 0, fmt.Sprintf("EXIF timezone offsets differ (%q vs %q)", exif[i].ExifOffset, exif[0].ExifOffset)
		}
		if !gpsAgree(exif[i], exif[0]) {
			return 0, "EXIF GPS positions differ"
		}
	}
	best := 0
	for i := 1; i < len(all); i++ {
		if all[i].instant < all[best].instant {
			best = i
		}
	}
	return indexof(imgs, exif[all[best].idx].ID), ""
}

// bestPixels picks the file with the most pixels, breaking ties on file
// size, then input order.
func bestPixels(imgs []Image) Image {
	best := 0
	for i := 1; i < len(imgs); i++ {
		a, b := imgs[best], imgs[i]
		ca, cb := int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height)
		if cb > ca || (cb == ca && b.Size > a.Size) {
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

func gpsAgree(a, b Image) bool {
	if a.HasGPS != b.HasGPS {
		return false
	}
	if !a.HasGPS {
		return true
	}
	return math.Abs(a.GPSLat-b.GPSLat) < gpsTolerance && math.Abs(a.GPSLon-b.GPSLon) < gpsTolerance
}

// parseCapture turns an EXIF wall clock (YYYY:MM:DD HH:MM:SS) plus its
// normalized offset into a UTC instant. An empty offset is treated as UTC
// (callers compare offsets for agreement separately).
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
