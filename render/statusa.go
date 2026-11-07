package render

// ISO 5-3 Status A densitometry, from the standard's published spectral
// products (statusa.json). Status A is not a set of three narrow-band reads:
// each channel is a measured spectral response about 35-60 nm wide, so the
// density it reports is an integral of the dye transmission through that
// response. The table is the one the film curves themselves were measured
// with; read it at its own resolution rather than at a fitted wavelength.
//
// The table is normalised to a 5.000 peak in log10 units. Only ratios matter
// in a density, so the relative spectral product is 10^(value-5).

import (
	_ "embed"
	"encoding/json"
	"math"
	"sort"
	"sync"
)

//go:embed statusa.json
var statusAJSON []byte

type statusACurve struct {
	Points [][2]float64 `json:"points"` // nm -> log10 spectral product
	Below  [2]float64   `json:"below"`  // [first tabulated nm, slope per nm]
	Above  [2]float64   `json:"above"`  // [last tabulated nm, slope per nm]
}

type statusAData struct {
	Standard    string                   `json:"standard"`
	Designation string                   `json:"designation"`
	Note        string                   `json:"note"`
	Source      string                   `json:"source"`
	URL         string                   `json:"url"`
	SHA256      string                   `json:"sha256"`
	Normalized  float64                  `json:"normalized"`
	Units       string                   `json:"units"`
	Interp      string                   `json:"interpolation"`
	Channels    map[string]*statusACurve `json:"channels"`
	Figure      string                   `json:"figure"`
}

var statusA_ = sync.OnceValue(func() *statusAData {
	var d statusAData
	if err := json.Unmarshal(statusAJSON, &d); err != nil {
		panic("statusa.json: " + err.Error())
	}
	for _, c := range d.Channels {
		sort.Slice(c.Points, func(i, j int) bool { return c.Points[i][0] < c.Points[j][0] })
	}
	return &d
})

// statusALogAt is a channel's tabulated log10 spectral product at λ: linear in
// log10 between the 10 nm entries, and the printed edge slope outside them.
func (c *statusACurve) logAt(lam float64) float64 {
	n := len(c.Points)
	if lam <= c.Points[0][0] {
		return c.Points[0][1] + c.Below[1]*(lam-c.Points[0][0])
	}
	if lam >= c.Points[n-1][0] {
		return c.Points[n-1][1] + c.Above[1]*(lam-c.Points[n-1][0])
	}
	i := sort.Search(n, func(i int) bool { return c.Points[i][0] >= lam })
	if c.Points[i][0] == lam {
		return c.Points[i][1]
	}
	t := (lam - c.Points[i-1][0]) / (c.Points[i][0] - c.Points[i-1][0])
	return c.Points[i-1][1] + t*(c.Points[i][1]-c.Points[i-1][1])
}

// relAt is the channel's relative spectral product (peak 1).
func (c *statusACurve) relAt(lam float64) float64 {
	v := math.Pow(10, c.logAt(lam)-statusA_().Normalized)
	if v < 1e-12 {
		// The printed slopes run to -1e-6 or below outside the passband;
		// below a part in a million the response is numerical noise.
		return 0
	}
	return v
}

// The integration grid. The Status A table is 10 nm; 5 nm resolves the
// tabulated corners and the dye curves without oversampling the integrals.
// The visible passbands all lie inside 380-780 nm.
const (
	statusALo   = 380.0
	statusAHi   = 780.0
	statusAStep = 5.0
)

type statusAGridT struct {
	lams  []float64
	s     [3][]float64
	denom [3]float64
}

// statusAGrid is the shared Status A integration grid: wavelengths, the three
// relative spectral products (R, G, B) and each channel's integral.
var statusAGrid_ = sync.OnceValue(func() statusAGridT {
	var g statusAGridT
	n := int(math.Round((statusAHi-statusALo)/statusAStep)) + 1
	g.lams = make([]float64, n)
	for k := range g.s {
		g.s[k] = make([]float64, n)
	}
	d := statusA_()
	names := [3]string{"R", "G", "B"}
	for i := 0; i < n; i++ {
		lam := statusALo + float64(i)*statusAStep
		g.lams[i] = lam
		for k, name := range names {
			c := d.Channels[name]
			if c == nil {
				panic("statusa.json: no " + name + " channel")
			}
			v := c.relAt(lam)
			g.s[k][i] = v
			g.denom[k] += v
		}
	}
	return g
})

func statusAGrid() statusAGridT { return statusAGrid_() }

// StatusA returns the stored ISO 5-3 Status A table's citation and the
// relative spectral products sampled on the integration grid. It is exported
// so the report's proof can draw the table over a film's dyes.
func StatusA() (lams []float64, s [3][]float64, citation string) {
	g := statusAGrid()
	d := statusA_()
	return g.lams, g.s, d.Designation + ", " + d.Source
}
