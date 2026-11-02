package render

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func flat(w, h int, v float32) *Linear {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := range l.Pix {
		l.Pix[i] = v
	}
	return l
}

// A smaller aperture sees more density noise, but not the full Selwyn
// 1/√a: once a clump is larger than the pixel its contrast no longer grows
// as the pixel shrinks. The old per-pixel model was the 1/√a limit; the new
// one is flatter, which is the point of the clump tail.
func TestGrainAperture(t *testing.T) {
	p := preset.Params{Grain: 17}
	a := NewBase(flat(4704, 64, 0.2)).newFilm(p, false)
	b := NewBase(flat(2352, 64, 0.2)).newFilm(p, false)
	r := float64(a.sigma / b.sigma)
	if r < 1.25 || r > 2.05 {
		t.Errorf("sigma ratio %.3f, want between 1.25 and the Selwyn 2", r)
	}
	if a.sigma <= 0 || b.sigma <= 0 {
		t.Fatalf("no grain: %g %g", a.sigma, b.sigma)
	}
}

// The model is normalized so a 48 µm aperture measures the requested G.
func TestGrainCalibration48(t *testing.T) {
	const want = 0.017
	f := NewBase(flat(750, 500, 0.214)).newFilm(preset.Params{Grain: 17}, true)
	var s, s2 float64
	n := 750 * 500
	for i := 0; i < n; i++ {
		r, _, _ := f.grain(i, 0.214, 0.214, 0.214)
		d := -math.Log10(float64(r) / 0.214)
		s += d
		s2 += d * d
	}
	sd := math.Sqrt(s2/float64(n) - (s/float64(n))*(s/float64(n)))
	if math.Abs(sd-want) > 0.05*want {
		t.Errorf("density sd at 48 µm %.4f, want %.4f", sd, want)
	}
}

// The measured density noise of a mid-grey matches sigma; black stays black.
func TestGrainStats(t *testing.T) {
	f := NewBase(flat(256, 256, 0.2)).newFilm(preset.Params{Grain: 17}, true)
	var s, s2 float64
	n := 256 * 256
	for i := 0; i < n; i++ {
		r, _, _ := f.grain(i, 0.2, 0.2, 0.2)
		d := -math.Log10(float64(r) / 0.2)
		s, s2 = s+d, s2+d*d
	}
	sd := math.Sqrt(s2/float64(n) - (s/float64(n))*(s/float64(n)))
	u := float64(enc(0.2))
	want := float64(f.sigma) * 2 * math.Sqrt(u*(1-u))
	if math.Abs(sd-want) > 0.05*want {
		t.Errorf("density sd %.4f, want %.4f", sd, want)
	}
	if r, _, _ := f.grain(5, 0, 0, 0); r != 0 {
		t.Errorf("black got grain: %g", r)
	}
}

// A fast film's grain is larger and stronger than a slow film's, at the
// same view size; a pushed film gets more grain still.
func TestGrainSpeedAndPush(t *testing.T) {
	b := NewBase(flat(1200, 800, 0.2))
	slow, _ := FilmByKey("Ilford Pan F Plus")
	fast, _ := FilmByKey("Ilford Delta 3200")
	a := b.newFilm(preset.Params{Film: "Ilford Pan F Plus", Grain: slow.Grain}, true)
	z := b.newFilm(preset.Params{Film: "Ilford Delta 3200", Grain: fast.Grain}, true)
	if z.sigma < 2*a.sigma {
		t.Errorf("ISO 3200 sigma %.4f is not well above ISO 50 %.4f", z.sigma, a.sigma)
	}
	pushed := b.newFilm(preset.Params{Film: "Ilford Delta 3200", Grain: fast.Grain, Push: 2}, true)
	if pushed.sigma <= z.sigma {
		t.Errorf("push did not add grain: %.4f -> %.4f", z.sigma, pushed.sigma)
	}
}

// The renderer never invents grain from a named film: a preset that stores
// Grain 0 is grain-free, whatever film it names. This is the regression test
// for the slider at 0 (on 36bea55 the renderer filled the film's grain in).
func TestFilmGrainIsNotFilledByRenderer(t *testing.T) {
	b := NewBase(flat(1200, 800, 0.2))
	for _, p := range []preset.Params{
		{Film: "Ilford Pan F Plus"},
		{Film: "Kodak Portra 800"},
		{Film: "Fujifilm Fujichrome Velvia 50"},
	} {
		if f := b.newFilm(p, p.Film != "Fujifilm Fujichrome Velvia 50"); f != nil && f.sigma != 0 {
			t.Errorf("%s with Grain 0 built grain %.3f", p.Film, f.sigma)
		}
	}
}

