package render

// The film library: each film's diffuse RMS granularity as its maker's own
// technical data publishes it (render/films.json, with the document), for
// the grain (film.go). A negative's granularity is measured on the
// negative, which has a low contrast (gamma ~0.6); the print or scan that
// makes the positive raises it ~2.3x (to an overall ~1.4), and the density
// noise with it, so a negative's RMS 17 shows as ~40 on the positive. A
// slide's is measured on the image itself.
//
// Two kinds of number are not RMS. Kodak publishes a Print Grain Index for
// its colour negative films: a psychometric print-graininess scale on which
// 25 is the threshold of any perceived grain and two points is one
// just-noticeable difference (Universalis, "Photographie — Sensitométrie";
// Kodak's US 6,091,861 gives the logarithmic relation, slope 29.64). PGI is
// converted for the model, never treated as RMS: pgiGrain anchors PGI 25 at
// a positive-density RMS of 4.0 and uses the measured slope. Films whose
// maker publishes neither number carry an estimated RMS-equivalent in
// est_rms, with the neighbouring published films it came from, and are
// marked estimated in the data and the UI.

import (
	_ "embed"
	"encoding/json"
	"math"
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
	RMS   float64 `json:"rms"` // published diffuse RMS on the negative/slide; 0 = none
	// PGI is Kodak's Print Grain Index where that is what the maker
	// publishes (0 = none). It is a perceptual print-graininess scale, not
	// RMS; pgiGrain converts it for the model (see the file comment).
	PGI int `json:"pgi,omitempty"`
	// EstRMS is an estimated RMS-equivalent on the same scale as RMS, for a
	// film whose maker publishes none. RMSNote names the films it was
	// estimated from; GrainEstimated carries it to the UI.
	EstRMS  float64 `json:"est_rms,omitempty"`
	RMSNote string  `json:"rms_note,omitempty"`
	// GrainEstimated: EstRMS is an estimate, not a published number.
	GrainEstimated bool `json:"grain_estimated,omitempty"`
	// GrainSource is "rms", "pgi" or "estimated": where Grain came from.
	GrainSource string `json:"grain_source,omitempty"`
	// Formats are the film sizes the maker sold this stock in: "135"
	// (35 mm), "120"/"220" (medium-format rolls), "4x5"/"8x10" (sheets),
	// read from the film's own data sheet. Empty means unverified: the model
	// then offers 35 mm only rather than guessing a format the film may never
	// have been made in, since the format sets the grain's physical size.
	Formats    []string `json:"formats,omitempty"`
	FormatsSrc string   `json:"formats_src,omitempty"`
	Src        string   `json:"src"`
	URL        string   `json:"url,omitempty"`
	// Grain is the positive-density RMS the model uses: a published RMS
	// × printGain for a negative, pgiGrain for a PGI film, or EstRMS ×
	// printGain for an estimate. GrainSource says which; 0 = none.
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
	// Push: a colour negative's published push families (stops over its
	// ISO, the exposure index they are rated at, and the sheet's page).
	// Empty for every other film and for a negative whose maker publishes
	// none.
	Push []FilmPush `json:"push,omitempty"`
}

// FilmPush is one published push family of a colour negative.
type FilmPush struct {
	Stops float64 `json:"stops"`
	EI    float64 `json:"ei"`
	Src   string  `json:"src"`
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

// PGI conversion. Kodak's scale puts the threshold of perceived grain at
// 25; the patent's fitted logarithmic relation has a slope of 29.64 PGI
// points per decade of visual granularity. We anchor 25 at a positive
// density RMS of 4.0 — a density fluctuation of 0.004, the order of the
// finest colour negative in the library — and convert the published index
// as a perceptual scale. This is an interpretation, not a Kodak RMS
// measurement; the data keeps pgi separate from rms and the UI says PGI.
const (
	pgiThreshold = 25.0
	pgiAnchorRMS = 4.0
	pgiSlope     = 29.64
)

func pgiGrain(pgi int) float64 {
	return pgiAnchorRMS * math.Pow(10, (float64(pgi)-pgiThreshold)/pgiSlope)
}

// Films is the library, with each film's grain on the positive.
func Films() []Film {
	var d struct {
		Films []Film `json:"films"`
	}
	json.Unmarshal(filmsJSON, &d)
	for i := range d.Films {
		f := &d.Films[i]
		switch {
		case f.RMS > 0:
			f.Grain = f.RMS
			f.GrainSource = "rms"
		case f.PGI > 0:
			f.Grain = pgiGrain(f.PGI)
			f.GrainSource = "pgi"
		case f.EstRMS > 0:
			f.Grain = f.EstRMS
			f.GrainEstimated = true
			f.GrainSource = "estimated"
		}
		if f.Kind != "slide" {
			f.Grain *= printGain
		}
		f.Curve = filmCurves()[f.Maker+" "+f.Name]
		f.Color = IsColorFilm(f.Maker + " " + f.Name)
		f.Push = ColorNegPushes(f.Maker + " " + f.Name)
	}
	return d.Films
}
