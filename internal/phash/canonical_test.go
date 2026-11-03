package phash

import (
	"image"
	"image/color"
	"testing"
)

func TestCanonicalRotationInvariant(t *testing.T) {
	// Asymmetric 6×10 picture so no two rotations can coincide by accident.
	src := image.NewNRGBA(image.Rect(0, 0, 6, 10))
	for y := range 10 {
		for x := range 6 {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 40), G: uint8(y * 25), B: uint8((x + y) * 13)})
		}
	}
	cfg := Config{Alg: AlgBlockhash, Size: Size16}

	want, err := ComputeWith(Canonical(src), cfg)
	if err != nil {
		t.Fatal(err)
	}
	rot := src
	for range 3 {
		rot = rotate90cw(rot)
		got, err := ComputeWith(Canonical(rot), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Hex() != want.Hex() {
			t.Fatalf("canonical hash changed under rotation: %s vs %s", got.Hex(), want.Hex())
		}
	}
}
