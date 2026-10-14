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
	}
	return d.Films
}
