package x3f

import (
	"math/rand"
	"testing"
)

// SGRows.Fill gives exactly At's values, for single- and multi-channel
// tables and an image size that does not divide the table's.
func TestSGRowsMatchesAt(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	tab := func(rows, cols, ch, chan0 int) sgTable {
		g := make([]float64, rows*cols*ch)
		for i := range g {
			g[i] = 0.8 + 0.4*r.Float64()
		}
		return sgTable{rows: rows, cols: cols, channels: ch, chan0: chan0, gain: g}
	}
	for _, s := range []*SpatialGain{
		{tabs: []sgTable{tab(17, 25, 3, 0)}},
		{tabs: []sgTable{tab(9, 13, 1, 0), tab(9, 13, 1, 1), tab(11, 7, 1, 2)}},
		{tabs: []sgTable{tab(5, 6, 2, 1), tab(4, 4, 1, 0)}},
	} {
		rows, cols := 301, 457
		rr := s.NewRows(rows, cols)
		g := make([]float64, cols*3)
		for y := 0; y < rows; y += 37 {
			rr.Fill(y, g)
			for x := 0; x < cols; x++ {
				for k := 0; k < 3; k++ {
					if want := s.At(y, x, k, rows, cols); g[x*3+k] != want {
						t.Fatalf("row %d col %d ch %d: %v, want %v", y, x, k, g[x*3+k], want)
					}
				}
			}
		}
	}
}
