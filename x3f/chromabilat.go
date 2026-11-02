package x3f

// Task 43: one continuous chroma filter instead of a gate.
//
// Task 41's gate switched between a narrow and a wide guided filter wherever a
// chroma-edge detector fired. The switch itself followed noise, so it left
// pink/green clouds in flat grey, green mottle on a silver strip and blocky
// steps inside a red spine. This file replaces the switch with a single
// weighted average: a joint bilateral filter over a wide support (128 px at
// full resolution) whose range weight is the difference in a heavily smoothed
// chroma estimate. The weight falls continuously with that difference, so the
// filter averages along a spine and not across it; there is no mask, no gate
// and no second filter to switch to.
//
// The guide and the base are the existing luma-guided filter's output: that is
// the clean, edge-aware chroma estimate the task asks for, and reading it
// instead of the raw layers is what makes a small range sigma usable at ISO
// 6400 (in the raw layers the colour signal is tiny next to the noise). The
// bilateral then averages that estimate over 128 px. Its spatial support is
// narrowed continuously where the denoised luma is textured, so thread fibres
// and label type are not averaged into rope. It runs at 1/4 size, where the
// support is 32 px, and the output replaces the wide guided filter for
// ISO >= 800 only: ISO 400 and below keep the old path and are bit-identical.
//
// Tuning used only the DP 1 Merrill and sd Quattro H ladders; DP 3 Merrill,
// dp0 and dp2 Quattro were held out.

import "math"

var (
	// dnCB = 0 turns the filter off (FOVEON_DN_CB); the rest are research
	// switches for the support, spatial sigma, guide smoothing and range
	// sigma, all in full-resolution px except the range sigma (layer units).
	dnCB      = envFloat("FOVEON_DN_CB", 1)
	dnCBSup   = envFloat("FOVEON_DN_CBR", 128)
	dnCBSig   = envFloat("FOVEON_DN_CBS", 40)
	dnCBGuide = envFloat("FOVEON_DN_CBGR", 4)
	dnCBGU    = envFloat("FOVEON_DN_CBGU", 1)    // 1 = guided output is also the base
	dnCBRange = envFloat("FOVEON_DN_CBG", 0.005) // chromaticity units; 0 = from rel
	dnCBDown  = envFloat("FOVEON_DN_CBD", 4)
	dnCBBaseR = envFloat("FOVEON_DN_CBBR", 24)
	// dnCBLuma is the range sigma of the denoised luminance guide, in relative
	// luma. It is a second, continuous range term: it keeps the filter from
	// averaging across a lit fibre into a shadowed one (similar chroma, very
	// different luma) while leaving a smooth lighting gradient free.
	dnCBLuma = envFloat("FOVEON_DN_CBL", 0.15)
	// dnCBRangeK scales the automatic range sigma by the frame's relative
	// luminance noise: the smoothed guide's residual noise is a fraction of
	// rel, and a real colour difference is much larger than either.
	dnCBRangeK  = envFloat("FOVEON_DN_CBGK", 0.45)
	dnCBTexture = envFloat("FOVEON_DN_CBT", 12)
	// dnCBFine is the full-resolution scale below which chroma detail is
	// added back; dnCBFineAmt is the fraction of that band kept (1 = all of
	// it, 0 = none). A little of it keeps the fibre-level colour texture that
	// is real, at the price of some grain.
	dnCBFine    = envFloat("FOVEON_DN_CBFINE", 4)
	dnCBFineAmt = envFloat("FOVEON_DN_CBFA", 0.0)
)

