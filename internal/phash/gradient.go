package phash

import "image"

// computeDoubleGradient implements czkawka/image_hasher HashAlg::DoubleGradient
// for hash_size(N, N): resize luma to (N/2+1)×(N/2+1) with LANCZOS3, quantize
// to 8-bit (Bytes path — Czkawka does not enable DCT), then pack horizontal
// adjacent-pixel comparisons followed by vertical ones (bit = last < this),
// LSB-first.
//
// Bit count is N*(N/2+1)*2 (40 / 144 / 544 for N = 8 / 16 / 32).
func computeDoubleGradient(img image.Image, b image.Rectangle, n int) (Hash, error) {
	if n%2 != 0 {
		n++
	}
	rw, rh := n/2+1, n/2+1
	luma := lanczosU8(img, b, rw, rh)
	return packBoolBits(doubleGradientBits(luma, rw)), nil
}

// packBoolBits packs bools LSB-first into a Hash.
func packBoolBits(bitsOut []bool) Hash {
	nbytes := (len(bitsOut) + 7) / 8
	hsh := make(Hash, nbytes)
	for i, v := range bitsOut {
		if v {
			hsh[i/8] |= 1 << uint(i%8)
		}
	}
	return hsh
}
