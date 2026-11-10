package phash

// Bit-layout rotations.
//
// Rotation-tolerant matching keeps ONE hash per algorithm per picture and
// compares it under every rotation by transforming the hash BITS instead of
// the pixels: each layout is a bit grid whose rotation is an index
// permutation, so a stored hash can be asked "what would you look like if
// the picture had been stored rotated 90°/180°/270°?". The permutations are
// verified empirically (see rotate_test and the scratch harness):
//
//   - blockhash: exact permutation; the 180° turn is bit-exact (4-row bands
//     map onto bands, so the per-band median threshold is preserved), while
//     90°/270° map rows onto columns and the destination bands then carry
//     different medians — a few dozen bits can flip on high-variance
//     pictures (measured 0–72/256, larger for flat images with near-median
//     ties).
//   - double-gradient: exact permutation plus a negation of the
//     vertical→horizontal half: a 90° turn reverses one comparison order,
//     so (a<b) lands as (b<a). Ties (equal neighbours) negate wrongly — a
//     handful of bits. 180° is two negations and cancels. Measured total
//     rotation tax on real photos: 0–4 bits.
//   - the legacy 16×8 DCT has NO bit rotation: its bits are DCT
//     coefficients against a threshold, and a 180° turn negates the
//     odd-frequency coefficients (measured 52–74/128 bit flips), so no
//     permutation expresses it.
//
// Rotations are deterministic bit transforms, so a min-rotation distance
// differs from the true best-rotation picture distance only by the
// threshold noise described above.

import (
	"math"
	"slices"
)

// Rotations returns the 90°-clockwise bit transforms of h for alg: index k
// is h rotated k*90°. Square-grid layouts (blockhash, double-gradient) have
// all four; every other algorithm returns just {h}.
func Rotations(h Hash, alg Alg) []Hash {
	switch alg {
	case AlgBlockhash:
		return rotChain(h, rotBlockhash90)
	case AlgDoubleGradient:
		return rotChain(h, rotDoubleGradient90)
	}
	return []Hash{h}
}

// MinRotDistance is the smallest Hamming distance between a and any bit
// rotation of b under alg (a and b lie in the same orientation space).
// Hashes of different lengths are maximally different, as in Hamming.
func MinRotDistance(a, b Hash, alg Alg) int {
	return MinRotDistanceRots(a, Rotations(b, alg))
}

// MinRotDistanceRots is the smallest Hamming distance between a and any
// hash in rots (typically the result of Rotations). Callers that reuse the
// same b across many comparisons should precompute Rotations(b) once.
func MinRotDistanceRots(a Hash, rots []Hash) int {
	if len(rots) == 0 {
		return a.BitLen() + 1
	}
	best := a.Hamming(rots[0])
	for _, r := range rots[1:] {
		if d := a.Hamming(r); d < best {
			best = d
		}
	}
	return best
}

// rotChain applies one step successively: [h, s(h), s(s(h)), s(s(s(h)))].
func rotChain(h Hash, step func(Hash) Hash) []Hash {
	out := make([]Hash, 0, 4)
	out = append(out, h)
	for range 3 {
		out = append(out, step(out[len(out)-1]))
	}
	return out
}

// rotBlockhash90 rotates the n×n block-bit grid 90° clockwise: source pixel
// (x,y) lands at (n-1-y, x) (bit y*n+x → x*n+n-1-y; verified empirically,
// see rotate_test). n comes from the bit count: 64/256/1024 at sizes 8/16/32.
func rotBlockhash90(h Hash) Hash {
	n, ok := map[int]int{64: 8, 256: 16, 1024: 32}[h.BitLen()]
	if !ok {
		return slices.Clone(h)
	}
	out := make(Hash, len(h))
	for y := range n {
		for x := range n {
			out.setBit(x*n+(n-1-y), h.bit(y*n+x))
		}
	}
	return out
}

// rotDoubleGradient90 rotates the double-gradient layout 90° clockwise.
// With g grid cells per side (g = n/2+1, so 9/17/33) and c = g-1, the hash
// is row-major H bits (H[y][x] = grid[y][x] < grid[y][x+1], index y*c+x)
// followed by COLUMN-major V bits (V[x][y] = grid[y][x] < grid[y+1][x] at
// hLen + x*c+y). A 90° turn maps grid point (x,y) to (g-1-y, x); following
// the compared pairs through that map (verified empirically) gives
//
//	H[y][x] → V[g-1-y][x]        (bit preserved)
//	V[x][y] → H[x][c-1-y] negated (the turn reverses this comparison order)
//
// The fourfold composition is the identity because each 360° cycle negates
// exactly twice.
func rotDoubleGradient90(h Hash) Hash {
	g := dgSide(h)
	if g == 0 {
		return slices.Clone(h)
	}
	c := g - 1
	hLen := g * c
	out := make(Hash, len(h))
	for y := range g {
		for x := range c {
			out.setBit(hLen+(g-1-y)*c+x, h.bit(y*c+x)) // H → V
		}
	}
	for x := range g {
		for y := range c {
			out.setBit(x*c+(c-1-y), !h.bit(hLen+x*c+y)) // V → H, negated
		}
	}
	return out
}

// dgSide returns the double-gradient grid side g implied by the bit count
// (bits = 2·g·(g−1)), or 0 when the length is not a double-gradient hash.
func dgSide(h Hash) int {
	g := int(math.Sqrt(float64(h.BitLen() / 2)))
	for cand := max(g-1, 2); cand <= g+1; cand++ {
		if 2*cand*(cand-1) == h.BitLen() {
			return cand
		}
	}
	return 0
}

func (h Hash) bit(i int) bool { return h[i/8]&(1<<(i%8)) != 0 }

func (h Hash) setBit(i int, v bool) {
	if v {
		h[i/8] |= 1 << uint(i%8)
	}
}
