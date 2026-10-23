package phash

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"

	"photo-library-repair/internal/testimg"
)

func gradient(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{uint8(255 * (x + y) / (w + h - 2))})
		}
	}
	return img
}

// photoLike builds synthetic content with energy spread over low and mid
// frequencies, closer to real photographs than a pure ramp.
func photoLike(w, h int, noise float64) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			fx, fy := float64(x), float64(y)
			v := 128.0 +
				40.0*math.Sin(fx*0.05) +
				25.0*math.Sin(fy*0.03+1) +
				15.0*math.Sin((fx+fy)*0.02+2) +
				10.0*math.Sin(fx*0.15)*math.Sin(fy*0.11)
			if noise > 0 {
				v += (math.Sin(float64(x*y*7919))*0.5 + 0.5) * noise
			}
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			img.SetGray(x, y, color.Gray{uint8(v)})
		}
	}
	return img
}

func constantImg(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetGray(x, y, color.Gray{128})
		}
	}
	return img
}

func jpegEncode(t *testing.T, img image.Image, q int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	dec, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

func TestComputeDeterministic(t *testing.T) {
	a, err := Compute(gradient(300, 200))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compute(gradient(300, 200))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("hash not deterministic: %x vs %x", a, b)
	}
	if a.Hamming(b) != 0 {
		t.Fatalf("identical images must have hamming distance 0")
	}
}

