package render

// Any colour filter, computed live: the filter pad. A filter is its
// transmission spectrum; on a Foveon sensor it changes the three layers'
// responses, which the same physical model as the Wratten filters
// (layerfilter.go, tools/foveonmodel) turns into a 3×3 matrix on
// white-balanced layers. The model: silicon's absorption (Green 2008),
// α = 4πk/λ, each layer collecting what is absorbed between its depths
// (the generation's fitted d0..d3 µm) behind the IR cut (logistic edge).
// The matrix is the least-squares map from the unfiltered to the filtered
// layers over 400 reflectance spectra in 5500 K daylight, the filter's
// light loss kept (the render compensates it like a filter factor).
//
// The pad's filters pass a band: T(λ) = 1 − s·(1 − P(λ)), P = rising edge
// at a times falling edge at b (logistic, 10 nm, as the Wratten edges), a
// at or below 380 nm = no lower cut, b at or above 720 nm = no upper cut,
// s the strength (0 = clear glass, 1 = the full filter). Wratten 8 is
// a = 495 with no b; Wratten 47 is b = 505 with no a; Wratten 58 is 490-585.

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed si_green2008.txt
var siGreen2008 string

const (
	specMin, specMax, specStep = 380.0, 720.0, 5.0
	padEdgeWidth               = 10.0 // nm, the Wratten edges' logistic slope
)

// spectral holds the wavelength grid, silicon's absorption and the
// reflectance set, built once.
var spectral = sync.OnceValue(func() (s struct {
	lams, alpha, illum []float64
	refl               [][]float64
}) {
	type row struct{ l, k float64 }
	var rows []row
	sc := bufio.NewScanner(strings.NewReader(siGreen2008))
	for sc.Scan() {
		var l, n, k float64
		if c, _ := fmt.Sscan(sc.Text(), &l, &n, &k); c == 3 {
			rows = append(rows, row{l * 1000, k})
		}
	}
	for l := specMin; l <= specMax+1e-9; l += specStep {
		i := sort.Search(len(rows), func(i int) bool { return rows[i].l >= l })
		k := rows[i].k
		if rows[i].l > l {
			t := (l - rows[i-1].l) / (rows[i].l - rows[i-1].l)
			k = math.Exp(math.Log(rows[i-1].k)*(1-t) + math.Log(rows[i].k)*t)
		}
		s.lams = append(s.lams, l)
		s.alpha = append(s.alpha, 4*math.Pi*k/(l*1e-3))
		lm := l * 1e-9
		s.illum = append(s.illum, 1/(math.Pow(lm, 5)*(math.Exp(1.4388e-2/(lm*5500))-1)))
	}
	// Smooth reflectances: a base plus 1-3 Gaussian bumps or dips (as
	// tools/foveonmodel's scene).
	rng := rand.New(rand.NewSource(1))
	for j := 0; j < 400; j++ {
		r := make([]float64, len(s.lams))
		base := 0.05 + 0.3*rng.Float64()
		for i := range r {
			r[i] = base
		}
		for b := 0; b < 1+rng.Intn(3); b++ {
			mu, sd, amp := 380+340*rng.Float64(), 15+100*rng.Float64(), 0.7*rng.Float64()
			if rng.Float64() < 0.3 {
				amp = -amp * base / 0.7
			}
			for i, l := range s.lams {
				r[i] += amp * math.Exp(-0.5*((l-mu)/sd)*((l-mu)/sd))
			}
		}
		for i := range r {
			r[i] = math.Max(0.01, math.Min(0.95, r[i]))
		}
		s.refl = append(s.refl, r)
	}
	return s
})

//go:embed layersens.json
var layerSensJSON []byte

// measuredSens is the measured layer sensitivities per generation
// (layersens.json), on the spectral grid.
var measuredSens = sync.OnceValue(func() map[string][][3]float64 {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(layerSensJSON, &d); err != nil {
		panic("layersens.json: " + err.Error())
	}
	sp := spectral()
	out := map[string][][3]float64{}
	for gen, raw := range d {
		var g struct {
			Curves [][4]float64 `json:"curves"`
		}
		if json.Unmarshal(raw, &g) != nil || len(g.Curves) == 0 {
			continue
		}
		S := make([][3]float64, len(sp.lams))
		for i, l := range sp.lams {
			for _, r := range g.Curves {
				if math.Abs(r[0]-l) < 1e-6 {
					S[i] = [3]float64{r[1], r[2], r[3]}
				}
			}
		}
		out[gen] = S
	}
	return out
})

