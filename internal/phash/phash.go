// Package phash computes perceptual hashes for near-duplicate grouping.
//
// # Legacy DCT hash (Compute / Config{Alg: AlgDCT})
//
// The default 16×8 (128-bit) DCT path mirrors qarmin/img_hash's optional DCT
// preprocessing. Square DCT sizes 8×8 / 16×16 / 32×32 are also available.
//
// # Czkawka-compatible hashes
//
// ComputeWith implements the algorithms Czkawka/Krokiet expose via
// image_hasher — Mean, Median, Gradient, VertGradient, DoubleGradient,
// Blockhash — at square sizes 8, 16 and 32. Bits are packed LSB-first.
package phash

import (
	"encoding/hex"
	"fmt"
	"image"
	"math/bits"
	"slices"
	"strings"
)

const (
	// Bytes is the legacy default perceptual hash size in bytes (DCT 16×8).
	Bytes = 16
	// Bits is the legacy default perceptual hash size in bits.
	Bits = Bytes * 8
)

// Alg selects the perceptual-hash construction.
type Alg string

const (
	// AlgDCT is median-thresholded DCT (legacy pHash). Empty Config.Alg defaults here.
	AlgDCT Alg = "dct"
	// AlgMean compares each resized pixel to the mean (aHash-like).
	AlgMean Alg = "mean"
	// AlgMedian compares each resized pixel to the median.
	AlgMedian Alg = "median"
	// AlgGradient is the horizontal gradient hash (dHash-like).
	AlgGradient Alg = "gradient"
	// AlgVertGradient is the vertical gradient hash.
	AlgVertGradient Alg = "vert-gradient"
	// AlgDoubleGradient chains horizontal then vertical gradients.
	AlgDoubleGradient Alg = "double-gradient"
	// AlgBlockhash is the blockhash.io algorithm (no resize).
	AlgBlockhash Alg = "blockhash"
)

// Size is the square hash dimension N for an N×N hash (8, 16, or 32).
type Size int

const (
	Size8  Size = 8
	Size16 Size = 16
	Size32 Size = 32
)

// Config selects the hash algorithm and dimension.
//
// For [AlgDCT], Size 0 keeps the legacy 16×8 (128-bit) hash; Size 8/16/32
// produce square N×N DCT hashes (N² bits). For Czkawka algorithms, Size
// defaults to 16 when zero and must be 8, 16, or 32.
type Config struct {
	Alg  Alg
	Size Size
}

// Normalize fills defaults and rejects unsupported combinations.
func (c Config) Normalize() (Config, error) {
	if c.Alg == "" {
		c.Alg = AlgDCT
	}
	switch c.Alg {
	case AlgDCT, AlgMean, AlgMedian, AlgGradient, AlgVertGradient, AlgDoubleGradient, AlgBlockhash:
	default:
		return c, fmt.Errorf("unknown phash alg %q", c.Alg)
	}
	if c.Size == 0 {
		if c.Alg != AlgDCT {
			c.Size = Size16
		}
		return c, nil
	}
	switch c.Size {
	case Size8, Size16, Size32:
	default:
		return c, fmt.Errorf("phash size must be 8, 16, or 32, got %d", c.Size)
	}
	return c, nil
}

// ParseAlg maps a Czkawka/Krokiet algorithm name (case-insensitive).
func ParseAlg(s string) (Alg, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dct", "phash", "":
		return AlgDCT, nil
	case "mean":
		return AlgMean, nil
	case "median":
		return AlgMedian, nil
	case "gradient":
		return AlgGradient, nil
	case "vertgradient", "vert-gradient", "vert_gradient", "v.grad.", "vgrad":
		return AlgVertGradient, nil
	case "doublegradient", "double-gradient", "double_gradient", "d.grad.", "dgrad":
		return AlgDoubleGradient, nil
	case "blockhash":
		return AlgBlockhash, nil
	default:
		return "", fmt.Errorf("unknown hash algorithm %q", s)
	}
}

// ParseSize accepts 8, 16 or 32.
func ParseSize(n int) (Size, error) {
	switch Size(n) {
	case Size8, Size16, Size32:
		return Size(n), nil
	default:
		return 0, fmt.Errorf("hash size %d not supported (want 8, 16 or 32)", n)
	}
}

// BitLen returns how many hash bits Alg produces at size (0 = legacy DCT 128).
func (a Alg) BitLen(size Size) int {
	if a == AlgDCT && size == 0 {
		return Bits
	}
	n := int(size)
	if n == 0 {
		n = 16
	}
	switch a {
	case AlgDCT, AlgMean, AlgMedian, AlgGradient, AlgVertGradient, AlgBlockhash:
		return n * n
	case AlgDoubleGradient:
		return n*n/2 + n
	default:
		return 0
	}
}

