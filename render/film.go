package render

// Film grain and halation, from the physics of the film.
//
// Grain. An emulsion is a random scatter of developed silver grains (dye
// clouds in colour film). Through an aperture of area a the density
// fluctuation falls as 1/√a (Selwyn's law: σ·√a is constant), and makers
// publish it as "diffuse RMS granularity" G = 1000·σ_D through a 48 µm
// aperture. That single number fixes the total variance; it does not fix
// the sizes of the grains that carry it.
//
// The model is a Gaussian field matched to a Boolean model of grains: a
// Poisson scatter of developed grains of random radius, the model Newson,
// Delon and Galerne use for resolution-independent rendering (A Stochastic
// Film Grain Model for Resolution-Independent Rendering, Computer Graphics
// Forum 36(8), 2017) and Zhang, Wang, Tian and Pappas render analytically
// once the grains are below a pixel (Film Grain Rendering and Parameter
// Estimation, ACM TOG 42(4), 2023, art. 63). When many grains fall in one
// output pixel their sum is Gaussian; we synthesise that field directly as
// a sum of octaves, each a band of grain/clump sizes, and give each octave
// the variance that survives an aperture of that size. The distribution of
// sizes is log-normal around a median set by the film's speed (faster
// emulsions have larger grains and larger clumps), with a small coarse
// clump population; the octave amplitudes are normalised so the 48 µm
// aperture reproduces the published G exactly.
//
// The aperture factor for one value-noise octave of correlation length s
// through a square aperture L is F(s,L) = 2.25·(s/L)²·(1−s/3L)² for s<L
// and 1 above; it is the exact variance of a box average of a bilinear
// value-noise field. A pixel covering p µm of film therefore gets
// σ² = Σ a_j²·F(s_j,p), which is Selwyn's law for fine grains and flattens
// when a clump is larger than the pixel — so clumps survive a reduced view
// where per-pixel noise alone would have averaged away. The clump sizes are
// laid on the film's own frame (FilmWidth, 36 mm for 35 mm film), so a crop
// and a reduced view keep the film's sizes rather than the pixel's.
//
// Tone. A developed grain is a black-and-white event; the density
// fluctuation it makes is strongest in the mid-tones and vanishes at clear
// and fully exposed film, where a grain changes nothing (Bayer, Relation
// between granularity and density for a random-dot model, JOSA 54(12),
// 1964). We modulate the density noise by 2√(u(1−u)) on the encoded
// brightness u, the Boolean model's coverage variance.
//
// Colour. A colour film has three emulsion layers; each forms dye clouds
// around its own developed silver. The model gives each output channel its
// own dye-cloud field, with a small common component (rho: the print/scan
// shares some density between layers). A slide has finer, more independent
// clouds; a negative has coarser clouds and a stronger common component,
// which is why the two do not look alike. A black-and-white film is one
// silver layer, shared by all three channels.
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
	minPitch = 4.0 // µm: below a grain clump, Selwyn's law no longer holds
	// haloSigma is the halo's spread on the film (µm): the base's double
	// pass (2 × 125 µm) at the angles that reflect most (~40°), halved
	// for a Gaussian's sigma.
	haloSigma = 105.0
	// haloMax: the share of the light returned with no anti-halation
	// layer (Halation = 1): what the emulsion scatters past the base's
	// critical angle and the red layer then catches, ~10%. 0.25 laid a red
	// veil over a whole bright orange surface (an SD1 kitchen frame).
	haloMax = 0.12

	// grainOctaves is the number of size bands the distribution is split
	// into. Nine bands span 0.4× to 16× the median grain diameter.
	grainOctaves = 9
	// grainSigmaLn: the geometric standard deviation of the grain-size
	// distribution (silver halide emulsions are approximately log-normal).
	grainSigmaLn = 0.85
	// grainClumpMin/Max: the share of the density variance carried by
	// clumps, centred on 8× the median grain diameter. A fast emulsion
	// clumps more; a slow one is a finer, more even scatter.
	grainClumpMin  = 0.02
	grainClumpMax  = 0.10
	grainClumpMed  = 8.0
	grainClumpLn   = 0.60
	grainApertureL = 48.0 // µm, the published-RMS aperture
)