// layerSens is the generation's layer sensitivities (bottom, middle, top)
// on the spectral grid: measured where a measurement exists (Merrill,
// SD9/SD10), else the fitted depth model; nil if neither.
func layerSens(sensor string) [][3]float64 {
	if S, ok := measuredSens()[sensor]; ok {
		return S
	}
	return modelSens(sensor)
}

// modelSens is the fitted depth model's layer sensitivities (layerfilters
// .json "model"): the IR looks use it beyond 700 nm, where nothing is
// measured.
func modelSens(sensor string) [][3]float64 {
	g, ok := layerFilters[sensor]
	if !ok || len(g.Model) != 6 {
		return nil
	}
	d0, d1, d2, d3, lc, w := g.Model[0], g.Model[1], g.Model[2], g.Model[3], g.Model[4], g.Model[5]
	sp := spectral()
	S := make([][3]float64, len(sp.lams))
	for i, l := range sp.lams {
		a := sp.alpha[i]
		ir := 1 / (1 + math.Exp((l-lc)/w))
		e := func(z float64) float64 { return math.Exp(-a * z) }
		S[i] = [3]float64{ir * (e(d2) - e(d3)), ir * (e(d1) - e(d2)), ir * (e(d0) - e(d1))}
	}
	return S
}

// PadTransmission is the pad filter's transmission at λ nm.
func PadTransmission(l, a, b, s float64) float64 {
	p := 1.0
	if a > specMin {
		p *= 1 / (1 + math.Exp(-(l-a)/padEdgeWidth))
	}
	if b < specMax {
		p *= 1 - 1/(1+math.Exp(-(l-b)/padEdgeWidth))
	}
	return 1 - s*(1-p)
}

// filterMatrixFor is the layer-space matrix of transmission T on sensor:
// unfiltered layers -> filtered layers, both normalised by the unfiltered
// white (the light loss kept).
func filterMatrixFor(sensor string, T func(float64) float64) *[9]float64 {
	S := layerSens(sensor)
	if S == nil {
		return nil
	}
	sp := spectral()
	var wht [3]float64
	for i := range sp.lams {
		for k := 0; k < 3; k++ {
			wht[k] += S[i][k] * sp.illum[i]
		}
	}
	tr := make([]float64, len(sp.lams))
	for i, l := range sp.lams {
		tr[i] = T(l)
	}
	var ata, atb [3][3]float64
	for _, r := range sp.refl {
		var p, f [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			for k := 0; k < 3; k++ {
				p[k] += S[i][k] * e
				f[k] += S[i][k] * e * tr[i]
			}
		}
		for k := 0; k < 3; k++ {
			p[k] /= wht[k]
			f[k] /= wht[k]
		}
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				ata[r][q] += p[r] * p[q]
				atb[r][q] += f[r] * p[q]
			}
		}
	}
	A := [9]float64{ata[0][0], ata[0][1], ata[0][2], ata[1][0], ata[1][1], ata[1][2], ata[2][0], ata[2][1], ata[2][2]}
	inv, ok := invert3x3(A)
	if !ok {
		return nil
	}
	var F [9]float64
	for r := 0; r < 3; r++ {
		for q := 0; q < 3; q++ {
			for k := 0; k < 3; k++ {
				F[r*3+q] += atb[r][k] * inv[k*3+q]
			}
		}
	}
	return &F
}

var (
	padMu    sync.Mutex
	padCache = map[string]*[9]float64{}
)

// padFilter parses "pad:a,b,s" (nm, nm, 0-1) and returns its matrix on
// sensor (cached), or nil.
func padFilter(sensor, name string) *[9]float64 {
	v := strings.Split(strings.TrimPrefix(name, "pad:"), ",")
	if len(v) != 3 {
		return nil
	}
	var p [3]float64
	for i, x := range v {
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return nil
		}
		p[i] = f
	}
	a, b, s := p[0], p[1], math.Max(0, math.Min(1, p[2]))
	key := sensor + "|" + name
	padMu.Lock()
	defer padMu.Unlock()
	if m, ok := padCache[key]; ok {
		return m
	}
	m := filterMatrixFor(sensor, func(l float64) float64 { return PadTransmission(l, a, b, s) })
	if len(padCache) > 256 {
		clear(padCache)
	}
	padCache[key] = m
	return m
}
