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
