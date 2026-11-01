package render

// SPP sharpens every image it develops, at Sharpness 0 too: measured
// (tools/sharpfit, SD1 against SPP's own exports, detail energy per
// frequency band on sRGB luminance) as an unsharp mask of a Gaussian sigma
// 1.0 px on the full-size image, amount 2.7 at ISO 100 (SDIM0260, 0042:
// every band within ~5% of SPP's, from 2.9x short in the finest), falling
// with ISO as SPP's noise reduction takes the fine detail away (the
// SDIM0358-0363 ISO 200-6400 series, each fitted on its own). SPP sharpens
// the SD15 far less (SDIM1240-1245, ISO 100-3200: 0.2-0.7, fitted after
// the black-level fix). The DP Merrills, though Merrill sensors, are
// sharpened differently: a finer mask (sigma 0.7) that stays strong at high
// ISO (the DP1 Merrill studio series ISO 100-6400, each frame fitted on its
// own, October 2026). With the SD1's mask they came out ~20% oversharp at
// ISO 100 and short of SPP's detail from ISO 800 (detail kept vs the ISO 100
// frame at ISO 800, SPP 0.90, ours 0.68). Generations not measured yet
// (SD9/SD10, Quattro) get none.
// A reduced view gets the full-size filter's gain at its own Nyquist
// frequency, as a sigma-1 mask at that size.

import (
	"image"
	"math"
	"os"
	"strconv"
)

const sppSharpSigma = 1.0

// usmClamp limits an unsharp-mask result to the 3×3 local range of the
// unsharpened image, plus half that range. A step edge's overshoot is then at
// most the edge's own height, so the mask cannot paint the wide bright/dark
// halos a strong amount leaves: on the DP1 Merrill pine shadow our fixed-basis
// residual was 3.5x SPP's with a matching slope (task 20, part 3). The clamp
// changes nothing where the fitted amount is small (TRUEII/DP/Quattro) and
// only touches the Merrill bodies' strong masks: 21-frame detail study, pine
// slope 3.804 -> 3.570 and residual 0.0397 -> 0.0374 (SPP 0.0396), alley
// 0.0540 -> 0.0494, SD1 0.0317 -> 0.0292. FOVEON_USM_NOCLAMP=1 disables it.
var usmClamp = os.Getenv("FOVEON_USM_NOCLAMP") == ""

