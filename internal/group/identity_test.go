package group

import (
	"fmt"
	"testing"
	"time"

	"photo-library-repair/internal/phash"
)

type mapLookup map[string]phash.Hash

func (m mapLookup) Phash(sha256 string, alg phash.Alg, size phash.Size) (phash.Hash, bool, error) {
	h, ok := m[fmt.Sprintf("%s:%s:%d", sha256, alg, size)]
	return h, ok, nil
}

func TestPassThroughRefine(t *testing.T) {
	imgs := []Image{{ID: "a"}, {ID: "b"}}
	got, err := (PassThrough{}).Refine(imgs)
	if err != nil || len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("got %v err %v", got, err)
	}
}

func TestDoublePhashIntersection(t *testing.T) {
	blockSame := mustHash(16)
	gradSame := mustHash(18)
	blockFar := mustHash(16)
	for i := range blockFar {
		blockFar[i] = ^blockSame[i]
	}
	look := mapLookup{
		"s1:blockhash:16":       blockSame,
		"s1:double-gradient:16": gradSame,
		"s2:blockhash:16":       blockSame,
		"s2:double-gradient:16": gradSame,
		"s3:blockhash:16":       blockFar,
		"s3:double-gradient:16": gradSame,
	}
	d := DoublePhash{Lookup: look, BlockHamming: 8, GradHamming: 8}
	parts, err := d.Refine([]Image{
		{ID: "1", SHA256: "s1"},
		{ID: "2", SHA256: "s2"},
		{ID: "3", SHA256: "s3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("want 2 identities, got %d: %v", len(parts), parts)
	}
}

func TestDoublePhashShaForcesJoin(t *testing.T) {
	d := DoublePhash{Lookup: mapLookup{}, BlockHamming: 1, GradHamming: 1}
	parts, err := d.Refine([]Image{
		{ID: "1", SHA256: "same"},
		{ID: "2", SHA256: "same"},
	})
	if err != nil || len(parts) != 1 || len(parts[0]) != 2 {
		t.Fatalf("sha match must join: %v err %v", parts, err)
	}
}

func TestPipelinePassEqualsAggregate(t *testing.T) {
	imgs := []Image{
		{ID: "a", SHA256: "1", Phash: "00000000000000000000000000000001"},
		{ID: "b", SHA256: "2", Phash: "00000000000000000000000000000001"},
		{ID: "c", SHA256: "3", Phash: "ffffffffffffffffffffffffffffffff"},
	}
	_, groupsA := Aggregate(imgs, 0)
	_, groupsP, err := (Pipeline{CandidateRadius: 0, Identity: PassThrough{}}).Run(imgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(groupsA) != len(groupsP) {
		t.Fatalf("groups %d vs %d", len(groupsA), len(groupsP))
	}
}

func TestVisionStubKeepsCandidate(t *testing.T) {
	imgs := []Image{{ID: "a"}, {ID: "b"}}
	parts, err := (DecisionModel{Decider: UnsureDecider{}}).Refine(imgs)
	if err != nil || len(parts) != 1 || len(parts[0]) != 2 {
		t.Fatalf("stub should keep candidate: %v", parts)
	}
}

func exifImg(id, sha, capture, offset string) Image {
	im := Image{ID: id, Name: id + ".jpg", SHA256: sha, Width: 10, Height: 10, Size: 1}
	im.ExifCapture, im.ExifOffset = capture, offset
	return im
}

func TestExifFirstGateRunsBeforeEXIF(t *testing.T) {
	// The gate runs first: a shared capture second without hashes is not
	// evidence, and neither is one with a far gradient.
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	b := exifImg("b", "s2", "2024:05:01 12:00:01", "+02:00")
	parts, err := (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("same instant without hashes must split: %v", parts)
	}

	block, grad := mustHash(16), mustHash(18)
	farGrad := make(phash.Hash, len(grad))
	for i := range farGrad {
		farGrad[i] = ^grad[i]
	}
	look := mapLookup{
		"s1:blockhash:16": block, "s1:double-gradient:16": grad,
		"s2:blockhash:16": block, "s2:double-gradient:16": farGrad,
	}
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("far gradient must split despite a shared second: %v", parts)
	}

	// Within the gate the pair joins, at any distance in time.
	look["s2:double-gradient:16"] = grad
	b.ExifCapture = "2024:05:01 12:00:02"
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("within the gate must merge: %v", parts)
	}
}

func TestExifFirstCameraBlockVetoesSameInstant(t *testing.T) {
	// Two recorded cameras firing at one instant, hashes agreeing: EXIF
	// vetoes what the hashes joined.
	block, grad := mustHash(16), mustHash(18)
	look := mapLookup{
		"s1:blockhash:16": block, "s1:double-gradient:16": grad,
		"s2:blockhash:16": block, "s2:double-gradient:16": grad,
	}
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	a.ExifCamera = "Canon Canon EOS"
	b := exifImg("b", "s2", "2024:05:01 12:00:00", "+02:00")
	b.ExifCamera = "Nikon Nikon Z6"
	parts, err := (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("distinct cameras at one instant are two pictures: %v", parts)
	}

	// One unknown camera block proves nothing: the pair merges.
	b.ExifCamera = ""
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("unknown camera must not veto: %v", parts)
	}

	// A camera mismatch beyond the window is no veto: identical
	// hashes still merge the pair.
	b.ExifCamera, b.ExifCapture = "Nikon Nikon Z6", "2024:05:01 12:00:05"
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("hashes must decide beyond the window: %v", parts)
	}
}

