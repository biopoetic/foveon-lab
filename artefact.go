package main

// Task 43: chroma structure the high-ISO frame has and its own ISO 100 frame
// does not.
//
// The task 41 edge measure asks whether a colour step moved across an object
// boundary. That is blind to a mottled patch, a blocky strip or a cloud in a
// flat field: they are structure *inside* an object, and a median over edge
// profiles does not see them. The band table in coloursplit.go does look at
// those scales, but it averages the error over flat blocks, so a small but
// visible green blotch inside a silver strip is diluted by the rest of the
// crop.
//
// This measure builds the target's chroma scale space and the reference's on
// the same pixels, and at each scale keeps only the part where the target has
// more band-passed chroma than the reference. It reports that excess at 4-8,
// 8-16, 16-32, 32-64 and 64-96 px, plus new 2-4 px chroma edges, as a high
// quantile over two masks chosen on the ISO 100 frame: its flat 16x16 blocks
// and a chroma-flat "inside an object" mask that also stays clear of large
// chroma edges. A high quantile, not a mean, is the point: one block or cloud
// is what the eye catches.
//
// Analysis only (nr-score); nothing here is on a render path.

import "math"

// artefactBands are the band edges of the chroma-structure measure, in px.
var artefactBands = [6]int{4, 8, 16, 32, 64, 96}

// artefactMetric is one pipeline's chroma-structure excess over its own ISO
// 100 frame. All values are CIELAB dE; higher is worse. Indices of Bands are
// 4-8, 8-16, 16-32, 32-64, 64-96 px.
type artefactMetric struct {
	N         int        `json:"n"`
	Edge      float64    `json:"edge"`      // new 2-4 px chroma edges, dE/px
	Bands     [5]float64 `json:"bands"`     // structure excess per band, p99
	Total     float64    `json:"total"`     // weighted total over the inside mask
	FlatTotal float64    `json:"flatTotal"` // the same over the ISO 100 flat blocks
	InsideN   int        `json:"insideN"`
	FlatN     int        `json:"flatN"`
}

// artefactWeights weight the bands in Total. A block or cloud at 4-32 px is
// what reads as a change of material inside an object; the coarser bands
// still count, but a wide filter's smooth bleed already scores there and the
// task's complaint was about the new 4-32 px structure. The weights are not a
// fit to any crop: they say how visible a band is at 1:1, and the whole table
// is reported alongside the total so a reader can re-weight it.
var artefactWeights = [5]float64{1, 1, 1, 0.5, 0.25}

// artefactTotal combines the pieces. Edge is already in dE/px; the bands are
// dE. The sum is a score with no physical unit, comparable only between
// pipelines on the same frame.
func artefactTotal(edge float64, bands [5]float64) float64 {
	t := edge
	for i, w := range artefactWeights {
		t += w * bands[i]
	}
	return t
}

// quantileMask is the q quantile (0..1) of v over mask, from a fixed
// histogram: the values are dE and a resolution of 1/128 is far below
// anything visible. Returns 0 for an empty mask.
func quantileMask(v []float32, mask []bool, q float64) float64 {
	const hi = 64.0
	const bins = 8192
	var h [bins]int32
	n := 0
	for i, x := range v {
		if !mask[i] {
			continue
		}
		b := int(float64(x) * bins / hi)
		if b < 0 {
			b = 0
		}
		if b >= bins {
			b = bins - 1
		}
		h[b]++
		n++
	}
	if n == 0 {
		return 0
	}
	want := int(math.Ceil(q * float64(n)))
	if want < 1 {
		want = 1
	}
	acc := 0
	for b, c := range h {
		acc += int(c)
		if acc >= want {
			return (float64(b) + 0.5) * hi / bins
		}
	}
	return hi
}

// scaleSpaceA is one plane's box blurs at the radii artefactBands plus the
// guide radii used by the masks.
type artefactSpace struct {
	r   []int
	low [][]float32
}

func newArtefactSpace(p []float32, w, h int, radii []int) artefactSpace {
	s := artefactSpace{r: radii, low: make([][]float32, len(radii))}
	for i, r := range radii {
		s.low[i] = boxPlane(p, w, h, r)
	}
	return s
}

// bandOf returns low[r1] - low[r2] for a radius pair present in the space.
func (s *artefactSpace) bandOf(r1, r2 int) ([]float32, bool) {
	i1, i2 := -1, -1
	for i, r := range s.r {
		if r == r1 {
			i1 = i
		}
		if r == r2 {
			i2 = i
		}
	}
	if i1 < 0 || i2 < 0 {
		return nil, false
	}
	out := make([]float32, len(s.low[0]))
	for i := range out {
		out[i] = s.low[i1][i] - s.low[i2][i]
	}
	return out, true
}

