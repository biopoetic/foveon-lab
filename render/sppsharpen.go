package render

// SPP sharpens every image it develops, at Sharpness 0 too: measured
// (tools/sharpfit, SD1 against SPP's own exports, detail energy per
// frequency band on sRGB luminance) as an unsharp mask of a Gaussian sigma
// 1.0 px on the full-size image, amount 2.7 at ISO 100 (SDIM0260, 0042:
// every band within ~5% of SPP's, from 2.9x short in the finest), falling
// with ISO as SPP's noise reduction takes the fine detail away (the
// SDIM0358-0363 ISO 200-6400 series, each fitted on its own). SPP sharpens
// the SD15 far less (SDIM1240-1245, ISO 100-3200: 0.2-0.7, fitted after
// the black-level fix). Generations not measured yet
// (SD9/SD10, Quattro) get none.
// A reduced view gets the full-size filter's gain at its own Nyquist
// frequency, as a sigma-1 mask at that size.

import (
	"image"
	"math"
)

const sppSharpSigma = 1.0

// sppSharpByISO: the fitted amount per sensor generation and ISO (log2
// ISO 100 = 0 … 6400 = 6).
var sppSharpByISO = map[string][]float64{
	"Merrill": {2.7, 2.5, 2.4, 1.4, 1.0, 0.8, 0.5},
	"TRUEII":  {0.3, 0.2, 0.7, 0.5, 0.4, 0.2, 0.2},
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
	return interp(steps, tab, math.Log2(iso/100))
}

// sppAmount is the amount of a sigma-1 mask at a view of scale s that
// matches the full-size mask's gain at the view's Nyquist frequency.
func sppAmount(s, iso float64, sensor string) float32 {
	a := sppFullAmount(iso, sensor)
	if s >= 1 {
		return float32(a)
	}
	hp := func(sigma, f float64) float64 { return 1 - math.Exp(-2*math.Pi*math.Pi*sigma*sigma*f*f) }
	return float32(a * hp(sppSharpSigma, 0.5*s) / hp(1, 0.5))
}

// gauss1 is a normalised Gaussian kernel of sigma 1 (radius 3).
var gauss1 = func() [7]float32 {
	var k [7]float32
	var sum float32
	for i := range k {
		x := float64(i - 3)
		k[i] = float32(math.Exp(-x * x / 2))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	return k
}()

// usmPlanes sharpens interleaved channels (n per pixel, the first 3
// sharpened) given as floats: v + a·(v − gauss(v)), edges clamped. Each
// channel goes through its own plane: a horizontal pass (no clamping away
// from the edges) and a vertical pass accumulated row by row.
func usmPlanes(v []float32, w, h, n int, a float32) []float32 {
	out := make([]float32, len(v))
	copy(out, v)
	k := gauss1
	for c := 0; c < 3; c++ {
		pl := make([]float32, w*h)
		for i := range pl {
			pl[i] = v[i*n+c]
		}
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
					out[i+c] = pl[y*w+x] + a*(pl[y*w+x]-acc[x])
				}
			}
		})
	}
	return out
}

// sppSharpen8 applies SPP's sharpening to an 8-bit image at view scale s.
func sppSharpen8(img *image.RGBA, s, iso float64, sensor string) {
	a := sppAmount(s, iso, sensor)
	if a == 0 {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	v := make([]float32, len(img.Pix))
	for i, b := range img.Pix {
		v[i] = float32(b)
	}
	o := usmPlanes(v, w, h, 4, a)
	for i := range img.Pix {
		if i%4 != 3 {
			img.Pix[i] = uint8(max(0, min(255, o[i])) + 0.5)
		}
	}
}

// sppSharpen16 is sppSharpen8 for 16-bit images (big-endian samples).
func sppSharpen16(img *image.RGBA64, s, iso float64, sensor string) {
	a := sppAmount(s, iso, sensor)
	if a == 0 {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	v := make([]float32, w*h*4)
	for i := range v {
		v[i] = float32(uint16(img.Pix[2*i])<<8 | uint16(img.Pix[2*i+1]))
	}
	o := usmPlanes(v, w, h, 4, a)
	for i := range v {
		if i%4 != 3 {
			u := uint16(max(0, min(65535, o[i])) + 0.5)
			img.Pix[2*i], img.Pix[2*i+1] = uint8(u>>8), uint8(u)
		}
	}
}
