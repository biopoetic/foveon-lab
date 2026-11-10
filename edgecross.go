package main

// Task 41: colour across an object's edge.
//
// The band tables in coloursplit.go say where in space the colour error sits,
// but not whether a colour step moved across an object boundary. This measure
// starts from the ISO 100 frame's chroma edges (a*/b* gradient, no luma used),
// and for each edge samples the a*/b* profile on both sides along the gradient
// normal. The excess width is the area between the observed step and an ideal
// one, in px: about zero on a sharp ISO 100 frame, wider when a colour filter
// has averaged one object into its neighbour. "Gain" is the extra width over
// the same pipeline's own ISO 100 frame (so a sharper or softer base export
// cancels), and "contrast" is the step that survives at +-R pixels.
//
// Analysis only (nr-score); nothing here is on a render path.
//
// Analysis only (nr-score); nothing here is on a render path.

import "math"

// edgeSample is one chroma edge found on the reference: its midpoint, the
// unit normal pointing towards the positive side of the chroma step, and the
// step vector (da, db) measured at +-R px.
type edgeSample struct {
	x, y   float64
	nx, ny float64
}

// edgeScale maps the analysis width to the sensor's full width, so the +-R
// sampling covers the same scene distance on a 1548 px sd Quattro H export
// as on a 4704 px Merrill export.
func edgeScale(w int) float64 {
	s := float64(w) / 4704
	if s < 0.30 {
		s = 0.30
	}
	if s > 1.30 {
		s = 1.30
	}
	return s
}

// findChromaEdges returns the ISO 100 frame's chroma edges, thinned by
// non-maximum suppression along the gradient normal. It uses only a* and b*,
// so an edge between two objects of equal luma but different colour is found;
// a luma-only edge is not. The blur and baseline grow with the analysis scale
// so the same probe survives the SPP export's downscaling.
func findChromaEdges(ref *lab8, scale float64) []edgeSample {
	w, h := ref.w, ref.h
	if w < 32 || h < 32 {
		return nil
	}
	s := max(2, int(math.Round(3*scale)))
	d := max(2, int(math.Round(4*scale)))
	rMax := max(4, int(math.Round(16*scale)))
	if w <= 2*rMax+4 || h <= 2*rMax+4 {
		return nil
	}
	A := boxPlane(ref.a, w, h, s)
	B := boxPlane(ref.b, w, h, s)
	// E is the squared chroma difference between the two sides of a d-px
	// baseline; this integrates noise along the baseline, which is what makes
	// an ISO 1600-scale colour step detectable at all. The spatial direction is
	// the structure tensor's principal eigenvector: a and b are independent
	// value axes, so their spatial gradients are summed as outer products, not
	// added channel to channel.
	E := make([]float32, w*h)
	nxP := make([]float32, w*h)
	nyP := make([]float32, w*h)
	for y := d; y < h-d; y++ {
		for x := d; x < w-d; x++ {
			i := y*w + x
			ax := (A[i+d] - A[i-d]) / 2
			ay := (A[i+w*d] - A[i-w*d]) / 2
			bx := (B[i+d] - B[i-d]) / 2
			by := (B[i+w*d] - B[i-w*d]) / 2
			jxx := ax*ax + bx*bx
			jyy := ay*ay + by*by
			jxy := ax*ay + bx*by
			hh := (jxx + jyy) / 2
			disc := float32(math.Hypot(float64((jxx-jyy)/2), float64(jxy)))
			E[i] = hh + disc
			ang := 0.5 * math.Atan2(float64(2*jxy), float64(jxx-jyy))
			nxP[i], nyP[i] = float32(math.Cos(ang)), float32(math.Sin(ang))
		}
	}
	var edges []edgeSample
	for y := rMax + 2; y < h-rMax-2; y++ {
		for x := rMax + 2; x < w-rMax-2; x++ {
			i := y*w + x
			if E[i] <= 0 {
				continue
			}
			// The reference's step across the probe baseline must be a real
			// colour difference: at least 8 dE, in a*/b*, either axis.
			step := math.Sqrt(float64(E[i])) * 2 * float64(d)
			if step < 8 {
				continue
			}
			nx, ny := float64(nxP[i]), float64(nyP[i])
			// Non-maximum suppression: one sample per ridge, taken at the
			// gradient maximum, so a 5 px edge does not count five times.
			jx := int(math.Round(float64(x) + nx))
			jy := int(math.Round(float64(y) + ny))
			kx := int(math.Round(float64(x) - nx))
			ky := int(math.Round(float64(y) - ny))
			if jx < 0 || jy < 0 || kx < 0 || ky < 0 || jx >= w || kx >= w || jy >= h || ky >= h {
				continue
			}
			if E[jy*w+jx] > E[i] || E[ky*w+kx] > E[i] {
				continue
			}
			// Skip clipped or near-black reference pixels: their chroma is
			// either not a colour or not a reliable edge.
			if ref.y[i] <= 6 || ref.y[i] >= 250 {
				continue
			}
			edges = append(edges, edgeSample{x: float64(x), y: float64(y), nx: nx, ny: ny})
		}
	}
	// A textured scene can return tens of thousands of edges; a deterministic
	// stride keeps the measurement fast and does not favour one region.
	const maxEdges = 12000
	if len(edges) > maxEdges {
		stride := (len(edges) + maxEdges - 1) / maxEdges
		kept := edges[:0]
		for i := 0; i < len(edges); i += stride {
			kept = append(kept, edges[i])
		}
		edges = kept
	}
	return edges
}

