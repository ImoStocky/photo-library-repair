package phash

import (
	"fmt"
	"image"
	"math"
)

// blockhash implements blockhash.io as used by qarmin/img_hash / Czkawka.
// width and height must be multiples of 4 (8/16/32 qualify). No resize:
// the full image is partitioned into width×height blocks.
func blockhash(img image.Image, b image.Rectangle, width, height int) (Hash, error) {
	if width%4 != 0 || height%4 != 0 {
		return nil, fmt.Errorf("blockhash size must be a multiple of 4 (got %dx%d)", width, height)
	}
	iw, ih := b.Dx(), b.Dy()
	if iw == 0 || ih == 0 {
		return nil, fmt.Errorf("empty image")
	}

	evenX := iw%width == 0
	evenY := ih%height == 0
	if evenX && evenY {
		return blockhashEven(img, b, width, height)
	}
	return blockhashUneven(img, b, width, height, evenX, evenY)
}

func blockhashEven(img image.Image, b image.Rectangle, width, height int) (Hash, error) {
	iw, ih := b.Dx(), b.Dy()
	blockW := iw / width
	blockH := ih / height
	blocks := make([]float64, width*height)
	for by := range height {
		for bx := range width {
			var sum float64
			for iy := range blockH {
				for ix := range blockW {
					sum += float64(pixelSum(img, b.Min.X+bx*blockW+ix, b.Min.Y+by*blockH+iy))
				}
			}
			blocks[by*width+bx] = sum
		}
	}
	half := float64(blockW*blockH) * 255 * 3 / 2
	return packBoolBits(blockBits(blocks, width, half)), nil
}

func blockhashUneven(img image.Image, b image.Rectangle, width, height int, evenX, evenY bool) (Hash, error) {
	iw, ih := b.Dx(), b.Dy()
	bw := float64(iw) / float64(width)
	bh := float64(ih) / float64(height)
	blocks := make([]float64, width*height)

	for y := range ih {
		for x := range iw {
			px := float64(pixelSum(img, b.Min.X+x, b.Min.Y+y))
			fx, fy := float64(x), float64(y)

			var weightLeft, weightRight, weightTop, weightBottom float64
			var blockLeft, blockRight, blockTop, blockBottom int

			if evenX {
				blockLeft = int(fx / bw)
				blockRight = blockLeft
				weightLeft, weightRight = 1, 0
			} else {
				xMod := math.Mod(fx+1, bw)
				xFrac := xMod - math.Floor(xMod)
				weightLeft = 1 - xFrac
				weightRight = xFrac
				blockLeft = int(fx / bw)
				if math.Floor(xMod) > 0 || x+1 == iw {
					blockRight = blockLeft
				} else {
					blockRight = min(blockLeft+1, width-1)
				}
			}

			if evenY {
				blockTop = int(fy / bh)
				blockBottom = blockTop
				weightTop, weightBottom = 1, 0
			} else {
				yMod := math.Mod(fy+1, bh)
				yFrac := yMod - math.Floor(yMod)
				weightTop = 1 - yFrac
				weightBottom = yFrac
				blockTop = int(fy / bh)
				if math.Floor(yMod) > 0 || y+1 == ih {
					blockBottom = blockTop
				} else {
					blockBottom = min(blockTop+1, height-1)
				}
			}

			blocks[blockTop*width+blockLeft] += px * weightLeft * weightTop
			if weightBottom != 0 {
				blocks[blockBottom*width+blockLeft] += px * weightLeft * weightBottom
			}
			if weightRight != 0 {
				blocks[blockTop*width+blockRight] += px * weightRight * weightTop
			}
			if weightRight != 0 && weightBottom != 0 {
				blocks[blockBottom*width+blockRight] += px * weightRight * weightBottom
			}
		}
	}
	half := bw * bh * 255 * 3 / 2
	return packBoolBits(blockBits(blocks, width, half)), nil
}

// pixelSum is r+g+b (0..765); fully transparent pixels count as white (765),
// matching blockhash-python / img_hash.
func pixelSum(img image.Image, x, y int) int {
	r, g, b, a := img.At(x, y).RGBA()
	if a>>8 == 0 {
		return 255 * 3
	}
	return int(r>>8) + int(g>>8) + int(b>>8)
}

// blockBits thresholds each block against the median of its img_hash
// group (chunks of width*4), matching qarmin/img_hash gen_hash!.
func blockBits(blocks []float64, width int, halfBlockValue float64) []bool {
	groupLen := width * 4
	out := make([]bool, len(blocks))
	for start := 0; start < len(blocks); start += groupLen {
		end := min(start+groupLen, len(blocks))
		band := blocks[start:end]
		med := medianF64(band)
		for i, v := range band {
			eq := math.Abs(v-med) < 0.001
			out[start+i] = v > med || (eq && med > halfBlockValue)
		}
	}
	return out
}

func medianF64(v []float64) float64 {
	s := append([]float64(nil), v...)
	for i := 1; i < len(s); i++ {
		x := s[i]
		j := i
		for j > 0 && s[j-1] > x {
			s[j] = s[j-1]
			j--
		}
		s[j] = x
	}
	mid := len(s) / 2
	if len(s)%2 == 0 {
		return (s[mid-1] + s[mid]) / 2
	}
	return s[mid]
}
