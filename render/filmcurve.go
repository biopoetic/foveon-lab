package render

// A black-and-white film's tone, from its characteristic curve: the scene
// exposes the negative, the developer sets its contrast, the print makes the
// positive (render/filmcurves.json has the makers' curves).
//
// Negative: the metered mid-grey gets the exposure lg H = lg(10/EI) lux·s (an
// exposure meter puts it ~3.6 stops above the ISO speed point 0.8/ISO, where
// the density is fog + 0.1; on the APX 400 curve 0.8/400 lands there). The
// density follows the data sheet's curve, toe and shoulder included.
// Development: density above fog grows with development, so the curve is the
// standard one with its density above fog scaled by gamma / gamma_ref, gamma
// from the developer's gamma-time curve. A developer the data sheet rates
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
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
)

//go:embed filmcurves.json
var filmCurvesJSON []byte

// Developer is one developer of a film's data sheet.
type Developer struct {
	Name  string    `json:"name"`
	EI    float64   `json:"ei"`   // the exposure index at its standard time
	Time  float64   `json:"time"` // its standard time (minutes)
	T     []float64 `json:"t"`
	Gamma []float64 `json:"gamma"`
}

// FilmCurve is a film's characteristic curve at standard development.
type FilmCurve struct {
	Film       string      `json:"film"`
	Src        string      `json:"src"`
	URL        string      `json:"url"`
	LgH        []float64   `json:"lgh"`
	D          []float64   `json:"d"`
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
		m[c.Film] = c
	}
	return m
})

// FilmCurveFor is the characteristic curve of the library film named key.
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

// Fog is the base + fog density.
func (c *FilmCurve) Fog() float64 { return c.D[0] }

// GammaRef is the standard curve's gamma: the slope of its straight part,
// fitted where the density is 0.3 above fog up to 0.5 below its maximum.
func (c *FilmCurve) GammaRef() float64 {
	var sx, sy, sxx, sxy, n float64
	lo, hi := c.Fog()+0.3, c.D[len(c.D)-1]-0.5
	for i := range c.LgH {
		if c.D[i] < lo || c.D[i] > hi {
			continue
		}
		x, y := c.LgH[i], c.D[i]
		sx, sy, sxx, sxy, n = sx+x, sy+y, sxx+x*x, sxy+x*y, n+1
	}
	return (n*sxy - sx*sy) / (n*sxx - sx*sx)
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

// Development is how a film was developed: the developer, its time and the
// exposure index the film was rated at.
type Development struct {
	Dev   Developer
	Time  float64 // minutes
	EI    float64
	Gamma float64
	Scale float64 // gamma / gamma_ref: the contrast (and the grain) against standard
}

// DevelopmentFor is the development p asks for: the developer's standard
// time and EI where p leaves them 0; push in stops raises the EI.
func (c *FilmCurve) DevelopmentFor(p preset.Params) Development {
	d := c.Developer(p.Developer)
	dv := Development{Dev: d, Time: d.Time, EI: d.EI * math.Exp2(p.Push)}
	if p.DevTime > 0 {
		dv.Time = p.DevTime
	}
	dv.Gamma = interp(d.T, d.Gamma, dv.Time)
	dv.Scale = dv.Gamma / c.GammaRef()
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
	gRef, fog := c.GammaRef(), c.Fog()
	iso := float64(f.ISO)
	// The metered grey's exposure, moved by the developer's speed: a
	// developer rated at EI 320 for ISO 400 needs 1/3 stop more.
	lgGrey := math.Log10(10/dv.EI) - math.Log10(iso/dv.Dev.EI)
	density := func(lgH float64) float64 {
		return fog + (interp(c.LgH, c.D, lgH)-fog)*dv.Scale
	}
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
