package phash

import (
	"image"
	"math"
)

// lanczosKernel is the LANCZOS3 windowed-sinc kernel (window a=3).
func lanczosKernel(x float64) float64 {
	if x == 0 {
		return 1
	}
	if x <= -3 || x >= 3 {
		return 0
	}
	px := math.Pi * x
	return (math.Sin(px) / px) * (math.Sin(px/3) / (px / 3))
}

// lanczosGrid rescales the picture in b to the DCT grid (gridW x gridH)
// with a separable LANCZOS3 filter.
//
// The horizontal pass reads the source row by row: every output pixel is a
// windowed average of at most 7 samples of one source row (the kernel
// support is fixed in kernel units, independent of the ratio), so the pass
// costs O(gridW x sourceHeight) and the only working buffer is
// gridW x sourceHeight floats. The result is identical for every source
// size, including upscale, and degenerates to the identity when the source
// already is the grid size.
func lanczosGrid(img image.Image, b image.Rectangle) []float64 {
	sw, sh := b.Dx(), b.Dy()

	// Horizontal pass: b -> gridW x sh.
	mid := make([]float64, gridW*sh)
	sx := float64(sw) / float64(gridW)
	for y := range sh {
		for x := range gridW {
			c := (float64(x)+0.5)*sx - 0.5
			var num, den float64
			for k := int(math.Floor(c - 3)); k <= int(math.Ceil(c+3)); k++ {
				if k < 0 || k >= sw {
					continue
				}
				r, g, bl, _ := img.At(b.Min.X+k, b.Min.Y+y).RGBA()
				luma := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
				wgt := lanczosKernel(c - float64(k))
				num += luma * wgt
				den += wgt
			}
			if den != 0 {
				mid[y*gridW+x] = num / den
			}
		}
	}

	// Vertical pass: gridW x sh -> gridW x gridH.
	out := make([]float64, gridW*gridH)
	sy := float64(sh) / float64(gridH)
	for y := range gridH {
		for x := range gridW {
			c := (float64(y)+0.5)*sy - 0.5
			var num, den float64
			for k := int(math.Floor(c - 3)); k <= int(math.Ceil(c+3)); k++ {
				if k < 0 || k >= sh {
					continue
				}
				wgt := lanczosKernel(c - float64(k))
				num += mid[k*gridW+x] * wgt
				den += wgt
			}
			if den != 0 {
				out[y*gridW+x] = num / den
			}
		}
	}
	return out
}
