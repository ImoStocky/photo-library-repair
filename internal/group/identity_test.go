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

func TestExifFirstSameInstantNeedsNoHashes(t *testing.T) {
	// Different files sharing a capture moment are the same image even
	// with no secondary hashes at all for the pair.
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	b := exifImg("b", "s2", "2024:05:01 12:00:01", "+02:00")
	parts, err := (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || len(parts[0]) != 2 {
		t.Fatalf("1 s apart must merge: %v", parts)
	}

	// Two seconds apart is no veto: the hash gate decides and identical
	// dual hashes re-join the pair.
	block, grad := mustHash(16), mustHash(18)
	look := mapLookup{
		"s1:blockhash:16": block, "s1:double-gradient:16": grad,
		"s2:blockhash:16": block, "s2:double-gradient:16": grad,
	}
	b.ExifCapture = "2024:05:01 12:00:02"
	parts, err = (ExifFirst{Lookup: look}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("identical hashes must merge beyond the window: %v", parts)
	}

	// Without hashes the same pair stays split.
	parts, err = (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("beyond the window without hashes: %v", parts)
	}
}

func TestExifFirstCameraBlockVetoesSameInstant(t *testing.T) {
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	a.ExifCamera = "Canon Canon EOS"
	b := exifImg("b", "s2", "2024:05:01 12:00:00", "+02:00")
	b.ExifCamera = "Nikon Nikon Z6"
	parts, err := (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("distinct cameras at one instant are two pictures: %v", parts)
	}

	// One unknown camera block proves nothing: the pair merges.
	b.ExifCamera = ""
	parts, err = (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("unknown camera must not veto: %v", parts)
	}

	// A camera mismatch beyond the window is no veto either: identical
	// hashes still merge the pair.
	block, grad := mustHash(16), mustHash(18)
	look := mapLookup{
		"s1:blockhash:16": block, "s1:double-gradient:16": grad,
		"s2:blockhash:16": block, "s2:double-gradient:16": grad,
	}
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
	// An unknown offset is never re-based on the partner's zone: equal
	// wall clocks in different zones are different instants, and without
	// hashes the pair stays split.
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "")
	b := exifImg("b", "s2", "2024:05:01 12:00:00", "+11:00")
	parts, err := (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil || len(parts) != 2 {
		t.Fatalf("unknown offset compares its wall clock: %v err %v", parts, err)
	}

	// Two unknown offsets compare their wall clocks: equal clocks merge.
	b.ExifOffset = ""
	parts, err = (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{a, b})
	if err != nil || len(parts) != 1 {
		t.Fatalf("equal wall clocks must merge: %v err %v", parts, err)
	}

	// Two known zones compare as instants: 12:00+02:00 is 10:00+00:00.
	c := exifImg("c", "s3", "2024:05:01 12:00:00", "+02:00")
	d := exifImg("d", "s4", "2024:05:01 10:00:00", "+00:00")
	parts, err = (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{c, d})
	if err != nil || len(parts) != 1 {
		t.Fatalf("equal instants must merge: %v err %v", parts, err)
	}
	d.ExifCapture = "2024:05:01 09:30:00"
	parts, err = (ExifFirst{Lookup: mapLookup{}}).Refine([]Image{c, d})
	if err != nil || len(parts) != 2 {
		t.Fatalf("half an hour apart must split: %v err %v", parts, err)
	}
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
	// a≡b through the shared capture moment, b≡c through the dual hashes:
	// the closure puts all three in one identity.
	a := exifImg("a", "s1", "2024:05:01 12:00:00", "+02:00")
	b := exifImg("b", "s2", "2024:05:01 12:00:00", "+02:00")
	c := Image{ID: "c", SHA256: "s3"}
	block, grad := mustHash(16), mustHash(18)
	look := mapLookup{
		"s2:blockhash:16": block, "s2:double-gradient:16": grad,
		"s3:blockhash:16": block, "s3:double-gradient:16": grad,
	}
	parts, err := (ExifFirst{Lookup: look}).Refine([]Image{a, b, c})
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
	if !same(a, b) {
		t.Fatal("same instant must pass the gate")
	}
	c := exifImg("c", "s3", "2024:05:01 12:00:05", "+02:00")
	if same(a, c) {
		t.Fatal("5 s apart must fail the gate")
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