func TestExifFirstZoneResolution(t *testing.T) {
	// Zone resolution drives the same-instant veto: both sides pass the
	// gate and record different cameras, so the pair splits exactly when
	// their zone-resolved instants fall within the window.
	block, grad := mustHash(16), mustHash(18)
	look := mapLookup{
		"s1:blockhash:16": block, "s1:double-gradient:16": grad,
		"s2:blockhash:16": block, "s2:double-gradient:16": grad,
		"s3:blockhash:16": block, "s3:double-gradient:16": grad,
		"s4:blockhash:16": block, "s4:double-gradient:16": grad,
	}
	veto := func(a, b Image, want string) {
		t.Helper()
		a.ExifCamera, b.ExifCamera = "Canon Canon EOS", "Nikon Nikon Z6"
		parts, err := (ExifFirst{Lookup: look}).Refine([]Image{a, b})
		if err != nil {
			t.Fatal(err)
		}
		if want == "split" && len(parts) != 2 {
			t.Fatalf("%s: must split: %v", want, parts)
		}
		if want == "merge" && len(parts) != 1 {
			t.Fatalf("%s: must merge: %v", want, parts)
		}
	}

	// An unknown offset inherits the partner's known zone: equal wall
	// clocks are then the same instant, so the veto fires.
	veto(exifImg("a", "s1", "2024:05:01 12:00:00", ""),
		exifImg("b", "s2", "2024:05:01 12:00:00", "+11:00"), "split")

	// Two unknown offsets compare their wall clocks: equal clocks are the
	// same instant, the veto fires.
	veto(exifImg("a", "s1", "2024:05:01 12:00:00", ""),
		exifImg("b", "s2", "2024:05:01 12:00:00", ""), "split")

	// Two known zones compare as instants: 12:00+02:00 is 10:00+00:00.
	veto(exifImg("c", "s3", "2024:05:01 12:00:00", "+02:00"),
		exifImg("d", "s4", "2024:05:01 10:00:00", "+00:00"), "split")

	// Half an hour apart is outside the window: no veto, the gate holds.
	veto(exifImg("c", "s3", "2024:05:01 12:00:00", "+02:00"),
		exifImg("d", "s4", "2024:05:01 09:30:00", "+00:00"), "merge")
}

