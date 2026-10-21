package group

import (
	"errors"
	"testing"
	"time"
)

// Controlled 128-bit hashes: a<b<c chain at hamming 1-2, d is far away.
const (
	ha = "00000000000000000000000000000001"
	hb = "00000000000000000000000000000003"
	hc = "0000000000000000000000000000000f"
	hd = "ffffffffffffffffffffffffffffffff"
)

func ts(y int, m time.Month, d, h, mi, s int) int64 {
	return time.Date(y, m, d, h, mi, s, 0, time.UTC).Unix()
}

func im(id, name, phash string, created int64, w, hgt int, size int64) Image {
	return Image{ID: id, Name: name, Phash: phash, CreatedAt: created, Width: w, Height: hgt, Size: size, SHA256: "sha-" + id}
}

func TestAggregateShaAndHammingChain(t *testing.T) {
	imgs := []Image{
		im("a", "a.jpg", ha, ts(2024, 5, 1, 0, 0, 0), 10, 10, 1),
		im("b", "b.jpg", hb, ts(2024, 5, 1, 0, 0, 1), 10, 10, 1),
		im("c", "c.jpg", hc, ts(2024, 5, 1, 0, 0, 2), 10, 10, 1),
		im("d", "d.jpg", hd, ts(2024, 5, 1, 0, 0, 3), 10, 10, 1),
	}
	assign, groups := Aggregate(imgs, 2)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if assign["a"] != assign["b"] || assign["b"] != assign["c"] {
		t.Fatalf("hamming chain must group a,b,c: %v", assign)
	}
	if assign["d"] == assign["a"] {
		t.Fatal("d must be separate")
	}
	if len(groups[0]) != 3 || len(groups[1]) != 1 {
		t.Fatalf("group sizes %d,%d", len(groups[0]), len(groups[1]))
	}
}

func TestAggregateIdenticalSHA(t *testing.T) {
	a := im("a", "a.jpg", "", ts(2024, 5, 1, 0, 0, 0), 10, 10, 1)
	b := im("b", "b.jpg", "", ts(2024, 5, 1, 0, 0, 1), 10, 10, 1)
	a.SHA256, b.SHA256 = "same", "same"
	assign, _ := Aggregate([]Image{a, b}, 0)
	if assign["a"] != assign["b"] {
		t.Fatal("identical sha256 must group")
	}
}

func TestResolveSingleton(t *testing.T) {
	one := im("a", "a.jpg", ha, ts(2024, 5, 1, 12, 0, 0), 10, 10, 1)
	one.ExifCapture = "2024:05:01 12:00:00"
	one.ExifOffset = "Z"
	p := ResolveGroup([]Image{one}, nil)
	if p.Status != StatusSingleton || p.PixelImageID != "a" || p.CaptureImageID != "a" {
		t.Fatalf("plan = %+v", p)
	}
	if p.CaptureInstant != ts(2024, 5, 1, 12, 0, 0) {
		t.Fatalf("instant = %d", p.CaptureInstant)
	}
}

func TestResolveNoEXIFUsesEarliestCreation(t *testing.T) {
	late := im("late", "IMG_0001.jpg", ha, ts(2024, 5, 1, 12, 0, 0), 800, 600, 100)
	early := im("early", "IMG_0001 (1).jpg", hb, ts(2024, 5, 1, 10, 0, 0), 640, 480, 50)
	p := ResolveGroup([]Image{late, early}, nil)
	if p.Status != StatusResolved || !p.CaptureFallback {
		t.Fatalf("plan = %+v", p)
	}
	if p.CaptureImageID != "early" {
		t.Fatalf("capture = %s, want early", p.CaptureImageID)
	}
	if p.CaptureTime != "2024-05-01 10:00:00" {
		t.Fatalf("capture time = %q", p.CaptureTime)
	}
	// Pixels come from the higher-resolution file, keeping its hash.
	if p.PixelImageID != "late" || p.PixelPhash != ha {
		t.Fatalf("pixels = %s %s, want late/ha", p.PixelImageID, p.PixelPhash)
	}
	// The original-looking name wins over the "(1)" copy.
	if p.StoredName != "IMG_0001.jpg" {
		t.Fatalf("name = %q", p.StoredName)
	}
}