// cbDownsample area-averages p by f in each direction.
func cbDownsample(p []float32, w, h, f int) ([]float32, int, int) {
	cw, ch := (w+f-1)/f, (h+f-1)/f
	out := make([]float32, cw*ch)
	for cy := 0; cy < ch; cy++ {
		y0, y1 := cy*f, min((cy+1)*f, h)
		for cx := 0; cx < cw; cx++ {
			x0, x1 := cx*f, min((cx+1)*f, w)
			var s float32
			for y := y0; y < y1; y++ {
				row := p[y*w:]
				for x := x0; x < x1; x++ {
					s += row[x]
				}
			}
			out[cy*cw+cx] = s / float32((y1-y0)*(x1-x0))
		}
	}
	return out, cw, ch
}

// cbUpsample is bilinear resampling of a coarse plane to w x h.
func cbUpsample(c []float32, cw, ch, w, h, f int) []float32 {
	out := make([]float32, w*h)
	inv := 1 / float64(f)
	parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sy := (float64(y)+0.5)*inv - 0.5
			if sy < 0 {
				sy = 0
			}
			if sy > float64(ch-1) {
				sy = float64(ch - 1)
			}
			cy0 := int(sy)
			cy1 := min(cy0+1, ch-1)
			fy := sy - float64(cy0)
			for x := 0; x < w; x++ {
				sx := (float64(x)+0.5)*inv - 0.5
				if sx < 0 {
					sx = 0
				}
				if sx > float64(cw-1) {
					sx = float64(cw - 1)
				}
				cx0 := int(sx)
				cx1 := min(cx0+1, cw-1)
				fx := sx - float64(cx0)
				v00 := float64(c[cy0*cw+cx0])
				v10 := float64(c[cy0*cw+cx1])
				v01 := float64(c[cy1*cw+cx0])
				v11 := float64(c[cy1*cw+cx1])
				out[y*w+x] = float32((v00*(1-fx)+v10*fx)*(1-fy) + (v01*(1-fx)+v11*fx)*fy)
			}
		}
	})
	return out
}

// cbRangeLUT is exp(-t) over t in [0, maxT], indexed linearly. A table keeps
// the inner loop to one multiply and one lookup instead of an exp.
func cbRangeLUT(sig float64) []float32 {
	const n = 2048
	const maxT = 12.0
	lut := make([]float32, n)
	for i := range lut {
		t := maxT * float64(i) / float64(n-1)
		lut[i] = float32(math.Exp(-t))
	}
	return lut
}

// cbWeight is the range weight for one offset. d2 is the squared difference
// of the smoothed guide chroma; both terms are continuous.
func cbWeight(t, lutMaxT float64, lut []float32) float32 {
	if t >= lutMaxT {
		return 0
	}
	i := int(t / lutMaxT * float64(len(lut)-1))
	return lut[i]
}

func cbAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// chromaBilateral filters each normalised layer plane with the same weights,
// built from the difference between two orthogonal chroma combinations of a
// heavily smoothed copy of the layers. guide, when non-nil, is a denoised
// chroma estimate (the luma-guided filter's output); it is used only to build
// the weights, never blended into the result. It returns full-resolution
// planes.
func chromaBilateral(pn [3][]float32, guide [3][]float32, G []float32, w, h int, scale, rel float64) [3][]float32 {
	f := int(math.Round(dnCBDown * scale))
	if f < 2 {
		f = 2
	}
	if f > 8 {
		f = 8
	}
	// Coarse base: a box mean at the downsampling scale, so the 0-4 px band
	// stays out of the filter and can be added back, in part, unchanged.
	r0 := max(1, f/2)
	var c [3][]float32
	var cw, ch int
	for k := 0; k < 3; k++ {
		lo := boxBlur(pn[k], w, h, r0)
		c[k], cw, ch = cbDownsample(lo, w, h, f)
		putPlane(lo)
	}
	// Guide: the coarse planes smoothed again, then two chroma differences.
	// This is the "heavily smoothed chroma estimate" whose difference sets
	// the range weight. When guide is given it is a denoised estimate of the
	// same scene, so the estimate can be smooth without the base being so.
	hasGuide := guide[0] != nil
	g := max(0, int(math.Round(dnCBGuide*scale/float64(f))))
	var gc [3][]float32
	for k := 0; k < 3; k++ {
		switch {
		case hasGuide:
			gr := max(1, int(math.Round(dnCBGuide*scale)))
			lo := boxBlur(guide[k], w, h, gr)
			gc[k], _, _ = cbDownsample(lo, w, h, f)
			putPlane(lo)
		case g > 0:
			gc[k] = boxBlur(c[k], cw, ch, g)
		default:
			gc[k] = append([]float32(nil), c[k]...)
		}
	}
	g0 := getPlane(cw * ch)
	g1 := getPlane(cw * ch)
	gl, _, _ := cbDownsample(G, w, h, f)
	// Chromaticity, not the raw layer difference: divide by the smoothed
	// layer sum. The planes are normalised by the frame mean, so a raw
	// difference scales as 1/mean and a dark ISO 6400 frame would make every
	// colour difference enormous; the ratio is exposure-invariant and a
	// range sigma in it means the same thing on every frame and body.
	for i := range g0 {
		sum := gc[0][i] + gc[1][i] + gc[2][i] + 1e-3
		g0[i] = (gc[0][i] - gc[1][i]) / sum
		g1[i] = (gc[2][i] - gc[1][i]) / sum
	}
	for k := 0; k < 3; k++ {
		putPlane(gc[k])
	}
	// Support and sigmas in coarse pixels.
	R := int(math.Round(dnCBSup * scale / float64(f)))
	if R < 3 {
		R = 3
	}
	if lim := min(cw, ch) / 2; R > lim {
		R = max(3, lim)
	}
	sigS := dnCBSig * scale / float64(f)
	if sigS < 1 {
		sigS = 1
	}
	sigL := dnCBLuma
	if sigL < 0.01 {
		sigL = 0.01
	}
	sigR := dnCBRange
	if sigR <= 0 {
		sigR = dnCBRangeK * rel
		if sigR < 0.010 {
			sigR = 0.010
		}
	}
	lut := cbRangeLUT(sigR)
	const lutMaxT = 12.0
	// Spatial weights, narrowed continuously where the denoised luma carries
	// texture. A thread's fibres and a book's paper are high-frequency luma
	// structure; a flat background or a book's spine is not. Narrowing the
	// support there keeps the fibres from being averaged into one rope, while
	// the flat areas still get the full 96 px support that removes clouds.
	// This is not a switch: the support moves smoothly with the local gradient.
	const texTiers = 16
	wsT := make([][]float32, texTiers)
	for ti := 0; ti < texTiers; ti++ {
		tt := float64(ti) / float64(texTiers-1)
		s := sigS / (1 + dnCBTexture*tt)
		if s < 1 {
			s = 1
		}
		row := make([]float32, R+1)
		for d := 0; d <= R; d++ {
			row[d] = float32(math.Exp(-0.5 * float64(d*d) / (s * s)))
		}
		wsT[ti] = row
	}
	var tex []float32
	if dnCBTexture > 0 {
		tf := make([]float32, cw*ch)
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				i := y*cw + x
				var gx, gy float64
				if x > 0 && x+1 < cw {
					gx = (float64(gl[i+1]) - float64(gl[i-1])) / 2
				}
				if y > 0 && y+1 < ch {
					gy = (float64(gl[i+cw]) - float64(gl[i-cw])) / 2
				}
				tf[i] = float32(math.Min(1, math.Hypot(gx, gy)))
			}
		}
		tex = tf
	}
	texTier := func(i int) int {
		if tex == nil {
			return 0
		}
		ti := int(tex[i] * texTiers)
		if ti >= texTiers {
			ti = texTiers - 1
		}
		return ti
	}
	// Horizontal pass: shared weights, three channel sums.
	h0 := getPlane(cw * ch)
	h1 := getPlane(cw * ch)
	h2 := getPlane(cw * ch)
	parallel(ch, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			base := y * cw
			for x := 0; x < cw; x++ {
				i := base + x
				ti := texTier(i)
				g0i, g1i := g0[i], g1[i]
				gLi := gl[i]
				var s0, s1, s2, sw float32
				for d := -R; d <= R; d++ {
					j := x + d
					if j < 0 {
						j = 0
					} else if j >= cw {
						j = cw - 1
					}
					j += base
					d0 := float64(g0[j] - g0i)
					d1 := float64(g1[j] - g1i)
					dl := float64(gl[j] - gLi)
					t := (d0*d0+d1*d1)/(2*sigR*sigR) + (dl*dl)/(2*sigL*sigL)
					wr := cbWeight(t, lutMaxT, lut)
					wgt := wsT[ti][cbAbs(d)] * wr
					if wgt == 0 {
						continue
					}
					s0 += wgt * c[0][j]
					s1 += wgt * c[1][j]
					s2 += wgt * c[2][j]
					sw += wgt
				}
				if sw <= 0 {
					h0[i], h1[i], h2[i] = c[0][i], c[1][i], c[2][i]
					continue
				}
				isw := 1 / sw
				h0[i], h1[i], h2[i] = s0*isw, s1*isw, s2*isw
			}
		}
	})
	// Vertical pass, banded over columns for cache locality.
	out0 := getPlane(cw * ch)
	out1 := getPlane(cw * ch)
	out2 := getPlane(cw * ch)
	const band = 64
	nb := (cw + band - 1) / band
	parallel(nb, func(b0, b1 int) {
		for bi := b0; bi < b1; bi++ {
			x0 := bi * band
			bw := min(band, cw-x0)
			for x := x0; x < x0+bw; x++ {
				for y := 0; y < ch; y++ {
					i := y*cw + x
					ti := texTier(i)
					g0i, g1i := g0[i], g1[i]
					gLi := gl[i]
					var s0, s1, s2, sw float32
					for d := -R; d <= R; d++ {
						j := y + d
						if j < 0 {
							j = 0
						} else if j >= ch {
							j = ch - 1
						}
						jj := j*cw + x
						d0 := float64(g0[jj] - g0i)
						d1 := float64(g1[jj] - g1i)
						dl := float64(gl[jj] - gLi)
						t := (d0*d0+d1*d1)/(2*sigR*sigR) + (dl*dl)/(2*sigL*sigL)
						wr := cbWeight(t, lutMaxT, lut)
						wgt := wsT[ti][cbAbs(d)] * wr
						if wgt == 0 {
							continue
						}
						s0 += wgt * h0[jj]
						s1 += wgt * h1[jj]
						s2 += wgt * h2[jj]
						sw += wgt
					}
					if sw <= 0 {
						out0[i], out1[i], out2[i] = h0[i], h1[i], h2[i]
						continue
					}
					isw := 1 / sw
					out0[i], out1[i], out2[i] = s0*isw, s1*isw, s2*isw
				}
			}
		}
	})
	putPlane(h0)
	putPlane(h1)
	putPlane(h2)
	putPlane(g0)
	putPlane(g1)
	putPlane(gl)
	var res [3][]float32
	for k := 0; k < 3; k++ {
		var filt []float32
		switch k {
		case 0:
			filt = out0
		case 1:
			filt = out1
		case 2:
			filt = out2
		}
		upF := cbUpsample(filt, cw, ch, w, h, f)
		o := getPlane(w * h)
		if dnCBFine > 0 {
			upC := cbUpsample(c[k], cw, ch, w, h, f)
			amt := float32(dnCBFineAmt)
			for i := range o {
				o[i] = upF[i] + amt*(pn[k][i]-upC[i])
			}
			putPlane(upC)
		} else {
			copy(o, upF)
		}
		putPlane(upF)
		res[k] = o
		putPlane(c[k])
	}
	putPlane(out0)
	putPlane(out1)
	putPlane(out2)
	return res
}
