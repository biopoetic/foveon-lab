package render

// SPP's Monochrome Film Grain (task 50), kept apart from the film library's
// grain in film.go. SPP's grain is one layer (the same value in R, G and B),
// independent of ISO, and its strength follows the encoded brightness as a
// random-dot coverage's: strongest at mid-grey, about half at black and
// white. Its Size control is mostly strength (SD 1.7 at .1, 12 at .5, 31 at
// 1 on the 01 crop), and Roughness adds high frequencies and some strength.
// The measured exports also carry a small positive mean at large sizes
// (+4.2 levels at Size 1), which this model adds with the same tone weight.
//
// The field is the same value-noise lattice and Gaussian the film grain uses
// (grain.go), so web/gpu.js draws exactly the same numbers. One coarse
// octave sets the size; a per-pixel octave is the roughness.
//
// At a reduced view (task 81) the grain is held out of the tone pass and laid
// on after SPP's sharpening and the Sharpness slider, like the film grain
// (task 58): the export's grain was sharpened at 1:1 and then reduced, so the
// view draws the variance that reduction keeps (grainBoxRatio, grainFineFactor)
// with the sharpening gain it retains. The 1:1 path is the old code exactly.

import (
	"image"
	"math"

	"github.com/biopoetic/foveon-lab/preset"
)

// monoGrain is one render's SPP monochrome grain. sigma and mean are in
// 8-bit sRGB levels at the tone curve's peak; q is the tone exponent.
type monoGrain struct {
	sigma, mean, q float32
	oct            []grainOctave
	fine           float32
	seed           uint32
	noise          []float32
	// deferred: at a reduced view the grain is laid on after sharpening,
	// with gain (the export's retained sharpening gain), not in the tone pass.
	deferred bool
	gain     float32
	// toning is SPP's constant per-channel 8-bit offset, so the deferred
	// grain can recover the grey it is weighted on after the toning was
	// applied (zero for B&W).
	toning [3]float32
}

// monoGrainParams are the numbers both the CPU field and the GPU recipe are
// built from.
type monoGrainParams struct {
	size, rough float64
	oct         []grainOctave // spatial octaves at this view (cell in pixels)
	fine        float64       // variance share of the per-pixel octave
	sigma, mean float32       // 8-bit levels at the view's scale
	q           float32
	seed        uint32
	n           float64 // view pitch over the export's (1 at full resolution)
	deferred    bool
	gain        float64
}

func monoGrainSeed(p preset.Params) uint32 {
	h := uint32(0x50a17b3d)
	h ^= uint32(p.MonoGrainSize*1000) * 0x9e3779b1
	h ^= uint32(p.MonoGrainRough*1000) * 0x85ebca6b
	return hash32(h)
}

// monoGrainN is the view pitch over the full-resolution export's: 1 at 1:1,
// 2.94 for a 1600 px view of a 4704 px frame, 7.84 for 600 px. It uses the
// native frame, so a fit view decoded from a binned raw still knows the
// export's size (the same reference film.go fullPitch uses).
func (b *Base) monoGrainN() float64 {
	if b.NativeFrame <= 0 {
		return 1
	}
	w := float64(max(b.W, b.H))
	if w <= 0 {
		return 1
	}
	n := float64(b.NativeFrame) / w
	if n < 1 {
		return 1
	}
	return n
}

// monoGrainBase is the 1:1 model: the measured peak SD and mean, the coarse
// correlation length c1 in full-resolution pixels, and the roughness
// variance share f.
func (b *Base) monoGrainBase(p preset.Params) (*monoGrainParams, float64, float64) {
	size := math.Max(0, math.Min(1, p.MonoGrainSize))
	rough := math.Max(0, math.Min(1, p.MonoGrainRough))
	if size <= 0 {
		return nil, 0, 0
	}
	// A(size): the peak SD, interpolating the measured .1/.5/1 points
	// (2.72, 11.97, 31.14 at Roughness .5); B the measured roughness gain,
	// whose slope is steeper on the finer sizes.
	a := 16.9*size*size + 13.0*size + 1.25
	bb := 1 + (rough-0.5)*(1.0-0.7*size)
	sd := a * bb
	fine := math.Max(0, math.Min(0.5, 0.45*rough-0.05))
	c1 := 1.75 + 0.25*size
	gp := &monoGrainParams{
		size: size, rough: rough,
		oct:   []grainOctave{{cell: c1, amp: float32(math.Sqrt(1 - fine))}},
		fine:  fine,
		sigma: float32(sd), mean: float32(0.16*a*bb - 0.95),
		q:    monoGrainQ,
		seed: monoGrainSeed(p),
		n:    1,
	}
	return gp, sd, c1
}

