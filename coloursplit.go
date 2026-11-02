package main

// Task 39 colour split: the parts of the ISO-ladder colour error that a
// per-pixel number hides. Everything here is analysis-only (nr-score).
//
// The headline the task asks about ("colour error against the ISO 100 frame")
// is ColourErrFlat: the mean CIELAB a*/b* distance over the ISO 100 frame's
// flat blocks. A per-pixel figure of that kind cannot see a 20 px blotch whose
// local mean is off while its pixel-to-pixel variation is small, so the same
// difference is also split into spatial bands (ChromaErr) and into the
// patch-scale means (nrMetric.ColourErr, SatRetain from measureNR).
//
// Everything also reports where in the scene it was measured: flat blocks and
// textured pixels separately, both chosen on the ISO 100 reference so the
// noise under test cannot pick its own areas.

import "math"

// texBands is the spatial bands of the extended chroma error, in px.
var texBands = [7][2]int{{0, 1}, {1, 2}, {2, 4}, {4, 8}, {8, 16}, {16, 32}, {32, 64}}

// texturedMask marks pixels whose ISO 100 neighbourhood has structure wider
// than the noise: the gradient of a sigma-2 blur above 0.02 (normalised
// luminance), with clipped pixels excluded. It is deliberately the same
// threshold task 37 used.
func texturedMask(ref *lab8) []bool {
	w, h := ref.w, ref.h
	sm := boxPlane(ref.y, w, h, 2)
	tex := make([]bool, w*h)
	const thr = 0.02 * 255
	for y := 1; y+1 < h; y++ {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			if ref.y[i] <= 4 || ref.y[i] >= 251 {
				continue
			}
			gx := (float64(sm[i+1]) - float64(sm[i-1])) / 2
			gy := (float64(sm[i+w]) - float64(sm[i-w])) / 2
			tex[i] = math.Hypot(gx, gy) > thr
		}
	}
	return tex
}

// strictFlatMask keeps the flat blocks that are also clear of chroma edges.
// The ordinary flat mask accepts a block whose own 16x16 spread is low even
// when a colour patch edge runs just outside it; the difference field then
// contains the scene's edge (colour bleeding), not noise. This mask dilates
// the ISO 100 chroma edges by 16 px and also rejects a smooth chroma gradient
// in the 48 px surround, so the per-band numbers are noise on genuinely flat
// colour.
func strictFlatMask(ref *lab8, sel []bool) []bool {
	w, h := ref.w, ref.h
	aa := boxPlane(ref.a, w, h, 4)
	bb := boxPlane(ref.b, w, h, 4)
	edge := make([]float32, w*h)
	for y := 1; y+1 < h; y++ {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			gx := math.Hypot(float64(aa[i+1])-float64(aa[i-1]), float64(bb[i+1])-float64(bb[i-1])) / 2
			gy := math.Hypot(float64(aa[i+w])-float64(aa[i-w]), float64(bb[i+w])-float64(bb[i-w])) / 2
			if math.Hypot(gx, gy) > 3 {
				edge[i] = 1
			}
		}
	}
	dil := boxPlane(edge, w, h, 16)
	ma := boxPlane(ref.a, w, h, 24)
	mb := boxPlane(ref.b, w, h, 24)
	qa := boxPlane(squarePlane(ref.a), w, h, 24)
	qb := boxPlane(squarePlane(ref.b), w, h, 24)
	out := make([]bool, w*h)
	for i := range out {
		if !sel[i] || dil[i] > 0 {
			continue
		}
		va := float64(qa[i]) - float64(ma[i])*float64(ma[i])
		vb := float64(qb[i]) - float64(mb[i])*float64(mb[i])
		if math.Sqrt(math.Max(0, (va+vb)/2)) > 1.5 {
			continue
		}
		out[i] = true
	}
	return out
}

func squarePlane(p []float32) []float32 {
	o := make([]float32, len(p))
	for i := range o {
		o[i] = p[i] * p[i]
	}
	return o
}

// measureColour fills the colour-error fields: the mean a*/b* error in flat
// and textured pixels, the error per spatial band (the low bands are the
// blotches), and the slope of the fine chroma detail on the reference.
func (m *nrMetric) measureColour(target, ref *lab8, sel, strict, tex []bool) {
	w, h := target.w, target.h
	da := make([]float32, w*h)
	db := make([]float32, w*h)
	for i := range da {
		da[i] = target.a[i] - ref.a[i]
		db[i] = target.b[i] - ref.b[i]
	}
	var sf, nf, st, nt float64
	for i := range da {
		d := math.Hypot(float64(da[i]), float64(db[i]))
		if sel[i] {
			sf += d
			nf++
		}
		if tex[i] {
			st += d
			nt++
		}
	}
	if nf > 0 {
		m.ColourErrFlat = sf / nf
	}
	if nt > 0 {
		m.ColourErrTex = st / nt
	}
	for b, r := range texBands {
		ba := bandPlane(da, w, h, r[0], r[1])
		bb := bandPlane(db, w, h, r[0], r[1])
		var ss, n float64
		for i := range ba {
			if strict[i] {
				ss += float64(ba[i])*float64(ba[i]) + float64(bb[i])*float64(bb[i])
				n++
			}
		}
		if n > 0 {
			m.ChromaErr[b] = math.Sqrt(ss / (2 * n))
		}
	}
	m.Blotch32 = m.ChromaErr[6]
	// Fine chroma detail: regress the reference's band-passed (a*,b*) vector
	// on the target's over textured pixels; 1 = all kept, 0 = all removed.
	for b := 0; b < 4; b++ {
		ta := bandPlane(target.a, w, h, nrBands[b], nrBands[b+1])
		tb := bandPlane(target.b, w, h, nrBands[b], nrBands[b+1])
		ra := bandPlane(ref.a, w, h, nrBands[b], nrBands[b+1])
		rb := bandPlane(ref.b, w, h, nrBands[b], nrBands[b+1])
		var num, den float64
		for i := range ta {
			if !tex[i] {
				continue
			}
			num += float64(ta[i])*float64(ra[i]) + float64(tb[i])*float64(rb[i])
			den += float64(ra[i])*float64(ra[i]) + float64(rb[i])*float64(rb[i])
		}
		if den > 1e-9 {
			m.ChromaDetail[b] = num / den
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// estimateShift finds the integer translation of target content against ref// on the luma high-pass, sampled every two pixels and searched to +-8 px.
// It is a check, not a warp: the ladders were shot on a tripod, and a zero
// here means the comparison needs no registration (the report says so).
func estimateShift(ref, target *lab8) (int, int) {
	w, h := ref.w, ref.h
	hp := func(p *lab8) []float32 {
		bl := boxPlane(p.y, w, h, 4)
		o := make([]float32, len(p.y))
		for i := range o {
			o[i] = p.y[i] - bl[i]
		}
		return o
	}
	hr, ht := hp(ref), hp(target)
	best, bx, by := math.Inf(1), 0, 0
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			var s float64
			var n int
			for y := 8; y+8 < h; y += 2 {
				yy := y + dy*2
				for x := 8; x+8 < w; x += 2 {
					xx := x + dx*2
					s += math.Abs(float64(ht[yy*w+xx] - hr[y*w+x]))
					n++
				}
			}
			if n == 0 {
				continue
			}
			if s/float64(n) < best {
				best, bx, by = s/float64(n), dx*2, dy*2
			}
		}
	}
	return bx, by
}