// artefactInsideMask is "inside an object" on the ISO 100 frame: locally
// chroma-flat at 8 px and clear of the chroma edges at 4 px, so a bleed across
// an object boundary is not counted as new interior structure. The edge
// detector has to be at 4 px: a 12 px box smears a chart-patch step over
// 24 px, its gradient drops below any useful threshold, and the patch
// boundary leaks into the "interior" and is scored as a soft edge.
func artefactInsideMask(ref *lab8) []bool {
	w, h := ref.w, ref.h
	sa := newArtefactSpace(ref.a, w, h, []int{4, 8})
	sb := newArtefactSpace(ref.b, w, h, []int{4, 8})
	ma, mb := sa.low[1], sb.low[1]
	qa := boxPlane(squarePlane(ref.a), w, h, 8)
	qb := boxPlane(squarePlane(ref.b), w, h, 8)
	edge := make([]float32, w*h)
	for y := 1; y+1 < h; y++ {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			gx := math.Hypot(float64(sa.low[0][i+1])-float64(sa.low[0][i-1]), float64(sb.low[0][i+1])-float64(sb.low[0][i-1])) / 2
			gy := math.Hypot(float64(sa.low[0][i+w])-float64(sa.low[0][i-w]), float64(sb.low[0][i+w])-float64(sb.low[0][i-w])) / 2
			if math.Hypot(gx, gy) > 2.5 {
				edge[i] = 1
			}
		}
	}
	dil := boxPlane(edge, w, h, 16)
	out := make([]bool, w*h)
	for i := range out {
		va := math.Max(0, float64(qa[i])-float64(ma[i])*float64(ma[i]))
		vb := math.Max(0, float64(qb[i])-float64(mb[i])*float64(mb[i]))
		if ref.y[i] <= 8 || ref.y[i] >= 248 || dil[i] > 0 {
			continue
		}
		if math.Sqrt((va+vb)/2) < 1.5 {
			out[i] = true
		}
	}
	return out
}

// measureArtefact fills m for target against ref. Edges and bands are the
// new chroma structure the target has and the reference does not: at each
// scale the reference's local band energy is subtracted from the target's,
// and only the positive part is kept. A pipeline whose high-ISO frame looks
// like its own ISO 100 frame scores zero; a smooth filter whose only error is
// a wide bleed scores in the coarse bands; a switching filter that invents a
// block or a mottle scores in the bands the eye sees it.
func (m *artefactMetric) measureArtefact(target, ref *lab8) {
	if target.w < 64 || target.h < 64 || target.w != ref.w || target.h != ref.h {
		return
	}
	w, h := target.w, target.h
	radii := []int{2, 4, 8, 16, 32, 64, 96}
	ta := newArtefactSpace(target.a, w, h, radii)
	tb := newArtefactSpace(target.b, w, h, radii)
	ra := newArtefactSpace(ref.a, w, h, radii)
	rb := newArtefactSpace(ref.b, w, h, radii)
	flat, _ := flatMask(ref)
	inside := artefactInsideMask(ref)
	m.InsideN = 0
	m.FlatN = 0
	for _, v := range inside {
		if v {
			m.InsideN++
		}
	}
	for _, v := range flat {
		if v {
			m.FlatN++
		}
	}
	// New chroma edges at 2-4 px: the difference field's gradient, smoothed
	// over 2 px so single-pixel grain does not set the high quantile, then
	// over 4 px more so a coherent edge or block survives and random grain
	// does not. A block step or a new boundary that the ISO 100 frame does
	// not have survives both; ordinary grain does not.
	da := make([]float32, w*h)
	db := make([]float32, w*h)
	for i := range da {
		da[i] = target.a[i] - ref.a[i]
		db[i] = target.b[i] - ref.b[i]
	}
	sa := boxPlane(da, w, h, 2)
	sb := boxPlane(db, w, h, 2)
	edge := make([]float32, w*h)
	for y := 1; y+1 < h; y++ {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			gx := math.Hypot(float64(sa[i+1])-float64(sa[i-1]), float64(sb[i+1])-float64(sb[i-1])) / 2
			gy := math.Hypot(float64(sa[i+w])-float64(sa[i-w]), float64(sb[i+w])-float64(sb[i-w])) / 2
			edge[i] = float32(math.Hypot(gx, gy))
		}
	}
	edge = boxPlane(edge, w, h, 4)
	m.Edge = quantileMask(edge, inside, 0.95)
	var flatBands [5]float64
	for k := 0; k < 5; k++ {
		r1, r2 := artefactBands[k], artefactBands[k+1]
		ta1, _ := ta.bandOf(r1, r2)
		tb1, _ := tb.bandOf(r1, r2)
		ra1, _ := ra.bandOf(r1, r2)
		rb1, _ := rb.bandOf(r1, r2)
		et := boxPlane(squareSum(ta1, tb1), w, h, r1)
		er := boxPlane(squareSum(ra1, rb1), w, h, r1)
		// Integrate the local energy over a second window at the same scale:
		// a cloud or a block is coherent over it, grain is not.
		et = boxPlane(et, w, h, r1)
		er = boxPlane(er, w, h, r1)
		ex := make([]float32, w*h)
		for i := range ex {
			a := float64(et[i])
			if a < 0 {
				a = 0
			}
			b := float64(er[i])
			if b < 0 {
				b = 0
			}
			d := math.Sqrt(a) - math.Sqrt(b)
			if d > 0 {
				ex[i] = float32(d)
			}
		}
		m.Bands[k] = quantileMask(ex, inside, 0.99)
		flatBands[k] = quantileMask(ex, flat, 0.99)
	}
	m.Total = artefactTotal(m.Edge, m.Bands)
	m.FlatTotal = artefactTotal(m.Edge, flatBands)
	m.N = 1
}

// squareSum is a*a+b*b without a temporary per term.
func squareSum(a, b []float32) []float32 {
	out := make([]float32, len(a))
	for i := range out {
		out[i] = a[i]*a[i] + b[i]*b[i]
	}
	return out
}