// monoGrainFor computes the model for this Base's view scale. The old path:
// it is the 1:1 code exactly and the unsharpened reduced-view path (a quick
// preview while a slider is dragged). A sharpened reduced view uses
// monoGrainDeferredFor instead.
func (b *Base) monoGrainFor(p preset.Params) *monoGrainParams {
	base, sd, _ := b.monoGrainBase(p)
	if base == nil {
		return nil
	}
	sc := b.scale()
	if sc <= 0 {
		sc = 1
	}
	gp := *base
	gp.sigma = float32(sd * sc)
	gp.mean = float32((0.16*sd - 0.95) * sc)
	gp.oct = make([]grainOctave, len(base.oct))
	for i, o := range base.oct {
		gp.oct[i] = grainOctave{cell: math.Max(0.5, o.cell*sc), amp: o.amp}
	}
	// Our grain is added before Render's SPP sharpening; SPP's exported grain
	// has already been sharpened. Size the model so the sharpened picture
	// carries the measured SD.
	if g := b.monoGrainSharpGain(&gp); g > 1 {
		gp.sigma /= float32(g)
	}
	return &gp
}

// monoGrainDeferredFor is the reduced-view model (task 81). The export's
// grain, sharpened at 1:1 and area-reduced to this view, keeps:
//
//	V = (1-f)·R(c1,n) + f·F(n)
//
// of the 1:1 field's variance (R the discrete box-reduction ratio for the
// coarse lattice, F the per-pixel white factor) and the sharpening gain
// G = sppGrainGain(1/n) × sliderGrainGain(n, Sharpness). The view draws a
// unit field with that variance at the view's scale, is laid on after SPP and
// the slider, and is scaled by G.  The clump is below one view pixel
// (c1/n < 1 at every view width), so it is drawn as white noise with a small
// one-pixel correlated share λ that keeps the reduced export's correlation
// width; λ is the coarse variance's share and is fitted from the 1:1 reduced
// field (the same small share task 58 fitted for the film grain's sub-pixel
// clumps, but smaller here because SPP's mono clump is only 1–2 px at 1:1).
func (b *Base) monoGrainDeferredFor(p preset.Params) *monoGrainParams {
	base, sd, _ := b.monoGrainBase(p)
	if base == nil {
		return nil
	}
	n := b.monoGrainN()
	if n <= 1.0000001 {
		return b.monoGrainFor(p)
	}
	varRed, varOut, rho1 := b.monoGrainRetained(base, n, p.Sharpness)
	if varRed <= 0 || varOut <= 0 {
		return nil
	}
	// SPP's mask gain at 1:1 on this field (the pre-slider stage the 1:1
	// model divides by). This is the same 192-px estimate the 1:1 model uses;
	// the 256-px reduced-field simulation would estimate it differently at the
	// coarse Size 1 and move the 1:1-relative variance by 12%.
	g1 := b.monoGrainSharpGainAt(base, 1)
	if g1 < 1 {
		g1 = 1
	}
	// The deferred field is drawn with the reduced unsharpened variance and
	// then scaled by the retained sharpening gain; the 1:1 pre-sharpen field
	// is sd/g1.
	gp := *base
	gp.n = n
	gp.deferred = true
	gp.sigma = float32(sd / g1 * math.Sqrt(varRed))
	gp.gain = math.Sqrt(varOut / varRed)
	// The sharpened, reduced field's correlation: white plus a one-pixel
	// value noise, whose lag-1 correlation is 0.5. This keeps the clump
	// width the export's reduction keeps (lambda is near 0 at a strong
	// Sharpness, which whitens the grain).
	lambda := math.Max(0, math.Min(0.9, 2*rho1))
	gp.fine = 1 - lambda
	gp.oct = nil
	if lambda > 0 {
		// grainSample at cell 1 keeps 0.75² of a unit lattice, so the
		// amplitude carries the 1/0.75.
		gp.oct = []grainOctave{{cell: 1, amp: float32(math.Sqrt(lambda) / 0.75)}}
	}
	return &gp
}

