package phash

import (
	"image"
	"image/draw"
)

// canonicalCfg is the fixed selection hash of Canonical.
var canonicalCfg = Config{Alg: AlgBlockhash, Size: Size16}

// Canonical returns img rotated to a canonical orientation chosen from
// the four 90° rotations: a landscape one (width ≥ height) where the
// axes allow, and among the two landscape rotations the one whose
// 16×16 blockhash is lexicographically smallest. Orientation carries the
// rotation information and stays stable under content noise (crops,
// re-encodes); the blockhash only breaks the 180° tie. Two encodings of
// one picture stored at different rotations then hash alike, so every
// hash computed on the result is rotation-independent. Mirrors are not
// handled. If any rotation fails to hash, the input is returned
// unchanged.
//
// The canonical hash of a pair is one of its 16 rotation-pair distances,
// so canonicalising cannot merge pictures that no rotation of the pair
// already matches.
func Canonical(img image.Image) image.Image {
	src := toNRGBA(img)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	var rs [4]string
	best := -1
	cur := src
	for r := range rs {
		hh, err := ComputeWith(cur, canonicalCfg)
		if err != nil {
			return img
		}
		rs[r] = hh.Hex()
		landscape := w >= h
		if r%2 == 1 {
			landscape = h >= w
		}
		if landscape && (best < 0 || rs[r] < rs[best]) {
			best = r
		}
		cur = rotate90cw(cur)
	}
	for range best {
		src = rotate90cw(src)
	}
	return src
}

// toNRGBA copies src into an *image.NRGBA (a no-op when it already is one).
func toNRGBA(src image.Image) *image.NRGBA {
	if n, ok := src.(*image.NRGBA); ok {
		return n
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// rotate90cw returns src turned 90° clockwise: (x, y) → (h-1-y, x).
func rotate90cw(src *image.NRGBA) *image.NRGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := range h {
		for x := range w {
			si := y*src.Stride + x*4
			di := x*dst.Stride + (h-1-y)*4
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
