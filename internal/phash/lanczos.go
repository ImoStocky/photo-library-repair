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

// lanczosResize rescales the picture in b to tw×th with a separable
// LANCZOS3 filter and returns float luma row-major.
func lanczosResize(img image.Image, b image.Rectangle, tw, th int) []float64 {
	sw, sh := b.Dx(), b.Dy()

	mid := make([]float64, tw*sh)
	sx := float64(sw) / float64(tw)
	for y := range sh {
		for x := range tw {
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
				mid[y*tw+x] = num / den
			}
		}
	}

	out := make([]float64, tw*th)
	sy := float64(sh) / float64(th)
	for y := range th {
		for x := range tw {
			c := (float64(y)+0.5)*sy - 0.5
			var num, den float64
			for k := int(math.Floor(c - 3)); k <= int(math.Ceil(c+3)); k++ {
				if k < 0 || k >= sh {
					continue
				}
				wgt := lanczosKernel(c - float64(k))
				num += mid[k*tw+x] * wgt
				den += wgt
			}
			if den != 0 {
				out[y*tw+x] = num / den
			}
		}
	}
	return out
}

// lanczosU8 is lanczosResize quantized to 8-bit luma (image_hasher Bytes path).
func lanczosU8(img image.Image, b image.Rectangle, tw, th int) []uint8 {
	f := lanczosResize(img, b, tw, th)
	out := make([]uint8, len(f))
	for i, v := range f {
		out[i] = clamp8(v)
	}
	return out
}
