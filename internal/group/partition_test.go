package group

import (
	"testing"
)

const (
	hZero = "00000000000000000000000000000000"
	hOne  = "00000000000000000000000000000001"
	hFar  = "ffffffffffffffffffffffffffffffff"
)

func libm(id int64, sha, phash string, w, h int) LibMember {
	return LibMember{
		ID: sha, SHA256: sha, Phash: phash, Width: w, Height: h,
		GroupID: id,
	}
}

func TestPartitionPresent(t *testing.T) {
	lib := []LibMember{libm(1, "aaa", hZero, 100, 100)}
	comps := [][]Image{
		{ // same sha → present
			{ID: "n1", SHA256: "aaa", Phash: hZero, Width: 100, Height: 100},
		},
		{ // same phash + same size → present
			{ID: "n2", SHA256: "zzz", Phash: hZero, Width: 100, Height: 100},
		},
	}
	present, matched, unmatched := Partition(comps, lib, 12, nil)
	if len(present) != 2 || len(matched) != 0 || len(unmatched) != 0 {
		t.Fatalf("present=%d matched=%d unmatched=%d", len(present), len(matched), len(unmatched))
	}
	if present[0].GroupID != 1 || present[1].GroupID != 1 {
		t.Fatalf("present members should carry the library group: %v", present)
	}
}

func TestPartitionPresentVsMatchedBySize(t *testing.T) {
	lib := []LibMember{libm(1, "aaa", hZero, 100, 100)}
	// same phash, different size → matched (join group 1), not present
	comps := [][]Image{{
		{ID: "n1", SHA256: "bbb", Phash: hZero, Width: 200, Height: 200},
	}}
	present, matched, unmatched := Partition(comps, lib, 12, nil)
	if len(present) != 0 || len(unmatched) != 0 {
		t.Fatalf("present=%d unmatched=%d", len(present), len(unmatched))
	}
	if len(matched) != 1 || matched[0].GroupID != 1 || matched[0].Distance != 0 {
		t.Fatalf("matched=%v", matched)
	}
}

func TestPartitionMatchWithinRadius(t *testing.T) {
	lib := []LibMember{libm(7, "aaa", hZero, 100, 100)}
	comps := [][]Image{{
		{ID: "n1", SHA256: "b1", Phash: hOne, Width: 100, Height: 100}, // d=1
		{ID: "n2", SHA256: "b2", Phash: hFar, Width: 100, Height: 100}, // d=128
	}}
	_, matched, unmatched := Partition(comps, lib, 12, nil)
	if len(matched) != 1 || matched[0].GroupID != 7 || matched[0].Distance != 1 {
		t.Fatalf("matched=%v", matched)
	}
	if len(unmatched) != 1 || len(unmatched[0]) != 1 || unmatched[0][0].ID != "n2" {
		t.Fatalf("unmatched keeps component shape: %v", unmatched)
	}
}

func TestPartitionClosestGroupWins(t *testing.T) {
	lib := []LibMember{
		libm(3, "a1", hOne, 100, 100), // d=1 to hZero
		libm(4, "a2", hFar, 100, 100), // far
	}
	comps := [][]Image{{
		{ID: "n1", SHA256: "b1", Phash: hZero, Width: 100, Height: 100},
	}}
	_, matched, _ := Partition(comps, lib, 12, nil)
	if len(matched) != 1 || matched[0].GroupID != 3 || matched[0].Ambiguous {
		t.Fatalf("want unambiguous join of group 3: %v", matched)
	}
}

func TestPartitionAmbiguousTie(t *testing.T) {
	// One hash shared by both library groups → tie → lowest id + ambiguous.
	lib := []LibMember{
		libm(9, "a1", hZero, 100, 100),
		libm(5, "a2", hZero, 100, 100),
	}
	comps := [][]Image{{
		{ID: "n1", SHA256: "b1", Phash: hOne, Width: 100, Height: 100},
	}}
	_, matched, _ := Partition(comps, lib, 12, nil)
	if len(matched) != 1 {
		t.Fatalf("matched=%v", matched)
	}
	if !matched[0].Ambiguous || matched[0].GroupID != 5 {
		t.Fatalf("want ambiguous join of group 5: %v", matched)
	}
}

func TestPartitionUnhashableStaysUnmatched(t *testing.T) {
	lib := []LibMember{libm(1, "aaa", "", 100, 100)}
	comps := [][]Image{{
		{ID: "n1", SHA256: "b1", Phash: "", Width: 100, Height: 100},
	}}
	present, matched, unmatched := Partition(comps, lib, 12, nil)
	if len(present) != 0 || len(matched) != 0 || len(unmatched) != 1 {
		t.Fatalf("present=%d matched=%d unmatched=%d", len(present), len(matched), len(unmatched))
	}
}
