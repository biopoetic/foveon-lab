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
		func(l float64) float64 { return sig(l, 700, 10) },                              // infrared -> red
		func(l float64) float64 { return sig(l, 700, 10) },                              // infrared -> green
		func(l float64) float64 { return sig(l, edge, width) * (1 - sig(l, 700, 10)) },  // visible red -> blue
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
			lo, hi := 0.0, ata[0][0]+ata[1][1]+ata[2][2]
			for it := 0; it < 60; it++ {
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
	for gi, gen := range []string{"Merrill", "TRUEII", "SD9", "Quattro"} {
		m := loadModel(gen)
		if c, ok := irCalibrated[gen]; ok {
			m.d2, m.d3 = c[0], c[1]
			fmt.Printf("%s: IR-calibrated depths d2 %.2f d3 %.2f µm\n", gen, m.d2, m.d3)
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

// irCalibrated: the middle zone's lower edge and the bottom zone's depth
// (µm) measured on real full-spectrum shots, which visible light cannot
// pin down (it barely reaches below ~4 µm; the visible fit left the bottom
// zone at 15.8 µm, predicting twice the infrared a real SD1 gives).
// Merrill: an SD1 Merrill with its IR-cut dust protector removed, no lens
// filter, 2026-10-14 (SDIM0365-0368; tools/irprobe): a white window frame
// gave bottom/top 2.35, middle/top 1.27, sunlit foliage ~4.6 and ~1.65
// on the Sunlight-balanced layers; d2 2.8, d3 7.0 give 2.35, 1.27, 4.47,
// 1.66 (IRCALIB=1 IRD2=... IRD3=...). The other generations are not
// calibrated yet (no full-spectrum shots).
var irCalibrated = map[string][2]float64{
	"Merrill": {2.8, 7.0},
}
