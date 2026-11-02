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
	"fmt"
	"math"
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
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
	// GrainEstimated: Grain is an estimate, not a published number. A
	// colour negative's G is now always estimated on one scale (see
	// cnGrain), even when its maker published an RMS or a PGI.
	GrainEstimated bool `json:"grain_estimated,omitempty"`
	// GrainSource is "rms", "pgi" or "estimated": where Grain came from.
	GrainSource string `json:"grain_source,omitempty"`
	// GrainNote explains an estimated grain value (the published number it
	// was built from and the class it was placed in).
	GrainNote string `json:"grain_note,omitempty"`
	// CNClass is a colour negative's grain class: "consumer", "pro" or
	// "value" (see cnClass). Empty for every other kind of film.
	CNClass string `json:"cn_class,omitempty"`
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
	// printGain for an estimate. For a colour negative it is a rendered look
	// on a screen, estimated on one ISO/class scale, not the maker's
	// published negative number (which stays in RMS or PGI).
	// GrainSource says which; 0 = none.
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

// One scale for colour negatives.
//
// RMS and PGI are not the same measurement and cannot be mixed directly:
// Fuji's integer "RMS 4" for Superia 100, 200 and 400 is a rounded negative
// granularity, while Kodak's PGI is a perceptual print-graininess scale at a
// fixed 4x6 print, whose logarithmic relation has a 29.64-point decade. Fed
// through the old conversion, Ultramax 400 came out at G 47.7 against
// Superia X-TRA 400's 9.33 for the same speed and class — five times apart.
//
// A colour negative's grain is therefore estimated on ONE scale from its ISO
// and its class. The ISO-100 anchors are the published values that are fine
// enough to trust (Fuji's RMS 4 -> a consumer ISO 100 of 4.00); every class
// then uses the same exponent, 0.45, so speed drives the grain:
//
//	negative RMS = base * (ISO/100)^0.45
//	consumer: 4.00 -> RMS 4.00 at 100, 7.46 at 400, 10.20 at 800, 13.93 at 1600
//	pro:      3.10 -> RMS 3.10 at 100, 5.78 at 400, 7.90 at 800, 10.80 at 1600
//	value:    5.50 -> RMS 5.50 at 100, 10.26 at 400, 14.03 at 800
//
// The professional lines sit at 0.775x the consumer value (about 25% finer)
// at every speed. The consumer 400 result, 7.46, is above the rounded "RMS 4"
// Fuji prints for Superia X-TRA 400 and near the "RMS 5" some sheets print;
// the published digits are too coarse to rank a 400 against another 400, so
// speed and class set the ordering within that rounding. G is the positive-
// density RMS (x printGain).
//
// This G is a LOOK ON A SCREEN, not the maker's published negative number.
// The maker's RMS or PGI is a measurement of the negative; the model's G is
// the granularity the rendered positive carries, so it can be shown on a
// display at a chosen view. The published RMS or PGI stays beside it in the
// data and in the UI. The classes are the grain families the data supports:
// the professional lines (Portra, Ektar, Fuji Pro, Agfacolor Portrait) are
// finer; the 3M/Ferrania "Scotch" films are the published RMS 6-7 value
// line; everything else is consumer. Every colour negative's G is marked
// estimated. Slides keep their own published scale, and black-and-white
// films keep the published/estimated RMS path.
func cnClass(f Film) string {
	switch f.Name {
	case "Agfacolor Portrait 160",
		"Fujicolor Pro 160S", "Fujicolor Pro 160C", "Fujicolor Pro 400H",
		"Fujicolor Pro 800Z", "Fujicolor NPZ 800",
		"Portra 160", "Portra 400", "Portra 800", "Ektar 100":
		return "pro"
	}
	if f.Maker == "3M / Imation (made by Ferrania)" {
		return "value"
	}
	return "consumer"
}

func cnGrain(iso int, class string) float64 {
	// The ISO-100 anchors are published; the exponent is one speed law for
	// every class (see the comment above), so a faster stock is always
	// grainier and the professional lines stay about 25% finer.
	base := 4.00 // consumer
	switch class {
	case "pro":
		base = 3.10
	case "value":
		base = 5.50
	}
	iso = max(25, min(6400, iso))
	return base * math.Pow(float64(iso)/100, 0.45) * printGain
}

// cnPushGrain is the colour-negative push law: a film rated 2^n faster has
// the grain of the same emulsion at that ISO, so the target grows by 2^0.45n
// — the same exponent as cnGrain. At +2 that is 1.87x, which puts Portra 800
// +2 (EI 3200) at G about 34, near the 34 a 34-RMS look gives on the slider.
// Black and white, slides and manual grain keep the task 48 push factor.
func cnPushGrain(push float64) float64 { return math.Pow(2, 0.45*push) }

// FillFilmGrain settles a named film's grain once, when a preset is loaded or
// a film is chosen. A presets file that names a film but predates the grain
// model has Grain 0 and GrainSet false; it gets the film's grain here and is
// marked set. A parameter set that already has GrainSet true (a slider at 0,
// say) is left alone. The renderer deliberately does not do this: otherwise
// slider 0 could never turn grain off.
func FillFilmGrain(p *preset.Params) bool {
	if p == nil || p.Film == "" || p.GrainSet {
		return false
	}
	f, ok := FilmByKey(p.Film)
	if !ok {
		return false
	}
	if p.Grain <= 0 {
		// The UI's slider step is 0.5 RMS.
		p.Grain = math.Round(f.Grain*2) / 2
	}
	p.GrainSet = true
	return true
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
		case f.Kind == "cn":
			f.CNClass = cnClass(*f)
			f.Grain = cnGrain(f.ISO, f.CNClass)
			f.GrainSource = "estimated"
			f.GrainEstimated = true
			const look = "G is a rendered look on a screen, not the published negative number"
			switch {
			case f.RMS > 0:
				f.GrainNote = fmt.Sprintf("published RMS %g is a rounded negative granularity; %s %.1f is estimated on the one colour-negative scale from ISO %d and the %s class", f.RMS, look, f.Grain, f.ISO, f.CNClass)
			case f.PGI > 0:
				f.GrainNote = fmt.Sprintf("published PGI %d (Kodak's perceptual print scale); %s %.1f is estimated on the one colour-negative scale from ISO %d and the %s class", f.PGI, look, f.Grain, f.ISO, f.CNClass)
			default:
				f.GrainNote = fmt.Sprintf("%s %.1f is estimated on the one colour-negative scale from ISO %d and the %s class", look, f.Grain, f.ISO, f.CNClass)
			}
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
		// cnGrain already returns the positive-density G (it applies
		// printGain itself, because it is one scale rather than a
		// published negative RMS).
		if f.Kind != "slide" && f.Kind != "cn" {
			f.Grain *= printGain
		}
		f.Curve = filmCurves()[f.Maker+" "+f.Name]
		f.Color = IsColorFilm(f.Maker + " " + f.Name)
		f.Push = ColorNegPushes(f.Maker + " " + f.Name)
	}
	return d.Films
}
