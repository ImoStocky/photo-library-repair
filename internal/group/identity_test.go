package group

import (
	"fmt"
	"testing"

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

func mustHash(n int) phash.Hash {
	h := make(phash.Hash, n)
	for i := range h {
		h[i] = byte(i + 1)
	}
	return h
}
