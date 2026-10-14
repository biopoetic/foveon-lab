package render

// A black-and-white film's tone, from its characteristic curve: the scene
// exposes the negative, the developer sets its contrast, the print makes the
// positive (render/filmcurves.json has the makers' curves).
//
// Negative: the metered mid-grey gets the exposure lg H = lg(10/EI) lux·s, an
// exposure meter's ~3.6 stops above the ISO speed point 0.8/ISO (where the
// density is fog + 0.1). The curve's own speed point is put at the box ISO:
// Tri-X and APX 400 land there as printed, APX 100 and 25 are printed up to
// a stop faster than their rating, and the rating is what the photographer
// meters with. The density follows the curve, toe and shoulder included.
// Development: the developer's contrast-time curve (gamma or Kodak's
// contrast index) sets the gamma, relative to its standard time's, which
// gives the film's standard gamma. Where the sheet gives a family of curves
// (Kodak: one per time) the curve of that gamma is interpolated between
// them, so the toe changes as measured; past the family, or with one curve,
// the density above fog scales with gamma / gamma_ref. Pushed, the time is
// the sheet's push time unless set. A developer the data sheet rates
// below the box speed (Rodinal: EI 320 for ISO 400) needs that much more
// exposure for the same density. Push: rating the film at a higher EI puts
// less exposure on it, the shadows sink into the toe; developing longer
// raises the contrast to compensate, as in the darkroom.
// Print: the clear film base prints black ("minimum time for maximum
// black") and the mid-grey prints mid-grey (the printer's exposure); in
// between the positive's log brightness follows the negative's density at
// 1 / gamma_ref, so the standard development's straight part prints 1:1 and
// the film's own shape is what shows. The paper's curve is not modelled yet.

import (
	_ "embed"
	"encoding/json"
	"math"
	"strconv"
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
)

//go:embed filmcurves.json
var filmCurvesJSON []byte

// Developer is one developer of a film's data sheet.
type Developer struct {
	Name string    `json:"name"`
	EI   float64   `json:"ei"`   // the exposure index at its standard time
	Time float64   `json:"time"` // its standard time (minutes)
	T    []float64 `json:"t"`
	C    []float64 `json:"c"` // contrast (the sheet's gamma or contrast index) at time T
	// Push: the sheet's times (minutes) for pushing the given stops.
	Push map[string]float64 `json:"push,omitempty"`
	// Src: where the developer's data comes from when not the film's own
	// sheet (times from another source, a curve shape carried over).
	Src string `json:"src,omitempty"`
}

// Curve is one characteristic curve of a film's family.
type Curve struct {
	Time  float64   `json:"time,omitempty"` // development time in the sheet's process
	D     []float64 `json:"d"`
	gamma float64
}

// FilmCurve is a film's characteristic curves.
type FilmCurve struct {
	Film     string    `json:"film"`
	Src      string    `json:"src"`
	URL      string    `json:"url"`
	Contrast string    `json:"contrast"` // the sheet's measure: gamma or CI
	LgH      []float64 `json:"lgh"`
	Curves   []Curve   `json:"curves"`
	// RefTime: the standard time of the curves' process (several curves).
	RefTime    float64     `json:"ref_time,omitempty"`
	Developers []Developer `json:"developers"`
}

var filmCurves = sync.OnceValue(func() map[string]*FilmCurve {
	var d struct {
		Curves []*FilmCurve `json:"curves"`
	}
	if err := json.Unmarshal(filmCurvesJSON, &d); err != nil {
		panic("filmcurves.json: " + err.Error())
	}
	m := map[string]*FilmCurve{}
	for _, c := range d.Curves {
		for i := range c.Curves {
			c.Curves[i].gamma = fitGamma(c.LgH, c.Curves[i].D)
		}
		m[c.Film] = c
	}
	return m
})

// FilmCurveFor is the characteristic curves of the library film named key.
func FilmCurveFor(key string) (*FilmCurve, bool) {
	c, ok := filmCurves()[key]
	return c, ok
}

