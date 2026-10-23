package phash

import "math"

// cosTable builds the type-II DCT kernel cos(pi*k*(2n+1)/(2*size)) for
// transform lengths k = 0..size-1.
func cosTable(size int) [][]float64 {
	t := make([][]float64, size)
	for k := range size {
		t[k] = make([]float64, size)
		for n := range size {
			t[k][n] = math.Cos(math.Pi * float64(k) * float64(2*n+1) / (2 * float64(size)))
		}
	}
	return t
}

// dct2d computes the unnormalized type-II DCT over both axes of a rows x
// cols matrix (each axis transformed with its own length). It does not
// mutate its input.
//
// The reference pipeline (qarmin/img_hash, via rustdct) uses the
// unnormalized type-II DCT as well; only relative magnitudes matter for
// the median threshold, so no normalization constants are applied.
func dct2d(m [][]float64) [][]float64 {
	rows := len(m)
	cols := len(m[0])
	cosCols := cosTable(cols) // row transforms
	cosRows := cosTable(rows) // column transforms

	// DCT over each row.
	tmp := make([][]float64, rows)
	for i := range rows {
		tmp[i] = make([]float64, cols)
		for k := range cols {
			s := 0.0
			for x := range cols {
				s += m[i][x] * cosCols[k][x]
			}
			tmp[i][k] = s
		}
	}

	// DCT over each column.
	out := make([][]float64, rows)
	for i := range out {
		out[i] = make([]float64, cols)
	}
	for k := range cols {
		for i := range rows {
			s := 0.0
			for y := range rows {
				s += tmp[y][k] * cosRows[i][y]
			}
			out[i][k] = s
		}
	}
	return out
}
