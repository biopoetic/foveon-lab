package main

// Measurements for sharpen-study (TASK-90): how much of the Quattro raw
// base's error against SPP is the difference between SPP's sharpening and
// ours, measured at the 1200 px analysis frame on sRGB-encoded pixels.
//
// Everything here works on a flat []float32 of sRGB bytes (0..255), three per
// pixel, because that is the space calib.MeanError scores in (8-bit sRGB mean
// absolute difference). The frequency split, edge profiles and flat-patch
// noise are independent of the photo loading and are unit-tested in
// task90_test.go.

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"sort"
)

// t90SRGB turns a linear interleaved image into its sRGB bytes as floats
// (0..255).
func t90SRGB(pix []float32) []float32 {
	out := make([]float32, len(pix))
	for i, v := range pix {
		out[i] = toSRGB255(v)
	}
	return out
}

func toSRGB255(v float32) float32 {
	x := v
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	return sRGBEncode(x) * 255
}

// sRGBEncode mirrors render.ToSRGB's piecewise curve.
func sRGBEncode(x float32) float32 {
	if x <= 0.0031308 {
		return x * 12.92
	}
	return 1.055*float32(math.Pow(float64(x), 1/2.4)) - 0.055
}

// t90Luma is sRGB luminance in 0..255.
func t90Luma(s []float32, i int) float64 {
	return 0.2126*float64(s[i*3]) + 0.7152*float64(s[i*3+1]) + 0.0722*float64(s[i*3+2])
}

func t90Lumas(s []float32) []float64 {
	n := len(s) / 3
	out := make([]float64, n)
	for i := range out {
		out[i] = t90Luma(s, i)
	}
	return out
}

// meanAbs8 is calib.MeanError's quantity on sRGB-byte arrays.
func meanAbs8(a, b []float32) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}
	var e float64
	for i := 0; i < n; i++ {
		e += math.Abs(float64(a[i]) - float64(b[i]))
	}
	return e / float64(n)
}

// ---------- Gaussian low-pass ----------

func gauss1(sigma float64) []float64 {
	r := int(math.Ceil(3 * sigma))
	if r < 1 {
		r = 1
	}
	k := make([]float64, 2*r+1)
	var sum float64
	for i := range k {
		x := float64(i - r)
		k[i] = math.Exp(-x * x / (2 * sigma * sigma))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	return k
}

// gaussBlur3 blurs each channel of an interleaved 3-channel image, clamping
// at the frame edge.
func gaussBlur3(src []float32, w, h int, sigma float64) []float32 {
	k := gauss1(sigma)
	r := len(k) / 2
	tmp := make([]float32, len(src))
	out := make([]float32, len(src))
	for c := 0; c < 3; c++ {
		for y := 0; y < h; y++ {
			row := src[y*w*3 : (y+1)*w*3]
			for x := 0; x < w; x++ {
				var s float64
				for j := -r; j <= r; j++ {
					xx := x + j
					if xx < 0 {
						xx = 0
					} else if xx >= w {
						xx = w - 1
					}
					s += k[j+r] * float64(row[xx*3+c])
				}
				tmp[(y*w+x)*3+c] = float32(s)
			}
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s float64
				for j := -r; j <= r; j++ {
					yy := y + j
					if yy < 0 {
						yy = 0
					} else if yy >= h {
						yy = h - 1
					}
					s += k[j+r] * float64(tmp[(yy*w+x)*3+c])
				}
				out[(y*w+x)*3+c] = float32(s)
			}
		}
	}
	return out
}

// freqSplit takes both images through the same Gaussian low-pass and returns
// the full error, the error of the two low-passed images (tone, colour,
// exposure) and the remainder (detail). full == low + detail by construction,
// so the two terms always account for the measured error exactly.
func freqSplit(ours, spp []float32, w, h int, sigma float64) (full, low, detail float64) {
	full = meanAbs8(ours, spp)
	lo := gaussBlur3(ours, w, h, sigma)
	ls := gaussBlur3(spp, w, h, sigma)
	low = meanAbs8(lo, ls)
	return full, low, full - low
}

// ---------- edges ----------