// sharpAmountScale is a research multiplier on the fitted amounts
// (FOVEON_SHARP_SCALE=0.667), used to measure one amount change without a
// second build. 1 (the environment unset) ships.
var sharpAmountScale = func() float64 {
	if v := os.Getenv("FOVEON_SHARP_SCALE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 1
}()

// sppSharpByISO: the fitted amount per sharpening key (SharpKey) and ISO
// (log2 ISO 100 = 0 … 6400 = 6); sppSharpSigmaOf gives the mask's sigma.
//
// Task 23: the Merrill amounts were retuned to two thirds of the task-21
// fit as one change. On the 4 DPM/SD1 frames whose halos task 21 measured,
// the fixed-basis 1-px slope fell toward SPP's (pine 3.570 -> 3.133 against
// 2.423; alley 2.217 -> 1.965 against 1.561; SD1 2.225 -> 1.964 against
// 1.433) and the residual fell (pine 0.0374 -> 0.0291, SPP 0.0396). The
// 183-photo spp-check on the DPM and SD1 groups improved (DPM 6.510 -> 6.400,
// SD1 3.913 -> 3.624; 20 photos better by >0.05, none worse by >0.30), and
// the DPM ISO ladders' fine luma/chroma noise fell 5-13% with the 2-8 px
// detail unchanged. FOVEON_SHARP_SCALE still scales these amounts for a
// research run; the default (1) is the shipped fit.
var sppSharpByISO = map[string][]float64{
	"Merrill": {1.8, 1.67, 1.6, 0.93, 0.67, 0.53, 0.33},
	"DPM":     {1.67, 2.13, 2.2, 1.93, 1.4, 1.07, 0.93},
	"TRUEII":  {0.3, 0.2, 0.7, 0.5, 0.4, 0.2, 0.2},
}

// sppSharpSigmaOf is the full-size mask's sigma for a sharpening key.
func sppSharpSigmaOf(key string) float64 {
	if key == "DPM" {
		return 0.7
	}
	return sppSharpSigma
}

// SharpKey is the key SPP's sharpening is looked up by for a calibration
// group: its sensor generation, except the DP Merrills, which SPP
// sharpens differently from the SD1.
func SharpKey(group string) string {
	if group == "DPM" {
		return "DPM"
	}
	return SensorGeneration(group)
}

// sppFullAmount is the full-size amount for a photo of sensor at iso (0 =
// 100); 0 for a generation not measured.
func sppFullAmount(iso float64, sensor string) float64 {
	tab := sppSharpByISO[sensor]
	if tab == nil {
		return 0
	}
	if iso <= 0 {
		iso = 100
	}
	steps := make([]float64, len(tab))
	for i := range steps {
		steps[i] = float64(i)
	}
	return interp(steps, tab, math.Log2(iso/100)) * sharpAmountScale
}

// sppAmount is the amount of a sigma-1 mask at a view of scale s that
// matches the full-size mask's gain at the view's Nyquist frequency (the
// model before v0.4.51; sppView replaced it).
func sppAmount(s, iso float64, sensor string) float32 {
	a := sppFullAmount(iso, sensor)
	if s >= 1 {
		return float32(a)
	}
	hp := func(sigma, f float64) float64 { return 1 - math.Exp(-2*math.Pi*math.Pi*sigma*sigma*f*f) }
	sg := sppSharpSigmaOf(sensor)
	return float32(a * hp(sg, 0.5*s) / hp(sg, 0.5))
}

// sppView is the mask (amount and kernel) for a view of scale s whose
// gain matches, at every frequency the view holds, SPP's full-size mask
// seen through the reduction: a·(1-K(f·s)) for view frequency f. A
// sigma-1 mask matched only at the view's Nyquist (sppAmount) boosted
// the middle frequencies ~2.5x too much on a quarter-size preview: fine
// detail 1.25-1.47x SPP's (1-2 px band ~1.35x) on ISO 200 SD1 photos,
// once the previews were resampled exactly. Least squares over the
// view's band for kernel sigmas 0.5-1.0 (smaller ones fit as a
// near-identity blur with amounts of 30-100).
func sppView(s, iso float64, sensor string) (float32, [7]float32) {
	a := sppFullAmount(iso, sensor)
	full := gaussKernel(sppSharpSigmaOf(sensor))
	if a == 0 || s >= 1 {
		return float32(a), full
	}
	resp := func(k [7]float32, f float64) float64 { // 1 - the blur's gain
		g := float64(k[3])
		for j := 1; j <= 3; j++ {
			g += 2 * float64(k[3+j]) * math.Cos(2*math.Pi*f*float64(j))
		}
		return 1 - g
	}
	bestE, bestA, bestK := math.Inf(1), 0.0, full
	for sg := 0.5; sg <= 1.0001; sg += 0.05 {
		k := gaussKernel(sg)
		var th, hh, tt float64
		for i := 1; i <= 50; i++ {
			f := 0.5 * float64(i) / 50
			t, h := a*resp(full, f*s), resp(k, f)
			th, hh, tt = th+t*h, hh+h*h, tt+t*t
		}
		if hh <= 0 {
			continue
		}
		av := th / hh
		if e := tt - av*th; e < bestE {
			bestE, bestA, bestK = e, av, k
		}
	}
	return float32(bestA), bestK
}

// gaussKernel is a normalised Gaussian kernel of the given sigma (radius 3).
func gaussKernel(sigma float64) [7]float32 {
	var k [7]float32
	var sum float32
	for i := range k {
		x := float64(i - 3)
		k[i] = float32(math.Exp(-x * x / (2 * sigma * sigma)))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	return k
}

// gauss1 is a normalised Gaussian kernel of sigma 1 (radius 3).
var gauss1 = gaussKernel(1)

// usmPlanes sharpens interleaved channels (n per pixel, the first 3
// sharpened) given as floats: v + a·(v − gauss(v)), edges clamped. Each
// channel goes through its own plane: a horizontal pass (no clamping away
// from the edges) and a vertical pass accumulated row by row.
func usmPlanes(v []float32, w, h, n int, a float32, k [7]float32) []float32 {
	out := make([]float32, len(v))
	copy(out, v)
	for c := 0; c < 3; c++ {
		pl := make([]float32, w*h)
		parallelRows(w*h, func(i0, i1 int) {
			for i := i0; i < i1; i++ {
				pl[i] = v[i*n+c]
			}
		})
		tmp := make([]float32, w*h)
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row, t := pl[y*w:(y+1)*w], tmp[y*w:(y+1)*w]
				for x := 0; x < w; x++ {
					if x >= 3 && x < w-3 {
						r := row[x-3 : x+4]
						t[x] = k[0]*r[0] + k[1]*r[1] + k[2]*r[2] + k[3]*r[3] + k[4]*r[4] + k[5]*r[5] + k[6]*r[6]
						continue
					}
					var s float32
					for j := -3; j <= 3; j++ {
						s += k[j+3] * row[min(w-1, max(0, x+j))]
					}
					t[x] = s
				}
			}
		})
		var lo3, hi3 []float32
		if usmClamp {
			lo3, hi3 = make([]float32, w*h), make([]float32, w*h)
			parallelRows(h, func(y0, y1 int) {
				for y := y0; y < y1; y++ {
					for x := 0; x < w; x++ {
						lo, hi := float32(math.Inf(1)), float32(math.Inf(-1))
						for j := -1; j <= 1; j++ {
							yy := min(h-1, max(0, y+j))
							for k := -1; k <= 1; k++ {
								xx := min(w-1, max(0, x+k))
								v := pl[yy*w+xx]
								lo, hi = min(lo, v), max(hi, v)
							}
						}
						lo3[y*w+x], hi3[y*w+x] = lo, hi
					}
				}
			})
		}
		parallelRows(h, func(y0, y1 int) {
			acc := make([]float32, w)
			for y := y0; y < y1; y++ {
				clear(acc)
				for j := -3; j <= 3; j++ {
					src := tmp[min(h-1, max(0, y+j))*w:]
					kj := k[j+3]
					for x := 0; x < w; x++ {
						acc[x] += kj * src[x]
					}
				}
				for x := 0; x < w; x++ {
					i := (y*w + x) * n
					v := pl[y*w+x] + a*(pl[y*w+x]-acc[x])
					if usmClamp {
						// Allow half the local range beyond it: a 1 px line
						// still gains acutance, a step's halo cannot grow
						// wider than the step itself.
						m := 0.5 * (hi3[y*w+x] - lo3[y*w+x])
						v = min(hi3[y*w+x]+m, max(lo3[y*w+x]-m, v))
					}
					out[i+c] = v
				}
			}
		})
	}
	return out
}