// samplePlane is bilinear sampling of a plane at a float position.
func samplePlane(p []float32, w, h int, x, y float64) float64 {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x > float64(w-1) {
		x = float64(w - 1)
	}
	if y > float64(h-1) {
		y = float64(h - 1)
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := x-float64(x0), y-float64(y0)
	v00 := float64(p[y0*w+x0])
	v10 := float64(p[y0*w+x1])
	v01 := float64(p[y1*w+x0])
	v11 := float64(p[y1*w+x1])
	return (v00*(1-fx)+v10*fx)*(1-fy) + (v01*(1-fx)+v11*fx)*fy
}

// edgeProfile is the signed half-rise distance and the retained step for one
// edge, for one pipeline.
type edgeProfile struct {
	spread   float64 // 90 % rise distance in px, clipped to [0.5, rMax]
	half     float64 // 50 % rise distance, the old width
	contrast float64 // |step(R)| / |reference step(R)|
	at6      float64 // normalised step at one third of the range: 1 = sharp
	ok       bool
}

// chromaProfile samples D(r) = chroma(+r) - chroma(-r) along the edge normal,
// projects it on the reference's step direction and returns the half-rise
// distance of the normalised profile. Half a pixel is the sharpest a
// pixel-sampled step can be.
func chromaProfile(target *lab8, e edgeSample, ua, ub float64, refStep, rMax int) edgeProfile {
	w, h := target.w, target.h
	pa := make([]float64, rMax+1)
	pb := make([]float64, rMax+1)
	sa := make([]float64, rMax+1)
	sb := make([]float64, rMax+1)
	for r := 0; r <= rMax; r++ {
		// A small tangential average would need the edge tangent; instead the
		// per-edge profile is averaged over thousands of edges, which is the
		// same noise reduction without a second interpolation direction.
		rr := float64(r)
		xa, ya := e.x+rr*e.nx, e.y+rr*e.ny
		pa[r] = samplePlane(target.a, w, h, xa, ya)
		pb[r] = samplePlane(target.b, w, h, xa, ya)
		xb, yb := e.x-rr*e.nx, e.y-rr*e.ny
		sa[r] = samplePlane(target.a, w, h, xb, yb)
		sb[r] = samplePlane(target.b, w, h, xb, yb)
	}
	fullA, fullB := pa[rMax]-sa[rMax], pb[rMax]-sb[rMax]
	if fullA*ua+fullB*ub < 0 {
		// The step reversed: noise or a second edge, not a usable profile.
		return edgeProfile{}
	}
	// Normalise by the pipeline's own full step so the number is a width, not
	// an amplitude; a bled edge has a smaller but still monotone step.
	den := math.Hypot(fullA, fullB)
	if den < 1e-6 {
		return edgeProfile{}
	}
	refDen := float64(refStep)
	f := make([]float64, rMax+1)
	k6 := max(1, rMax/3)
	for r := 0; r <= rMax; r++ {
		ra := pa[r] - sa[r]
		rb := pb[r] - sb[r]
		v := (ra*ua + rb*ub) / den
		if r == rMax {
			v = 1
		}
		f[r] = v
	}
	// A three-point smoothing keeps a single noisy sample from setting the
	// width; the profile itself is physical and monotone.
	sm := make([]float64, rMax+1)
	copy(sm, f)
	for r := 1; r < rMax; r++ {
		sm[r] = (f[r-1] + 2*f[r] + f[r+1]) / 4
	}
	sm[rMax] = 1
	rise := func(level float64) float64 {
		prev := sm[0]
		for r := 1; r <= rMax; r++ {
			if prev < level && sm[r] >= level {
				return float64(r-1) + (level-prev)/(sm[r]-prev)
			}
			prev = sm[r]
		}
		return float64(rMax)
	}
	// The excess width is the area between the observed step and an ideal one:
	// sum over the probe of (1 - f(r)) in px. A sharp edge is under a pixel; a
	// box window of radius R reads about R/2. Unlike a half-rise it uses the
	// whole shoulder, so a filter that keeps the middle of the step but leaves
	// a wide pedestal still scores as spread.
	excess := 0.0
	for r := 1; r <= rMax; r++ {
		if sm[r] < 1 {
			excess += 1 - sm[r]
		}
	}
	return edgeProfile{
		spread:   excess,
		half:     rise(0.5),
		contrast: den / refDen,
		at6:      sm[k6],
		ok:       true,
	}
}

// measureEdgeCross fills m's cross-edge fields for target against ref. Edges
// are found only on ref (the ISO 100 frame), so both pipelines are measured on
// the same scene locations and directions.
func (m *nrMetric) measureEdgeCross(target, ref *lab8, edges []edgeSample, scale float64) {
	if len(edges) == 0 {
		return
	}
	rMax := max(4, int(math.Round(16*scale)))
	// Each edge's reference step and direction; both pipelines normalise by
	// their own ISO 100 step, which is where the contrast denominator comes
	// from. The reference's profiles are measured once and shared.
	type prep struct {
		e       edgeSample
		ua, ub  float64
		refStep float64
		refProf edgeProfile
	}
	preps := make([]prep, 0, len(edges))
	for _, e := range edges {
		w, h := ref.w, ref.h
		aP := samplePlane(ref.a, w, h, e.x+float64(rMax)*e.nx, e.y+float64(rMax)*e.ny)
		aM := samplePlane(ref.a, w, h, e.x-float64(rMax)*e.nx, e.y-float64(rMax)*e.ny)
		bP := samplePlane(ref.b, w, h, e.x+float64(rMax)*e.nx, e.y+float64(rMax)*e.ny)
		bM := samplePlane(ref.b, w, h, e.x-float64(rMax)*e.nx, e.y-float64(rMax)*e.ny)
		da, db := aP-aM, bP-bM
		den := math.Hypot(da, db)
		if den < 8 {
			continue
		}
		ua, ub := da/den, db/den
		rp := chromaProfile(ref, e, ua, ub, int(math.Round(den)), rMax)
		if !rp.ok || rp.at6 < 0.85 {
			// A reference edge that is still rising at a third of the probe
			// range is not an isolated step (a soft gradient, or a second edge
			// inside the window); its half-rise would measure the scene, not a
			// filter.
			continue
		}
		preps = append(preps, prep{e: e, ua: ua, ub: ub, refStep: den, refProf: rp})
	}
	if len(preps) == 0 {
		return
	}
	spreads := make([]float64, 0, len(preps))
	gains := make([]float64, 0, len(preps))
	contrasts := make([]float64, 0, len(preps))
	refSpreads := make([]float64, 0, len(preps))
	for _, p := range preps {
		tp := chromaProfile(target, p.e, p.ua, p.ub, int(math.Round(p.refStep)), rMax)
		if !tp.ok {
			continue
		}
		spreads = append(spreads, tp.spread)
		gains = append(gains, tp.spread-p.refProf.spread)
		contrasts = append(contrasts, tp.contrast)
		refSpreads = append(refSpreads, p.refProf.spread)
	}
	if len(spreads) == 0 {
		return
	}
	m.EdgeN = len(spreads)
	m.EdgeSpread = medianFloat(spreads)
	m.EdgeRefSpread = medianFloat(refSpreads)
	m.EdgeGain = medianFloat(gains)
	m.EdgeContrast = medianFloat(contrasts)
}

// medianFloat is the usual median, sorting a copy.
func medianFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