// edgeStat is one image's edge measurements from the mean aligned profile:
// transition width (10-90% of the step, px), overshoot beyond the plateaus
// (fraction of the step), the maximum slope (1/px), the halo extent (the
// number of profile pixels outside the plateaus by more than 0.2% of the
// step, px), the mean absolute perpendicular shift after registering the profiles
// (px), and the edge profile residual before and after that shift (fraction
// of the step). N is the number of edges behind the mean.
type edgeStat struct {
	N                int
	Width            float64
	Overshoot        float64
	Slope            float64
	Halo             float64
	Shift            float64
	ProfileUnaligned float64
	ProfileAligned   float64
}

const (
	edgeStep = 0.5 // profile sample spacing, px
	edgeT0   = -6.0
	edgeT1   = 6.0
	edgeMinG = 24.0 // Sobel magnitude, sRGB levels/px
	edgeMinA = 25.0 // step amplitude, sRGB levels
)

func edgeSamples() int { return int((edgeT1-edgeT0)/edgeStep) + 1 }

func t90Sobel(l []float64, w, h int) (mag, gx, gy []float64) {
	n := w * h
	mag, gx, gy = make([]float64, n), make([]float64, n), make([]float64, n)
	at := func(x, y int) float64 {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		return l[y*w+x]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tl, tc, tr := at(x-1, y-1), at(x, y-1), at(x+1, y-1)
			ml, mr := at(x-1, y), at(x+1, y)
			bl, bc, br := at(x-1, y+1), at(x, y+1), at(x+1, y+1)
			dx := (tr + 2*mr + br) - (tl + 2*ml + bl)
			dy := (bl + 2*bc + br) - (tl + 2*tc + tr)
			i := y*w + x
			gx[i], gy[i] = dx, dy
			mag[i] = math.Hypot(dx, dy)
		}
	}
	return
}

// bilinear samples a luma plane at (x, y), clamping at the edge.
func bilinear(l []float64, w, h int, x, y float64) float64 {
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
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	x1, y1 := min(w-1, x0+1), min(h-1, y0+1)
	fx, fy := x-float64(x0), y-float64(y0)
	a := l[y0*w+x0]*(1-fx) + l[y0*w+x1]*fx
	b := l[y1*w+x0]*(1-fx) + l[y1*w+x1]*fx
	return a*(1-fy) + b*fy
}

func profile(l []float64, w, h int, x, y, nx, ny float64) []float64 {
	n := edgeSamples()
	p := make([]float64, n)
	for i := 0; i < n; i++ {
		t := edgeT0 + float64(i)*edgeStep
		p[i] = bilinear(l, w, h, x+nx*t, y+ny*t)
	}
	return p
}

// normProfile puts a profile on 0..1 between its two plateaus and smooths it
// lightly. It returns nil if the step is too small to measure.
func normProfile(p []float64) []float64 {
	low := (p[0] + p[1] + p[2]) / 3
	high := (p[len(p)-1] + p[len(p)-2] + p[len(p)-3]) / 3
	step := high - low
	if math.Abs(step) < edgeMinA {
		return nil
	}
	np := make([]float64, len(p))
	for i := range p {
		np[i] = (p[i] - low) / step
	}
	for pass := 0; pass < 2; pass++ {
		q := make([]float64, len(np))
		for i := range np {
			a := np[max(0, i-1)]
			b := np[i]
			c := np[min(len(np)-1, i+1)]
			q[i] = 0.25*a + 0.5*b + 0.25*c
		}
		np = q
	}
	return np
}

// interpAt samples np at fractional sample index u.
func interpAt(np []float64, u float64) float64 {
	if u <= 0 {
		return np[0]
	}
	if u >= float64(len(np)-1) {
		return np[len(np)-1]
	}
	i := int(math.Floor(u))
	f := u - float64(i)
	return np[i]*(1-f) + np[i+1]*f
}

// bestShift is the sub-sample shift that minimises the mean absolute
// difference between po shifted onto ps and ps. It is a local 1-D
// registration: a lens-correction displacement shows up here, and aligning on
// the steepest sample alone is too noisy on real edges (it made the aligned
// residual worse than the unaligned one on the 60 Quattro photos).
func bestShift(po, ps []float64) float64 {
	errAt := func(s float64) float64 {
		var e float64
		for j := range ps {
			e += math.Abs(interpAt(po, float64(j)-s/edgeStep) - ps[j])
		}
		return e
	}
	bestS, bestE := 0.0, math.Inf(1)
	for s := -2.0; s <= 2.0001; s += 0.5 { // coarse, then 0.1 px
		if e := errAt(s); e < bestE {
			bestE, bestS = e, s
		}
	}
	for s := bestS - 0.5; s <= bestS+0.5+1e-9; s += 0.1 {
		if e := errAt(s); e < bestE {
			bestE, bestS = e, s
		}
	}
	return bestS
}

