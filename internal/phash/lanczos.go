package phash

import "math"

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

// lanczos3 resizes the grayscale source (sw x sh) to w x h using a separable
// LANCZOS3 filter, matching the LANCZOS resampling the reference
// implementation applies before the DCT.
func lanczos3(src []float64, sw, sh, w, h int) []float64 {
	// Horizontal pass: sw x sh -> w x sh.
	mid := make([]float64, w*sh)
	sx := float64(sw) / float64(w)
	for y := 0; y < sh; y++ {
		for x := 0; x < w; x++ {
			c := (float64(x)+0.5)*sx - 0.5
			var num, den float64
			for k := int(math.Floor(c - 3)); k <= int(math.Ceil(c+3)); k++ {
				if k < 0 || k >= sw {
					continue
				}
				wgt := lanczosKernel(c - float64(k))
				num += src[y*sw+k] * wgt
				den += wgt
			}
			if den != 0 {
				mid[y*w+x] = num / den
			}
		}
	}

	// Vertical pass: w x sh -> w x h.
	out := make([]float64, w*h)
	sy := float64(sh) / float64(h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := (float64(y)+0.5)*sy - 0.5
			var num, den float64
			for k := int(math.Floor(c - 3)); k <= int(math.Ceil(c+3)); k++ {
				if k < 0 || k >= sh {
					continue
				}
				wgt := lanczosKernel(c - float64(k))
				num += mid[k*w+x] * wgt
				den += wgt
			}
			if den != 0 {
				out[y*w+x] = num / den
			}
		}
	}
	return out
}
