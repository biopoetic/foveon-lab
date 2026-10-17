package render

// Photographic filters by brand and model (brandfilters.json): the list a
// photographer actually shops from — B+W 022, Hoya R72, Cokin 007,
// K&F Concept IR720 — instead of Wratten designations alone.
//
// Every entry carries its basis:
//   - glass:   a SCHOTT filter glass whose measured internal transmittance is
//              in schottglass.json. B+W's numbers ARE glass numbers
//              (495/022, 695/092, 830/093), so those filters are rendered on
//              the real measured curve.
//   - wratten: the maker documents the filter as the equivalent of a Wratten
//              number; it is rendered on the Wratten edge used for that number.
//   - edge:    the maker publishes only the cutoff (Hoya's R72: nothing below
//              740 nm, 95% from 760 nm); rendered at that cutoff.
// Nothing here is an invented curve: an entry without one of these bases is
// not in the catalogue.

import (
	_ "embed"
	"encoding/json"
	"math"
	"strings"
	"sync"
)

//go:embed brandfilters.json
var brandFiltersJSON []byte

// BrandFilter is one catalogue entry, as served to the UI.
type BrandFilter struct {
	ID     string `json:"id"`
	Brand  string `json:"brand"`
	Model  string `json:"model"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`   // "colour" or "ir"
	Look   string `json:"look"`   // for IR: "colour" or "mono"
	Glass  string `json:"glass"`  // SCHOTT glass, measured curve
	Wratten string `json:"wratten"` // Wratten edge this filter equals
	Edge   float64 `json:"edge"`  // published cutoff only
	Src    string `json:"src"`
}

var brandFilters = sync.OnceValue(func() []BrandFilter {
	var d struct {
		Filters []BrandFilter `json:"filters"`
	}
	if err := json.Unmarshal(brandFiltersJSON, &d); err != nil {
		panic("brandfilters.json: " + err.Error())
	}
	return d.Filters
})

// BrandFilters is the catalogue (colour and infrared entries).
func BrandFilters() []BrandFilter { return brandFilters() }

// brandFilter looks an entry up by id.
func brandFilter(id string) (BrandFilter, bool) {
	for _, f := range brandFilters() {
		if f.ID == id {
			return f, true
		}
	}
	return BrandFilter{}, false
}

// wrattenEdges are the Wratten long-pass edges the named set is rendered on
// (tools/foveonmodel). A brand filter documented as a Wratten equivalent
// resolves to the same edge.
var wrattenEdges = map[string]float64{
	"yellow": 495, "deepyellow": 530, "orange": 555, "red": 595, "deepred": 615,
}

// brandTransmission is the entry's transmission spectrum: a measured SCHOTT
// glass curve, the Wratten edge it equals, or its published cutoff.
func brandTransmission(f BrandFilter) func(float64) float64 {
	if f.Glass != "" {
		if T := SchottTransmission(f.Glass); T != nil {
			return T
		}
	}
	if e, ok := wrattenEdges[f.Wratten]; ok {
		return func(l float64) float64 { return 1 / (1 + math.Exp(-(l-e)/10)) }
	}
	e := f.Edge
	return func(l float64) float64 { return 1 / (1 + math.Exp(-(l-e)/8)) }
}

var (
	brandMu    sync.Mutex
	brandCache = map[string]*[9]float64{}
)

// brandFilterMatrix is a colour brand filter's layer-space matrix on sensor
// (cached), or nil if the entry is not a colour filter.
func brandFilterMatrix(sensor, id string) *[9]float64 {
	f, ok := brandFilter(id)
	if !ok || f.Kind != "colour" {
		return nil
	}
	key := sensor + "|" + id
	brandMu.Lock()
	defer brandMu.Unlock()
	if m, ok := brandCache[key]; ok {
		return m
	}
	m := filterMatrixFor(sensor, brandTransmission(f))
	if len(brandCache) > 256 {
		clear(brandCache)
	}
	brandCache[key] = m
	return m
}

// brandFilterNames reports whether id names a catalogue entry.
func brandFilterNames(id string) bool {
	_, ok := brandFilter(id)
	return ok
}

// IsBrandIR reports whether id is a brand infrared filter (its look is served
// through the infrared path, irlooks.json).
func IsBrandIR(id string) bool {
	f, ok := brandFilter(id)
	return ok && f.Kind == "ir"
}

// brandID strips the "br-" prefix the UI filters carry.
func brandID(filter string) string {
	return strings.TrimPrefix(filter, "br-")
}
