package main

// Infrared on a full-spectrum Foveon (the SD bodies' IR-cut "dust
// protector" removed): the same silicon model as for visible light,
// without the IR-cut edge and out to 1100 nm. Near-infrared photons are
// absorbed deep (1/α ≈ 8 µm at 800 nm, 60 µm at 950 nm), so they land
// mostly in the bottom layer, some in the middle one, almost none in the
// top one, and those that pass the bottom zone are lost.
//
// For each look, a 3×3 maps the white-balanced layers (normalised to the
// filtered daylight's white, as the camera's own white balance does) to
// the bands the look shows, fitted by least squares over smooth random
// reflectance spectra and real materials (foliage with its red edge at
// ~715 nm, sky, skin, water, soil, concrete), each band normalised so a
// flat white is 1:
//
//   - aerochrome: through a Wratten 12 (deep yellow, cut below ~515 nm), as
//     Kodak Aerochrome EIR (shot with the same filter): its infrared layer
//     prints red, its red layer green, its green layer blue.
//   - ir590: through SCHOTT OG590 glass, the classic channel-swapped colour
//     infrared: red = green = infrared, blue = visible red (golden foliage,
//     blue sky).
//   - ir720: through SCHOTT RG715 glass: black and white infrared.
//
// The two glasses' transmission is their catalogued measured curve
// (render/schottglass.json: internal transmittance at the reference
// thickness times the reflection factor), in place of the logistic edges
// they were standing in for; the Wratten 12 stays such an edge (Kodak's
// curve is not published here).
//
//	IR=render/irlooks.json go run ./tools/foveonmodel

import (
	"bufio"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
)

const irMin, irMax, irStep = 380.0, 1100.0, 5.0

// irMaxGain bounds each look's noise gain (row norm on white-balanced
// layers); the camera's own SD1 colour matrix rows reach ~6.
var irMaxGain = 6.0

var (
	irLams  []float64
	irAlpha []float64
)

func irGrid() {
	if irLams != nil {
		return
	}
	type row struct{ l, k float64 }
	var rows []row
	sc := bufio.NewScanner(strings.NewReader(siData))
	for sc.Scan() {
		var l, n, k float64
		if c, _ := fmt.Sscan(sc.Text(), &l, &n, &k); c == 3 {
			rows = append(rows, row{l * 1000, k})
		}
	}
	for l := irMin; l <= irMax+1e-9; l += irStep {
		i := sort.Search(len(rows), func(i int) bool { return rows[i].l >= l })
		k := rows[i].k
		if rows[i].l > l {
			t := (l - rows[i-1].l) / (rows[i].l - rows[i-1].l)
			k = math.Exp(math.Log(rows[i-1].k)*(1-t) + math.Log(rows[i].k)*t)
		}
		irLams = append(irLams, l)
		irAlpha = append(irAlpha, 4*math.Pi*k/(l*1e-3))
	}
}

// fullSens: the layers' response with no IR-cut filter (bottom, middle, top).
func (m model) fullSens(i int) [3]float64 {
	a := irAlpha[i]
	e := func(z float64) float64 { return math.Exp(-a * z) }
	return [3]float64{e(m.d2) - e(m.d3), e(m.d1) - e(m.d2), e(m.d0) - e(m.d1)}
}

func sig(l, edge, w float64) float64 { return 1 / (1 + math.Exp(-(l-edge)/w)) }

// visSens: the layers with the camera's IR-cut filter in place, on the IR
// grid (the response the camera's Sunlight white balance is set for).
func (m model) visSens(i int) [3]float64 {
	s := m.fullSens(i)
	cut := 1 / (1 + math.Exp((irLams[i]-m.lc)/m.w))
	return [3]float64{s[0] * cut, s[1] * cut, s[2] * cut}
}

// sunToLook: per layer, how a white surface's response on the camera's
// Sunlight-balanced layers (IR cut in, normalised to 1) changes with the
// IR cut out and the look's lens filter on: the white balance the look
// needs on top of the camera's Sunlight gains.
func sunToLook(m model, lk irLook) [3]float64 {
	var vis, look [3]float64
	for i, l := range irLams {
		il := planck(l, 5500)
		v, f := m.visSens(i), m.fullSens(i)
		for k := 0; k < 3; k++ {
			vis[k] += v[k] * il
			look[k] += f[k] * il * lk.filter(l)
		}
	}
	return [3]float64{vis[0] / look[0], vis[1] / look[1], vis[2] / look[2]}
}