type grainOctave struct {
	cell float64 // correlation length in output pixels (s/pt)
	amp  float32 // density-RMS contribution after the pixel aperture
}

type filmOp struct {
	// sigma is the density noise per pixel at this resolution (0 = none):
	// the RMS an aperture the size of the output pixel would measure.
	sigma float32
	// fine is the part of sigma carried by octaves finer than the pixel;
	// they are drawn as independent per-pixel noise. oct lists the octaves
	// at or above the pixel size, drawn as spatially correlated fields.
	fine float32
	oct  []grainOctave
	// rho: the share of each colour channel's variance that is a common
	// density component (0 = three independent layers).
	rho  float32
	mono bool
	// noise is the density noise per pixel: one value (mono) or three
	// (colour), already at the film's density amplitude.
	noise []float32
	halo  []float32 // the spread light, nil = none
	haloK float32
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
	return b.newFilmOp(p, mono, true)
}

// newFilmOp builds the grain parameters; buildNoise false is the GPU
// recipe's path, which only needs the octave list and the fine variance.
func (b *Base) newFilmOp(p preset.Params, mono, buildNoise bool) *filmOp {
	target := p.Grain / 1000 // RMS granularity -> density sigma
	film, hasFilm := FilmByKey(p.Film)
	if p.NoGrain {
		target = 0
	} else if target <= 0 && hasFilm {
		// Choosing a film turns its grain on. The slider still scales it
		// (p.Grain > 0 overrides this base value).
		target = film.Grain / 1000
	}
	if target <= 0 && p.Halation <= 0 {
		return nil
	}
	if hasFilm && film.BW {
		mono = true
	}
	f := &filmOp{mono: mono}
	if target > 0 {
		kind, iso := "cn", b.ISO
		if mono && !hasFilm {
			kind = "neg"
		}
		if hasFilm {
			kind, iso = film.Kind, float64(film.ISO)
			if mono && kind == "cn" {
				kind = "neg"
			}
		}
		if iso <= 0 {
			iso = 400
		}
		if iso > 6400 {
			iso = 6400
		}
		push := math.Max(0, p.Push)
		// A pushed film is underexposed and overdeveloped: its grains grow
		// and there are fewer of them per unit density. This is an estimate
		// (makers publish no pushed granularity), monotonic in the push.
		target *= 1 + 0.45*push + 0.08*push*push
		f.rho = grainRho(kind, mono)
		f.buildGrain(b, p, kind, iso, push, target, buildNoise)
	}
	if p.Halation > 0 {
		f.halo = b.haloLight(max(1, int(math.Round(haloSigma/b.pitch(p)))))
		f.haloK = float32(math.Min(1, p.Halation) * haloMax)
	}
	return f
}

// grainSizes returns the size bands (µm) and their normalized variance
// fractions. The median follows the film speed; the coarse clump population
// is what keeps grain visible when the picture is reduced.
func grainSizes(kind string, iso, push float64) ([]float64, []float32) {
	iso = math.Max(25, math.Min(6400, iso))
	base, exp := 0.65, 0.30 // black-and-white silver
	switch kind {
	case "slide":
		base, exp = 0.85, 0.26 // finer, less clumped dye clouds
	case "cn":
		base, exp = 1.15, 0.28 // dye clouds are larger than the silver grains
	case "chromo":
		base, exp = 1.05, 0.28
	}
	d50 := base * math.Pow(iso/100, exp)
	d50 *= 1 + 0.10*push // an estimated growth with development
	d50 = math.Max(0.35, math.Min(3.2, d50))

	lo := 0.4 * d50
	hi := math.Min(64, 16*d50)
	if hi < lo*2 {
		hi = lo * 2
	}
	s := make([]float64, grainOctaves)
	w := make([]float64, grainOctaves)
	var sum float64
	for i := range s {
		s[i] = lo * math.Pow(hi/lo, float64(i)/float64(grainOctaves-1))
		z := (math.Log(s[i]) - math.Log(d50)) / grainSigmaLn
		w[i] = math.Exp(-0.5 * z * z)
		sum += w[i]
	}
	c := make([]float64, grainOctaves)
	var csum float64
	for i := range s {
		z := (math.Log(s[i]) - math.Log(grainClumpMed*d50)) / grainClumpLn
		c[i] = math.Exp(-0.5 * z * z)
		csum += c[i]
	}
	clump := grainClumpMin + (grainClumpMax-grainClumpMin)*math.Min(1, math.Max(0, (iso-50)/3150))
	out := make([]float32, grainOctaves)
	for i := range out {
		out[i] = float32((1-clump)*w[i]/sum + clump*c[i]/csum)
	}
	return s, out
}

