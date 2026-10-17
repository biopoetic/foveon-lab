package render

// The ten physical lens filters of the lens menu: Kodak Wratten transmittance.
//
// The curves are Kodak's compilation "Transmission of Wratten Filters",
// compiled by Allie C. Peed Jr. for the Eastman Kodak Company (the tables
// reprinted in the CRC Handbook): percent transmittance at 10 nm intervals
// from 400 to 700 nm, with a printed luminous transmittance for each filter
// under illuminant C. wratten_test.go recomputes that luminous transmittance
// from the tabulated curve and fails unless it matches the printed value, so
// a curve cannot enter the catalogue unverified. Wratten 47 is the one filter
// the compilation disagrees with itself about (its printed spectrum gives
// 2.04% where the compilation prints 2.8%), so it is not entered and keeps
// the logistic approximation.
//
// The table's "--" (below its print floor) is null in wratten.json and reads
// as zero here. Outside the table the curve is held flat: 380-400 nm takes
// the 400 nm value, 700-720 nm the 700 nm value.

import (
	_ "embed"
	"encoding/json"
)

//go:embed wratten.json
var wrattenJSON []byte

type wrattenFilter struct {
	Wratten  string     `json:"wratten"`               // the Wratten number the entry is
	Luminous float64    `json:"luminousTransmittance"` // printed value, percent
	Percent  []*float64 `json:"transmittancePercent"`  // 400..700 nm at 10 nm; null = "--"
}

var wrattenCatalogue struct {
	Filters map[string]wrattenFilter `json:"filters"`
}

func init() {
	if err := json.Unmarshal(wrattenJSON, &wrattenCatalogue); err != nil {
		panic("wratten.json: " + err.Error())
	}
}

const (
	wrattenFirst = 400.0 // nm, the first tabulated wavelength
	wrattenStep  = 10.0  // nm
)

// WrattenTransmission is the measured transmission spectrum (0-1) of the lens
// menu's named filter, or nil when the catalogue holds no curve for it. The
// name is the lens menu's, not the Wratten number: wratten.json maps the two.
func WrattenTransmission(name string) func(float64) float64 {
	f, ok := wrattenCatalogue.Filters[name]
	if !ok || len(f.Percent) == 0 {
		return nil
	}
	t := f.Percent
	at := func(i int) float64 {
		if t[i] == nil {
			return 0
		}
		return *t[i] / 100
	}
	last := len(t) - 1
	return func(l float64) float64 {
		if l <= wrattenFirst {
			return at(0)
		}
		x := (l - wrattenFirst) / wrattenStep
		if x >= float64(last) {
			return at(last)
		}
		i := int(x)
		a, b := at(i), at(i+1)
		return a + (b-a)*(x-float64(i))
	}
}
