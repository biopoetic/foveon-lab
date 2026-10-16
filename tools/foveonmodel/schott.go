package main

// Real filter glass from SCHOTT's catalogue (render/schottglass.json,
// fetched from shop.schott.com on 2026-10-15): internal transmittance ti at
// the reference thickness, and the reflection factor pd, so a filter's
// transmission is pd * ti(l), linearly interpolated over the 10 nm grid of
// the catalogue (380-1100 nm: the infrared grid's whole span). Used where
// made-up logistic edges stood: the infrared looks' lens filters are the
// glasses they always described, OG590 and RG715.

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"sync"
)

type schottGlass struct {
	pd float64
	ti []float64
}

type schottCat struct {
	lambda  []float64
	glasses map[string]schottGlass
}

// schott reads the catalogue once (run from the repo root, as the tool's
// other data reads are).
var schott = sync.OnceValue(func() schottCat {
	data, err := os.ReadFile("render/schottglass.json")
	if err != nil {
		log.Fatalf("schottglass.json: %v", err)
	}
	var raw struct {
		Lambda  []float64 `json:"lambda"`
		Glasses map[string]struct {
			Pd float64   `json:"pd"`
			Ti []float64 `json:"ti"`
		} `json:"glasses"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Fatalf("schottglass.json: %v", err)
	}
	if len(raw.Lambda) == 0 {
		log.Fatal("schottglass.json: no wavelength grid")
	}
	c := schottCat{lambda: raw.Lambda, glasses: make(map[string]schottGlass, len(raw.Glasses))}
	for name, g := range raw.Glasses {
		if len(g.Ti) != len(raw.Lambda) {
			log.Fatalf("schottglass.json: %s has %d samples on a %d-point grid", name, len(g.Ti), len(raw.Lambda))
		}
		c.glasses[name] = schottGlass{pd: g.Pd, ti: g.Ti}
	}
	return c
})

// schottT is the named glass's transmission T(l) = pd * ti(l), linearly
// interpolated between the catalogue's 10 nm samples and held flat
// outside them.
func schottT(name string) func(l float64) float64 {
	c := schott()
	g, ok := c.glasses[name]
	if !ok {
		log.Fatalf("schottglass.json: no glass %q", name)
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
