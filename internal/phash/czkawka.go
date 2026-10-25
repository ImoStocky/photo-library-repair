package phash

// meanBits is image_hasher Mean: bit i is set when luma[i] >= mean.
func meanBits(luma []uint8) []bool {
	var sum uint64
	for _, v := range luma {
		sum += uint64(v)
	}
	mean := uint8(sum / uint64(len(luma)))
	out := make([]bool, len(luma))
	for i, v := range luma {
		out[i] = v >= mean
	}
	return out
}

// medianBits is image_hasher Median (without DCT preprocessing).
func medianBits(luma []uint8) []bool {
	med := medianU8(luma)
	out := make([]bool, len(luma))
	for i, v := range luma {
		out[i] = v >= med
	}
	return out
}

func medianU8(v []uint8) uint8 {
	s := append([]uint8(nil), v...)
	// Insertion sort is fine for ≤32²=1024 samples.
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
		return uint8((uint16(s[mid-1]) + uint16(s[mid])) / 2)
	}
	return s[mid]
}

// gradientBits is horizontal Gradient: per row, bit = (left < right).
// luma is row-major with the given rowstride (width = rowstride = n+1).
func gradientBits(luma []uint8, rowstride int) []bool {
	rows := len(luma) / rowstride
	out := make([]bool, 0, rows*(rowstride-1))
	for r := range rows {
		row := luma[r*rowstride : (r+1)*rowstride]
		for x := 1; x < rowstride; x++ {
			out = append(out, row[x-1] < row[x])
		}
	}
	return out
}

// vertGradientBits is VertGradient: per column, bit = (upper < lower).
// luma is row-major width×(n+1); width is the hash size n.
func vertGradientBits(luma []uint8, width int) []bool {
	height := len(luma) / width
	out := make([]bool, 0, width*(height-1))
	for x := range width {
		for y := 1; y < height; y++ {
			upper := luma[(y-1)*width+x]
			lower := luma[y*width+x]
			out = append(out, upper < lower)
		}
	}
	return out
}

// doubleGradientBits chains horizontal then vertical gradients
// (image_hasher DoubleGradient).
func doubleGradientBits(luma []uint8, rowstride int) []bool {
	h := gradientBits(luma, rowstride)
	v := vertGradientBits(luma, rowstride)
	return append(h, v...)
}