// monoGrainSharpGain is how much SPP's own sharpening (sppsharpen.go) raises
// this view's grain SD. SPP's exported grain has already been through it, and
// our grain is added before Render's sharpening, so the model's sigma is
// divided by this. A small field with the same octaves and fine share is run
// through the same 7-tap unsharp mask; the gain is a property of the noise,
// not of the picture.
func (b *Base) monoGrainSharpGain(gp *monoGrainParams) float64 {
	return b.monoGrainSharpGainAt(gp, b.scale())
}

func (b *Base) monoGrainSharpGainAt(gp *monoGrainParams, scale float64) float64 {
	if !b.Raw {
		return 1
	}
	a, k := sppView(scale, b.ISO, b.Sensor)
	if a == 0 {
		return 1
	}
	const n = 192
	field := monoUnitField(n, gp)
	vin := monoVariance(field)
	if vin <= 0 {
		return 1
	}
	// Separable 7-tap blur (clamped edges), then the unsharp mask.
	out := monoUSMField(field, n, a, k)
	vout := monoVariance(out)
	if vout <= vin {
		return 1
	}
	return math.Sqrt(vout / vin)
}

// monoUnitField builds a unit-variance field with gp's octaves and fine
// share: the same lattice and Gaussian as the real field, on an n×n patch.
func monoUnitField(n int, gp *monoGrainParams) []float32 {
	field := make([]float32, n*n)
	for j, o := range gp.oct {
		lw := int(math.Ceil(float64(n)/o.cell)) + 3
		lat := grainLattice(lw, lw, gp.seed^0x1234567^uint32(j)*0x9e3779b9)
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				field[y*n+x] += o.amp * grainSample(lat, lw, x, y, o.cell)
			}
		}
	}
	if gp.fine > 0 {
		fine := float32(math.Sqrt(gp.fine))
		for i := range field {
			field[i] += fine * grainGauss(i, gp.seed^0x1234567)
		}
	}
	return field
}

// monoUSMField applies SPP's separable 7-tap unsharp mask to an n×n field.
func monoUSMField(field []float32, n int, a float32, k [7]float32) []float32 {
	tmp := make([]float32, n*n)
	blur := make([]float32, n*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var v float32
			for j := -3; j <= 3; j++ {
				v += k[j+3] * field[y*n+min(n-1, max(0, x+j))]
			}
			tmp[y*n+x] = v
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var v float32
			for j := -3; j <= 3; j++ {
				v += k[j+3] * tmp[min(n-1, max(0, y+j))*n+x]
			}
			blur[y*n+x] = v
		}
	}
	out := make([]float32, len(field))
	for i, v := range field {
		out[i] = v + a*(v-blur[i])
	}
	return out
}

func monoVariance(v []float32) float64 {
	var s, s2 float64
	for _, x := range v {
		s += float64(x)
		s2 += float64(x) * float64(x)
	}
	n := float64(len(v))
	return s2/n - (s/n)*(s/n)
}

