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

import (
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
}

// monoGrainParams are the numbers both the CPU field and the GPU recipe are
// built from.
type monoGrainParams struct {
	size, rough float64
	cell        float64 // coarse correlation length, output pixels
	fine        float64 // share of the variance in the per-pixel octave
	sigma, mean float32 // 8-bit levels
	q           float32
	seed        uint32
}

func monoGrainSeed(p preset.Params) uint32 {
	h := uint32(0x50a17b3d)
	h ^= uint32(p.MonoGrainSize*1000) * 0x9e3779b1
	h ^= uint32(p.MonoGrainRough*1000) * 0x85ebca6b
	return hash32(h)
}

// monoGrainFor computes the model for this Base's view scale. SPP's grain is
// per full-resolution pixel; a reduced view covers more than one, so the
// amplitude falls with the linear scale (four pixels average to half the SD)
// and the clumps shrink with it.
func (b *Base) monoGrainFor(p preset.Params) *monoGrainParams {
	size := math.Max(0, math.Min(1, p.MonoGrainSize))
	rough := math.Max(0, math.Min(1, p.MonoGrainRough))
	if size <= 0 {
		return nil
	}
	sc := b.scale()
	if sc <= 0 {
		sc = 1
	}
	// A(size): the peak SD, interpolating the measured .1/.5/1 points
	// (2.72, 11.97, 31.14 at Roughness .5); B the measured roughness gain,
	// whose slope is steeper on the finer sizes.
	a := 16.9*size*size + 13.0*size + 1.25
	bb := 1 + (rough-0.5)*(1.0-0.7*size)
	sigma := float32(a * bb * sc)
	mean := float32((0.16*a*bb - 0.95) * sc)
	fine := math.Max(0, math.Min(0.5, 0.45*rough-0.05))
	gp := &monoGrainParams{
		size: size, rough: rough,
		cell:  math.Max(0.5, (1.75+0.25*size)*sc),
		fine:  fine,
		sigma: sigma, mean: mean,
		q:    monoGrainQ,
		seed: monoGrainSeed(p),
	}
	// Our grain is added before Render's SPP sharpening; SPP's exported grain
	// has already been sharpened. Size the model so the sharpened picture
	// carries the measured SD.
	if g := b.monoGrainSharpGain(gp); g > 1 {
		gp.sigma /= float32(g)
	}
	return gp
}

// monoGrainSharpGain is how much SPP's own sharpening (sppsharpen.go) raises
// this view's grain SD. SPP's exported grain has already been through it, and
// our grain is added before Render's sharpening, so the model's sigma is
// divided by this. A small field with the same cell and fine share is run
// through the same 7-tap unsharp mask; the gain is a property of the noise,
// not of the picture.
func (b *Base) monoGrainSharpGain(gp *monoGrainParams) float64 {
	if !b.Raw {
		return 1
	}
	a, k := sppView(b.scale(), b.ISO, b.Sensor)
	if a == 0 {
		return 1
	}
	const n = 192
	lw := int(math.Ceil(float64(n)/gp.cell)) + 3
	lat := grainLattice(lw, lw, gp.seed^0x1234567)
	field := make([]float32, n*n)
	amp := math.Sqrt(1 - gp.fine)
	fine := math.Sqrt(gp.fine)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			i := y*n + x
			field[i] = float32(amp) * grainSample(lat, lw, x, y, gp.cell)
			if fine > 0 {
				field[i] += float32(fine) * grainGauss(i, gp.seed^0x1234567)
			}
		}
	}
	var s, s2 float64
	for _, v := range field {
		s += float64(v)
		s2 += float64(v) * float64(v)
	}
	mean := s / float64(n*n)
	vin := s2/float64(n*n) - mean*mean
	if vin <= 0 {
		return 1
	}
	// Separable 7-tap blur (clamped edges), then the unsharp mask.
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
	var o2, om float64
	for i, v := range field {
		d := v + a*(v-blur[i])
		o2 += float64(d) * float64(d)
		om += float64(d)
	}
	om /= float64(n * n)
	vout := o2/float64(n*n) - om*om
	if vout <= vin {
		return 1
	}
	return math.Sqrt(vout / vin)
}

// newMonoGrain builds the field; on false (the GPU path) only the numbers are
// needed. It returns nil when the section or the control is off.
func (b *Base) newMonoGrain(p preset.Params, on bool) *monoGrain {
	if !on {
		return nil
	}
	gp := b.monoGrainFor(p)
	if gp == nil {
		return nil
	}
	g := &monoGrain{sigma: gp.sigma, mean: gp.mean, q: gp.q, seed: gp.seed}
	amp := math.Sqrt(1 - gp.fine)
	g.oct = []grainOctave{{cell: gp.cell, amp: float32(amp)}}
	g.fine = float32(math.Sqrt(gp.fine))
	w, h := b.W, b.H
	g.noise = make([]float32, w*h)
	// Coarse value noise.
	lw := int(math.Ceil(float64(w)/gp.cell)) + 3
	lh := int(math.Ceil(float64(h)/gp.cell)) + 3
	lat := grainLattice(lw, lh, gp.seed^0x1234567)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				g.noise[y*w+x] = float32(amp) * grainSample(lat, lw, x, y, gp.cell)
			}
		}
	})
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
