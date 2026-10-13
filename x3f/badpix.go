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
	if len(list) == 0 {
		return 0
	}
	fixed := 0
	relaxed := false
	for len(list) > 0 {
		var still []int32
		var done []int32
		vals := make([][3]uint16, 0, len(list))
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
			var s [3]uint32
			for _, q := range nb {
				o := (q[1]*w + q[0]) * 3
				for k := 0; k < 3; k++ {
					s[k] += uint32(raw.Pix[o+k])
				}
			}
			n := uint32(len(nb))
			vals = append(vals, [3]uint16{uint16((s[0] + n/2) / n), uint16((s[1] + n/2) / n), uint16((s[2] + n/2) / n)})
			done = append(done, p)
		}
		// Apply after the pass, so a pass reads only original good pixels.
		for i, p := range done {
			copy(raw.Pix[int(p)*3:int(p)*3+3], vals[i][:])
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