// edgeProfileStats measures one image pair. Both images must be the same
// frame. Edge candidates are the SPP image's high-gradient pixels; each
// image's profile is normalised by its own plateaus and registered onto the
// other by the shift that minimises their profile difference, so a
// lens-correction displacement does not count as blur.
func edgeProfileStats(ours, spp []float32, w, h int) (o, s edgeStat) {
	lo := t90Lumas(ours)
	ls := t90Lumas(spp)
	mag, gx, gy := t90Sobel(ls, w, h)
	n := edgeSamples()
	accO, accS := make([]float64, n), make([]float64, n)
	var cnt int
	var shiftSum, unSum, alSum float64
	const border = 8
	for y := border; y < h-border; y++ {
		for x := border; x < w-border; x++ {
			i := y*w + x
			m := mag[i]
			if m < edgeMinG {
				continue
			}
			// One-pixel thinning: a 3x3 maximum.
			if m < mag[i-1] || m < mag[i+1] || m < mag[i-w] || m < mag[i+w] ||
				m < mag[i-w-1] || m < mag[i-w+1] || m < mag[i+w-1] || m < mag[i+w+1] {
				continue
			}
			nx, ny := gx[i]/m, gy[i]/m
			po := normProfile(profile(lo, w, h, float64(x), float64(y), nx, ny))
			if po == nil {
				continue
			}
			ps := normProfile(profile(ls, w, h, float64(x), float64(y), nx, ny))
			if ps == nil {
				continue
			}
			// Register ours onto SPP's profile.
			shift := bestShift(po, ps)
			al := make([]float64, n)
			var un, ali float64
			for j := 0; j < n; j++ {
				u := float64(j) - shift/edgeStep
				al[j] = interpAt(po, u)
				accO[j] += al[j]
				accS[j] += ps[j]
				un += math.Abs(po[j] - ps[j])
				ali += math.Abs(al[j] - ps[j])
			}
			shiftSum += math.Abs(shift)
			unSum += un / float64(n)
			alSum += ali / float64(n)
			cnt++
		}
	}
	if cnt == 0 {
		return edgeStat{}, edgeStat{}
	}
	meanO, meanS := make([]float64, n), make([]float64, n)
	for j := 0; j < n; j++ {
		meanO[j] = accO[j] / float64(cnt)
		meanS[j] = accS[j] / float64(cnt)
	}
	o, s = edgeStat{N: cnt}, edgeStat{N: cnt}
	o.Width, o.Overshoot, o.Halo, o.Slope = profileNumbers(meanO)
	s.Width, s.Overshoot, s.Halo, s.Slope = profileNumbers(meanS)
	o.Shift = shiftSum / float64(cnt)
	o.ProfileUnaligned = unSum / float64(cnt)
	o.ProfileAligned = alSum / float64(cnt)
	return o, s
}

// profileNumbers reads width, overshoot, halo and slope from a mean normalised
// profile (plateaus 0 and 1, bright side on the right).
func profileNumbers(p []float64) (width, overshoot, halo, slope float64) {
	t := func(i float64) float64 { return edgeT0 + i*edgeStep }
	// 10% and 90% crossings, interpolated between samples.
	cross := func(level float64) float64 {
		for i := 1; i < len(p); i++ {
			if p[i] >= level && p[i-1] < level {
				d := p[i] - p[i-1]
				f := 0.0
				if d > 0 {
					f = (level - p[i-1]) / d
				}
				return t(float64(i-1) + f)
			}
		}
		return math.NaN()
	}
	t10, t90 := cross(0.1), cross(0.9)
	if !math.IsNaN(t10) && !math.IsNaN(t90) {
		width = t90 - t10
	} else {
		width = math.NaN()
	}
	over, under := 0.0, 0.0
	for _, v := range p {
		over = math.Max(over, v-1)
		under = math.Max(under, -v)
	}
	overshoot = (over + under) / 2
	best := 0.0
	for i := 0; i+1 < len(p); i++ {
		best = math.Max(best, math.Abs(p[i+1]-p[i])/edgeStep)
	}
	slope = best
	for _, v := range p {
		if v > 1.002 || v < -0.002 {
			halo += edgeStep
		}
	}
	return
}

// ---------- flat-patch noise ----------

