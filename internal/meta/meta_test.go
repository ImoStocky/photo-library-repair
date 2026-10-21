package meta

import (
	"errors"
	"testing"

	"photo-library-repair/internal/testimg"
)

func TestFromJPEGFullFacts(t *testing.T) {
	img := testimg.Textured(64, 48, 1)
	data := testimg.MakeEXIFJPEG(img, 90, testimg.EXIF{
		Orientation:        6,
		DateTimeOriginal:   "2024:05:01 12:30:00",
		OffsetTimeOriginal: "+02:00",
		GPS:                true,
	})
	f, err := FromJPEG(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Capture != "2024:05:01 12:30:00" {
		t.Fatalf("capture = %q", f.Capture)
	}
	if f.Offset != "+02:00" {
		t.Fatalf("offset = %q", f.Offset)
	}
	if f.Orientation != 6 {
		t.Fatalf("orientation = %d", f.Orientation)
	}
	if !f.HasGPS {
		t.Fatal("expected GPS")
	}
	// 52°31'13.3" N, 13°24'19.8" E
	if f.GPSLat < 52.5203 || f.GPSLat > 52.5204 {
		t.Fatalf("lat = %f", f.GPSLat)
	}
	if f.GPSLon < 13.4054 || f.GPSLon > 13.4056 {
		t.Fatalf("lon = %f", f.GPSLon)
	}
}

func TestFromJPEGNoExif(t *testing.T) {
	data := testimg.JPEG(testimg.Textured(16, 16, 2), 90)
	if _, err := FromJPEG(data); !errors.Is(err, ErrNoEXIF) {
		t.Fatalf("err = %v, want ErrNoEXIF", err)
	}
}

func TestFromJPEGGarbage(t *testing.T) {
	for _, in := range [][]byte{nil, {0xFF, 0xD8}, {0x00, 0x01, 0x02, 0x03}} {
		if _, err := FromJPEG(in); !errors.Is(err, ErrNoEXIF) {
			t.Fatalf("err = %v, want ErrNoEXIF", err)
		}
	}
}

func TestCaptureTimePriorityAndFallback(t *testing.T) {
	img := testimg.Textured(16, 16, 3)

	// DateTimeOriginal wins over DateTime.
	f, err := FromJPEG(testimg.MakeEXIFJPEG(img, 90, testimg.EXIF{
		DateTimeOriginal:   "2024:01:02 03:04:05",
		OffsetTimeOriginal: "+05:30",
		DateTime:           "2023:01:01 00:00:00",
		OffsetTime:         "Z",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if f.Capture != "2024:01:02 03:04:05" || f.Offset != "+05:30" {
		t.Fatalf("got %q %q", f.Capture, f.Offset)
	}

	// Without DateTimeOriginal the IFD0 DateTime is used with its own offset.
	f, err = FromJPEG(testimg.MakeEXIFJPEG(img, 90, testimg.EXIF{
		DateTime:   "2023:01:01 00:00:00",
		OffsetTime: "UTC",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if f.Capture != "2023:01:01 00:00:00" || f.Offset != "Z" {
		t.Fatalf("got %q %q", f.Capture, f.Offset)
	}
}

func TestFractionalSecondsDropped(t *testing.T) {
	img := testimg.Textured(16, 16, 4)
	f, err := FromJPEG(testimg.MakeEXIFJPEG(img, 90, testimg.EXIF{
		DateTimeOriginal:   "2024:05:01 12:30:00.678",
		OffsetTimeOriginal: "+02:00",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if f.Capture != "2024:05:01 12:30:00" {
		t.Fatalf("capture = %q", f.Capture)
	}
}

func TestBigEndianTIFF(t *testing.T) {
	// Hand-built MM (big-endian) TIFF: IFD0 at 8 with one ASCII record
	// (DateTime) whose value lives at offset 26.
	d := []byte{'M', 'M', 0x00, 0x2A, 0, 0, 0, 8, 0, 1,
		0x01, 0x32, // tag 0x0132
		0x00, 0x02, // type ASCII
		0x00, 0x00, 0x00, 0x13, // count 19
		0x00, 0x00, 0x00, 0x1A, // value offset 26
		0, 0, 0, 0} // next IFD
	var ts [19]byte
	copy(ts[:], "2020:05:01 12:30:00\x00")
	d = append(d, ts[:]...)

	f, err := fromTIFF(d)
	if err != nil {
		t.Fatal(err)
	}
	if f.Capture != "2020:05:01 12:30:00" || f.Offset != "" {
		t.Fatalf("got %q %q", f.Capture, f.Offset)
	}
}

func TestNormalizeOffset(t *testing.T) {
	cases := map[string]string{
		"+05:30":  "+05:30",
		"-03:30":  "-03:30",
		"+0530":   "+05:30",
		"+05":     "+05:00",
		"Z":       "Z",
		"UTC":     "Z",
		"GMT":     "Z",
		"-00:00":  "Z",
		"":        "",
		"garbage": "",
		"+99:00":  "",
		"+05:99":  "",
	}
	for in, want := range cases {
		if got := normalizeOffset(in); got != want {
			t.Fatalf("normalizeOffset(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGPSRequiresBothRefs(t *testing.T) {
	if _, _, ok := gpsPosition(nil); ok {
		t.Fatal("empty GPSIFD must not report GPS")
	}
}