// sppSharpen8 applies SPP's sharpening to an 8-bit image at view scale s.
func sppSharpen8(img *image.RGBA, s, iso float64, sensor string) {
	sppSharpen8Scale(img, s, iso, sensor, 1)
}

// sppSharpen8Scale is sppSharpen8 with the fitted amount multiplied by scale.
func sppSharpen8Scale(img *image.RGBA, s, iso float64, sensor string, scale float64) {
	a, k := sppView(s, iso, sensor)
	a *= float32(scale)
	if a == 0 {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	v := make([]float32, len(img.Pix))
	parallelRows(len(v), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			v[i] = float32(img.Pix[i])
		}
	})
	o := usmPlanes(v, w, h, 4, a, k)
	parallelRows(len(v), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			if i%4 != 3 {
				img.Pix[i] = uint8(max(0, min(255, o[i])) + 0.5)
			}
		}
	})
}

// sppSharpen16 is sppSharpen8 for 16-bit images (big-endian samples).
func sppSharpen16(img *image.RGBA64, s, iso float64, sensor string) {
	sppSharpen16Scale(img, s, iso, sensor, 1)
}

// sppSharpen16Scale is sppSharpen16 with the fitted amount multiplied by
// scale (see sppSharpen8Scale).
func sppSharpen16Scale(img *image.RGBA64, s, iso float64, sensor string, scale float64) {
	a, k := sppView(s, iso, sensor)
	a *= float32(scale)
	if a == 0 {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	v := make([]float32, w*h*4)
	parallelRows(len(v), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			v[i] = float32(uint16(img.Pix[2*i])<<8 | uint16(img.Pix[2*i+1]))
		}
	})
	o := usmPlanes(v, w, h, 4, a, k)
	parallelRows(len(v), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			if i%4 != 3 {
				u := uint16(max(0, min(65535, o[i])) + 0.5)
				img.Pix[2*i], img.Pix[2*i+1] = uint8(u>>8), uint8(u)
			}
		}
	})
}