// grainAperture is the fraction of a value-noise octave's variance that
// survives a square aperture L (the exact box average of a bilinear field).
func grainAperture(s, L float64) float64 {
	if s >= L {
		return 1
	}
	x := s / L
	t := 1 - x/3
	return 2.25 * x * x * t * t
}

// grainRho is the common share of the three colour layers' density noise.
func grainRho(kind string, mono bool) float32 {
	if mono {
		return 0
	}
	if kind == "slide" {
		return 0.10
	}
	return 0.30
}

// buildGrain sizes the field on the film and normalizes it so a 48 µm
// aperture measures the published (or estimated) G.
func (f *filmOp) buildGrain(b *Base, p preset.Params, kind string, iso, push, target float64, buildNoise bool) {
	pt := b.pitch(p)
	sizes, weights := grainSizes(kind, iso, push)
	var v48 float64
	for i := range sizes {
		v48 += float64(weights[i]) * grainAperture(sizes[i], grainApertureL)
	}
	if v48 <= 0 {
		return
	}
	A := target / math.Sqrt(v48)
	var sigma2, fine2 float64
	for i := range sizes {
		amp := A * math.Sqrt(float64(weights[i]))
		s := sizes[i]
		F := grainAperture(s, pt)
		sigma2 += amp * amp * F
		if s >= pt {
			// A clump at or above the pixel size is drawn as a spatial
			// field; its pixel value keeps the variance the pixel aperture
			// leaves, so the same calibration holds at every view size.
			f.oct = append(f.oct, grainOctave{cell: s / pt, amp: float32(amp * math.Sqrt(F))})
		} else {
			fine2 += amp * amp * F
		}
	}
	f.sigma = float32(math.Sqrt(sigma2))
	f.fine = float32(math.Sqrt(fine2))
	if buildNoise {
		f.buildNoise(b, p)
	}
}

