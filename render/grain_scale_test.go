package render

// Film grain must be the same grain at every view size (task 57). The
// reference is the full-resolution export: a view at a coarser pitch must
// carry what that export carries after an area/box reduction, both in std and
// in how it survives a further 2× and 4× reduction.

import (
	"fmt"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// t57Area reduces a float field by an exact area box.
func t57Area(src []float32, sw, sh, dw, dh int) []float32 {
	dst := make([]float32, dw*dh)
	sx := float64(sw) / float64(dw)
	sy := float64(sh) / float64(dh)
	for y := 0; y < dh; y++ {
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		for x := 0; x < dw; x++ {
			x0, x1 := float64(x)*sx, float64(x+1)*sx
			var sum, wsum float64
			for yy := int(math.Floor(y0)); yy < int(math.Ceil(y1)) && yy < sh; yy++ {
				wy := math.Min(y1, float64(yy+1)) - math.Max(y0, float64(yy))
				if wy <= 0 {
					continue
				}
				for xx := int(math.Floor(x0)); xx < int(math.Ceil(x1)) && xx < sw; xx++ {
					wx := math.Min(x1, float64(xx+1)) - math.Max(x0, float64(xx))
					if wx <= 0 {
						continue
					}
					sum += float64(src[yy*sw+xx]) * wx * wy
					wsum += wx * wy
				}
			}
			dst[y*dw+x] = float32(sum / wsum)
		}
	}
	return dst
}

func t57Sd(v []float32) float64 {
	var s, s2 float64
	for _, x := range v {
		s += float64(x)
		s2 += float64(x) * float64(x)
	}
	n := float64(len(v))
	m := s / n
	return math.Sqrt(math.Max(0, s2/n-m*m))
}

// t57GrainField builds one film octave field at the given view width. W and H
// are the pixels of the region; scale sets the view's pitch, exactly as the
// app's width does.
func t57GrainField(t *testing.T, filmName string, w, h int, scale float64) (*filmOp, []float32) {
	t.Helper()
	f, ok := FilmByKey(filmName)
	if !ok {
		t.Fatalf("%s missing", filmName)
	}
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: scale, Frame: 4704, NativeFrame: 4704, Raw: true, ISO: 100}
	for i := range l.Pix {
		l.Pix[i] = 0.2
	}
	b := NewBase(l)
	p := preset.Params{Film: filmName, Grain: f.Grain, FilmWidth: 36, Mono: f.BW, GrainSet: true}
	op := b.newFilm(p, f.BW)
	if op == nil || op.noise == nil {
		t.Fatalf("%s: no grain", filmName)
	}
	if f.BW {
		return op, op.noise
	}
	out := make([]float32, w*h)
	for i := range out {
		out[i] = op.noise[i*3]
	}
	return op, out
}

func TestGrainScaleConsistency(t *testing.T) {
	// One region of the film, sampled at three pitches: 1:1 (7.65 µm),
	// 1600 px wide (22.5 µm) and 600 px wide (60 µm). The pixel counts keep
	// the same film area, so the 1:1 field can be area-reduced onto the
	// coarse grids.
	const (
		w1, h1 = 1200, 900
		w2, h2 = 408, 306
		w3, h3 = 153, 115
	)
	for _, name := range []string{
		"Kodak Tri-X Pan 400 (TX)",
		"Fujifilm Fujichrome Velvia 50",
		"Kodak Portra 400",
		"Ilford HP5 Plus",
	} {
		_, full := t57GrainField(t, name, w1, h1, 1)
		f1, _ := t57GrainField(t, name, 64, 64, 1)
		sd1 := t57Sd(full)
		if math.Abs(sd1/float64(f1.sigma)-1) > 0.02 {
			t.Errorf("%s: 1:1 field sd %.5f, model %.5f", name, sd1, f1.sigma)
		}
		for _, tc := range []struct {
			w, h  int
			scale float64
		}{
			{w2, h2, 1600.0 / 4704.0},
			{w3, h3, 600.0 / 4704.0},
		} {
			f2, fresh := t57GrainField(t, name, tc.w, tc.h, tc.scale)
			reduced := t57Area(full, w1, h1, tc.w, tc.h)
			sdF, sdR := t57Sd(fresh), t57Sd(reduced)
			ratio := sdF / sdR
			fmt.Printf("GRAINSCALE %-32s %4dpx pitch %5.1fµm: fresh %.5f reduced %.5f ratio %.3f (model %.5f)\n",
				name, tc.w, 36e3/(4704*tc.scale), sdF, sdR, ratio, f2.sigma)
			if ratio < 0.90 || ratio > 1.10 {
				t.Errorf("%s at %d px: fresh %.5f, 1:1 reduced %.5f, ratio %.3f, want ±10%%", name, tc.w, sdF, sdR, ratio)
			}
			// The look: the field must also survive a further 2× and 4×
			// reduction like the reduced export does.
			for _, n := range []int{2, 4} {
				if tc.w/n < 2 || tc.h/n < 2 {
					continue
				}
				fr := t57Area(fresh, tc.w, tc.h, tc.w/n, tc.h/n)
				rr := t57Area(reduced, tc.w, tc.h, tc.w/n, tc.h/n)
				r := t57Sd(fr) / t57Sd(rr)
				fmt.Printf("GRAINSCALE   %-32s %4dpx /%d: fresh %.5f reduced %.5f ratio %.3f\n", name, tc.w, n, t57Sd(fr), t57Sd(rr), r)
				if r < 0.85 || r > 1.15 {
					t.Errorf("%s at %d px /%d: fresh %.5f, reduced %.5f, ratio %.3f, want ±15%%", name, tc.w, n, t57Sd(fr), t57Sd(rr), r)
				}
			}
		}
	}
}

// TestGrainFullResolutionUnchanged pins the 1:1 field to the old model: the
// full-resolution export is the calibration reference and must not move.
func TestGrainFullResolutionUnchanged(t *testing.T) {
	for _, name := range []string{
		"Kodak Tri-X Pan 400 (TX)",
		"Fujifilm Fujichrome Velvia 50",
		"Kodak Portra 400",
		"Ilford HP5 Plus",
	} {
		f, _ := FilmByKey(name)
		op, _ := t57GrainField(t, name, 64, 64, 1)
		// The old recipe: every octave at or above the pixel is spatial,
		// everything finer is independent. At n=1 this is also the new one.
		sizes, weights := grainSizes(f.Kind, float64(f.ISO), 0)
		if f.BW && f.Kind == "cn" {
			sizes, weights = grainSizes("neg", float64(f.ISO), 0)
		}
		var v48 float64
		for i := range sizes {
			v48 += float64(weights[i]) * grainAperture(sizes[i], grainApertureL)
		}
		if v48 <= 0 {
			t.Fatalf("%s: no aperture variance", name)
		}
		pt := 36e3 / 4704.0
		A := f.Grain / 1000 / math.Sqrt(v48)
		var want float64
		for i := range sizes {
			s := sizes[i]
			if s >= pt {
				want += A * A * float64(weights[i])
			} else {
				want += A * A * float64(weights[i]) * grainAperture(s, pt)
			}
		}
		if math.Abs(float64(op.sigma)/math.Sqrt(want)-1) > 1e-5 {
			t.Errorf("%s: 1:1 sigma %.8f, old model %.8f", name, op.sigma, math.Sqrt(want))
		}
	}
}
