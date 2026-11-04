package phash

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"
)

// TestRotationsFourFoldIdentity: four 90° steps must return the original
// bits for every rotation-supporting algorithm, at every supported size.
func TestRotationsFourFoldIdentity(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, alg := range []Alg{AlgBlockhash, AlgDoubleGradient} {
		for _, size := range []Size{Size8, Size16, Size32} {
			h := make(Hash, bitsLen(alg, size)/8)
			rng.Read(h)
			rots := Rotations(h, alg)
			if len(rots) != 4 {
				t.Fatalf("%s/%d: got %d rotations", alg, size, len(rots))
			}
			if got := rot90Step(rots[3], alg); got.Hamming(h) != 0 {
				t.Errorf("%s/%d: four 90° steps moved %d bits", alg, size, got.Hamming(h))
			}
		}
	}
}

// TestRotationTaxOnSmoothPicture pins the measured behaviour: 180° is
// bit-exact for both layouts, and the 90° tax stays far below the ~half-hash
// distance between unrelated pictures (blockhash pays its band-median noise,
// double-gradient only pays resampling ties).
func TestRotationTaxOnSmoothPicture(t *testing.T) {
	img := testPicture(128, 96, 1)
	cfgs := []struct {
		alg      Alg
		tax90Max int
	}{
		{AlgBlockhash, 96},
		{AlgDoubleGradient, 8},
	}
	for _, c := range cfgs {
		cfg := Config{Alg: c.alg, Size: Size16}
		rots := hashRotations(t, img, cfg)
		// 180° is exact up to threshold ties (equal neighbours negate
		// wrongly); blockhash compares floats so its ties are rarer.
		tax180Max := 8
		if c.alg == AlgBlockhash {
			tax180Max = 0
		}
		if d := rots[2].Hamming(mustHash(t, rot90(img, 2), cfg)); d > tax180Max {
			t.Errorf("%s: 180° tax = %d, want <= %d", c.alg, d, tax180Max)
		}
		if d := rots[1].Hamming(mustHash(t, rot90(img, 1), cfg)); d > c.tax90Max {
			t.Errorf("%s: 90° tax = %d, want <= %d", c.alg, d, c.tax90Max)
		}
	}
}

// TestMinRotDistanceSeparatesPictures: a rotated copy must stay far below an
// unrelated picture under min-rotation distance (the property the gate relies on).
func TestMinRotDistanceSeparatesPictures(t *testing.T) {
	cfgs := []struct {
		alg              Alg
		sameMax, diffMin int
	}{{AlgBlockhash, 80, 90}, {AlgDoubleGradient, 8, 40}}
	for _, c := range cfgs {
		cfg := Config{Alg: c.alg, Size: Size16}
		a := mustHash(t, testPicture(128, 96, 1), cfg)
		aRot := mustHash(t, rot90(testPicture(128, 96, 1), 1), cfg)
		other := mustHash(t, testPicture(128, 96, 7), cfg)
		if d := MinRotDistance(a, aRot, c.alg); d > c.sameMax {
			t.Errorf("%s: rotated same picture distance %d, want <= %d", c.alg, d, c.sameMax)
		}
		if d := MinRotDistance(a, other, c.alg); d < c.diffMin {
			t.Errorf("%s: unrelated picture distance %d, want >= %d", c.alg, d, c.diffMin)
		}
	}
}

// ---- helpers ----

func bitsLen(alg Alg, size Size) int {
	n := int(size)
	if alg == AlgDoubleGradient {
		return 2 * (n/2 + 1) * (n / 2)
	}
	return n * n
}

func rot90Step(h Hash, alg Alg) Hash {
	return Rotations(h, alg)[1]
}

func mustHash(t *testing.T, img image.Image, cfg Config) Hash {
	t.Helper()
	h, err := ComputeWith(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func hashRotations(t *testing.T, img image.Image, cfg Config) []Hash {
	t.Helper()
	return Rotations(mustHash(t, img, cfg), cfg.Alg)
}

// rot90 returns the picture rotated k*90° clockwise (exact pixel permutation).
func rot90(src image.Image, k int) image.Image {
	for range k % 4 {
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		out := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := range h {
			for x := range w {
				out.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
		src = out
	}
	return src
}

// testPicture builds a deterministic low-frequency test picture.
func testPicture(w, h, seed int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fx, fy := 2.0+float64(seed), 3.0+float64(seed%3)
	for y := range h {
		for x := range w {
			u, v := float64(x)/float64(w), float64(y)/float64(h)
			lum := uint8(128 + 90*math.Sin(fx*u)*math.Cos(fy*v+1))
			img.Set(x, y, color.NRGBA{lum, lum, lum, 255})
		}
	}
	return img
}
