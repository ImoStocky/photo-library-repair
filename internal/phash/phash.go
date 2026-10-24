// Package phash computes the 16-byte (128-bit) perceptual hash used for
// near-duplicate grouping.
//
// The construction mirrors the DCT preprocessing of qarmin/img_hash, the
// hashing library behind czkawka's similar-image search:
//
//  1. convert the picture to 8-bit luma,
//  2. resize to twice the hash region (32x16) with LANCZOS3, quantizing the
//     result back to 8-bit,
//  4. keep only the low-frequency top-left quadrant of the DCT (16x8 =
//     128 coefficients),
//  5. threshold each coefficient against the median of the quadrant: bit i
//     is 1 when the coefficient is >= the median.
//
// Bits are packed least-significant-bit first, like img_hash's default.
// Hamming distance over the 128 bits measures similarity.
//
// Compute works for any image size: the LANCZOS3 resize is streamed row by
// row, so working memory is bounded by the 32 x source-height grid
// intermediate no matter how large the picture is, and the filter
// degenerates to the identity when the source already is the 32x16 grid.
package phash

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"math/bits"
	"slices"
)

const (
	// Bytes is the perceptual hash size in bytes.
	Bytes = 16
	// Bits is the perceptual hash size in bits.
	Bits = Bytes * 8
	// hashW x hashH is the low-frequency DCT quadrant that becomes the hash.
	hashW = 16
	hashH = 8
	// The DCT is computed at twice the hash region (img_hash SIZE_MULTIPLIER).
	gridW = hashW * 2
	gridH = hashH * 2
)

// Hash is a 16-byte (128-bit) perceptual hash, bits packed LSB first.
type Hash [Bytes]byte

// Compute derives the perceptual hash of an image.
func Compute(img image.Image) (Hash, error) {
	b := img.Bounds()
	if b.Empty() {
		return Hash{}, fmt.Errorf("empty image")
	}

	// Resize to the DCT grid with LANCZOS3, streamed row by row so the
	// source is never fully materialized; quantize the result back to
	// 8-bit, matching the img_hash pipeline (the resize output is 8-bit
	// luma before the DCT).
	resized := lanczosGrid(img, b)
	grid := make([][]float64, gridH)
	for i := range grid {
		grid[i] = make([]float64, gridW)
		for j := range grid[i] {
			grid[i][j] = float64(clamp8(resized[i*gridW+j]))
		}
	}

	dct := dct2d(grid)

	// Crop to the low-frequency top-left quadrant and threshold against
	// its median.
	vals := quadrant(dct)
	med := median(vals)

	var hsh Hash
	for i, v := range vals {
		if v >= med {
			hsh[i/8] |= 1 << uint(i%8)
		}
	}
	return hsh, nil
}

// Valid reports whether the hash carries information. All-zero and
// all-ones hashes are rejected (constant or near-constant pictures),
// mirroring czkawka's is_hash_valid.
func (h Hash) Valid() bool {
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
func (h Hash) Hamming(o Hash) int {
	var d int
	for i := range h {
		d += bits.OnesCount8(h[i] ^ o[i])
	}
	return d
}

// Band returns the width bits of the hash starting at bit offset bit
// (offset 0 is the most significant bit of the first byte). Width must not
// exceed 64.
//
// Band is the building block of the band candidate index: split the hash
// into k+1 contiguous bands and, by the pigeonhole principle, any two
// hashes at hamming distance k or less share at least one whole band, so
// near-duplicate grouping only needs to exact-test bucket mates.
func (h Hash) Band(bit, width int) uint64 {
	hi := binary.BigEndian.Uint64(h[:8])
	lo := binary.BigEndian.Uint64(h[8:])
	switch {
	case bit+width <= 64:
		return hi >> uint(64-bit-width) & bandMask(width)
	case bit >= 64:
		b := bit - 64
		return lo >> uint(64-b-width) & bandMask(width)
	default:
		hiPart := 64 - bit
		return (hi&bandMask(hiPart))<<uint(width-hiPart) | lo>>uint(64-width+hiPart)
	}
}

func bandMask(width int) uint64 {
	if width >= 64 {
		return ^uint64(0)
	}
	return (1 << uint(width)) - 1
}

// Hex renders the hash as a 32-character lowercase hex string.
func (h Hash) Hex() string { return hex.EncodeToString(h[:]) }

// HashFromHex parses a 32-character hex string.
func HashFromHex(s string) (Hash, error) {
	var h Hash
	b, err := hex.DecodeString(s)
	if err != nil {
		return h, err
	}
	if len(b) != Bytes {
		return h, fmt.Errorf("perceptual hash has %d bytes, want %d", len(b), Bytes)
	}
	copy(h[:], b)
	return h, nil
}

// quadrant returns the top-left hashW x hashH block of the DCT matrix in
// row-major order (img_hash crop_2d).
func quadrant(dct [][]float64) []float64 {
	vals := make([]float64, 0, Bits)
	for i := range hashH {
		vals = append(vals, dct[i][:hashW]...)
	}
	return vals
}

// clamp8 rounds to the nearest 8-bit luma value, clamped to 0..255.
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
