package render

// Exact box (area) resampling: each output pixel is the area-weighted mean
// of the source pixels it covers, fractions included. Replaces the
// integer-edge box, which at non-integer ratios (a 2352-wide decode to a
// 1600 preview, 1600 to 1200) averaged one source pixel here and two
// there: an uneven, aliased result shifted by a fraction of a pixel per
// step. SDIM0031's 1200 px preview sat 0.75 px (3 sensor rows) below the
// full-size render shrunk to the same size.

type tap struct {
	i int
	w float32
}

// boxTaps lists, for each of dn outputs, the sources (of sn) it covers and
// their weights (summing to 1).
func boxTaps(sn, dn int) [][]tap {
	taps := make([][]tap, dn)
	s := float64(sn) / float64(dn)
	for o := range taps {
		a, b := float64(o)*s, float64(o+1)*s
		for i := int(a); i < sn && float64(i) < b; i++ {
			w := min(b, float64(i+1)) - max(a, float64(i))
			if w > 1e-9 {
				taps[o] = append(taps[o], tap{i, float32(w / s)})
			}
		}
	}
	return taps
}

// areaResample resamples an sw×sh RGB image to dw×dh (meant for
// shrinking; growing repeats pixels, with fractional edges).
func areaResample(src []float32, sw, sh, dw, dh int) []float32 {
	if sw == dw && sh == dh {
		return append([]float32(nil), src...)
	}
	tx, ty := boxTaps(sw, dw), boxTaps(sh, dh)
	tmp := make([]float32, dw*sh*3)
	parallelRows(sh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row, out := src[y*sw*3:], tmp[y*dw*3:]
			for x, ts := range tx {
				var r, g, b float32
				for _, t := range ts {
					r += row[t.i*3] * t.w
					g += row[t.i*3+1] * t.w
					b += row[t.i*3+2] * t.w
				}
				out[x*3], out[x*3+1], out[x*3+2] = r, g, b
			}
		}
	})
	dst := make([]float32, dw*dh*3)
	parallelRows(dh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			out := dst[y*dw*3 : (y+1)*dw*3]
			for _, t := range ty[y] {
				row := tmp[t.i*dw*3 : (t.i+1)*dw*3]
				for i, v := range row {
					out[i] += v * t.w
				}
			}
		}
	})
	return dst
}