// noiseStat is the median 16x16-block luma sigma on blocks that are flat in
// the SPP image, for both images, with the block count.
type noiseStat struct {
	Blocks int
	Ours   float64
	SPP    float64
}

// flatNoise measures luminance sigma in blocks whose SPP content has no
// gradient and little range. The SPP image chooses the blocks for both, so a
// noisy render cannot pick its own flat areas.
func flatNoise(ours, spp []float32, w, h int) noiseStat {
	lo := t90Lumas(ours)
	ls := t90Lumas(spp)
	grad := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			x0, x1 := max(0, x-1), min(w-1, x+1)
			y0, y1 := max(0, y-1), min(h-1, y+1)
			grad[y*w+x] = math.Abs(ls[y*w+x1]-ls[y*w+x0]) + math.Abs(ls[y1*w+x]-ls[y0*w+x])
		}
	}
	blockStd := func(l []float64, x0, y0 int) float64 {
		var sum, sum2 float64
		for y := y0; y < y0+16; y++ {
			for x := x0; x < x0+16; x++ {
				v := l[y*w+x]
				sum += v
				sum2 += v * v
			}
		}
		m := sum / 256
		v := sum2/256 - m*m
		if v < 0 {
			return 0
		}
		return math.Sqrt(v)
	}
	var bo, bs []float64
	const b = 16
	for y0 := 4; y0+b <= h-4; y0 += b {
		for x0 := 4; x0+b <= w-4; x0 += b {
			var maxG, minV, maxV float64
			maxG, minV, maxV = 0, math.Inf(1), math.Inf(-1)
			for y := y0; y < y0+b; y++ {
				for x := x0; x < x0+b; x++ {
					if g := grad[y*w+x]; g > maxG {
						maxG = g
					}
					v := ls[y*w+x]
					minV, maxV = math.Min(minV, v), math.Max(maxV, v)
				}
			}
			if maxG > 8 || maxV-minV > 14 || (minV+maxV)/2 < 6 || (minV+maxV)/2 > 249 {
				continue
			}
			bo = append(bo, blockStd(lo, x0, y0))
			bs = append(bs, blockStd(ls, x0, y0))
		}
	}
	if len(bo) == 0 {
		return noiseStat{}
	}
	return noiseStat{Blocks: len(bo), Ours: medianF(bo), SPP: medianF(bs)}
}

func medianF(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// ---------- crops ----------

// bestEdgeWindow finds the win×win window with the most Sobel energy.
func bestEdgeWindow(spp []float32, w, h, win, step int) (int, int) {
	ls := t90Lumas(spp)
	mag, _, _ := t90Sobel(ls, w, h)
	iw := w + 1
	ii := make([]float64, iw*(h+1))
	for y := 0; y < h; y++ {
		var row float64
		for x := 0; x < w; x++ {
			row += mag[y*w+x]
			ii[(y+1)*iw+x+1] = ii[y*iw+x+1] + row
		}
	}
	sum := func(x0, y0 int) float64 {
		return ii[(y0+win)*iw+x0+win] - ii[y0*iw+x0+win] - ii[(y0+win)*iw+x0] + ii[y0*iw+x0]
	}
	bestX, bestY, best := 0, 0, -1.0
	for y := 0; y+win <= h; y += step {
		for x := 0; x+win <= w; x += step {
			if s := sum(x, y); s > best {
				best, bestX, bestY = s, x, y
			}
		}
	}
	// Keep the crop fully inside the frame.
	bestX = min(max(0, bestX), w-win)
	bestY = min(max(0, bestY), h-win)
	return bestX, bestY
}

// writeCropPNG writes the win×win crop of each sRGB panel with 4 px white
// separators, pixel for pixel (no scaling).
func writeCropPNG(path string, panels [][]float32, w, h, x0, y0, win int) error {
	const gap = 4
	if len(panels) == 0 {
		return nil
	}
	outW := len(panels)*win + (len(panels)-1)*gap
	img := image.NewRGBA(image.Rect(0, 0, outW, win))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	to8 := func(v float32) uint8 {
		return uint8(math.Max(0, math.Min(255, float64(v))) + 0.5)
	}
	for p, panel := range panels {
		ox := p * (win + gap)
		for y := 0; y < win; y++ {
			for x := 0; x < win; x++ {
				i := ((y0+y)*w + x0 + x) * 3
				img.SetRGBA(ox+x, y, color.RGBA{to8(panel[i]), to8(panel[i+1]), to8(panel[i+2]), 0xff})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
