package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSourceTracking(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dir := "/photos/2024"
	if _, exists, _ := s.FindSource(dir); exists {
		t.Fatal("source should not exist yet")
	}
	id, err := s.AddSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("first source id = %d, want 1", id)
	}
	got, exists, err := s.FindSource(dir)
	if err != nil || !exists || got != id {
		t.Fatalf("FindSource = (%d, %v, %v), want (%d, true, nil)", got, exists, err, id)
	}
	// Adding the same directory again must not duplicate it.
	if _, err := s.AddSource(dir); err != nil {
		t.Fatal(err)
	}
	sources, err := s.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[id] != dir {
		t.Fatalf("Sources = %v", sources)
	}
}

func TestImageUpsertAndGroups(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.AddSource("/photos")
	if err != nil {
		t.Fatal(err)
	}
	img := Image{
		ID:          NewID(time.Now(), 1),
		SourceID:    id,
		RelPath:     filepath.Join("trip", "dsc_0001.jpg"),
		Name:        "dsc_0001.jpg",
		SHA256:      "aa",
		Phash:       "1234",
		CreatedAt:   1700000000,
		ExifCapture: "2023:11:14 10:20:30",
		ExifOffset:  "+01:00",
		Width:       4000,
		Height:      3000,
		Kind:        "jpeg",
		Size:        123456,
		MTime:       1700000000123456789,
	}
	gotID, created, err := s.UpsertImage(img)
	if err != nil || !created {
		t.Fatalf("UpsertImage = (%q, %v, %v), want created", gotID, created, err)
	}
	if gotID != img.ID {
		t.Fatalf("id = %q, want %q", gotID, img.ID)
	}

	// Re-upsert with changed content keeps the id and updates fields.
	img.SHA256 = "bb"
	img.Width = 8000
	img.Height = 6000
	if _, created, err := s.UpsertImage(img); err != nil || created {
		t.Fatalf("re-upsert = (created=%v, err=%v), want update", created, err)
	}
	images, err := s.AllImages()
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].SHA256 != "bb" || images[0].Width != 8000 {
		t.Fatalf("AllImages = %+v", images)
	}
	if images[0].MTime != 1700000000123456789 {
		t.Fatalf("MTime = %d, want the stored nanosecond value", images[0].MTime)
	}

	if err := s.SetGroupAssignments(map[string]int64{img.ID: 7}); err != nil {
		t.Fatal(err)
	}
	images, _ = s.AllImages()
	if images[0].GroupID != 7 {
		t.Fatalf("GroupID = %d, want 7", images[0].GroupID)
	}

	g := Group{ID: 7, Status: "resolved", PixelImageID: img.ID, CaptureImageID: img.ID,
		CaptureTime: "2023:11:14 10:20:30", CaptureInstant: 1700000000,
		HasGPS: true, GPSLat: 1.5, GPSLon: 2.5, StoredName: "dsc_0001.jpg"}
	if err := s.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	groups, err := s.AllGroups()
	if err != nil || len(groups) != 1 {
		t.Fatalf("AllGroups = (%+v, %v)", groups, err)
	}
	if groups[0].Status != "resolved" || !groups[0].HasGPS || groups[0].GPSLat != 1.5 {
		t.Fatalf("group = %+v", groups[0])
	}

	counts, err := s.GroupCounts()
	if err != nil || counts["resolved"] != 1 {
		t.Fatalf("GroupCounts = (%v, %v)", counts, err)
	}

	if err := s.ClearGroups(); err != nil {
		t.Fatal(err)
	}
	images, _ = s.AllImages()
	groups, _ = s.AllGroups()
	if images[0].GroupID != 0 || len(groups) != 0 {
		t.Fatalf("after ClearGroups: group_id=%d groups=%d", images[0].GroupID, len(groups))
	}
}

