package render

// Film grain and halation, from the physics of the film.
//
// Grain: an emulsion is a random scatter of developed silver grains (dye
// clouds in colour film). Its density fluctuation measured through an
// aperture of area a falls as 1/√a (Selwyn's law: σ·√a is constant), and
// film makers publish it as "diffuse RMS granularity" G = 1000·σ_D through
// a 48 µm aperture. So a pixel covering p×p µm of the film has a density
// noise σ = G/1000 · √(π·24²) / p: the same film looks finer on a reduced
// view and coarser on a crop, as on a real enlargement. The film frame's
// long side (FilmWidth, 36 mm for 35 mm film) sets p. The grain shows in
// the mid-tones and vanishes at full black and white (a Boolean grain
// model's coverage variance goes as u(1-u)); colour film has three dye
// layers, each grainy on its own.
//
// Halation: light that crosses the emulsion reflects off the back of the
// film base (~125 µm thick; total internal reflection beyond ~42° in a base
// of index 1.5) and comes back into the emulsion around where it entered,
// into the red-sensitive layer, nearest the base, and little into the
// green: a red-orange glow around bright lights, ~0.2 mm wide on the film
// (Cinestill-like with no anti-halation layer). It is linear in the light:
// every pixel spreads, but only a bright source on a dark ground shows it.

import (
	"math"

	"github.com/biopoetic/foveon-lab/preset"
)

const (
	selwynArea = math.Pi * 24 * 24 // µm², the 48 µm granularity aperture
	minPitch   = 4.0               // µm: below a grain clump, Selwyn's law no longer holds
	// haloSigma is the halo's spread on the film (µm): the base's double
	// pass (2 × 125 µm) at the angles that reflect most (~40°), halved
	// for a Gaussian's sigma.
	haloSigma = 105.0
	// haloMax: the share of the light returned with no anti-halation
	// layer (Halation = 1): what the emulsion scatters past the base's
	// critical angle and the red layer then catches, ~10%. 0.25 laid a red
	// veil over a whole bright orange surface (an SD1 kitchen frame).
	haloMax = 0.12
)

type filmOp struct {
	sigma  float32 // density noise per pixel at this resolution (0 = none)
	mono   bool
	halo   []float32 // the spread light, nil = none
	haloK  float32
	seed   uint64
	stride int
}

// pitch is the film width (µm) one pixel of b covers.
func (b *Base) pitch(p preset.Params) float64 {
	fw := p.FilmWidth
	if fw <= 0 {
		fw = 36
	}
	return math.Max(minPitch, fw*1000/(b.frame()*b.scale()))
}

func (b *Base) newFilm(p preset.Params, mono bool) *filmOp {
	if p.Grain <= 0 && p.Halation <= 0 {
		return nil
	}
	f := &filmOp{mono: mono, seed: 0x9e3779b97f4a7c15, stride: b.W}
	pt := b.pitch(p)
	if p.Grain > 0 {
		f.sigma = float32(p.Grain / 1000 * math.Sqrt(selwynArea) / pt)
	}
	if p.Halation > 0 {
		f.halo = b.haloLight(max(1, int(math.Round(haloSigma/pt))))
		f.haloK = float32(math.Min(1, p.Halation) * haloMax)
	}
	return f
}

// haloLight is the light reaching the film base (the red layer's, a little
// of the green's), spread by a Gaussian of sigma r pixels (three box
// passes); cached per radius on the base.
func (b *Base) haloLight(r int) []float32 {
	b.filmMu.Lock()
	defer b.filmMu.Unlock()
	if h, ok := b.halos[r]; ok {
		return h
	}
	n := b.W * b.H
	v := make([]float32, n)
	for i := range v {
		v[i] = b.Pix[i*3] + 0.3*b.Pix[i*3+1]
	}
	tmp := make([]float32, n)
	for k := 0; k < 3; k++ {
		boxBlur(v, tmp, b.W, b.H, r)
	}
	if b.halos == nil {
		b.halos = map[int][]float32{}
	}
	b.halos[r] = v
	return v
}

// addHalo adds the returned light to a scene-linear pixel.
func (f *filmOp) addHalo(i int, r, g, bl float32) (float32, float32, float32) {
	if f == nil || f.halo == nil {
		return r, g, bl
	}
	h := f.haloK * f.halo[i]
	return r + h, g + 0.2*h, bl
}

// gauss is a deterministic standard normal for (pixel, channel): the sum
// of four uniforms from one hash, rescaled (close enough for grain).
func (f *filmOp) gauss(i, c int) float32 {
	z := f.seed ^ uint64(i)*0xbf58476d1ce4e5b9 ^ uint64(c+1)*0x94d049bb133111eb
	z ^= z >> 31
	z *= 0x7fb5d329728ea185
	z ^= z >> 27
	z *= 0x81dadef4bc2dd44d
	z ^= z >> 33
	s := float32(z&0xffff) + float32(z>>16&0xffff) + float32(z>>32&0xffff) + float32(z>>48)
	return (s/65535 - 2) * 1.7320508 // variance of 4 uniforms is 1/3
}

// grain applies the density noise to an output-linear pixel.
func (f *filmOp) grain(i int, r, g, bl float32) (float32, float32, float32) {
	if f == nil || f.sigma == 0 {
		return r, g, bl
	}
	one := func(v float32, n float32) float32 {
		if v <= 0 {
			return v
		}
		u := min(1, enc(v))
		amp := 2 * float32(math.Sqrt(float64(u*(1-u))))
		return v * float32(math.Exp(-2.302585*float64(f.sigma*amp*n)))
	}
	if f.mono {
		n := f.gauss(i, 0)
		return one(r, n), one(g, n), one(bl, n)
	}
	return one(r, f.gauss(i, 0)), one(g, f.gauss(i, 1)), one(bl, f.gauss(i, 2))
}
