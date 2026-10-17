package render

// SCHOTT filter glass (schottglass.json, fetched from shop.schott.com): the
// internal transmittance ti at the reference thickness and the reflection
// factor pd, so a filter's transmission is pd * ti(l), linearly interpolated
// over the catalogue's 10 nm grid. This is the measured data behind the
// infrared looks (OG590, RG715) and the B+W filters, whose numbers are
// glass numbers (495/022, 695/092, 830/093).

import (
	_ "embed"
	"encoding/json"
	"math"
	"sort"
	"sync"
)

//go:embed schottglass.json
var schottGlassJSON []byte

type schottGlass struct {
	pd float64
	ti []float64
}

type schottCat struct {
	lambda  []float64
	glasses map[string]schottGlass
}

var schott = sync.OnceValue(func() schottCat {
	var raw struct {
		Lambda  []float64 `json:"lambda"`
		Glasses map[string]struct {
			Pd float64   `json:"pd"`
			Ti []float64 `json:"ti"`
		} `json:"glasses"`
	}
	if err := json.Unmarshal(schottGlassJSON, &raw); err != nil {
		panic("schottglass.json: " + err.Error())
	}
	if len(raw.Lambda) == 0 {
		panic("schottglass.json: no wavelength grid")
	}
	c := schottCat{lambda: raw.Lambda, glasses: make(map[string]schottGlass, len(raw.Glasses))}
	for name, g := range raw.Glasses {
		if len(g.Ti) != len(raw.Lambda) {
			panic("schottglass.json: " + name + " has the wrong number of samples")
		}
		c.glasses[name] = schottGlass{pd: g.Pd, ti: g.Ti}
	}
	return c
})

// SchottGlass reports whether the catalogue has that glass.
func SchottGlass(name string) bool {
	_, ok := schott().glasses[name]
	return ok
}

// SchottTransmission is the named glass's transmission T(l) = pd * ti(l),
// linearly interpolated between the catalogue's samples and held flat
// outside them. nil for an unknown glass.
func SchottTransmission(name string) func(float64) float64 {
	c := schott()
	g, ok := c.glasses[name]
	if !ok {
		return nil
	}
	lam, ti := c.lambda, g.ti
	return func(l float64) float64 {
		i := sort.SearchFloat64s(lam, l) // first sample >= l
		switch {
		case i == 0:
			return g.pd * ti[0]
		case i >= len(lam):
			return g.pd * ti[len(ti)-1]
		case lam[i] == l:
			return g.pd * ti[i]
		}
		t := (l - lam[i-1]) / (lam[i] - lam[i-1])
		return g.pd * (ti[i-1]*(1-t) + ti[i]*t)
	}
}

// SchottMeanTransmission is the glass's transmission averaged over a range,
// for reporting a filter's light loss.
func SchottMeanTransmission(name string, lo, hi, step float64) float64 {
	T := SchottTransmission(name)
	if T == nil || hi <= lo {
		return 0
	}
	var s float64
	n := 0.0
	for l := lo; l <= hi+1e-9; l += step {
		s += T(l)
		n++
	}
	return s / math.Max(n, 1)
}