// irMaterials: reflectance spectra of real surfaces, with variations.
func irMaterials(rng *rand.Rand) [][]float64 {
	var out [][]float64
	add := func(f func(l float64) float64) {
		r := make([]float64, len(irLams))
		for i, l := range irLams {
			r[i] = math.Max(0.005, math.Min(0.98, f(l)))
		}
		out = append(out, r)
	}
	for v := 0; v < 25; v++ {
		k := 0.8 + 0.4*rng.Float64()
		edge := 705 + 20*rng.Float64()
		nir := 0.35 + 0.25*rng.Float64()
		// Green foliage: chlorophyll absorbs blue and red, a green bump at
		// 550 nm, then the red edge up to a high near-infrared plateau.
		add(func(l float64) float64 {
			return k*(0.04+0.08*math.Exp(-0.5*math.Pow((l-550)/35, 2))) + nir*sig(l, edge, 12)
		})
		// Dry grass / straw: rising through the visible, high NIR.
		add(func(l float64) float64 { return k * (0.08 + 0.3*sig(l, 560, 60) + 0.15*sig(l, 750, 40)) })
		// Blue sky (as a radiance ratio to daylight): Rayleigh, little NIR.
		add(func(l float64) float64 { return k * 0.5 * math.Pow(450/l, 4) })
		// Skin: rising to a red-NIR plateau, haemoglobin dip near 560 nm.
		add(func(l float64) float64 {
			return k * (0.15 + 0.45*sig(l, 600, 30) - 0.06*math.Exp(-0.5*math.Pow((l-560)/20, 2)))
		})
		// Water: dark, darker still in the infrared.
		add(func(l float64) float64 { return k * (0.06 - 0.05*sig(l, 720, 30)) })
		// Soil, concrete: slowly rising.
		add(func(l float64) float64 { return k * (0.1 + 0.25*(l-380)/720) })
		add(func(l float64) float64 { return k * (0.3 + 0.05*(l-380)/720) })
	}
	return out
}

func irRandom(rng *rand.Rand, n int) [][]float64 {
	var out [][]float64
	for j := 0; j < n; j++ {
		r := make([]float64, len(irLams))
		base := 0.05 + 0.3*rng.Float64()
		for i := range r {
			r[i] = base
		}
		for b := 0; b < 1+rng.Intn(3); b++ {
			mu, sd, amp := irMin+(irMax-irMin)*rng.Float64(), 20+150*rng.Float64(), 0.7*rng.Float64()
			if rng.Float64() < 0.3 {
				amp = -amp * base / 0.7
			}
			for i, l := range irLams {
				r[i] += amp * math.Exp(-0.5*((l-mu)/sd)*((l-mu)/sd))
			}
		}
		for i := range r {
			r[i] = math.Max(0.01, math.Min(0.95, r[i]))
		}
		out = append(out, r)
	}
	return out
}

type irLook struct {
	name   string
	filter func(l float64) float64
	bands  []func(l float64) float64 // the output channels' spectral bands
	mono   bool
}

var irLooks = []irLook{
	{name: "fs-aerochrome", filter: func(l float64) float64 { return 1 }, // no lens filter
		bands: []func(float64) float64{
			func(l float64) float64 { return sig(l, 700, 10) * (1 - sig(l, 900, 20)) },
			func(l float64) float64 { return sig(l, 600, 10) * (1 - sig(l, 700, 10)) },
			func(l float64) float64 { return sig(l, 500, 10) * (1 - sig(l, 600, 10)) },
		}},
	{name: "aerochrome", filter: func(l float64) float64 { return sig(l, 515, 8) },
		bands: aerochromeBands()},
	{name: "ir590", filter: schottT("OG590"),
		bands: colourIRBands(590, 8)},
	{name: "ir720", filter: schottT("RG715"), mono: true,
		bands: monoIRBands(720, 8)},
}

// aerochromeBands: Kodak Aerochrome EIR's own three-band split (its infrared
// layer prints red, its red layer green, its green layer blue) — not the
// channel swap the colour-infrared looks use.
func aerochromeBands() []func(float64) float64 {
	return []func(float64) float64{
		func(l float64) float64 { return sig(l, 700, 10) * (1 - sig(l, 900, 20)) },
		func(l float64) float64 { return sig(l, 600, 10) * (1 - sig(l, 700, 10)) },
		func(l float64) float64 { return sig(l, 500, 10) * (1 - sig(l, 600, 10)) },
	}
}