func grainSeed(p preset.Params) uint32 {
	h := uint32(0x47a1c3e5)
	for i := 0; i < len(p.Film); i++ {
		h = h*31 + uint32(p.Film[i])
	}
	h ^= uint32(p.FilmWidth * 10)
	return hash32(h)
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

// gauss is a deterministic standard normal for (pixel, seed): the sum of
// four 16-bit uniforms from two 32-bit hashes (lowbias32), rescaled (close
// enough for grain). 32-bit so the GPU preview (web/gpu.js) draws exactly
// the same grain.
func grainGauss(i int, seed uint32) float32 {
	a := hash32(uint32(i)*0x9e3779b1 + seed)
	b := hash32(a ^ 0x85ebca6b)
	s := float32(a&0xffff) + float32(a>>16) + float32(b&0xffff) + float32(b>>16)
	return (s/65535 - 2) * 1.7320508 // variance of 4 uniforms is 1/3
}

// hash32 is lowbias32 (Chris Wellons): a 32-bit integer hash.
func hash32(x uint32) uint32 {
	x ^= x >> 16
	x *= 0x7feb352d
	x ^= x >> 15
	x *= 0x846ca68b
	x ^= x >> 16
	return x
}

// grainLattice is a value-noise lattice: one standard-normal-ish value per
// cell, variance 1 (uniform in ±√3).
func grainLattice(lw, lh int, seed uint32) []float32 {
	v := make([]float32, lw*lh)
	for y := 0; y < lh; y++ {
		for x := 0; x < lw; x++ {
			h := hash32(uint32(x)*0x9e3779b1 ^ uint32(y)*0x85ebca77 ^ seed)
			u := float32(h>>8) * (1.0 / 16777216.0)
			v[y*lw+x] = (2*u - 1) * 1.7320508
		}
	}
	return v
}

// grainSample bilinearly samples a lattice at pixel (x,y). The 1.5 makes
// the average over a cell unit variance.
func grainSample(lat []float32, lw, x, y int, cell float64) float32 {
	fx := (float64(x) + 0.5) / cell
	fy := (float64(y) + 0.5) / cell
	ix, iy := int(math.Floor(fx)), int(math.Floor(fy))
	tx := float32(fx - float64(ix))
	ty := float32(fy - float64(iy))
	i := iy*lw + ix
	a := lat[i] + (lat[i+1]-lat[i])*tx
	b := lat[i+lw] + (lat[i+lw+1]-lat[i+lw])*tx
	return (a + (b-a)*ty) * 1.5
}

// buildNoise fills f.noise with the density field at b's resolution.
func (f *filmOp) buildNoise(b *Base, p preset.Params) {
	w, h := b.W, b.H
	ch := 1
	if !f.mono {
		ch = 3
	}
	f.noise = make([]float32, w*h*ch)
	seed := grainSeed(p)
	sqi := float32(math.Sqrt(float64(1 - f.rho)))
	sqr := float32(math.Sqrt(float64(f.rho)))

	// Spatially resolved coarse octaves.
	for j, o := range f.oct {
		lw := int(math.Ceil(float64(w)/o.cell)) + 3
		lh := int(math.Ceil(float64(h)/o.cell)) + 3
		var common []float32
		if !f.mono && f.rho > 0 {
			common = grainLattice(lw, lh, seed^0x1234567^uint32(j)*0x9e3779b9)
		}
		for c := 0; c < ch; c++ {
			cseed := seed ^ 0x5bf03635 ^ uint32(j)*0x9e3779b9 ^ uint32(c)*0x85ebca6b
			if f.mono {
				cseed = seed ^ 0x1234567 ^ uint32(j)*0x9e3779b9
			}
			lat := grainLattice(lw, lh, cseed)
			if common != nil {
				for k := range lat {
					lat[k] = sqi*lat[k] + sqr*common[k]
				}
			}
			parallelRows(h, func(y0, y1 int) {
				for y := y0; y < y1; y++ {
					row := (y*w)*ch + c
					for x := 0; x < w; x++ {
						f.noise[row+x*ch] += o.amp * grainSample(lat, lw, x, y, o.cell)
					}
				}
			})
		}
	}

	// Octaves finer than a pixel: independent white noise with the variance
	// the pixel aperture leaves.
	if f.fine > 0 {
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					if f.mono {
						f.noise[i] += f.fine * grainGauss(i, seed^0x1234567)
						continue
					}
					var nc float32
					if f.rho > 0 {
						nc = grainGauss(i, seed^0x1234567)
					}
					for c := 0; c < 3; c++ {
						ni := grainGauss(i, seed^0x5bf03635^uint32(c)*0x85ebca6b)
						v := sqi*ni + sqr*nc
						f.noise[i*3+c] += f.fine * v
					}
				}
			}
		})
	}
}

// grain applies the density noise to an output-linear pixel.
func (f *filmOp) grain(i int, r, g, bl float32) (float32, float32, float32) {
	if f == nil || f.noise == nil {
		return r, g, bl
	}
	one := func(v float32, n float32) float32 {
		if v <= 0 {
			return v
		}
		u := min(1, enc(v))
		amp := 2 * float32(math.Sqrt(float64(u*(1-u))))
		return v * float32(math.Exp(-2.302585*float64(n*amp)))
	}
	if f.mono {
		n := f.noise[i]
		return one(r, n), one(g, n), one(bl, n)
	}
	return one(r, f.noise[i*3]), one(g, f.noise[i*3+1]), one(bl, f.noise[i*3+2])
}