// interp is piecewise-linear interpolation, extended flat past the ends.
func interp(xs, ys []float64, x float64) float64 {
	if x <= xs[0] {
		return ys[0]
	}
	n := len(xs) - 1
	if x >= xs[n] {
		return ys[n]
	}
	i := 1
	for xs[i] < x {
		i++
	}
	t := (x - xs[i-1]) / (xs[i] - xs[i-1])
	return ys[i-1] + t*(ys[i]-ys[i-1])
}

// fitGamma is a curve's gamma: the slope of its straight part, fitted
// where the density is 0.3 above fog up to 0.5 below its maximum.
func fitGamma(lgH, D []float64) float64 {
	var sx, sy, sxx, sxy, n float64
	lo, hi := D[0]+0.3, D[len(D)-1]-0.5
	for i := range lgH {
		if D[i] < lo || D[i] > hi {
			continue
		}
		x, y := lgH[i], D[i]
		sx, sy, sxx, sxy, n = sx+x, sy+y, sxx+x*x, sxy+x*y, n+1
	}
	return (n*sxy - sx*sy) / (n*sxx - sx*sx)
}

// GammaRef is the gamma of the standard development: the curve's, or the
// family's at its standard time.
func (c *FilmCurve) GammaRef() float64 {
	if len(c.Curves) == 1 || c.RefTime <= 0 {
		return c.Curves[0].gamma
	}
	ts, gs := make([]float64, len(c.Curves)), make([]float64, len(c.Curves))
	for i, k := range c.Curves {
		ts[i], gs[i] = k.Time, k.gamma
	}
	return interp(ts, gs, c.RefTime)
}

// Density is the density at lgH of the film developed to gamma g: between
// two of the family's curves, interpolated by gamma (the toe changes with
// development as measured); past the family, the nearest curve with its
// density above fog scaled. Above the measured range the curve continues
// at its last slope.
func (c *FilmCurve) Density(g float64) func(lgH float64) float64 {
	one := func(k Curve, scale float64) func(float64) float64 {
		n := len(c.LgH) - 1
		top := (k.D[n] - k.D[n-1]) / (c.LgH[n] - c.LgH[n-1])
		fog := k.D[0]
		return func(x float64) float64 {
			d := interp(c.LgH, k.D, x)
			if x > c.LgH[n] {
				d += top * (x - c.LgH[n])
			}
			return fog + (d-fog)*scale
		}
	}
	ks := c.Curves
	lo, hi := ks[0], ks[0]
	for _, k := range ks {
		if k.gamma < lo.gamma {
			lo = k
		}
		if k.gamma > hi.gamma {
			hi = k
		}
	}
	if g <= lo.gamma {
		return one(lo, g/lo.gamma)
	}
	if g >= hi.gamma {
		return one(hi, g/hi.gamma)
	}
	var a, b Curve
	for _, k := range ks {
		if k.gamma <= g && (a.D == nil || k.gamma > a.gamma) {
			a = k
		}
		if k.gamma >= g && (b.D == nil || k.gamma < b.gamma) {
			b = k
		}
	}
	if b.gamma == a.gamma {
		return one(a, 1)
	}
	w := (g - a.gamma) / (b.gamma - a.gamma)
	fa, fb := one(a, 1), one(b, 1)
	return func(x float64) float64 { return (1-w)*fa(x) + w*fb(x) }
}

// SpeedPoint is the lg H where the standard development's density is
// fog + 0.1 (the ISO speed point).
func (c *FilmCurve) SpeedPoint() float64 {
	dens := c.Density(c.GammaRef())
	fog := dens(c.LgH[0])
	lo, hi := c.LgH[0], c.LgH[len(c.LgH)-1]
	for i := 0; i < 50; i++ {
		m := (lo + hi) / 2
		if dens(m)-fog < 0.1 {
			lo = m
		} else {
			hi = m
		}
	}
	return (lo + hi) / 2
}

// Developer is the film's developer by name (the first one if unknown).
func (c *FilmCurve) Developer(name string) Developer {
	for _, d := range c.Developers {
		if d.Name == name {
			return d
		}
	}
	return c.Developers[0]
}