func TestExifFirstDualHashGate(t *testing.T) {
	block := mustHash(16)
	grad := mustHash(18)
	gradFar := make(phash.Hash, len(grad))
	for i := range gradFar {
		gradFar[i] = ^grad[i]
	}
	nearBlock := make(phash.Hash, len(block))
	copy(nearBlock, block)
	nearBlock[0] ^= 1 // distance 1, within the default radius

	a, b := Image{ID: "a", SHA256: "s1"}, Image{ID: "b", SHA256: "s2"}
	both := func() mapLookup {
		return mapLookup{
			"s1:blockhash:16": block, "s1:double-gradient:16": grad,
			"s2:blockhash:16": nearBlock, "s2:double-gradient:16": grad,
		}
	}
	parts, err := (ExifFirst{Lookup: both()}).Refine([]Image{a, b})
	if err != nil || len(parts) != 1 {
		t.Fatalf("both hashes near must merge: %v err %v", parts, err)
	}

	// One hash far: split.
	look := both()
	look["s2:double-gradient:16"] = gradFar
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil || len(parts) != 2 {
		t.Fatalf("far gradient must split: %v err %v", parts, err)
	}

	// One hash missing: no evidence, split.
	look = both()
	delete(look, "s2:double-gradient:16")
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil || len(parts) != 2 {
		t.Fatalf("missing gradient must split: %v err %v", parts, err)
	}
}

func TestExifFirstTransitiveClosure(t *testing.T) {
	// a≡b and b≡c are within radius 1 of each other while a and c are two
	// bits apart: the closure puts all three in one identity.
	block, grad := mustHash(16), mustHash(18)
	bNear, cNear := make(phash.Hash, 16), make(phash.Hash, 16)
	copy(bNear, block)
	copy(cNear, block)
	bNear[0] ^= 1 // a–b distance 1
	cNear[0] ^= 3 // a–c distance 2, b–c distance 1
	look := mapLookup{
		"s1:blockhash:16": block, "s1:double-gradient:16": grad,
		"s2:blockhash:16": bNear, "s2:double-gradient:16": grad,
		"s3:blockhash:16": cNear, "s3:double-gradient:16": grad,
	}
	a := Image{ID: "a", SHA256: "s1"}
	b := Image{ID: "b", SHA256: "s2"}
	c := Image{ID: "c", SHA256: "s3"}
	strategy := ExifFirst{Lookup: look, BlockHamming: 1, GradHamming: 1}
	if parts, err := strategy.Refine([]Image{a, c}); err != nil || len(parts) != 2 {
		t.Fatalf("two bits apart must stay apart: %v err %v", parts, err)
	}
	parts, err := strategy.Refine([]Image{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || len(parts[0]) != 3 {
		t.Fatalf("closure must join all three: %v", parts)
	}
}

func TestExifFirstSameFunc(t *testing.T) {
	same := (ExifFirst{Lookup: mapLookup{}}).SameFunc()
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	b := exifImg("b", "s2", "2024:05:01 12:00:00", "+02:00")
	if same(a, b) {
		t.Fatal("same instant without hashes must fail the gate")
	}
	if !same(a, a) {
		t.Fatal("identical sha256 must pass the gate")
	}
}

func TestSweepTimestampsMergesChunks(t *testing.T) {
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	x := Image{ID: "x", SHA256: "sx"}
	b := exifImg("b", "s2", "2024:05:01 12:00:01", "+02:00")
	merged := sweepTimestamps([][]Image{{a, x}, {b}}, time.Second)
	if len(merged) != 1 || len(merged[0]) != 3 {
		t.Fatalf("1 s apart across chunks must merge: %v", merged)
	}

	far := exifImg("f", "sf", "2024:05:01 13:00:00", "+02:00")
	if got := sweepTimestamps([][]Image{{a}, {far}}, time.Second); len(got) != 2 {
		t.Fatalf("1 h apart must stay apart: %v", got)
	}
	// A zero window leaves the chunks untouched.
	if got := sweepTimestamps([][]Image{{a, x}, {b}}, 0); len(got) != 2 {
		t.Fatalf("zero window must be a no-op: %v", got)
	}

	// Same instant in two zones: some zone projection pairs them.
	c := exifImg("c", "s3", "2024:05:01 12:00:00", "+02:00")
	d := exifImg("d", "s4", "2024:05:01 10:00:00", "+00:00")
	if got := sweepTimestamps([][]Image{{c}, {d}}, time.Second); len(got) != 1 {
		t.Fatalf("equal instants across zones must merge: %v", got)
	}
}

func mustHash(n int) phash.Hash {
	h := make(phash.Hash, n)
	for i := range h {
		h[i] = byte(i + 1)
	}
	return h
}