func TestComputeConstantImage(t *testing.T) {
	// A constant image resizes to the same constant at any source size, so
	// hashes of the same constant must match exactly. The DC coefficient
	// dominates the quadrant, so its bit (bit 0, packed LSB first) must be
	// set; the remaining bits encode DCT floating-point residue.
	a, err := Compute(constantImg(32, 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compute(constantImg(200, 150))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("constant image hash differs by source size: %x vs %x", a, b)
	}
	if a[0]&1 == 0 {
		t.Fatalf("DC bit (bit 0) not set in constant image hash %x", a)
	}
}

func TestJpegRoundTripSimilar(t *testing.T) {
	orig := photoLike(400, 300, 0)
	a, _ := Compute(orig)
	for _, tc := range []struct {
		q    int
		maxD int
	}{
		{95, 16},
		{90, 16},
		{70, 40},
	} {
		b, _ := Compute(jpegEncode(t, orig, tc.q))
		if d := a.Hamming(b); d > tc.maxD {
			t.Fatalf("jpeg round trip q=%d hamming = %d, want <= %d", tc.q, d, tc.maxD)
		}
	}
}

func TestDifferentImagesDiffer(t *testing.T) {
	stripes := photoLike(400, 300, 0)
	inv := image.NewGray(stripes.Bounds())
	for y := range 300 {
		for x := range 400 {
			g := stripes.GrayAt(x, y)
			inv.SetGray(x, y, color.Gray{uint8(255 - g.Y)})
		}
	}
	a, _ := Compute(stripes)
	b, _ := Compute(inv)
	if d := a.Hamming(b); d < 24 {
		t.Fatalf("inverted image hamming = %d, want >= 24", d)
	}
}

func TestHammingAndHex(t *testing.T) {
	var a, b Hash
	a[0] = 0xFF
	b[0] = 0x0F
	if d := a.Hamming(b); d != 4 {
		t.Fatalf("Hamming = %d, want 4", d)
	}
	hexStr := a.Hex()
	if len(hexStr) != 32 {
		t.Fatalf("Hex length = %d, want 32", len(hexStr))
	}
	c, err := HashFromHex(hexStr)
	if err != nil {
		t.Fatal(err)
	}
	if c != a {
		t.Fatalf("hex round trip = %x, want %x", c, a)
	}
	if _, err := HashFromHex("abcd"); err == nil {
		t.Fatal("HashFromHex must reject wrong length")
	}
}

func TestValid(t *testing.T) {
	var zero Hash
	if zero.Valid() {
		t.Fatal("all-zero hash must be invalid")
	}
	var one Hash
	for i := range one {
		one[i] = 0xFF
	}
	if one.Valid() {
		t.Fatal("all-ones hash must be invalid")
	}
	h, err := Compute(photoLike(100, 80, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !h.Valid() {
		t.Fatalf("hash of textured image must be valid: %x", h)
	}
}

func TestDctDCOnly(t *testing.T) {
	m := make([][]float64, 16)
	for i := range m {
		m[i] = make([]float64, 16)
		for j := range m[i] {
			m[i][j] = 1
		}
	}
	out := dct2d(m)
	if out[0][0] < 255.9 || out[0][0] > 256.1 {
		t.Fatalf("DC coefficient = %f, want 256", out[0][0])
	}
	maxAC := 0.0
	for i := range 16 {
		for j := range 16 {
			if i == 0 && j == 0 {
				continue
			}
			v := out[i][j]
			if v < 0 {
				v = -v
			}
			if v > maxAC {
				maxAC = v
			}
		}
	}
	if maxAC > 1e-6 {
		t.Fatalf("largest AC coefficient = %f, want ~0", maxAC)
	}
}

func TestDctRectangular(t *testing.T) {
	m := make([][]float64, 4)
	for i := range m {
		m[i] = make([]float64, 8)
		for j := range m[i] {
			m[i][j] = 1
		}
	}
	out := dct2d(m)
	if len(out) != 4 || len(out[0]) != 8 {
		t.Fatalf("DCT output shape = %dx%d, want 4x8", len(out), len(out[0]))
	}
	if out[0][0] < 31.99 || out[0][0] > 32.01 {
		t.Fatalf("DC coefficient = %f, want 32", out[0][0])
	}
	for i := range 4 {
		for j := range 8 {
			if i == 0 && j == 0 {
				continue
			}
			v := out[i][j]
			if v < 0 {
				v = -v
			}
			if v > 1e-6 {
				t.Fatalf("AC coefficient [%d][%d] = %f, want ~0", i, j, v)
			}
		}
	}
}

func TestLanczosGridConstant(t *testing.T) {
	// A constant picture rescales to the same constant through the
	// streaming resize, at any source size relative to the grid.
	img := image.NewGray(image.Rect(0, 0, 10, 10))
	for i := range img.Pix {
		img.Pix[i] = 43
	}
	out := lanczosGrid(img, img.Bounds())
	if len(out) != gridW*gridH {
		t.Fatalf("grid length = %d, want %d", len(out), gridW*gridH)
	}
	for i, v := range out {
		if v < 42.99 || v > 43.01 {
			t.Fatalf("out[%d] = %f, want ~43", i, v)
		}
	}
}

// TestComputeAnySize proves Compute never fails on arbitrary source sizes:
// sub-pixel, grid-size (identity), extreme aspect ratios, and photo scale.
func TestComputeAnySize(t *testing.T) {
	sizes := []struct {
		w, h int
	}{
		{1, 1}, {2, 1}, {1, 2}, {8, 8}, {16, 9}, {32, 16},
		{64, 48}, {100, 1}, {1, 100}, {400, 300}, {1000, 300}, {2000, 1500},
	}
	for _, s := range sizes {
		h, err := Compute(photoLike(s.w, s.h, 0))
		if err != nil {
			t.Fatalf("Compute(%dx%d) failed: %v", s.w, s.h, err)
		}
		if h == (Hash{}) {
			t.Fatalf("Compute(%dx%d) produced an all-zero hash", s.w, s.h)
		}
	}
}

// TestComputeGolden pins the hash of a fixed file across pipeline
// restructures: the streaming LANCZOS3 resize must produce bit-identical
// hashes to the previous full-buffer implementation.
func TestComputeGolden(t *testing.T) {
	data := testimg.MakeEXIFJPEG(
		testimg.Textured(400, 300, 1),
		95,
		testimg.EXIF{
			DateTimeOriginal:   "2024:05:01 12:30:00",
			OffsetTimeOriginal: "+02:00",
			GPS:                true,
		},
	)
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	h, err := Compute(img)
	if err != nil {
		t.Fatal(err)
	}
	const want = "7700f8ffadaa07506c750741ec550775"
	if h.Hex() != want {
		t.Fatalf("golden hash = %s, want %s", h.Hex(), want)
	}
}
