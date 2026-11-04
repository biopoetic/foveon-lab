package render

// SPP's Monochrome section (task 50): the mixer, the measured base tone, the
// toning offsets, the Shading Effect and the monochrome grain.

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func monoFlat(w, h int, r, g, b float32) *Linear {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Raw: false}
	for i := 0; i < w*h; i++ {
		l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = r, g, b
	}
	return l
}

func monoCC() *[9]float64 {
	var cc [9]float64
	cc[0], cc[4], cc[8] = 1, 1, 1
	return &cc
}

func monoParams() preset.Params {
	p := preset.Neutral()
	p.Mono, p.MonoMixSet = true, true
	p.MonoR, p.MonoG, p.MonoB = 1.0/3, 1.0/3, 1.0/3
	return p
}

func ptrMix(w [3]float64) *[3]float64 { return &w }

func TestMonoFreshMixRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *[3]float64
		want [3]float64
	}{
		{"absent header", nil, [3]float64{1.0 / 3, 1.0 / 3, 1.0 / 3}},
		{"stored zeros", ptrMix([3]float64{0, 0, 0}), [3]float64{0, 0, 1}},
		{"stored equal", ptrMix([3]float64{0.33, 0.33, 0.34}), [3]float64{0.33, 0.33, 0.34}},
		{"stored B", ptrMix([3]float64{0, 0, 1}), [3]float64{0, 0, 1}},
		{"negative", ptrMix([3]float64{-1, 0, 2}), [3]float64{1.0 / 3, 1.0 / 3, 1.0 / 3}},
	} {
		got := MonoFreshMix(tc.in)
		for i := range got {
			if math.Abs(got[i]-tc.want[i]) > 1e-9 {
				t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestMonoMixerDefaultIsSPPFreshOpen(t *testing.T) {
	// No header field and no compensation: SPP's fallback is equal thirds.
	w := monoWeights(preset.Params{Mono: true}, Compensation{})
	for i := range w {
		if math.Abs(w[i]-1.0/3) > 1e-12 {
			t.Errorf("default mix %v, want equal thirds", w)
		}
	}
	// A stored all-zero 3.0 field opens as B.
	b := ptrMix([3]float64{0, 0, 0})
	w = monoWeights(preset.Params{Mono: true}, Compensation{MonoDefault: b})
	if w != [3]float64{0, 0, 1} {
		t.Errorf("stored-zero default mix %v, want the top layer only", w)
	}
	// An explicit preset mixer wins over the photo's fresh-open mix.
	w = monoWeights(preset.Params{Mono: true, MonoMixSet: true, MonoR: 1.0 / 3, MonoG: 1.0 / 3, MonoB: 1.0 / 3}, Compensation{MonoDefault: b})
	for i := range w {
		if math.Abs(w[i]-1.0/3) > 1e-12 {
			t.Errorf("Reset mix %v, want equal thirds", w)
		}
	}
	w = monoWeights(preset.Params{Mono: true, MonoMixSet: true, MonoB: 1}, Compensation{MonoDefault: ptrMix([3]float64{1.0 / 3, 1.0 / 3, 1.0 / 3})})
	if w[0] != 0 || w[1] != 0 || w[2] != 1 {
		t.Errorf("pure B mix %v", w)
	}
}

// The photo's fresh-open mix must reach both the CPU op and the GPU recipe;
// the shader itself already reads uMonoW, so this pins the shared rule at the
// recipe boundary (task 55).
func TestMonoFreshMixReachesGPURecipe(t *testing.T) {
	p := preset.Params{Mono: true}
	c := Compensation{CC: monoCC(), SatFactor: 1, MonoDefault: ptrMix([3]float64{0, 0, 1})}
	l := monoFlat(4, 4, 0.8, 0.5, 0.2)
	l.MonoLayers = make([]float32, 4*4*3)
	for i := range l.MonoLayers {
		l.MonoLayers[i] = 0.5
	}
	b := NewBase(l)
	op := newPixOp(p, c)
	r := NewGPURecipe(b, p, c)
	if op.monoW != [3]float32{0, 0, 1} || r.MonoW != op.monoW {
		t.Errorf("CPU %v GPU %v, want the photo's B mix", op.monoW, r.MonoW)
	}
}

func TestMonoMixerMakesTheGrey(t *testing.T) {
	// With an identity camera matrix the layers are linear sRGB; the equal
	// mix is the mean, then SPP's base gamma.
	l := monoFlat(4, 4, 0.8, 0.5, 0.2)
	b := NewBase(l)
	out := Render(b, monoParams(), Compensation{CC: monoCC(), SatFactor: 1})
	mean := float32(0.5)
	want := uint8(enc(float32(math.Pow(float64(mean), monoBaseGamma)))*255 + 0.5)
	if out.Pix[1] != want {
		t.Errorf("equal mix = %d, want %d", out.Pix[1], want)
	}
	p := monoParams()
	p.MonoR, p.MonoG, p.MonoB = 0, 0, 1
	out = Render(b, p, Compensation{CC: monoCC(), SatFactor: 1})
	want = uint8(enc(float32(math.Pow(0.2, monoBaseGamma)))*255 + 0.5)
	if out.Pix[1] != want {
		t.Errorf("top-layer mix = %d, want %d", out.Pix[1], want)
	}
}

func TestMonoToneIsTheMeasuredOffset(t *testing.T) {
	l := monoFlat(4, 4, 0.5, 0.5, 0.5)
	b := NewBase(l)
	base := Render(b, monoParams(), Compensation{CC: monoCC(), SatFactor: 1})
	for name, want := range map[string][3]int{
		"Sepia": {11, -2, -22},
		"Blue":  {-12, 2, 18},
		"Red":   {13, -6, -11},
	} {
		p := monoParams()
		p.MonoTone = name
		out := Render(b, p, Compensation{CC: monoCC(), SatFactor: 1})
		for c := 0; c < 3; c++ {
			got := int(out.Pix[c]) - int(base.Pix[c])
			if d := got - want[c]; d < -1 || d > 1 {
				t.Errorf("%s channel %d: %+d, want %+d", name, c, got, want[c])
			}
		}
	}
}

func TestMonoToneClipsAtBlack(t *testing.T) {
	// In deep shadow the negative offsets clip at 0, as SPP's exports do.
	l := monoFlat(4, 4, 0.002, 0.002, 0.002)
	b := NewBase(l)
	p := monoParams()
	p.MonoTone = "Sepia"
	out := Render(b, p, Compensation{CC: monoCC(), SatFactor: 1})
	if out.Pix[2] != 0 {
		t.Errorf("Sepia blue in deep shadow = %d, want 0", out.Pix[2])
	}
}

func TestMonoShadingDarkensTheCorner(t *testing.T) {
	l := monoFlat(200, 200, 0.5, 0.5, 0.5)
	b := NewBase(l)
	p := monoParams()
	p.MonoShadeRadius, p.MonoShadeAmount = .5, 1
	out := Render(b, p, Compensation{CC: monoCC(), SatFactor: 1})
	base := Render(b, monoParams(), Compensation{CC: monoCC(), SatFactor: 1})
	at := func(x, y int) int { return int(out.Pix[(y*200+x)*4+1]) - int(base.Pix[(y*200+x)*4+1]) }
	if d := at(100, 100); d != -1 {
		t.Errorf("centre %+d, want the measured -1 offset", d)
	}
	if d := at(1, 1); d > -30 {
		t.Errorf("corner %+d, want a strong darkening", d)
	}
}

func TestMonoGrainGrowsWithSize(t *testing.T) {
	l := monoFlat(160, 160, 0.5, 0.5, 0.5)
	b := NewBase(l)
	base := Render(b, monoParams(), Compensation{CC: monoCC(), SatFactor: 1})
	var prev float64
	for _, size := range []float64{.1, .5, 1} {
		p := monoParams()
		p.MonoGrainSize, p.MonoGrainRough = size, .5
		out := Render(b, p, Compensation{CC: monoCC(), SatFactor: 1})
		var s, s2 float64
		for i := 0; i < 160*160; i++ {
			d := float64(int(out.Pix[i*4+1]) - int(base.Pix[i*4+1]))
			s += d
			s2 += d * d
		}
		n := 160.0 * 160
		sd := math.Sqrt(s2/n - (s/n)*(s/n))
		if sd <= prev {
			t.Errorf("size %.1f SD %.2f not above the smaller size's %.2f", size, sd, prev)
		}
		prev = sd
		for i := 0; i < 400; i++ {
			if out.Pix[i*4] != out.Pix[i*4+1] || out.Pix[i*4+1] != out.Pix[i*4+2] {
				t.Fatalf("grain is not one layer at %d", i)
			}
		}
	}
}

func TestMonoFilmIgnoresTheSPPControls(t *testing.T) {
	f, ok := FilmByKey("Kodak Tri-X Pan 400 (TX)")
	if !ok {
		t.Fatal("Tri-X missing")
	}
	l := monoFlat(64, 48, 0.2, 0.2, 0.2)
	b := NewBase(l)
	plain := preset.Params{Film: f.Maker + " " + f.Name, Grain: f.Grain, FilmWidth: 36, Mono: true}
	loaded := plain
	loaded.MonoMixSet = true
	loaded.MonoR, loaded.MonoG, loaded.MonoB = 0, 0, 1
	loaded.MonoTone = "Sepia"
	loaded.MonoShadeRadius, loaded.MonoShadeAmount = .5, 1
	loaded.MonoGrainSize, loaded.MonoGrainRough = 1, 1
	a := Render(b, plain, Compensation{})
	c := Render(b, loaded, Compensation{})
	for i := range a.Pix {
		if a.Pix[i] != c.Pix[i] {
			t.Fatalf("a film preset changed with the SPP mono controls at byte %d: %d vs %d", i, a.Pix[i], c.Pix[i])
		}
	}
}

func TestMonoControlsLeaveColourAlone(t *testing.T) {
	l := monoFlat(16, 16, 0.3, 0.2, 0.1)
	b := NewBase(l)
	a := Render(b, preset.Neutral(), Compensation{CC: monoCC(), SatFactor: 1})
	p := preset.Neutral()
	p.MonoMixSet = true
	p.MonoR, p.MonoG, p.MonoB = 0, 0, 1
	p.MonoTone = "Sepia"
	p.MonoShadeRadius, p.MonoShadeAmount = 1, 1
	p.MonoGrainSize, p.MonoGrainRough = 1, 1
	c := Render(b, p, Compensation{CC: monoCC(), SatFactor: 1})
	for i := range a.Pix {
		if a.Pix[i] != c.Pix[i] {
			t.Fatalf("a colour render changed at byte %d", i)
		}
	}
}

func TestMonoGrainSupersedesManualGrain(t *testing.T) {
	l := monoFlat(48, 40, 0.3, 0.3, 0.3)
	b := NewBase(l)
	a := monoParams()
	a.MonoGrainSize, a.MonoGrainRough = .5, .5
	a.Grain = 25
	c := monoParams()
	c.MonoGrainSize, c.MonoGrainRough = .5, .5
	ra := Render(b, a, Compensation{CC: monoCC(), SatFactor: 1})
	rc := Render(b, c, Compensation{CC: monoCC(), SatFactor: 1})
	for i := range ra.Pix {
		if ra.Pix[i] != rc.Pix[i] {
			t.Fatalf("manual grain was added on top of SPP's mono grain at byte %d", i)
		}
	}
	// The GPU recipe must report the same: the film grain is off, the SPP
	// grain is the one that runs.
	r := NewGPURecipe(b, a, Compensation{CC: monoCC(), SatFactor: 1})
	if r.SGrain == nil || r.GrainFine != 0 || len(r.GrainOct) != 0 {
		t.Errorf("GPU recipe: sGrain %v, film fine %v, film octaves %v", r.SGrain != nil, r.GrainFine, r.GrainOct)
	}
}

// TestMonoGrainGPUAgrees mirrors web/gpu.js's monochrome grain: the same
// lattice, the same seed and the same fine gaussian.
func TestMonoGrainGPUAgrees(t *testing.T) {
	l := monoFlat(120, 80, 0.5, 0.5, 0.5)
	b := NewBase(l)
	p := monoParams()
	p.MonoGrainSize, p.MonoGrainRough = .5, .5
	cpu := b.newMonoGrain(p, true)
	r := NewGPURecipe(b, p, Compensation{CC: monoCC(), SatFactor: 1})
	if r.SGrain == nil || len(r.SGrain.Oct) != len(cpu.oct) {
		t.Fatalf("recipe grain %+v, CPU %+v", r.SGrain, cpu.oct)
	}
	for _, i := range []int{0, 1, 37, 133, 199, 1234, 5000, 9599} {
		x, y := i%120, i/120
		var n float32
		for j, o := range r.SGrain.Oct {
			n += o.Amp * t47GPUSample(x, y, float64(o.Cell), r.SGrain.Seed^0x1234567^uint32(j)*0x9e3779b9)
		}
		n += r.SGrain.Fine * t47GPUGauss(i, r.SGrain.Seed^0x1234567)
		if d := math.Abs(float64(n - cpu.noise[i])); d > 2e-5 {
			t.Errorf("pixel %d: GPU %.7f, CPU %.7f", i, n, cpu.noise[i])
		}
	}
}

// BenchmarkMonoRender is the mono path over a 1400 px crop with the mixer,
// toning, shading and grain all on. BenchmarkColourRender is the same frame
// in colour for comparison.
func BenchmarkMonoRender(b *testing.B) {
	l := monoFlat(1400, 1400, 0.2, 0.2, 0.2)
	base := NewBase(l)
	p := monoParams()
	p.MonoTone = "Sepia"
	p.MonoShadeRadius, p.MonoShadeAmount = .5, 1
	p.MonoGrainSize, p.MonoGrainRough = .5, .5
	c := Compensation{CC: monoCC(), SatFactor: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderInto(base, p, c, func(int, float32, float32, float32) {})
	}
}

func BenchmarkColourRender(b *testing.B) {
	l := monoFlat(1400, 1400, 0.2, 0.2, 0.2)
	base := NewBase(l)
	c := Compensation{CC: monoCC(), SatFactor: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderInto(base, preset.Neutral(), c, func(int, float32, float32, float32) {})
	}
}

func TestMonoGrainRecipeJSONNamesMatchGPUJS(t *testing.T) {
	// web/gpu.js reads sGrain.sigma/.mean/.q/.seed/.fine/.oct; untagged
	// fields went out capitalised and the preview drew no grain.
	b, err := json.Marshal(GPUMonoGrain{Sigma: 1, Mean: 1, Q: 1, Seed: 1, Fine: 1, Oct: []GPUGrainOct{{Cell: 1, Amp: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sigma", "mean", "q", "seed", "fine", "oct"} {
		if _, ok := m[k]; !ok {
			t.Errorf("recipe sGrain lacks %q: %s", k, b)
		}
	}
}
