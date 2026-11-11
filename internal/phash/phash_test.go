package phash

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand/v2"
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
	if !bytes.Equal(a, b) {
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
	if !bytes.Equal(a, b) {
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
	a := make(Hash, Bytes)
	b := make(Hash, Bytes)
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
	if !bytes.Equal(c, a) {
		t.Fatalf("hex round trip = %x, want %x", c, a)
	}
	if _, err := HashFromHex(""); err == nil {
		t.Fatal("HashFromHex must reject empty string")
	}
}

func TestValid(t *testing.T) {
	var zero Hash
	if zero.Valid() {
		t.Fatal("all-zero hash must be invalid")
	}
	one := make(Hash, Bytes)
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

func TestLanczosResizeConstant(t *testing.T) {
	// A constant picture rescales to the same constant through the
	// streaming resize, at any source size relative to the destination.
	img := image.NewGray(image.Rect(0, 0, 10, 10))
	for i := range img.Pix {
		img.Pix[i] = 43
	}
	const dw, dh = 32, 16
	out := lanczosResize(img, img.Bounds(), dw, dh)
	if len(out) != dw*dh {
		t.Fatalf("grid length = %d, want %d", len(out), dw*dh)
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
		if len(h) != Bytes {
			t.Fatalf("Compute(%dx%d) length = %d, want %d", s.w, s.h, len(h), Bytes)
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

func TestBandCoversTheHash(t *testing.T) {
	a, _ := HashFromHex("0123456789abcdef0123456789abcdef")
	// 16 bands of 8 bits reconstruct the hash byte by byte.
	got := make(Hash, 16)
	for i := range 16 {
		got[i] = byte(a.Band(i*8, 8))
	}
	if !bytes.Equal(got, a) {
		t.Fatalf("8-bit bands = %x, want %x", got, a)
	}
	// 128 bands of 1 bit match a big-endian bit readout.
	for bit := range Bits {
		want := byte((a[bit/8] >> uint(7-bit%8)) & 1)
		if got := a.Band(bit, 1); got != uint64(want) {
			t.Fatalf("bit %d = %d, want %d", bit, got, want)
		}
	}
	// A straddling band takes the low bits of byte 7, all of byte 8,
	// and the high bits of byte 9.
	want := uint64(a[7]&0x0F)<<12 | uint64(a[8])<<4 | uint64(a[9]>>4)
	if got := a.Band(60, 16); got != want {
		t.Fatalf("Band(60,16) = %x, want %x", got, want)
	}
}

func TestBandPigeonhole(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for k := 0; k <= 4; k++ {
		width := min((Bits+k)/(k+1), 64)
		for trial := range 100 {
			a := make(Hash, Bytes)
			binary.BigEndian.PutUint64(a[:8], rng.Uint64())
			binary.BigEndian.PutUint64(a[8:], rng.Uint64())
			b := Hash(bytes.Clone(a))
			for d := 0; d < k; d++ {
				pos := int(rng.Uint32() % Bytes)
				b[pos] ^= 1 << (rng.Uint32() % 8)
			}
			found := false
			for bnd := 0; bnd*width < Bits; bnd++ {
				if a.Band(bnd*width, width) == b.Band(bnd*width, width) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("k=%d trial %d: distance-%d pair %x/%x shares no band",
					k, trial, a.Hamming(b), a, b)
			}
		}
	}
}

func TestComputeWithSizes(t *testing.T) {
	img := photoLike(400, 300, 0)
	for _, size := range []Size{Size8, Size16, Size32} {
		h, err := ComputeWith(img, Config{Alg: AlgDCT, Size: size})
		if err != nil {
			t.Fatalf("dct size %d: %v", size, err)
		}
		wantBits := int(size) * int(size)
		if h.BitLen() < wantBits || h.BitLen() > wantBits+7 {
			// BitLen is 8*ceil(bits/8); exact bit count is size².
			t.Fatalf("dct size %d: BitLen=%d, want packed length for %d bits", size, h.BitLen(), wantBits)
		}
		if len(h) != (wantBits+7)/8 {
			t.Fatalf("dct size %d: %d bytes, want %d", size, len(h), (wantBits+7)/8)
		}
	}
}

func TestDoubleGradientSizes(t *testing.T) {
	img := photoLike(400, 300, 0)
	// image_hasher: N*(N/2+1)*2 bits for hash_size(N,N).
	wantBits := map[Size]int{Size8: 40, Size16: 144, Size32: 544}
	for size, bits := range wantBits {
		h, err := ComputeWith(img, Config{Alg: AlgDoubleGradient, Size: size})
		if err != nil {
			t.Fatalf("gradient size %d: %v", size, err)
		}
		if len(h) != (bits+7)/8 {
			t.Fatalf("gradient size %d: %d bytes, want %d for %d bits", size, len(h), (bits+7)/8, bits)
		}
		// Deterministic.
		h2, _ := ComputeWith(img, Config{Alg: AlgDoubleGradient, Size: size})
		if !bytes.Equal(h, h2) {
			t.Fatalf("gradient size %d not deterministic", size)
		}
	}
}

func TestDoubleGradientSimilarJPEG(t *testing.T) {
	orig := photoLike(400, 300, 0)
	a, err := ComputeWith(orig, Config{Alg: AlgDoubleGradient, Size: Size16})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComputeWith(jpegEncode(t, orig, 90), Config{Alg: AlgDoubleGradient, Size: Size16})
	if err != nil {
		t.Fatal(err)
	}
	// 144-bit hash: allow a generous re-encode budget.
	if d := a.Hamming(b); d > 40 {
		t.Fatalf("gradient jpeg q=90 hamming = %d, want <= 40", d)
	}
}

func TestConfigNormalize(t *testing.T) {
	c, err := (Config{}).Normalize()
	if err != nil || c.Alg != AlgDCT || c.Size != 0 {
		t.Fatalf("default = %+v, err %v", c, err)
	}
	c, err = (Config{Alg: AlgDoubleGradient}).Normalize()
	if err != nil || c.Size != Size16 {
		t.Fatalf("gradient default size = %+v, err %v", c, err)
	}
	if _, err := (Config{Alg: "nope"}).Normalize(); err == nil {
		t.Fatal("want error for unknown alg")
	}
	if _, err := (Config{Size: 12}).Normalize(); err == nil {
		t.Fatal("want error for bad size")
	}
}

func TestCzkawkaAlgsSizes(t *testing.T) {
	img := photoLike(400, 300, 0)
	algs := []Alg{AlgMean, AlgMedian, AlgGradient, AlgVertGradient, AlgDoubleGradient, AlgBlockhash}
	for _, alg := range algs {
		for _, size := range []Size{Size8, Size16, Size32} {
			h, err := ComputeWith(img, Config{Alg: alg, Size: size})
			if err != nil {
				t.Fatalf("%s size %d: %v", alg, size, err)
			}
			want := (alg.BitLen(size) + 7) / 8
			if len(h) != want {
				t.Fatalf("%s size %d: %d bytes, want %d (bitlen %d)",
					alg, size, len(h), want, alg.BitLen(size))
			}
			h2, err := ComputeWith(img, Config{Alg: alg, Size: size})
			if err != nil || !bytes.Equal(h, h2) {
				t.Fatalf("%s size %d not deterministic", alg, size)
			}
			if !h.Valid() {
				t.Fatalf("%s size %d produced invalid (constant) hash %x", alg, size, h)
			}
		}
	}
}

func TestCzkawkaAlgsDistinguishImages(t *testing.T) {
	a := photoLike(400, 300, 0)
	b := gradient(400, 300) // structurally different
	algs := []Alg{AlgMean, AlgMedian, AlgGradient, AlgVertGradient, AlgDoubleGradient, AlgBlockhash}
	for _, alg := range algs {
		ha, err := ComputeWith(a, Config{Alg: alg, Size: Size16})
		if err != nil {
			t.Fatal(err)
		}
		hb, err := ComputeWith(b, Config{Alg: alg, Size: Size16})
		if err != nil {
			t.Fatal(err)
		}
		if d := ha.Hamming(hb); d == 0 {
			t.Fatalf("%s: different images hashed equal", alg)
		}
	}
}

func TestParseAlg(t *testing.T) {
	cases := map[string]Alg{
		"mean": AlgMean, "Mean": AlgMean,
		"double_gradient": AlgDoubleGradient, "d.grad.": AlgDoubleGradient,
		"blockhash": AlgBlockhash, "dct": AlgDCT,
	}
	for in, want := range cases {
		got, err := ParseAlg(in)
		if err != nil || got != want {
			t.Fatalf("ParseAlg(%q) = %q, %v (want %q)", in, got, err, want)
		}
	}
	if _, err := ParseAlg("nope"); err == nil {
		t.Fatal("want error")
	}
}

func pixelSumAt(img image.Image, x, y int) int {
	r, g, b, a := img.At(x, y).RGBA()
	if a>>8 == 0 {
		return 255 * 3
	}
	return int(r>>8) + int(g>>8) + int(b>>8)
}

func TestBindPixelSumMatchesAt(t *testing.T) {
	base := photoLike(64, 48, 8)
	nrgba := image.NewNRGBA(base.Bounds())
	for y := base.Bounds().Min.Y; y < base.Bounds().Max.Y; y++ {
		for x := base.Bounds().Min.X; x < base.Bounds().Max.X; x++ {
			v := base.GrayAt(x, y).Y
			a := uint8(255)
			if (x+y)%17 == 0 {
				a = 0
			} else if (x+y)%11 == 0 {
				a = 128
			}
			nrgba.SetNRGBA(x, y, color.NRGBA{R: v, G: v / 2, B: 255 - v, A: a})
		}
	}
	rgba := image.NewRGBA(base.Bounds())
	for y := base.Bounds().Min.Y; y < base.Bounds().Max.Y; y++ {
		for x := base.Bounds().Min.X; x < base.Bounds().Max.X; x++ {
			rgba.Set(x, y, nrgba.At(x, y))
		}
	}
	// Gray JPEG round-trips as *image.Gray; feed RGB so Decode yields YCbCr.
	ycbcr := jpegEncode(t, nrgba, 90)
	if _, ok := ycbcr.(*image.YCbCr); !ok {
		t.Fatalf("jpeg.Decode returned %T, want *image.YCbCr", ycbcr)
	}

	for _, img := range []image.Image{base, nrgba, rgba, ycbcr} {
		sum := bindPixelSum(img)
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				got, want := sum(x, y), pixelSumAt(img, x, y)
				if got != want {
					t.Fatalf("%T (%d,%d): bind=%d at=%d", img, x, y, got, want)
				}
			}
		}
	}
}

func TestBlockhashYCbCrMatchesGeneric(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 320, 240))
	g := photoLike(320, 240, 12)
	for y := range 240 {
		for x := range 320 {
			v := g.GrayAt(x, y).Y
			src.SetNRGBA(x, y, color.NRGBA{R: v, G: uint8(255 - v), B: v / 2, A: 255})
		}
	}
	ycbcr := jpegEncode(t, src, 85)
	if _, ok := ycbcr.(*image.YCbCr); !ok {
		t.Fatalf("jpeg.Decode returned %T, want *image.YCbCr", ycbcr)
	}
	fast, err := blockhash(ycbcr, ycbcr.Bounds(), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	// Force the At fallback by wrapping so type switch misses.
	slow, err := blockhash(image.Image(atOnly{ycbcr}), ycbcr.Bounds(), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	if fast.Hex() != slow.Hex() {
		t.Fatalf("fast %s != slow %s", fast.Hex(), slow.Hex())
	}
}

// atOnly hides concrete types so bindPixelSum uses img.At.
type atOnly struct{ image.Image }
