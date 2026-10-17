package render

// The film library: each film's diffuse RMS granularity as its maker's own
// technical data publishes it (render/films.json, with the document), for
// the grain (film.go). A negative's granularity is measured on the
// negative, which has a low contrast (gamma ~0.6); the print or scan that
// makes the positive raises it ~2.3x (to an overall ~1.4), and the density
// noise with it, so a negative's RMS 17 shows as ~40 on the positive. A
// slide's is measured on the image itself.

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed films.json
var filmsJSON []byte

// Film is one film stock of the library.
type Film struct {
	Maker string  `json:"maker"`
	Name  string  `json:"name"`
	Kind  string  `json:"kind"` // slide, neg (B&W), cn (colour negative), chromo (chromogenic B&W)
	ISO   int     `json:"iso"`
	RMS   float64 `json:"rms"` // 0 = the maker publishes none
	// PGI is Kodak's Print Grain Index where that is what the maker publishes
	// (0 = none). It is recorded, never converted: Kodak's data sheets state
	// that the index "has a different scale which cannot be compared to rms
	// granularity", so it cannot feed the grain model, which is RMS-based.
	PGI int `json:"pgi,omitempty"`
	// Formats are the film sizes the maker sold this stock in: "135"
	// (35 mm), "120"/"220" (medium-format rolls), "4x5"/"8x10" (sheets),
	// read from the film's own data sheet. Empty means unverified: the model
	// then offers 35 mm only rather than guessing a format the film may never
	// have been made in, since the format sets the grain's physical size.
	Formats    []string `json:"formats,omitempty"`
	FormatsSrc string   `json:"formats_src,omitempty"`
	Src   string  `json:"src"`
	URL   string  `json:"url,omitempty"`
	// Grain is the RMS on the positive, the value the Film grain slider
	// takes (0 if unknown).
	Grain float64 `json:"grain"`
	// BW: a black-and-white film. Mono: its spectral sensitivity (from its
	// data sheet) as weights of the white-balanced Foveon layers, per
	// sensor generation (tools/foveonmodel FILMBW); none = luminance.
	BW          bool                  `json:"bw"`
	Mono        map[string][3]float64 `json:"mono,omitempty"`
	SpectralSrc string                `json:"spectral_src,omitempty"`
	// Curve: its characteristic curve and developers (filmcurve.go), nil
	// if the library has none.
	Curve *FilmCurve `json:"curve,omitempty"`
	// Color: a colour film rendered from its own layers and dyes
	// (colorfilm.go, colorneg.go).
	Color bool `json:"color,omitempty"`
}

var filmByKey = sync.OnceValue(func() map[string]Film {
	m := map[string]Film{}
	for _, f := range Films() {
		m[f.Maker+" "+f.Name] = f
	}
	return m
})

// FilmByKey is the library film named maker + " " + name.
func FilmByKey(k string) (Film, bool) {
	f, ok := filmByKey()[k]
	return f, ok
}

// printGain: a negative's density noise on the positive (overall gamma
// ~1.4 over a negative gamma ~0.6).
const printGain = 1.4 / 0.6

// Films is the library, with each film's grain on the positive.
func Films() []Film {
	var d struct {
		Films []Film `json:"films"`
	}
	json.Unmarshal(filmsJSON, &d)
	for i := range d.Films {
		f := &d.Films[i]
		f.Grain = f.RMS
		if f.Kind != "slide" {
			f.Grain *= printGain
		}
		f.Curve = filmCurves()[f.Maker+" "+f.Name]
		f.Color = IsColorFilm(f.Maker + " " + f.Name)
	}
	return d.Films
}