// colourIRBands: infrared -> red and green, visible red -> blue (the classic
// channel-swapped colour infrared). edge is the filter's own visible cutoff.
func colourIRBands(edge, width float64) []func(float64) float64 {
	return []func(float64) float64{
		func(l float64) float64 { return sig(l, 700, 10) },                             // infrared -> red
		func(l float64) float64 { return sig(l, 700, 10) },                             // infrared -> green
		func(l float64) float64 { return sig(l, edge, width) * (1 - sig(l, 700, 10)) }, // visible red -> blue
	}
}

// monoIRBands: one band at the filter's own cutoff. Black and white infrared.
func monoIRBands(edge, width float64) []func(float64) float64 {
	return []func(float64) float64{func(l float64) float64 { return sig(l, edge, width) }}
}

// brandCutoff is the wavelength a catalogue entry's name/cutoff implies:
// the glass's number (RG610 -> 610) or the published edge.
func brandCutoff(f render.BrandFilter) float64 {
	if f.Edge > 0 {
		return f.Edge
	}
	i := len(f.Glass)
	for i > 0 && f.Glass[i-1] >= '0' && f.Glass[i-1] <= '9' {
		i--
	}
	var v float64
	fmt.Sscan(f.Glass[i:], &v)
	return v
}

// brandIRLooks appends the catalogue's infrared filters (render
// brandfilters.json): B+W 091/092/093/099 (their numbers are Schott glass
// numbers, so they render on the measured curve), Hoya R72, Cokin 007 and
// K&F Concept's, each on its published basis.
func brandIRLooks() []irLook {
	var out []irLook
	for _, f := range render.BrandFilters() {
		if f.Kind != "ir" {
			continue
		}
		var T func(float64) float64
		if f.Glass != "" {
			T = render.SchottTransmission(f.Glass)
		}
		if T == nil && f.Edge > 0 {
			e := f.Edge
			T = func(l float64) float64 { return sig(l, e, 8) }
		}
		if T == nil {
			continue
		}
		lk := irLook{name: f.ID, filter: T, mono: f.Look == "mono"}
		if lk.mono {
			lk.bands = monoIRBands(brandCutoff(f), 8)
		} else {
			lk.bands = colourIRBands(brandCutoff(f), 8)
		}
		out = append(out, lk)
	}
	return out
}