// Hash is a packed LSB-first bit string of variable length.
type Hash []byte

// Compute derives the legacy 16×8 DCT perceptual hash of an image.
func Compute(img image.Image) (Hash, error) {
	return ComputeWith(img, Config{})
}

// ComputeWith derives a perceptual hash under cfg.
func ComputeWith(img image.Image, cfg Config) (Hash, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Empty() {
		return nil, fmt.Errorf("empty image")
	}
	n := int(cfg.Size)

	switch cfg.Alg {
	case AlgDCT:
		if cfg.Size == 0 {
			return computeDCT(img, b, 16, 8)
		}
		return computeDCT(img, b, n, n)
	case AlgMean:
		luma := lanczosU8(img, b, n, n)
		return packBoolBits(meanBits(luma)), nil
	case AlgMedian:
		luma := lanczosU8(img, b, n, n)
		return packBoolBits(medianBits(luma)), nil
	case AlgGradient:
		luma := lanczosU8(img, b, n+1, n)
		return packBoolBits(gradientBits(luma, n+1)), nil
	case AlgVertGradient:
		luma := lanczosU8(img, b, n, n+1)
		return packBoolBits(vertGradientBits(luma, n)), nil
	case AlgDoubleGradient:
		return computeDoubleGradient(img, b, n)
	case AlgBlockhash:
		return blockhash(img, b, n, n)
	default:
		return nil, fmt.Errorf("unknown phash alg %q", cfg.Alg)
	}
}

// computeDCT resizes to (2*hashW)×(2*hashH), runs a 2-D DCT, and
// median-thresholds the top-left hashW×hashH quadrant.
func computeDCT(img image.Image, b image.Rectangle, hashW, hashH int) (Hash, error) {
	gridW, gridH := hashW*2, hashH*2
	resized := lanczosResize(img, b, gridW, gridH)
	grid := make([][]float64, gridH)
	for i := range grid {
		grid[i] = make([]float64, gridW)
		for j := range grid[i] {
			grid[i][j] = float64(clamp8(resized[i*gridW+j]))
		}
	}
	dct := dct2d(grid)
	vals := make([]float64, 0, hashW*hashH)
	for i := range hashH {
		vals = append(vals, dct[i][:hashW]...)
	}
	return packMedianBits(vals), nil
}

func packMedianBits(vals []float64) Hash {
	med := median(vals)
	nbytes := (len(vals) + 7) / 8
	hsh := make(Hash, nbytes)
	for i, v := range vals {
		if v >= med {
			hsh[i/8] |= 1 << uint(i%8)
		}
	}
	return hsh
}

// BitLen returns the number of bits in the hash (8 × len(h)).
func (h Hash) BitLen() int { return len(h) * 8 }

// Valid reports whether the hash carries information. All-zero and
// all-ones hashes are rejected (constant or near-constant pictures),
// mirroring czkawka's is_hash_valid. A nil/empty hash is invalid.
func (h Hash) Valid() bool {
	if len(h) == 0 {
		return false
	}
	allZero, allOne := true, true
	for _, b := range h {
		if b != 0 {
			allZero = false
		}
		if b != 0xFF {
			allOne = false
		}
		if !allZero && !allOne {
			return true
		}
	}
	return false
}

// Hamming returns the number of bits that differ between two hashes.
// Hashes of unequal length are treated as maximally different.
func (h Hash) Hamming(o Hash) int {
	if len(h) != len(o) {
		return max(len(h), len(o)) * 8
	}
	var d int
	for i := range h {
		d += bits.OnesCount8(h[i] ^ o[i])
	}
	return d
}

// Band returns the width bits of the hash starting at bit offset bit
// (offset 0 is the most significant bit of the first byte). Width must not
// exceed 64; bits past the end of the hash read as zero.
func (h Hash) Band(bit, width int) uint64 {
	if width <= 0 {
		return 0
	}
	if width > 64 {
		width = 64
	}
	var out uint64
	for i := range width {
		pos := bit + i
		by := pos / 8
		if by >= len(h) {
			break
		}
		if h[by]&(1<<uint(7-pos%8)) != 0 {
			out |= 1 << uint(width-1-i)
		}
	}
	return out
}

// Hex renders the hash as a lowercase hex string.
func (h Hash) Hex() string { return hex.EncodeToString(h) }

// HashFromHex parses a hex string into a Hash. Length must be even and
// non-zero (any algorithm/size is accepted).
func HashFromHex(s string) (Hash, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("perceptual hash is empty")
	}
	return Hash(b), nil
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