// PushTime is the developer's time for pushing push stops: the sheet's
// push times, interpolated from the standard time; past them the last one;
// the standard time where the sheet gives none (and for a pull).
func (d Developer) PushTime(push float64) float64 {
	if push <= 0 || len(d.Push) == 0 {
		return d.Time
	}
	xs, ys := []float64{0}, []float64{d.Time}
	for s := 1; s <= 6; s++ {
		if t, ok := d.Push[strconv.Itoa(s)]; ok {
			xs, ys = append(xs, float64(s)), append(ys, t)
		}
	}
	return interp(xs, ys, push)
}

// Development is how a film was developed: the developer, its time and the
// exposure index the film was rated at.
type Development struct {
	Dev   Developer
	Time  float64 // minutes
	EI    float64
	Gamma float64
	Scale float64 // gamma / gamma_ref: the contrast (and the grain) against standard
}

// DevelopmentFor is the development p asks for: push in stops raises the
// EI; the time is the sheet's for that push where p leaves it 0. The
// developer's contrast at that time, relative to its standard time's,
// moves the film from its standard gamma.
func (c *FilmCurve) DevelopmentFor(p preset.Params) Development {
	d := c.Developer(p.Developer)
	dv := Development{Dev: d, Time: d.PushTime(p.Push), EI: d.EI * math.Exp2(p.Push)}
	if p.DevTime > 0 {
		dv.Time = p.DevTime
	}
	gRef := c.GammaRef()
	dv.Gamma = gRef * interp(d.T, d.C, dv.Time) / interp(d.T, d.C, d.Time)
	dv.Scale = dv.Gamma / gRef
	return dv
}

// filmToneLUT: Y (linear, 0.18 = mid-grey) -> the print's Y, sampled on
// t = Y^(1/4) over 0..filmToneMax^(1/4).
const (
	filmToneN   = 4096
	filmToneMax = 64.0
)

type filmTone struct {
	lut   [filmToneN + 1]float32
	scale float32 // filmToneN / filmToneMax^(1/4)
}

// newFilmTone is the tone of film c with development dv, nil if p uses no
// curve.
func newFilmTone(p preset.Params) *filmTone {
	f, ok := FilmByKey(p.Film)
	if !ok || !f.BW {
		return nil
	}
	c, ok := FilmCurveFor(p.Film)
	if !ok {
		return nil
	}
	dv := c.DevelopmentFor(p)
	gRef := c.GammaRef()
	iso := float64(f.ISO)
	// The metered grey's exposure, moved by the developer's speed: a
	// developer rated at EI 320 for ISO 400 needs 1/3 stop more.
	lgGrey := math.Log10(10/dv.EI) - math.Log10(iso/dv.Dev.EI) + c.SpeedPoint() - math.Log10(0.8/iso)
	density := c.Density(dv.Gamma)
	fog := density(c.LgH[0])
	dGrey := density(lgGrey)
	pos := func(d float64) float64 { return math.Pow(10, (d-dGrey)/gRef) }
	black := pos(fog)
	norm := 0.18 / (pos(dGrey) - black)
	t := &filmTone{scale: float32(filmToneN / math.Pow(filmToneMax, 0.25))}
	for i := 0; i <= filmToneN; i++ {
		y := math.Pow(float64(i)/float64(t.scale), 4)
		if y <= 0 {
			continue
		}
		d := density(lgGrey + math.Log10(y/0.18))
		t.lut[i] = float32((pos(d) - black) * norm)
	}
	return t
}

// at is the print's Y for scene Y.
func (t *filmTone) at(y float32) float32 {
	if y <= 0 {
		return 0
	}
	x := float32(math.Sqrt(math.Sqrt(float64(y)))) * t.scale
	if x >= filmToneN {
		// Past the table: the shoulder is flat by then.
		return t.lut[filmToneN]
	}
	i := int(x)
	fr := x - float32(i)
	return t.lut[i] + fr*(t.lut[i+1]-t.lut[i])
}