// A film's grain is filled once, by the loader or the film picker, and a
// slider at 0 afterwards means off.
func TestFillFilmGrain(t *testing.T) {
	f800, _ := FilmByKey("Kodak Portra 800")
	p := preset.Params{Film: "Kodak Portra 800"}
	if !FillFilmGrain(&p) {
		t.Fatal("an old preset naming a film did not get its grain")
	}
	want := math.Round(f800.Grain*2) / 2
	if p.Grain != want || !p.GrainSet {
		t.Errorf("filled grain %.2f set=%v, want %.2f true", p.Grain, p.GrainSet, want)
	}
	// Loading it again does not change a value the user has since moved.
	p.Grain = 3
	FillFilmGrain(&p)
	if p.Grain != 3 {
		t.Errorf("a settled grain was overwritten: %.2f", p.Grain)
	}
	// An explicit 0 is settled: leave it alone and build no grain.
	off := preset.Params{Film: "Kodak Portra 800", GrainSet: true}
	if FillFilmGrain(&off) {
		t.Error("FillFilmGrain touched an explicitly set 0")
	}
	if f := NewBase(flat(1200, 800, 0.2)).newFilm(off, false); f != nil && f.sigma != 0 {
		t.Errorf("slider 0 still built grain %.3f", f.sigma)
	}
	// NoGrain (the Compare films tiles) still wins over a set grain.
	full := preset.Params{Film: "Kodak Portra 800", Grain: 40, GrainSet: true, NoGrain: true}
	if f := NewBase(flat(1200, 800, 0.2)).newFilm(full, false); f != nil && f.sigma != 0 {
		t.Errorf("NoGrain still built grain %.3f", f.sigma)
	}
}

// The slider scales the settled grain linearly.
func TestFilmGrainSliderScales(t *testing.T) {
	b := NewBase(flat(1200, 800, 0.2))
	f, _ := FilmByKey("Ilford Pan F Plus")
	a := b.newFilm(preset.Params{Film: "Ilford Pan F Plus", Grain: f.Grain, GrainSet: true}, true)
	if a == nil || a.sigma == 0 {
		t.Fatal("no grain")
	}
	d := b.newFilm(preset.Params{Film: "Ilford Pan F Plus", Grain: 2 * f.Grain, GrainSet: true}, true)
	if d.sigma < 1.9*a.sigma || d.sigma > 2.1*a.sigma {
		t.Errorf("doubling the slider gave %.3f x the grain, want 2", d.sigma/a.sigma)
	}
}

// Grain fades at paper white and near black instead of laying sand on the
// ends. On 36bea55 the near-white patch carried 58% of the mid-grey SD.
func TestGrainToneFades(t *testing.T) {
	f, _ := FilmByKey("Kodak Portra 400")
	sd := func(v float32) float64 {
		b := NewBase(flat(800, 533, v))
		p := preset.Params{Film: "Kodak Portra 400", Grain: f.Grain, GrainSet: true}
		out := make([]float32, b.W*b.H*3)
		renderInto(b, p, Compensation{}, func(i int, r, g, bl float32) {
			out[i*3], out[i*3+1], out[i*3+2] = r, g, bl
		})
		var s, s2 float64
		n := float64(b.W * b.H)
		for i := 0; i < b.W*b.H; i++ {
			l := 0.2126*float64(enc(out[i*3])) + 0.7152*float64(enc(out[i*3+1])) + 0.0722*float64(enc(out[i*3+2]))
			s, s2 = s+l, s2+l*l
		}
		return math.Sqrt(s2/n - (s/n)*(s/n))
	}
	mid, white, black := sd(0.2), sd(0.95), sd(0.02)
	if white > 0.45*mid {
		t.Errorf("near-white grain %.4f is %.0f%% of mid %.4f, want under 45%%", white, 100*white/mid, mid)
	}
	if black > 0.45*mid {
		t.Errorf("near-black grain %.4f is %.0f%% of mid %.4f, want under 45%%", black, 100*black/mid, mid)
	}
}

// The push law is the colour-negative speed law only. Colour negatives get
// 2^0.45n (so Portra 800 +2 is G 32-36); B&W, slides and manual grain keep
// the task 48 2^0.30n and are not moved by this task.
func TestPushLawByKind(t *testing.T) {
	const push = 2.0
	if got, want := pushGrain("cn", true, push), math.Pow(2, 0.9); math.Abs(got-want) > 1e-9 {
		t.Errorf("colour-negative push %.4f, want %.4f", got, want)
	}
	if got, want := pushGrain("neg", true, push), math.Pow(2, 0.6); math.Abs(got-want) > 1e-9 {
		t.Errorf("black-and-white push %.4f, want the unchanged %.4f", got, want)
	}
	if got, want := pushGrain("slide", true, push), math.Pow(2, 0.6); math.Abs(got-want) > 1e-9 {
		t.Errorf("slide push %.4f, want the unchanged %.4f", got, want)
	}
	if got, want := pushGrain("cn", false, push), math.Pow(2, 0.6); math.Abs(got-want) > 1e-9 {
		t.Errorf("manual-grain push %.4f, want the unchanged %.4f", got, want)
	}
}

// Halation: light spreads into red (a little green), never blue.
func TestHalationRed(t *testing.T) {
	l := flat(101, 101, 0)
	c := (50*101 + 50) * 3
	l.Pix[c], l.Pix[c+1], l.Pix[c+2] = 50, 50, 50
	l.Scale, l.Frame = 1, 4704 // a 101 px window of a full SD1 frame: 7.65 µm pixels
	b := NewBase(l)
	f := b.newFilm(preset.Params{Halation: 1}, false)
	r, g, bl := f.addHalo(50*101+60, 0, 0, 0)
	if r <= 0 || g >= r || bl != 0 {
		t.Errorf("halo at 10 px: %g %g %g", r, g, bl)
	}
}