func TestResolveSingleEXIF(t *testing.T) {
	created := ts(2024, 5, 1, 12, 0, 30)
	a := im("a", "a.jpg", ha, created, 640, 480, 10)
	a.ExifCapture = "2024:05:01 12:00:00"
	a.ExifOffset = "+02:00"
	b := im("b", "b.jpg", hb, created, 800, 600, 20)
	p := ResolveGroup([]Image{a, b}, nil)
	if p.Status != StatusResolved || p.CaptureFallback {
		t.Fatalf("plan = %+v", p)
	}
	if p.CaptureImageID != "a" || p.CaptureInstant != ts(2024, 5, 1, 10, 0, 0) {
		t.Fatalf("capture %s instant %d, want a %d", p.CaptureImageID, p.CaptureInstant, ts(2024, 5, 1, 10, 0, 0))
	}
	if p.PixelImageID != "b" {
		t.Fatalf("pixels = %s, want b", p.PixelImageID)
	}
}

func TestResolveSingleEXIFFileTooOldConflicts(t *testing.T) {
	created := ts(2024, 5, 1, 11, 59, 50) // 10 s before capture
	a := im("a", "a.jpg", ha, created, 640, 480, 10)
	a.ExifCapture = "2024:05:01 12:00:00"
	a.ExifOffset = "Z"
	b := im("b", "b.jpg", hb, created, 800, 600, 20)
	p := ResolveGroup([]Image{a, b}, nil)
	if p.Status != StatusConflict {
		t.Fatalf("plan = %+v", p)
	}
	if p.Reason == "" {
		t.Fatal("conflict needs a reason")
	}

	// Within the 5 s tolerance it resolves.
	created = ts(2024, 5, 1, 11, 59, 57)
	a.CreatedAt = created
	p = ResolveGroup([]Image{a, b}, nil)
	if p.Status != StatusResolved {
		t.Fatalf("within tolerance must resolve: %+v", p)
	}
}

func TestResolveMultipleEXIFAgree(t *testing.T) {
	early := im("early", "IMG_0001.jpg", ha, ts(2024, 5, 1, 9, 0, 0), 640, 480, 10)
	early.ExifCapture = "2024:05:01 10:30:00"
	early.ExifOffset = "+02:00"
	late := im("late", "IMG_0002.jpg", hb, ts(2024, 5, 1, 9, 0, 1), 800, 600, 20)
	late.ExifCapture = "2024:05:01 11:30:00"
	late.ExifOffset = "+02:00"
	p := ResolveGroup([]Image{late, early}, nil)
	if p.Status != StatusResolved {
		t.Fatalf("plan = %+v", p)
	}
	if p.CaptureImageID != "early" {
		t.Fatalf("capture = %s, want early", p.CaptureImageID)
	}
	if p.CaptureInstant != ts(2024, 5, 1, 8, 30, 0) {
		t.Fatalf("instant = %d, want %d", p.CaptureInstant, ts(2024, 5, 1, 8, 30, 0))
	}
	if p.PixelImageID != "late" {
		t.Fatalf("pixels = %s, want late", p.PixelImageID)
	}
}

func TestResolveMultipleEXIFOffsetDisagreementConflicts(t *testing.T) {
	a := im("a", "a.jpg", ha, ts(2024, 5, 1, 9, 0, 0), 640, 480, 10)
	a.ExifCapture, a.ExifOffset = "2024:05:01 12:00:00", "+02:00"
	b := im("b", "b.jpg", hb, ts(2024, 5, 1, 9, 0, 1), 800, 600, 20)
	b.ExifCapture, b.ExifOffset = "2024:05:01 12:00:00", "+05:30"
	p := ResolveGroup([]Image{a, b}, nil)
	if p.Status != StatusConflict || p.Reason == "" {
		t.Fatalf("plan = %+v", p)
	}
}

