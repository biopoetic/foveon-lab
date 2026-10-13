package x3f

// Bad pixel repair from the camera's own lists in CAMF. A Go port of the
// list handling in x3fuse-core's interpolate_bad_pixels (Copyright 2026 Sang
// Lee, Apache-2.0), itself from the Kalpanika x3f tools (BSD-style);
// licences and notices in third_party/x3fuse-core/.
//
// Lists: BadPixels (packed row<<20 | col<<8, older bodies; relative to the
// KeepImageArea), BadPixelsF20 and Jpeg_BadClusters (row, col, level
// triples; Merrill). Each listed pixel is replaced by the mean of its
// good 4-neighbours, in passes, so clusters fill in from their edges.

// FixBadPixels repairs raw in place and returns how many pixels it fixed.
func FixBadPixels(raw *RawImage, c *CAMF) int {
	if raw == nil || c == nil {
		return 0
	}
	w, h := raw.W, raw.H
	bad := make([]bool, w*h)
	var list []int32
	mark := func(col, row int) {
		if col < 0 || row < 0 || col >= w || row >= h || bad[row*w+col] {
			return
		}
		bad[row*w+col] = true
		list = append(list, int32(row*w+col))
	}
	kx, ky := 0, 0
	if k := c.Matrices["KeepImageArea"]; len(k) == 4 {
		kx, ky = int(k[0]), int(k[1])
	}
	for _, v := range c.Matrices["BadPixels"] {
		u := uint32(v)
		mark(int(u&0x000fff00>>8)-kx, int(u>>20)-ky)
	}
	for _, name := range []string{"BadPixelsF20", "Jpeg_BadClusters"} {
		m, d := c.Matrices[name], c.Dims[name]
		if len(d) != 2 || d[0] != 3 {
			continue
		}
		for i := 0; i+2 < len(m); i += 3 {
			mark(int(m[i+1]), int(m[i]))
		}
	}
	markHighlightPixels(c, w, h, mark)
	return fixMarked(raw.Pix, w, h, 3, bad, list)
}

// markHighlightPixels marks the regular grid of highlight pixels some
// sensors carry (HighlightPixelsInfo: first column, first row, column and
// row stride; DP Quattro: every 16th column of every 4th row of the
// quarter-resolution layers), which read differently from their neighbours.
func markHighlightPixels(c *CAMF, w, h int, mark func(col, row int)) {
	hp := c.Matrices["HighlightPixelsInfo"]
	if len(hp) != 4 || hp[2] < 1 || hp[3] < 1 {
		return
	}
	for row := int(hp[1]); row < h; row += int(hp[3]) {
		for col := int(hp[0]); col < w; col += int(hp[2]) {
			mark(col, row)
		}
	}
}

// fixMarked replaces each listed pixel (index y*w+x, marked in bad) of an
// interleaved nch-channel plane with the mean of its good 4-neighbours, in
// passes, so clusters fill in from their edges. It returns how many it fixed.
func fixMarked(pix []uint16, w, h, nch int, bad []bool, list []int32) int {
	if len(list) == 0 {
		return 0
	}
	fixed := 0
	relaxed := false
	for len(list) > 0 {
		var still []int32
		var done []int32
		vals := make([][]uint16, 0, len(list))
		for _, p := range list {
			x, y := int(p)%w, int(p)/w
			ok := func(xx, yy int) bool { return xx >= 0 && yy >= 0 && xx < w && yy < h && !bad[yy*w+xx] }
			var nb [][2]int
			l, r, u, d := ok(x-1, y), ok(x+1, y), ok(x, y-1), ok(x, y+1)
			switch {
			case l && r && u && d:
				nb = [][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}}
			case l && r:
				nb = [][2]int{{x - 1, y}, {x + 1, y}}
			case u && d:
				nb = [][2]int{{x, y - 1}, {x, y + 1}}
			case relaxed:
				for _, q := range [][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}} {
					if ok(q[0], q[1]) {
						nb = append(nb, q)
					}
				}
			}
			if len(nb) == 0 {
				still = append(still, p)
				continue
			}
			var s [4]uint32
			for _, q := range nb {
				o := (q[1]*w + q[0]) * nch
				for k := 0; k < nch; k++ {
					s[k] += uint32(pix[o+k])
				}
			}
			n := uint32(len(nb))
			v := make([]uint16, nch)
			for k := range v {
				v[k] = uint16((s[k] + n/2) / n)
			}
			vals = append(vals, v)
			done = append(done, p)
		}
		// Apply after the pass, so a pass reads only original good pixels.
		for i, p := range done {
			copy(pix[int(p)*nch:int(p)*nch+nch], vals[i])
			bad[p] = false
		}
		fixed += len(done)
		if len(done) == 0 {
			if relaxed {
				break // isolated from every good pixel: leave as is
			}
			relaxed = true
		}
		list = still
	}
	return fixed
}