// monoReduce area-reduces an n×n field to dw×dh (boxTaps, the app's own
// fractional box) and returns the reduced field.
func monoReduce(src []float32, n, dw, dh int) []float32 {
	tx, ty := boxTaps(n, dw), boxTaps(n, dh)
	tmp := make([]float32, dw*n)
	for y := 0; y < n; y++ {
		row, out := src[y*n:], tmp[y*dw:]
		for x, ts := range tx {
			var v float32
			for _, t := range ts {
				v += row[t.i] * t.w
			}
			out[x] = v
		}
	}
	dst := make([]float32, dw*dh)
	for y, ts := range ty {
		out := dst[y*dw : (y+1)*dw]
		for _, t := range ts {
			row := tmp[t.i*dw : (t.i+1)*dw]
			for i, v := range row {
				out[i] += v * t.w
			}
		}
	}
	return dst
}

// monoLag1 is the lag-1 autocorrelation (both axes, mean removed) of a
// dw×dh field.
func monoLag1(v []float32, dw, dh int) float64 {
	var mean float64
	for _, x := range v {
		mean += float64(x)
	}
	mean /= float64(len(v))
	var num, den float64
	for y := 1; y < dh; y++ {
		for x := 1; x < dw; x++ {
			i := y*dw + x
			a := float64(v[i]) - mean
			for _, j := range [2]int{i - 1, i - dw} {
				b := float64(v[j]) - mean
				num += a * b
				den += a * a
			}
		}
	}
	if den <= 0 {
		return 0
	}
	return num / den
}

// monoGrainRetained is how the export's 1:1 mono field survives SPP's mask
// and the Sharpness slider at 1:1 and the area reduction to n. A unit field
// with the same lattice and fine split is built, sharpened with the very
// masks RenderOpt uses, box-reduced, and measured: varRed is the reduced
// unsharpened field's variance, varOut the reduced sharpened field's, and
// rho1 the reduced field's lag-1 correlation. This is grainBoxRatio/grainFineFactor
// derived for the mono lattice, with the two sharpeners' interaction kept
// instead of approximating them as white-grain gains (which overestimated
// the retained gain at Sharpness +1 by 20–30%).
func (b *Base) monoGrainRetained(gp *monoGrainParams, n, sharpness float64) (varRed, varOut, rho1 float64) {
	const N = 256
	dw := max(1, int(math.Round(float64(N)/n)))
	raw := monoUnitField(N, gp)
	varRed = monoVariance(monoReduce(raw, N, dw, dw))
	out := raw
	if b.Raw {
		if a, k := sppView(1, b.ISO, b.Sensor); a != 0 {
			out = monoUSMField(raw, N, a, k)
		}
	}
	if sharpness != 0 {
		av := float32(math.Max(-1, math.Min(2, sharpness*1.5)))
		v := append([]float32(nil), out...)
		for y := 1; y < N-1; y++ {
			for x := 1; x < N-1; x++ {
				i := y*N + x
				nb := (out[i-1] + out[i+1] + out[i-N] + out[i+N]) / 4
				v[i] = out[i] + av*(out[i]-nb)
			}
		}
		out = v
	}
	red := monoReduce(out, N, dw, dw)
	varOut = monoVariance(red)
	rho1 = monoLag1(red, dw, dw)
	return
}

// newMonoGrain builds the field; on false (the GPU path) only the numbers are
// needed. It returns nil when the section or the control is off.
func (b *Base) newMonoGrain(p preset.Params, on bool) *monoGrain {
	if !on {
		return nil
	}
	return b.newMonoGrainFrom(b.monoGrainFor(p))
}

// newMonoGrainDeferred builds the reduced view's grain field (task 81).
func (b *Base) newMonoGrainDeferred(p preset.Params) *monoGrain {
	g := b.newMonoGrainFrom(b.monoGrainDeferredFor(p))
	if g != nil {
		if v, ok := preset.MonoToneOffset(p.MonoTone); ok {
			for i := range v {
				g.toning[i] = float32(v[i] / 255)
			}
		}
	}
	return g
}