func TestResolveMultipleEXIFGPSDisagreementConflicts(t *testing.T) {
	a := im("a", "a.jpg", ha, ts(2024, 5, 1, 9, 0, 0), 640, 480, 10)
	a.ExifCapture, a.ExifOffset = "2024:05:01 12:00:00", "+02:00"
	a.HasGPS, a.GPSLat, a.GPSLon = true, 52.52, 13.40
	b := im("b", "b.jpg", hb, ts(2024, 5, 1, 9, 0, 1), 800, 600, 20)
	b.ExifCapture, b.ExifOffset = "2024:05:01 12:00:00", "+02:00"
	b.HasGPS, b.GPSLat, b.GPSLon = true, 48.85, 2.35
	p := ResolveGroup([]Image{a, b}, nil)
	if p.Status != StatusConflict {
		t.Fatalf("plan = %+v", p)
	}

	// Missing GPS on one side also disagrees.
	b.HasGPS, b.GPSLat, b.GPSLon = false, 0, 0
	p = ResolveGroup([]Image{a, b}, nil)
	if p.Status != StatusConflict {
		t.Fatalf("gps presence must agree: %+v", p)
	}
}

type fakeResolver struct{ pick string }

func (f fakeResolver) Resolve(c Conflict) (Decision, error) {
	return Decision{PixelImageID: f.pick, CaptureImageID: f.pick, StoredName: "chosen.jpg"}, nil
}

func TestResolveConflictWithResolver(t *testing.T) {
	a := im("a", "a.jpg", ha, ts(2024, 5, 1, 11, 59, 50), 640, 480, 10)
	a.ExifCapture, a.ExifOffset = "2024:05:01 12:00:00", "Z"
	b := im("b", "b.jpg", hb, ts(2024, 5, 1, 11, 59, 50), 800, 600, 20)
	p := ResolveGroup([]Image{a, b}, fakeResolver{pick: "b"})
	if p.Status != StatusResolved {
		t.Fatalf("resolver must settle the conflict: %+v", p)
	}
	if p.PixelImageID != "b" || p.StoredName != "chosen.jpg" {
		t.Fatalf("decision not applied: %+v", p)
	}
}

func TestResolveConflictResolverFailureKeepsConflict(t *testing.T) {
	a := im("a", "a.jpg", ha, ts(2024, 5, 1, 11, 59, 50), 640, 480, 10)
	a.ExifCapture, a.ExifOffset = "2024:05:01 12:00:00", "Z"
	b := im("b", "b.jpg", hb, ts(2024, 5, 1, 11, 59, 50), 800, 600, 20)
	p := ResolveGroup([]Image{a, b}, failingResolver{})
	if p.Status != StatusConflict {
		t.Fatalf("failed resolver must keep the conflict: %+v", p)
	}
}

type failingResolver struct{}

func (failingResolver) Resolve(Conflict) (Decision, error) {
	return Decision{}, errors.New("resolver exploded")
}

func TestPickNamePreferences(t *testing.T) {
	cases := []struct {
		names []string
		pixel string
		want  string
	}{
		{[]string{"IMG_1234 (1).jpg", "IMG_1234.jpg", "2024-05-01 12.30.00.jpg"}, "IMG_1234.jpg", "IMG_1234.jpg"},
		{[]string{"copy of IMG_0001.jpg", "IMG_0001.jpg"}, "IMG_0001.jpg", "IMG_0001.jpg"},
		{[]string{"photo (2).jpg", "photo.jpg"}, "photo (2).jpg", "photo.jpg"},
	}
	for _, tc := range cases {
		imgs := make([]Image, len(tc.names))
		for i, n := range tc.names {
			imgs[i] = Image{ID: "id" + string(rune('a'+i)), Name: n}
		}
		if got := pickName(imgs, tc.pixel); got != tc.want {
			t.Fatalf("pickName(%v, %s) = %q, want %q", tc.names, tc.pixel, got, tc.want)
		}
	}
}
