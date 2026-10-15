package render

import (
	"math"
	"testing"
)

func srgbHue(r, g, b float64) float64 {
	e := func(x float64) float64 { return float64(enc(float32(math.Max(0, math.Min(1, x))))) }
	r, g, b = e(r), e(g), e(b)
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	if mx == mn {
		return 0
	}
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/(mx-mn), 6)
	case g:
		h = (b-r)/(mx-mn) + 2
	default:
		h = (r-g)/(mx-mn) + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

// The sensor -> film mapping (fitLayerMap) on saturated reds: the film fed
// through the sensor model must land near the film fed its exact spectral
// exposures (the old 3×3 turned a red tulip orange). "true hue" is the
// colorimetric hue; the films themselves render reds warmer than that.
func TestLayerMapReds(t *testing.T) {
	sp := spectral()
	S := layerSens("Merrill")
	edge := func(e float64) []float64 {
		r := make([]float64, len(sp.lams))
		for i, l := range sp.lams {
			r[i] = 0.04 + 0.71/(1+math.Exp(-(l-e)/8))
		}
		return r
	}
	white := make([]float64, len(sp.lams))
	for i := range white {
		white[i] = 1
	}
	srgb := func(r []float64) (o [3]float64) {
		var xyz [3]float64
		for i, l := range sp.lams {
			c := cmf1931(l)
			for k := 0; k < 3; k++ {
				xyz[k] += c[k] * sp.illum[i] * r[i]
			}
		}
		for k := 0; k < 3; k++ {
			o[k] = xyzToLinSRGB[k*3]*xyz[0] + xyzToLinSRGB[k*3+1]*xyz[1] + xyzToLinSRGB[k*3+2]*xyz[2]
		}
		return
	}
	w := srgb(white)
	for _, key := range []string{"Fujifilm Fujichrome Velvia 100", "Fujifilm Fujichrome Provia 400F", "Kodak Portra 160 / 400 / 800"} {
		f := colorFilmFor(key, "Merrill")
		var sens map[string][][2]float64
		if d := colorFilmData_()[key]; d != nil {
			sens = d.Sens
		} else {
			sens = colorNegs().negs[key].Sens
		}
		var ws, wf [3]float64
		fs := make([][3]float64, len(sp.lams))
		for i, l := range sp.lams {
			fs[i] = [3]float64{sensAt(sens["R"], l), sensAt(sens["G"], l), sensAt(sens["B"], l)}
			for k := 0; k < 3; k++ {
				ws[k] += S[i][k] * sp.illum[i]
				wf[k] += fs[i][k] * sp.illum[i]
			}
		}
		for _, e := range []float64{580, 590, 600, 610} {
			r := edge(e)
			var p, h [3]float64
			for i := range sp.lams {
				ee := sp.illum[i] * r[i]
				for k := 0; k < 3; k++ {
					p[k] += S[i][k] * ee
					h[k] += fs[i][k] * ee
				}
			}
			for k := 0; k < 3; k++ {
				p[k] /= ws[k]
				h[k] /= wf[k]
			}
			c := srgb(r)
			for k := 0; k < 3; k++ {
				c[k] /= w[k]
			}
			a1, a2, a3 := f.fromExposure(h)
			b1, b2, b3 := f.apply(float32(p[0]), float32(p[1]), float32(p[2]))
			ex, se := srgbHue(float64(a1), float64(a2), float64(a3)), srgbHue(float64(b1), float64(b2), float64(b3))
			// Past ~605 nm the sensor's layer ratios no longer change with
			// the red (layermap.go): logged, not held to the bar.
			if d := math.Abs(math.Mod(se-ex+540, 360) - 180); d > 8 && e <= 600 {
				t.Errorf("%s edge %.0f: through the sensor hue %.0f, exact %.0f", key, e, se, ex)
			}
			t.Logf("%-32s edge %.0f: true hue %3.0f | film(exact exposure) %3.0f  rgb %.3f %.3f %.3f | film(sensor) %3.0f rgb %.3f %.3f %.3f",
				key, e, srgbHue(c[0], c[1], c[2]), srgbHue(float64(a1), float64(a2), float64(a3)), a1, a2, a3, srgbHue(float64(b1), float64(b2), float64(b3)), b1, b2, b3)
		}
	}
}