func TestReplacePlansAndDeletePlans(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sid, err := s.AddSource("/photos")
	if err != nil {
		t.Fatal(err)
	}
	a := Image{ID: "1-000001", SourceID: sid, RelPath: "a.jpg", Name: "a.jpg", SHA256: "aa"}
	b := Image{ID: "1-000002", SourceID: sid, RelPath: "b.jpg", Name: "b.jpg", SHA256: "bb"}
	for _, img := range []Image{a, b} {
		if _, _, err := s.UpsertImage(img); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveGroup(Group{ID: 9, Status: "resolved", PixelImageID: a.ID, StoredName: "old.jpg"}); err != nil {
		t.Fatal(err)
	}

	plans := []Group{
		{ID: 1, Status: "resolved", PixelImageID: a.ID, StoredName: "a.jpg"},
		{ID: 2, Status: "singleton", PixelImageID: b.ID, StoredName: "b.jpg"},
	}
	assign := map[string]int64{a.ID: 1, b.ID: 2}
	if err := s.ReplacePlans(plans, assign); err != nil {
		t.Fatal(err)
	}
	groups, err := s.AllGroups()
	if err != nil || len(groups) != 2 {
		t.Fatalf("AllGroups after ReplacePlans = (%+v, %v)", groups, err)
	}
	for _, g := range groups {
		if g.ID == 9 {
			t.Fatal("obsolete plan 9 survived ReplacePlans")
		}
	}
	images, err := s.AllImages()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Image{}
	for _, img := range images {
		byID[img.ID] = img
	}
	if byID[a.ID].GroupID != 1 || byID[b.ID].GroupID != 2 {
		t.Fatalf("assignments = a:%d b:%d", byID[a.ID].GroupID, byID[b.ID].GroupID)
	}

	if err := s.DeletePlans(); err != nil {
		t.Fatal(err)
	}
	groups, err = s.AllGroups()
	if err != nil || len(groups) != 0 {
		t.Fatalf("AllGroups after DeletePlans = (%+v, %v)", groups, err)
	}
	images, _ = s.AllImages()
	byID = map[string]Image{}
	for _, img := range images {
		byID[img.ID] = img
	}
	if byID[a.ID].GroupID != 1 || byID[b.ID].GroupID != 2 {
		t.Fatalf("DeletePlans cleared assignments: a:%d b:%d", byID[a.ID].GroupID, byID[b.ID].GroupID)
	}
}

func TestUpsertImageWithHashes(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sid, err := s.AddSource("/photos")
	if err != nil {
		t.Fatal(err)
	}
	img := Image{ID: "1-000001", SourceID: sid, RelPath: "a.jpg", Name: "a.jpg", SHA256: "abc", Phash: "aa"}
	hashes := []ContentPhash{
		{SHA256: "abc", Alg: "dct", Size: 0, Hex: "aa"},
		{SHA256: "abc", Alg: "blockhash", Size: 16, Hex: "bb"},
	}
	id, created, err := s.UpsertImageWithHashes(img, hashes)
	if err != nil || !created || id != img.ID {
		t.Fatalf("UpsertImageWithHashes = (%q, %v, %v)", id, created, err)
	}
	all, err := s.ContentPhashes("abc")
	if err != nil || len(all) != 2 {
		t.Fatalf("ContentPhashes = %v, err %v", all, err)
	}
}

func TestContentPhashKeyedBySHA256(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	h := ContentPhash{SHA256: "abc", Alg: "dct", Size: 0, Hex: "aa"}
	if err := s.PutContentPhash(h); err != nil {
		t.Fatal(err)
	}
	if err := s.PutContentPhash(ContentPhash{SHA256: "abc", Alg: "blockhash", Size: 16, Hex: "bb"}); err != nil {
		t.Fatal(err)
	}
	// Upsert same key updates hex, does not duplicate.
	if err := s.PutContentPhash(ContentPhash{SHA256: "abc", Alg: "dct", Size: 0, Hex: "cc"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetContentPhash("abc", "dct", 0)
	if err != nil || !ok || got != "cc" {
		t.Fatalf("GetContentPhash dct = (%q, %v, %v), want cc", got, ok, err)
	}
	all, err := s.ContentPhashes("abc")
	if err != nil || len(all) != 2 {
		t.Fatalf("ContentPhashes = %v, err %v", all, err)
	}
	if _, ok, err := s.GetContentPhash("missing", "dct", 0); err != nil || ok {
		t.Fatalf("missing key should be !ok, got ok=%v err=%v", ok, err)
	}
}

func TestCommitAndRollback(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.AddSource("/photos")
	if err != nil {
		t.Fatal(err)
	}
	a := Image{ID: "1-000001", SourceID: id, RelPath: "a.jpg", Name: "a.jpg", SHA256: "aa"}
	b := Image{ID: "1-000002", SourceID: id, RelPath: "b.jpg", Name: "b.jpg", SHA256: "bb"}
	for _, img := range []Image{a, b} {
		if _, _, err := s.UpsertImage(img); err != nil {
			t.Fatal(err)
		}
	}

	g1 := Group{ID: 1, Status: "resolved", PixelImageID: a.ID, StoredName: "a.jpg"}
	g2 := Group{ID: 2, Status: "conflict", Reason: "offset", StoredName: "b.jpg"}
	for _, g := range []Group{g1, g2} {
		if err := s.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}

	// First commit: both groups are new.
	res, err := s.Commit("baseline")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit.ID != 1 || len(res.Actions) != 2 {
		t.Fatalf("first commit = %+v actions %+v", res.Commit, res.Actions)
	}
	if res.Actions[0].Action != ActionNew || res.Actions[1].Action != ActionNew {
		t.Fatalf("actions = %+v, want both new", res.Actions)
	}

	// Renaming g2's plan shows up as one updated action.
	g2.StoredName = "b2.jpg"
	if err := s.SaveGroup(g2); err != nil {
		t.Fatal(err)
	}
	res, err = s.Commit("rename")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit.ID != 2 || len(res.Actions) != 1 ||
		res.Actions[0].Action != ActionUpdated ||
		res.Actions[0].GroupID != 2 ||
		res.Actions[0].PrevName != "b.jpg" || res.Actions[0].NewName != "b2.jpg" {
		t.Fatalf("second commit = %+v actions %+v", res.Commit, res.Actions)
	}

	// Dropping g2 (a re-plan that no longer produces it) shows a removal.
	if err := s.ClearGroups(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(g1); err != nil {
		t.Fatal(err)
	}
	res, err = s.Commit("shrink")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Action != ActionRemoved || res.Actions[0].GroupID != 2 {
		t.Fatalf("third commit actions = %+v, want g2 removed", res.Actions)
	}

	// Dangling assignments: a -> 1 (survives the baseline), b -> 9 (does
	// not). Rolling back to commit 1 restores g2, keeps a's assignment and
	// zeros b's.
	if err := s.SetGroupAssignments(map[string]int64{a.ID: 1, b.ID: 9}); err != nil {
		t.Fatal(err)
	}
	res, err = s.Rollback(1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit.ID != 4 || res.Commit.Summary != "rollback to commit 1" {
		t.Fatalf("rollback commit = %+v", res.Commit)
	}
	if len(res.Actions) != 1 || res.Actions[0].Action != ActionNew || res.Actions[0].GroupID != 2 {
		t.Fatalf("rollback actions = %+v, want g2 new", res.Actions)
	}
	groups, err := s.AllGroups()
	if err != nil || len(groups) != 2 {
		t.Fatalf("AllGroups = (%+v, %v), want the two baseline groups", groups, err)
	}
	byID := map[int64]Group{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	if byID[2].Status != "conflict" || byID[2].StoredName != "b.jpg" {
		t.Fatalf("restored g2 = %+v", byID[2])
	}
	images, err := s.AllImages()
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range images {
		switch img.ID {
		case a.ID:
			if img.GroupID != 1 {
				t.Fatalf("a.group_id = %d, want 1 (group 1 survives)", img.GroupID)
			}
		case b.ID:
			if img.GroupID != 0 {
				t.Fatalf("b.group_id = %d, want 0 (group 9 does not survive)", img.GroupID)
			}
		}
	}

	if _, err := s.Rollback(99); err == nil {
		t.Fatal("Rollback(99) must fail for a missing commit")
	}
	actions, err := s.CommitActions(2)
	if err != nil || len(actions) != 1 || actions[0].Action != ActionUpdated {
		t.Fatalf("CommitActions(2) = (%+v, %v)", actions, err)
	}
	commits, err := s.Commits()
	if err != nil || len(commits) != 4 {
		t.Fatalf("Commits = (%+v, %v), want 4 entries", commits, err)
	}
}