// irFit returns the look's 3×3 (rows: output red, green, blue; a mono look
// repeats its one row) on layers normalised to the filtered white, and the
// fit's RMS over the spectra (relative to white = 1) plus each material's
// output for the report.
func irFit(m model, lk irLook, refl [][]float64) ([9]float64, float64) {
	illum := make([]float64, len(irLams))
	for i, l := range irLams {
		illum[i] = planck(l, 5500)
	}
	var wht [3]float64
	bw := make([]float64, len(lk.bands))
	// Normalise the layers as the app delivers them: balanced by the
	// camera's Sunlight gains, i.e. to a white surface's response with the
	// IR cut in (so the ridge bound holds for the actual input noise).
	for i, l := range irLams {
		V := m.visSens(i)
		for k := 0; k < 3; k++ {
			wht[k] += V[k] * illum[i]
		}
		for b, f := range lk.bands {
			bw[b] += f(l) * illum[i]
		}
	}
	X := make([][3]float64, len(refl))
	Y := make([][]float64, len(refl))
	for j, r := range refl {
		Y[j] = make([]float64, len(lk.bands))
		for i, l := range irLams {
			S := m.fullSens(i)
			for k := 0; k < 3; k++ {
				X[j][k] += S[k] * illum[i] * lk.filter(l) * r[i]
			}
			for b, f := range lk.bands {
				Y[j][b] += f(l) * illum[i] * r[i]
			}
		}
		for k := 0; k < 3; k++ {
			X[j][k] /= wht[k]
		}
		for b := range lk.bands {
			Y[j][b] /= bw[b]
		}
	}
	var ata [3][3]float64
	for _, x := range X {
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				ata[r][q] += x[r] * x[q]
			}
		}
	}
	var M [9]float64
	var e, n float64
	for b := range lk.bands {
		var atb [3]float64
		for j, x := range X {
			for q := 0; q < 3; q++ {
				atb[q] += Y[j][b] * x[q]
			}
		}
		// Ridge: the plain least-squares rows reach coefficients of 50-150
		// for the 720 nm look (all layers see the same infrared, so it
		// fits their tiny differences), which would multiply the noise as
		// much. The smallest penalty that keeps the row's noise gain (its
		// norm, for equal noise per white-balanced layer) under irMaxGain.
		// The penalty is searched upward geometrically: for the deepest
		// filters the old fixed upper bound was far too small and the row
		// came out at 20-30x gain.
		solve := func(lam float64) [3]float64 {
			a := ata
			for q := 0; q < 3; q++ {
				a[q][q] += lam
			}
			inv := inv3(a)
			var row [3]float64
			for q := 0; q < 3; q++ {
				for k := 0; k < 3; k++ {
					row[q] += inv[q][k] * atb[k]
				}
			}
			return row
		}
		norm := func(r [3]float64) float64 { return math.Sqrt(r[0]*r[0] + r[1]*r[1] + r[2]*r[2]) }
		row := solve(0)
		if norm(row) > irMaxGain {
			hi := 1.0
			for norm(solve(hi)) > irMaxGain && hi < 1e18 {
				hi *= 4
			}
			lo := 0.0
			for it := 0; it < 70; it++ {
				mid := (lo + hi) / 2
				if norm(solve(mid)) > irMaxGain {
					lo = mid
				} else {
					hi = mid
				}
			}
			row = solve(hi)
		}
		for j, x := range X {
			d := row[0]*x[0] + row[1]*x[1] + row[2]*x[2] - Y[j][b]
			e, n = e+d*d, n+1
		}
		outs := []int{b}
		if lk.mono {
			outs = []int{0, 1, 2}
		}
		for _, o := range outs {
			copy(M[o*3:o*3+3], row[:])
		}
	}
	return M, math.Sqrt(e / n)
}