func (b *Base) newMonoGrainFrom(gp *monoGrainParams) *monoGrain {
	if gp == nil {
		return nil
	}
	g := &monoGrain{sigma: gp.sigma, mean: gp.mean, q: gp.q, seed: gp.seed,
		oct: gp.oct, fine: float32(math.Sqrt(math.Max(0, gp.fine))),
		deferred: gp.deferred, gain: float32(gp.gain)}
	w, h := b.W, b.H
	g.noise = make([]float32, w*h)
	for j, o := range gp.oct {
		lw := int(math.Ceil(float64(w)/o.cell)) + 3
		lh := int(math.Ceil(float64(h)/o.cell)) + 3
		lat := grainLattice(lw, lh, gp.seed^0x1234567^uint32(j)*0x9e3779b9)
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					g.noise[y*w+x] += o.amp * grainSample(lat, lw, x, y, o.cell)
				}
			}
		})
	}
	if g.fine > 0 {
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					g.noise[i] += g.fine * grainGauss(i, gp.seed^0x1234567)
				}
			}
		})
	}
	return g
}

// monoGrainQ is the tone weight's exponent, fitted to the exports: the
// measured grain SD is flatter across the tones than 2√(u(1-u)).
const monoGrainQ = 0.45

// monoGrainTone is SPP's tone weight (4u(1-u))^q at the model's fixed q,
// sampled once; the per-pixel path is a table lookup rather than a pow.
var monoGrainTone = func() [1025]float32 {
	var t [1025]float32
	for i := range t {
		u := float64(i) / 1024
		t[i] = float32(math.Pow(math.Max(0, 4*u*(1-u)), monoGrainQ))
	}
	return t
}()

func monoGrainWeight(u float32) float32 {
	if u <= 0 || u >= 1 {
		return 0
	}
	f := u * 1024
	i := int(f)
	if i >= 1024 {
		i = 1023
	}
	fr := f - float32(i)
	return monoGrainTone[i] + fr*(monoGrainTone[i+1]-monoGrainTone[i])
}

// apply adds one sample, in the encoded sRGB domain. SPP's grain is one
// layer: the same n goes to all three channels, which mono has already made
// equal, so one tone weight is enough.
func (g *monoGrain) apply(i int, r, gg, bl float32) (float32, float32, float32) {
	if g == nil || g.noise == nil {
		return r, gg, bl
	}
	n := g.noise[i]
	u := float64(enc(r))
	u += float64((g.mean + g.sigma*n) / 255 * monoGrainWeight(float32(u)))
	u = math.Max(0, math.Min(1, u))
	v := dec(float32(u))
	return v, v, v
}

// add8 lays the deferred grain on the sharpened 8-bit image (task 81). The
// grain is applied to the encoded value, as apply does before Render's
// encoding, and the same delta goes to all three channels so SPP's toning
// (already applied) is preserved.
func (g *monoGrain) add8(img *image.RGBA, gain float64) {
	if g == nil || len(g.noise) == 0 || !g.deferred {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	gg := float32(gain)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				o := i * 4
				u := float32(img.Pix[o])/255 - g.toning[0]
				wt := monoGrainWeight(u)
				d := (g.mean + g.sigma*g.noise[i]*gg) / 255 * wt
				for c := 0; c < 3; c++ {
					v := float32(img.Pix[o+c])/255 + d
					img.Pix[o+c] = uint8(max(0, min(255, v*255+0.5)))
				}
			}
		}
	})
}

// add16 is add8 for 16-bit sRGB (big-endian samples).
func (g *monoGrain) add16(img *image.RGBA64, gain float64) {
	if g == nil || len(g.noise) == 0 || !g.deferred {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	gg := float32(gain)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				o := i * 8
				u0 := uint16(img.Pix[o])<<8 | uint16(img.Pix[o+1])
				u := float32(u0)/65535 - g.toning[0]
				wt := monoGrainWeight(u)
				d := (g.mean + g.sigma*g.noise[i]*gg) / 255 * wt
				for c := 0; c < 3; c++ {
					p := o + c*2
					v := uint16(img.Pix[p])<<8 | uint16(img.Pix[p+1])
					e := float32(v)/65535 + d
					e = max(0, min(1, e))
					nv := uint16(e*65535 + 0.5)
					img.Pix[p], img.Pix[p+1] = uint8(nv>>8), uint8(nv)
				}
			}
		}
	})
}
