package exifwrite

import (
	"math"
	"testing"

	"photo-library-repair/internal/meta"
	"photo-library-repair/internal/testimg"
)

func writeAndRead(t *testing.T, jpeg []byte, f Facts) meta.Facts {
	t.Helper()
	out, err := SetJPEG(jpeg, f)
	if err != nil {
		t.Fatalf("SetJPEG: %v", err)
	}
	got, err := meta.FromJPEG(out)
	if err != nil {
		t.Fatalf("meta.FromJPEG after write: %v", err)
	}
	return got
}

func TestSetJPEGInsertAndRead(t *testing.T) {
	plain := testimg.JPEG(testimg.Textured(32, 24, 2), 90)
	got := writeAndRead(t, plain, Facts{
		Orientation: 6,
		Capture:     "2024:05:01 12:30:00",
		Offset:      "+02:00",
	})
	if got.Capture != "2024:05:01 12:30:00" || got.Offset != "+02:00" {
		t.Fatalf("capture = %q %q", got.Capture, got.Offset)
	}
	if got.Orientation != 6 {
		t.Fatalf("orientation = %d", got.Orientation)
	}
	if got.HasGPS {
		t.Fatal("unexpected GPS")
	}
}

func TestSetJPEGReplacesExistingEXIF(t *testing.T) {
	src := testimg.MakeEXIFJPEG(testimg.Textured(32, 24, 3), 90, testimg.EXIF{
		Orientation:        3,
		DateTimeOriginal:   "2019:01:02 03:04:05",
		OffsetTimeOriginal: "+05:30",
		GPS:                true,
	})
	got := writeAndRead(t, src, Facts{
		Orientation: 1,
		Capture:     "2024:12:31 23:59:59",
		Offset:      "Z",
	})
	if got.Capture != "2024:12:31 23:59:59" || got.Offset != "Z" {
		t.Fatalf("capture = %q %q", got.Capture, got.Offset)
	}
	if got.Orientation != 1 {
		t.Fatalf("orientation = %d, want 1 (old value must be dropped)", got.Orientation)
	}
	if got.HasGPS {
		t.Fatal("old GPS must be dropped when HasGPS is false")
	}
}

func TestSetJPEGGPSRoundTrip(t *testing.T) {
	plain := testimg.JPEG(testimg.Textured(32, 24, 4), 90)
	got := writeAndRead(t, plain, Facts{
		Capture: "2024:05:01 12:30:00",
		Offset:  "+02:00",
		HasGPS:  true,
		GPSLat:  -33.85,
		GPSLon:  151.21,
	})
	if !got.HasGPS {
		t.Fatal("expected GPS")
	}
	if math.Abs(got.GPSLat-(-33.85)) > 1e-5 || math.Abs(got.GPSLon-151.21) > 1e-5 {
		t.Fatalf("gps = %v %v, want -33.85 151.21", got.GPSLat, got.GPSLon)
	}
}

func TestSetJPEGSouthWest(t *testing.T) {
	plain := testimg.JPEG(testimg.Textured(32, 24, 5), 90)
	got := writeAndRead(t, plain, Facts{HasGPS: true, GPSLat: -1, GPSLon: -2})
	if !got.HasGPS {
		t.Fatal("expected GPS")
	}
	if got.GPSLat > 0 || got.GPSLon > 0 {
		t.Fatalf("signs must be preserved: %v %v", got.GPSLat, got.GPSLon)
	}
}

func TestSetJPEGRejectsNonJPEG(t *testing.T) {
	if _, err := SetJPEG(testimg.PNG(testimg.Textured(8, 8, 1)), Facts{}); err != ErrNotJPEG {
		t.Fatalf("err = %v, want ErrNotJPEG", err)
	}
}
