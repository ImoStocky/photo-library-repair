package phash

import (
	"fmt"
	"image"
	"image/color"
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
	sum := bindPixelSum(img)
	if evenX && evenY {
		return blockhashEven(sum, b, width, height)
	}
	return blockhashUneven(sum, b, width, height, evenX, evenY)
}

func blockhashEven(sum func(x, y int) int, b image.Rectangle, width, height int) (Hash, error) {
	iw, ih := b.Dx(), b.Dy()
	blockW := iw / width
	blockH := ih / height
	blocks := make([]float64, width*height)
	for by := range height {
		for bx := range width {
			var s float64
			for iy := range blockH {
				for ix := range blockW {
					s += float64(sum(b.Min.X+bx*blockW+ix, b.Min.Y+by*blockH+iy))
				}
			}
			blocks[by*width+bx] = s
		}
	}
	half := float64(blockW*blockH) * 255 * 3 / 2
	return packBoolBits(blockBits(blocks, width, half)), nil
}

func blockhashUneven(sum func(x, y int) int, b image.Rectangle, width, height int, evenX, evenY bool) (Hash, error) {
	iw, ih := b.Dx(), b.Dy()
	bw := float64(iw) / float64(width)
	bh := float64(ih) / float64(height)
	blocks := make([]float64, width*height)

	for y := range ih {
		for x := range iw {
			px := float64(sum(b.Min.X+x, b.Min.Y+y))
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

// bindPixelSum returns a per-image sampler matching img.At(x,y).RGBA()>>8
// channel sums without boxing a color.Color on every pixel; fully
// transparent pixels count as white (765), matching blockhash-python /
// img_hash. JPEG-heavy scans spent ~30% in YCbCr.At interface conversion;
// *image.YCbCr reads the planes directly.
func bindPixelSum(img image.Image) func(x, y int) int {
	switch m := img.(type) {
	case *image.YCbCr:
		return func(x, y int) int {
			yi := m.YOffset(x, y)
			ci := m.COffset(x, y)
			r, g, b := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			return int(r) + int(g) + int(b)
		}
	case *image.NYCbCrA:
		return func(x, y int) int {
			a := uint32(m.A[m.AOffset(x, y)])
			if a == 0 {
				return 255 * 3
			}
			yi := m.YOffset(x, y)
			ci := m.COffset(x, y)
			r8, g8, b8 := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			if a == 255 {
				return int(r8) + int(g8) + int(b8)
			}
			sum := 0
			for _, c := range []uint8{r8, g8, b8} {
				v := uint32(c)
				v |= v << 8
				v *= a
				v /= 0xff
				sum += int(v >> 8)
			}
			return sum
		}
	case *image.NRGBA:
		return func(x, y int) int {
			i := m.PixOffset(x, y)
			s := m.Pix[i : i+4 : i+4]
			a := uint32(s[3])
			if a == 0 {
				return 255 * 3
			}
			if a == 255 {
				return int(s[0]) + int(s[1]) + int(s[2])
			}
			// Match color.NRGBA.RGBA: expand 8→16, premultiply, then >>8.
			sum := 0
			for _, c := range s[:3] {
				v := uint32(c)
				v |= v << 8
				v *= a
				v /= 0xff
				sum += int(v >> 8)
			}
			return sum
		}
	case *image.RGBA:
		return func(x, y int) int {
			i := m.PixOffset(x, y)
			s := m.Pix[i : i+4 : i+4]
			if s[3] == 0 {
				return 255 * 3
			}
			return int(s[0]) + int(s[1]) + int(s[2])
		}
	case *image.Gray:
		return func(x, y int) int {
			v := int(m.Pix[m.PixOffset(x, y)])
			return 3 * v
		}
	default:
		return func(x, y int) int {
			r, g, b, a := img.At(x, y).RGBA()
			if a>>8 == 0 {
				return 255 * 3
			}
			return int(r>>8) + int(g>>8) + int(b>>8)
		}
	}
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