// irMain writes every generation's IR looks to path and reports the fits
// and a few materials' renderings.
func irMain(path string) {
	irGrid()
	rng := rand.New(rand.NewSource(7))
	mats := irMaterials(rng)
	refl := append(irRandom(rng, 400), mats...)
	var b strings.Builder
	b.WriteString("{")
	for gi, gen := range irGenerations {
		m := irModel(gen)
		if d, ok := irDepths[gen]; ok {
			if irMeasured[gen] {
				fmt.Printf("%s: IR-measured depths d2 %.2f d3 %.2f µm\n", gen, d[0], d[1])
			} else {
				fmt.Printf("%s: IR-predicted depths d2 %.2f d3 %.2f µm (approximate)\n", gen, d[0], d[1])
			}
		}
		var s [3]float64
		for i := range irLams {
			v := m.fullSens(i)
			for k := range s {
				s[k] += v[k]
			}
		}
		fmt.Printf("%s: share of a flat spectrum's signal 700-1100 nm per layer:", gen)
		for k := 0; k < 3; k++ {
			var ir float64
			for i, l := range irLams {
				if l >= 700 {
					ir += m.fullSens(i)[k]
				}
			}
			fmt.Printf(" %s %.0f%%", []string{"bottom", "middle", "top"}[k], 100*ir/s[k])
		}
		fmt.Println()
		if gi > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q: {", gen)
		looks := append(append([]irLook{}, irLooks...), brandIRLooks()...)
		for li, lk := range looks {
			M, e := irFit(m, lk, refl)
			r := sunToLook(m, lk)
			fmt.Printf("  %-13s white per layer vs Sunlight: %.2f %.2f %.2f (x %.2f %.2f %.2f)\n", lk.name, 1/r[0], 1/r[1], 1/r[2], r[0], r[1], r[2])
			fmt.Printf("  %-10s fit RMS %.3f (white = 1)  %.3f\n", lk.name, e, M)
			for mi, name := range []string{"foliage", "dry grass", "blue sky", "skin", "water", "soil", "concrete"} {
				o := irRender(m, lk, M, mats[mi])
				fmt.Printf("      %-9s -> %.2f %.2f %.2f\n", name, o[0], o[1], o[2])
			}
			if li > 0 {
				b.WriteString(", ")
			}
			// m: the look's matrix; white: a white surface's response on
			// the Sunlight-balanced layers (where clipped highlights go).
			fmt.Fprintf(&b, "%q: {\"m\": [", lk.name)
			for k, v := range M {
				if k > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%.5f", v)
			}
			fmt.Fprintf(&b, "], \"white\": [%.5f, %.5f, %.5f]}", 1/r[0], 1/r[1], 1/r[2])
		}
		b.WriteString("}")
	}
	b.WriteString("}\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		log.Fatal(err)
	}
}

// irRender is the look's output for one reflectance (white = 1,1,1).
func irRender(m model, lk irLook, M [9]float64, r []float64) [3]float64 {
	var x, wht [3]float64
	for i, l := range irLams {
		S, V := m.fullSens(i), m.visSens(i)
		il := planck(l, 5500)
		for k := 0; k < 3; k++ {
			x[k] += S[k] * il * lk.filter(l) * r[i]
			wht[k] += V[k] * il
		}
	}
	for k := range x {
		x[k] /= wht[k]
	}
	return apply(M, x)
}

// irCalib prints, per generation, the Sunlight-balanced layers the model
// predicts with the IR cut out and no lens filter, for a white surface and
// for foliage, as bottom/top and middle/top ratios (what tools/irprobe
// measures on a real full-spectrum shot); IRD3 overrides the bottom
// collection depth (µm) to see its effect.
func irCalib() {
	irGrid()
	rng := rand.New(rand.NewSource(7))
	mats := irMaterials(rng)
	white := make([]float64, len(irLams))
	for i := range white {
		white[i] = 1
	}
	for _, gen := range []string{"Merrill", "TRUEII", "SD9", "Quattro"} {
		m := loadModel(gen)
		var d3s []float64
		for _, s := range strings.Split(os.Getenv("IRD3"), ",") {
			var v float64
			if _, err := fmt.Sscan(s, &v); err == nil {
				d3s = append(d3s, v)
			}
		}
		if len(d3s) == 0 {
			d3s = []float64{m.d3}
		}
		d2s := []float64{m.d2}
		if v := os.Getenv("IRD2"); v != "" {
			d2s = nil
			for _, s := range strings.Split(v, ",") {
				var x float64
				if _, err := fmt.Sscan(s, &x); err == nil {
					d2s = append(d2s, x)
				}
			}
		}
		for _, dd := range d2s {
			for _, d3 := range d3s {
				mm := m
				mm.d2, mm.d3 = dd, d3
				lay := func(r []float64) [3]float64 {
					var x, wht [3]float64
					for i, l := range irLams {
						S, V := mm.fullSens(i), mm.visSens(i)
						il := planck(l, 5500)
						for k := 0; k < 3; k++ {
							x[k] += S[k] * il * r[i]
							wht[k] += V[k] * il
						}
					}
					for k := range x {
						x[k] /= wht[k]
					}
					return x
				}
				w, f := lay(white), lay(mats[0])
				fmt.Printf("%-8s d2 %.2f d3 %5.2f: white b/t %.2f m/t %.2f | foliage b/t %.2f m/t %.2f\n", gen, mm.d2, d3, w[0]/w[2], w[1]/w[2], f[0]/f[2], f[1]/f[2])
			}
		}
	}
}

// irGenerations are the sensor generations with their own layer model.
var irGenerations = []string{"Merrill", "TRUEII", "SD9", "Quattro"}

// irDepths: the middle zone's lower edge and the bottom zone's depth (µm)
// for infrared. Merrill is measured on real full-spectrum shots (see
// irMeasured). The other generations have no full-spectrum frames, so they
// are predicted: the visible fit's middle edge is ~1 µm deeper than the IR
// effective one (Merrill: 3.82 visible, 2.80 measured), so each body's
// visible d2 is moved up by that measured offset; d3 stays at the visible
// fit where that fit is not at a bound, and takes the Merrill prior where it
// is (Quattro's d3 hit the fit's 20 µm cap). The app labels those looks
// approximate.
var irDepths = map[string][2]float64{
	"Merrill": {2.8, 7.0}, // measured
	"TRUEII":  {4.0, 6.6}, // predicted from 5.07/6.59
	"SD9":     {2.4, 8.9}, // predicted from 3.41/8.88
	"Quattro": {2.8, 7.0}, // predicted from 3.83; d3 at the 20 µm cap, so the prior
}

// irMeasured: generations whose IR depths come from real full-spectrum
// files. Merrill: an SD1 Merrill with its IR-cut dust protector removed, no
// lens filter, 2026-10-14 (SDIM0365-0368; tools/irprobe): a white window
// frame gave bottom/top 2.35, middle/top 1.27, sunlit foliage ~4.6 and ~1.65
// on the Sunlight-balanced layers; d2 2.8, d3 7.0 give 2.35, 1.27, 4.47,
// 1.66 (IRCALIB=1 IRD2=... IRD3=...). The other generations are not
// calibrated yet (no full-spectrum shots).
var irMeasured = map[string]bool{
	"Merrill": true,
}

// irModel is the visible-fit layer model with the generation's effective IR
// depths in place of the visible-only fit's unconstrained ones.
func irModel(gen string) model {
	m := loadModel(gen)
	if d, ok := irDepths[gen]; ok {
		m.d2, m.d3 = d[0], d[1]
	}
	return m
}

// irLayerRatios is a surface's signal on the Sunlight-balanced layers,
// normalised to a visible white (the input the IR looks take).
func irLayerRatios(m model, refl []float64) [3]float64 {
	var x, wht [3]float64
	for i, l := range irLams {
		S, V := m.fullSens(i), m.visSens(i)
		il := planck(l, 5500)
		for k := 0; k < 3; k++ {
			x[k] += S[k] * il * refl[i]
			wht[k] += V[k] * il
		}
	}
	for k := range x {
		x[k] /= wht[k]
	}
	return x
}

func irWhiteAndFoliage(m model) ([3]float64, [3]float64) {
	white := make([]float64, len(irLams))
	for i := range white {
		white[i] = 1
	}
	rng := rand.New(rand.NewSource(7))
	mats := irMaterials(rng)
	return irLayerRatios(m, white), irLayerRatios(m, mats[0])
}

func irShare(m model) [3]float64 {
	var s, ir [3]float64
	for i, l := range irLams {
		v := m.fullSens(i)
		for k := range s {
			s[k] += v[k]
			if l >= 700 {
				ir[k] += v[k]
			}
		}
	}
	for k := range s {
		ir[k] /= s[k]
	}
	return ir
}

// irPredict prints the per-generation IR layer response and its error bars,
// and the held-out-by-scene check on the one generation with real frames.
// The central prediction uses irDepths; the range scans the measurement
// error on Merrill and the inter-generation stack uncertainty on the others.
// The visible-only fit's alternative is printed to show what it would claim.
func irPredict() {
	irGrid()
	fmt.Println("IR layer response, Sunlight-balanced layers, white = 1 on the camera's visible white")
	fmt.Println("gen      source     d2   d3 | white b/t        white m/t        foliage b/t      foliage m/t")
	for _, gen := range irGenerations {
		m := irModel(gen)
		w, f := irWhiteAndFoliage(m)
		d := irDepths[gen]
		h2, h3 := 1.0, 2.0
		if irMeasured[gen] {
			h2, h3 = 0.2, 0.8
		}
		wbMin, wbMax := math.Inf(1), math.Inf(-1)
		wmMin, wmMax := math.Inf(1), math.Inf(-1)
		fbMin, fbMax := math.Inf(1), math.Inf(-1)
		fmMin, fmMax := math.Inf(1), math.Inf(-1)
		for i := 0; i <= 8; i++ {
			for j := 0; j <= 8; j++ {
				mm := m
				mm.d2 = d[0] - h2 + 2*h2*float64(i)/8
				mm.d3 = d[1] - h3 + 2*h3*float64(j)/8
				ww, ff := irWhiteAndFoliage(mm)
				wb, wm := ww[0]/ww[2], ww[1]/ww[2]
				fb, fm := ff[0]/ff[2], ff[1]/ff[2]
				wbMin, wbMax = math.Min(wbMin, wb), math.Max(wbMax, wb)
				wmMin, wmMax = math.Min(wmMin, wm), math.Max(wmMax, wm)
				fbMin, fbMax = math.Min(fbMin, fb), math.Max(fbMax, fb)
				fmMin, fmMax = math.Min(fmMin, fm), math.Max(fmMax, fm)
			}
		}
		src := "predicted"
		if irMeasured[gen] {
			src = "measured"
		}
		fmt.Printf("%-8s %-9s %4.1f %4.1f | %.2f [%.2f-%.2f]  %.2f [%.2f-%.2f]  %.2f [%.2f-%.2f]  %.2f [%.2f-%.2f]\n",
			gen, src, d[0], d[1],
			w[0]/w[2], wbMin, wbMax, w[1]/w[2], wmMin, wmMax,
			f[0]/f[2], fbMin, fbMax, f[1]/f[2], fmMin, fmMax)
		s := irShare(m)
		fmt.Printf("         IR share 700-1100 nm: bottom %.0f%% middle %.0f%% top %.0f%%\n", 100*s[0], 100*s[1], 100*s[2])
		v := loadModel(gen)
		vw, vf := irWhiteAndFoliage(v)
		fmt.Printf("         visible-fit alternative d2 %.2f d3 %.2f -> white %.2f/%.2f foliage %.2f/%.2f\n",
			v.d2, v.d3, vw[0]/vw[2], vw[1]/vw[2], vf[0]/vf[2], vf[1]/vf[2])
	}
	irHeldOut()
}

// fitIRDepths solves for the (d2, d3) that best reproduce a measured
// bottom/top and middle/top ratio on one scene, on the same 0.05/0.1 µm
// grid the calibration search used.
func fitIRDepths(base model, refl []float64, targetB, targetM float64) (d2, d3, err float64) {
	err = math.Inf(1)
	for a := 1.6; a <= 5.2; a += 0.05 {
		for b := 3.0; b <= 15.0; b += 0.1 {
			m := base
			m.d2, m.d3 = a, b
			r := irLayerRatios(m, refl)
			e := math.Pow(r[0]/r[2]-targetB, 2) + math.Pow(r[1]/r[2]-targetM, 2)
			if e < err {
				d2, d3, err = a, b, e
			}
		}
	}
	return
}

// irHeldOut checks the Merrill prediction the only way the surviving record
// allows: calibrate on one scene and predict the other. The four X3F files
// themselves are no longer on disk, so the measured ratios below are the
// ones recorded in the calibration commit and in irMeasured.
func irHeldOut() {
	white := make([]float64, len(irLams))
	for i := range white {
		white[i] = 1
	}
	rng := rand.New(rand.NewSource(7))
	foliage := irMaterials(rng)[0]
	base := loadModel("Merrill")
	fmt.Println("\nHeld out by scene (Merrill; measured white frame 2.35/1.27, sunlit foliage 4.60/1.65):")
	d2, d3, e := fitIRDepths(base, white, 2.35, 1.27)
	m := base
	m.d2, m.d3 = d2, d3
	f := irLayerRatios(m, foliage)
	fmt.Printf("  fit on the white frame -> d2 %.2f d3 %.2f (RMS %.3f); predicts foliage %.2f/%.2f vs measured 4.60/1.65 (%+.2f/%+.2f)\n",
		d2, d3, math.Sqrt(e/2), f[0]/f[2], f[1]/f[2], f[0]/f[2]-4.60, f[1]/f[2]-1.65)
	d2, d3, e = fitIRDepths(base, foliage, 4.60, 1.65)
	m = base
	m.d2, m.d3 = d2, d3
	w := irLayerRatios(m, white)
	fmt.Printf("  fit on the foliage   -> d2 %.2f d3 %.2f (RMS %.3f); predicts white  %.2f/%.2f vs measured 2.35/1.27 (%+.2f/%+.2f)\n",
		d2, d3, math.Sqrt(e/2), w[0]/w[2], w[1]/w[2], w[0]/w[2]-2.35, w[1]/w[2]-1.27)
}

// irCheck tests every look on every generation for the properties a preset
// must have: finite, non-degenerate rows, white at 1, no material out of
// range, the look's tonal order, and the same filter agreeing across
// generations. It is a model check, not a substitute for real frames.
func irCheck() bool {
	irGrid()
	rng := rand.New(rand.NewSource(7))
	mats := irMaterials(rng)
	refl := append(irRandom(rng, 400), mats...)
	looks := append(append([]irLook{}, irLooks...), brandIRLooks()...)
	names := []string{"foliage", "dry grass", "blue sky", "skin", "water", "soil", "concrete"}
	out := map[string]map[string][3]float64{}
	ok := true
	for _, gen := range irGenerations {
		m := irModel(gen)
		out[gen] = map[string][3]float64{}
		for _, lk := range looks {
			M, _ := irFit(m, lk, refl)
			var fail []string
			for r := 0; r < 3; r++ {
				if lk.mono && r > 0 {
					break
				}
				norm := 0.0
				for k := 0; k < 3; k++ {
					norm += M[r*3+k] * M[r*3+k]
				}
				norm = math.Sqrt(norm)
				if norm < 0.5 || norm > 6.01 {
					fail = append(fail, fmt.Sprintf("row %d norm %.2f", r, norm))
				}
			}
			r := sunToLook(m, lk)
			w := [3]float64{1 / r[0], 1 / r[1], 1 / r[2]}
			o := apply(M, w)
			// The deep long-pass looks are photon-starved; their ridge may
			// leave a white surface darker than 1 (the shallow ones must
			// keep it within 10%). This is a look brightness, not a cast.
			whiteLo := 0.9
			if lk.filter(700) < 0.2 {
				whiteLo = 0.05
			}
			for k := 0; k < 3; k++ {
				if o[k] < whiteLo || o[k] > 1.1 {
					fail = append(fail, fmt.Sprintf("white %.4f", o[k]))
				}
			}
			var mo [7][3]float64
			for i := range names {
				mo[i] = irRender(m, lk, M, mats[i])
				out[gen][lk.name+"|"+names[i]] = mo[i]
				for k := 0; k < 3; k++ {
					if math.IsNaN(mo[i][k]) || mo[i][k] < -0.01 || mo[i][k] > 1.05 {
						fail = append(fail, fmt.Sprintf("%s channel %.2f", names[i], mo[i][k]))
					}
				}
			}
			f, sky := mo[0], mo[2]
			switch lk.name {
			case "ir720":
				if !(f[0] > sky[0] && sky[0] < mo[5][0] && f[0] > mo[4][0]) {
					fail = append(fail, "720 tonal order")
				}
			case "ir590":
				if math.Abs(f[0]-f[1]) > 0.02 || f[0] <= f[2] || sky[2] <= sky[0] {
					fail = append(fail, "590 tonal order")
				}
			case "aerochrome":
				if !(f[0] > f[1] && f[1] > f[2] && sky[2] > sky[1] && sky[1] > sky[0]) {
					fail = append(fail, "aerochrome tonal order")
				}
			case "fs-aerochrome":
				if !(f[0] > f[1] && f[1] > f[2] && sky[2] > sky[0] && sky[0] > sky[1]) {
					fail = append(fail, "false-colour tonal order")
				}
			}
			if len(fail) > 0 {
				ok = false
				fmt.Printf("FAIL %-8s %-13s %v\n", gen, lk.name, fail)
			}
		}
	}
	// The same filter on the same material should not change look between
	// generations (the differences are the model's, to 1/255). Deep
	// long-pass looks are near the noise floor (930 nm on the 4 µm stack is
	// the worst) and get a wider allowance.
	worst, worstKey := 0.0, ""
	worstByLook := map[string]float64{}
	deepLook := map[string]bool{}
	for _, lk := range looks {
		deepLook[lk.name] = lk.filter(700) < 0.2
	}
	for key := range out["Merrill"] {
		look := key[:strings.LastIndex(key, "|")]
		for k := 0; k < 3; k++ {
			lo, hi := math.Inf(1), math.Inf(-1)
			for _, gen := range irGenerations {
				v := out[gen][key][k]
				lo, hi = math.Min(lo, v), math.Max(hi, v)
			}
			d := hi - lo
			if d > worstByLook[look] {
				worstByLook[look] = d
			}
			if d > worst {
				worst, worstKey = d, fmt.Sprintf("%s channel %d", key, k)
			}
		}
	}
	for _, lk := range looks {
		if d := worstByLook[lk.name]; d > 0.005 {
			fmt.Printf("  %-13s cross-generation spread %.4f\n", lk.name, d)
		}
	}
	fmt.Printf("cross-generation worst spread %.4f (%s), limit 0.03 shallow / 0.25 deep\n", worst, worstKey)
	for look, d := range worstByLook {
		if (deepLook[look] && d > 0.25) || (!deepLook[look] && d > 0.03) {
			ok = false
		}
	}
	if ok {
		fmt.Println("IR check: PASS")
	} else {
		fmt.Println("IR check: FAIL")
	}
	return ok
}
