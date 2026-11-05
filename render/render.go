// Package render applies an approximation of Sigma Photo Pro's adjustments
// to an already-rendered sRGB image (the JPEG preview embedded in an X3F).
//
// It is an EMULATION: SPP's own algorithms (X3 Fill Light, colour modes,
// highlight recovery) are not public, so the aim is to show the direction
// and relative strength of each preset, not to reproduce SPP pixel for pixel.
package render

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
)

// stageTiming prints the render's internal stages when FOVEON_TIMING is set,
// so an export profile can separate tone from sharpening.
var stageTiming = os.Getenv("FOVEON_TIMING") != ""

func stageLap(name string, start time.Time) {
	if stageTiming {
		fmt.Fprintf(os.Stderr, "  %-16s %s\n", name, time.Since(start).Round(time.Millisecond))
	}
}

// Linear is an RGB image in linear light, 3 float32 per pixel.
type Linear struct {
	W, H int
	Pix  []float32
	// Scale is this image's size relative to the original photo (0 = 1):
	// SPP sharpens the full-size image, so on a reduced view the effect is
	// proportionally smaller.
	Scale float64
	// Frame is the long side, in full-resolution pixels, of the whole frame
	// this image belongs to (0 = this image's own long side / Scale): a
	// 100% window or a crop keeps the frame's, so film grain and halation,
	// sized on the film, stay the same size.
	Frame int
	// NativeFrame is the whole frame's long side in the sensor's native
	// pixels, when this image was decoded from a binned or otherwise
	// reduced source (0 = unknown, use Frame/frame()). The film grain uses
	// it as the export's scale reference: a fit view decoded from a binned
	// raw must still draw the grain the full-resolution export would carry.
	NativeFrame int
	// OriginX/Y and FullW/H place a window inside the whole frame, in
	// full-resolution pixels: Origin is this image's top-left, Full is the
	// whole frame's size (0 = derive from W/H/Scale). SPP's Shading Effect
	// is radial about the whole frame, not about a crop, so the monochrome
	// section needs them. A whole frame has Origin 0 and Full 0.
	OriginX, OriginY int
	FullW, FullH     int
	// Raw: developed from the raw data, so without the sharpening SPP (or
	// the camera, for its JPEG) gives every image; Render adds SPP's.
	Raw bool
	// ISO the photo was taken at (0 = unknown): SPP sharpens less as
	// its noise reduction takes the fine detail away.
	ISO float64
	// Sensor is the sharpening key (SharpKey: the Foveon generation, the DP
	// Merrills apart): SPP sharpens each differently.
	Sensor string
	// FillMean is the whole frame's mean brightness (FrameMean), kept
	// through crops, zoom windows and rotations: SPP's Fill Light depends on
	// it, and SPP sees the whole frame. 0 = not measured yet.
	FillMean float32
	// MonoLayers is SPP's Monochrome layer tone: the three camera layers
	// (bottom/middle/top) after the raw base's measured neutral curve, in
	// linear light, 3 floats per pixel at this image's geometry. A raw
	// development fills it (task 54); nil everywhere else. The mono path
	// mixes these directly, in SPP's order: the layers' tone first, the
	// mixer after, so the mix can no longer change how noise is rectified
	// per channel.
	MonoLayers []float32
}

// frameMean is the whole frame's mean brightness: FillMean when known, else
// measured on l.
func (l *Linear) frameMean() float32 {
	if l.FillMean > 0 {
		return l.FillMean
	}
	return FrameMean(l)
}

// FrameMean is the mean encoded brightness of l averaged down to the Fill
// Light grid (at most surroundMaxW wide, linear block means, then encoded)
// — the quantity the Fill Light study fitted on (tools/fillstudy).
func FrameMean(l *Linear) float32 {
	gw := min(l.W, surroundMaxW)
	gh := max(1, int(math.Round(float64(l.H)*float64(gw)/float64(l.W))))
	sum := make([]float64, gw*gh)
	n := make([]float64, gw*gh)
	// Cell-local sums: a band of grid rows still visits its pixels in the
	// original scan order, so each cell's total is bit-identical.
	parallelRows(gh, func(g0, g1 int) {
		for y := 0; y < l.H; y++ {
			gy := min(gh-1, y*gh/l.H)
			if gy < g0 || gy >= g1 {
				continue
			}
			for x := 0; x < l.W; x++ {
				i := (y*l.W + x) * 3
				k := gy*gw + min(gw-1, x*gw/l.W)
				sum[k] += float64(0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2])
				n[k]++
			}
		}
	})
	var m float64
	cnt := 0
	for k := range sum {
		if n[k] > 0 {
			m += float64(enc(float32(max(0, sum[k]/n[k]))))
			cnt++
		}
	}
	if cnt == 0 {
		return 0
	}
	return float32(m / float64(cnt))
}

// frame is the whole frame's long side in full-resolution pixels.
func (l *Linear) frame() float64 {
	if l.Frame > 0 {
		return float64(l.Frame)
	}
	return float64(max(l.W, l.H)) / l.scale()
}

func (l *Linear) scale() float64 {
	if l.Scale <= 0 {
		return 1
	}
	return l.Scale
}

// fullSize is the whole frame's size in full-resolution pixels.
func (l *Linear) fullSize() (w, h float64) {
	if l.FullW > 0 && l.FullH > 0 {
		return float64(l.FullW), float64(l.FullH)
	}
	s := l.scale()
	return float64(l.W) / s, float64(l.H) / s
}

// fullCoord places output pixel (x,y) inside the whole frame, in
// full-resolution pixels.
func (l *Linear) fullCoord(x, y int) (fx, fy float64) {
	s := l.scale()
	return float64(l.OriginX) + (float64(x)+0.5)/s, float64(l.OriginY) + (float64(y)+0.5)/s
}

const lutN = 4096

var (
	dec8   [256]float32      // sRGB byte → linear
	encLUT [lutN + 1]float32 // linear [0,1] → sRGB [0,1]
	decLUT [lutN + 1]float32 // sRGB [0,1] → linear
)

func init() {
	for i := range dec8 {
		dec8[i] = float32(srgbToLinear(float64(i) / 255))
	}
	for i := 0; i <= lutN; i++ {
		x := float64(i) / lutN
		encLUT[i] = float32(linearToSrgb(x))
		decLUT[i] = float32(srgbToLinear(x))
	}
}

func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func linearToSrgb(v float64) float64 {
	if v <= 0.0031308 {
		return v * 12.92
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

func lut(t *[lutN + 1]float32, x float32) float32 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return t[lutN]
	}
	f := x * lutN
	i := int(f)
	return t[i] + (t[i+1]-t[i])*(f-float32(i))
}

func enc(x float32) float32 { return lut(&encLUT, x) }
func dec(x float32) float32 { return lut(&decLUT, x) }

// ToSRGB encodes a linear value in [0,1] to sRGB [0,1].
func ToSRGB(x float32) float32 { return enc(x) }

// ToLinear decodes an sRGB value in [0,1] to linear light.
func ToLinear(x float32) float32 { return dec(x) }

// ParallelRows is parallelRows, for other packages.
func ParallelRows(h int, fn func(y0, y1 int)) { parallelRows(h, fn) }

// parallelRows runs fn over row ranges on all CPUs.
func parallelRows(h int, fn func(y0, y1 int)) {
	n := runtime.GOMAXPROCS(0)
	if n > h {
		n = h
	}
	if n <= 1 {
		fn(0, h)
		return
	}
	var wg sync.WaitGroup
	step := (h + n - 1) / n
	for y := 0; y < h; y += step {
		y1 := min(y+step, h)
		wg.Add(1)
		go func(a, b int) { defer wg.Done(); fn(a, b) }(y, y1)
	}
	wg.Wait()
}

// FromImage converts a decoded image to linear light, box-downscaled so the
// result (after rotating by rot degrees clockwise) is at most maxW wide.
// maxW <= 0 keeps full size.
func FromImage(img image.Image, maxW, rot int) *Linear {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	outW := sw
	if rot == 90 || rot == 270 {
		outW = sh
	}
	s := 1.0
	if maxW > 0 && outW > maxW {
		s = float64(maxW) / float64(outW)
	}
	dw, dh := max(1, int(math.Round(float64(sw)*s))), max(1, int(math.Round(float64(sh)*s)))

	// Linear at full size, then an exact area reduction (areaResample): the
	// integer-edge box averaged one source pixel here and two there,
	// aliasing and shifting the image by a fraction of a pixel (SPP's
	// exports, the reference every calibration is fitted and scored on,
	// and the camera-JPEG previews).
	get := pixelGetter(img)
	full := make([]float32, sw*sh*3)
	parallelRows(sh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := full[y*sw*3:]
			for x := 0; x < sw; x++ {
				r, g, bb := get(b.Min.X+x, b.Min.Y+y)
				row[x*3], row[x*3+1], row[x*3+2] = dec8[r], dec8[g], dec8[bb]
			}
		}
	})
	pix := full
	if dw != sw || dh != sh {
		pix = areaResample(full, sw, sh, dw, dh)
	}
	l := &Linear{W: dw, H: dh, Pix: pix, Scale: float64(dw) / float64(sw)}
	return l.Rotate(rot)
}

func pixelGetter(img image.Image) func(x, y int) (uint8, uint8, uint8) {
	switch m := img.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint8, uint8, uint8) {
			ci := m.COffset(x, y)
			return color.YCbCrToRGB(m.Y[m.YOffset(x, y)], m.Cb[ci], m.Cr[ci])
		}
	case *image.Gray:
		return func(x, y int) (uint8, uint8, uint8) {
			v := m.Pix[m.PixOffset(x, y)]
			return v, v, v
		}
	case *image.RGBA:
		return func(x, y int) (uint8, uint8, uint8) {
			i := m.PixOffset(x, y)
			return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
		}
	case *image.NRGBA:
		return func(x, y int) (uint8, uint8, uint8) {
			i := m.PixOffset(x, y)
			return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
		}
	case *image.RGBA64: // 16-bit TIFF (high byte first)
		return func(x, y int) (uint8, uint8, uint8) {
			i := m.PixOffset(x, y)
			return m.Pix[i], m.Pix[i+2], m.Pix[i+4]
		}
	case *image.NRGBA64:
		return func(x, y int) (uint8, uint8, uint8) {
			i := m.PixOffset(x, y)
			return m.Pix[i], m.Pix[i+2], m.Pix[i+4]
		}
	}
	return func(x, y int) (uint8, uint8, uint8) {
		r, g, b, _ := img.At(x, y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
	}
}

// Rotate returns the image rotated clockwise by 90, 180 or 270 degrees.
// Window is the w×h part of l at (x0, y0) (clamped to l), at full scale.
func (l *Linear) Window(x0, y0, w, h int) *Linear {
	x0, y0 = max(0, min(x0, l.W-1)), max(0, min(y0, l.H-1))
	w, h = max(1, min(w, l.W-x0)), max(1, min(h, l.H-y0))
	fw, fh := l.fullSize()
	o := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, Frame: int(math.Round(l.frame())), NativeFrame: l.NativeFrame, Raw: l.Raw, ISO: l.ISO, Sensor: l.Sensor, FillMean: l.frameMean(),
		OriginX: l.OriginX + x0, OriginY: l.OriginY + y0, FullW: int(math.Round(fw)), FullH: int(math.Round(fh))}
	for y := 0; y < h; y++ {
		copy(o.Pix[y*w*3:(y+1)*w*3], l.Pix[((y+y0)*l.W+x0)*3:])
	}
	if l.MonoLayers != nil {
		o.MonoLayers = make([]float32, w*h*3)
		for y := 0; y < h; y++ {
			copy(o.MonoLayers[y*w*3:(y+1)*w*3], l.MonoLayers[((y+y0)*l.W+x0)*3:])
		}
	}
	return o
}

func (l *Linear) Rotate(deg int) *Linear {
	if deg != 90 && deg != 180 && deg != 270 {
		return l
	}
	w, h := l.W, l.H
	o := &Linear{W: w, H: h, Pix: make([]float32, len(l.Pix)), Scale: l.Scale, Frame: l.Frame, NativeFrame: l.NativeFrame, Raw: l.Raw, ISO: l.ISO, Sensor: l.Sensor, FillMean: l.FillMean}
	if deg != 180 {
		o.W, o.H = h, w
	}
	// A whole frame stays centred at the origin; a windowed rotation is not
	// placed exactly (shading about the frame edge is a preview nicety).
	o.OriginX, o.OriginY = 0, 0
	fw, fh := l.fullSize()
	o.FullW, o.FullH = int(math.Round(fw)), int(math.Round(fh))
	if deg == 90 || deg == 270 {
		o.FullW, o.FullH = int(math.Round(fh)), int(math.Round(fw))
	}
	// Each output row is filled from independent source pixels (a pure copy,
	// no arithmetic), so the rotation runs on all cores and the bytes are the
	// same as the serial nested loop.
	parallelRows(o.H, func(y0, y1 int) {
		for ny := y0; ny < y1; ny++ {
			for nx := 0; nx < o.W; nx++ {
				var x, y int
				switch deg {
				case 90:
					x, y = ny, h-1-nx
				case 180:
					x, y = w-1-nx, h-1-ny
				case 270:
					x, y = w-1-ny, nx
				}
				copy(o.Pix[(ny*o.W+nx)*3:][:3], l.Pix[(y*w+x)*3:][:3])
			}
		}
	})
	if l.MonoLayers != nil {
		o.MonoLayers = make([]float32, w*h*3)
		parallelRows(o.H, func(y0, y1 int) {
			for ny := y0; ny < y1; ny++ {
				for nx := 0; nx < o.W; nx++ {
					var x, y int
					switch deg {
					case 90:
						x, y = ny, h-1-nx
					case 180:
						x, y = w-1-nx, h-1-ny
					case 270:
						x, y = w-1-ny, nx
					}
					copy(o.MonoLayers[(ny*o.W+nx)*3:][:3], l.MonoLayers[(y*w+x)*3:][:3])
				}
			}
		})
	}
	return o
}

// Downscale box-filters a linear image to at most maxW wide.
func (l *Linear) Downscale(maxW int) *Linear {
	if maxW <= 0 || l.W <= maxW {
		return l
	}
	dw := maxW
	dh := max(1, int(math.Round(float64(l.H)*float64(dw)/float64(l.W))))
	fw, fh := l.fullSize()
	o := &Linear{W: dw, H: dh, Pix: areaResample(l.Pix, l.W, l.H, dw, dh), Scale: l.scale() * float64(dw) / float64(l.W), Frame: l.Frame, NativeFrame: l.NativeFrame, Raw: l.Raw, ISO: l.ISO, Sensor: l.Sensor, FillMean: l.FillMean,
		OriginX: l.OriginX, OriginY: l.OriginY, FullW: int(math.Round(fw)), FullH: int(math.Round(fh))}
	if l.MonoLayers != nil {
		o.MonoLayers = areaResample(l.MonoLayers, l.W, l.H, dw, dh)
	}
	return o
}

// mask is a heavily blurred perceptual-luminance map used by Fill Light, so
// shadows are lifted by region (keeping local contrast) instead of per pixel.
type mask struct {
	cell   float32
	gw, gh int
	v      []float32
}

func newMask(l *Linear) *mask {
	cell := max(1, l.W/160)
	gw, gh := (l.W+cell-1)/cell, (l.H+cell-1)/cell
	m := &mask{cell: float32(cell), gw: gw, gh: gh, v: make([]float32, gw*gh)}
	cnt := make([]float32, gw*gh)
	// Each grid cell has its own accumulator and its pixels are still visited
	// in the same y, then x, order, so every cell's float sum is unchanged.
	parallelRows(gh, func(g0, g1 int) {
		for y := 0; y < l.H; y++ {
			g := y / cell
			if g < g0 || g >= g1 {
				continue
			}
			for x := 0; x < l.W; x++ {
				i := (y*l.W + x) * 3
				Y := 0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2]
				k := g*gw + x/cell
				m.v[k] += enc(Y)
				cnt[k]++
			}
		}
	})
	for i := range m.v {
		m.v[i] /= cnt[i]
	}
	tmp := make([]float32, len(m.v))
	for pass := 0; pass < 3; pass++ {
		boxBlur(m.v, tmp, gw, gh, 3)
	}
	return m
}

// boxBlur blurs v in place (horizontal then vertical) with radius r.
func boxBlur(v, tmp []float32, w, h, r int) {
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				var s float32
				var n float32
				for k := max(0, x-r); k <= min(w-1, x+r); k++ {
					s += v[y*w+k]
					n++
				}
				tmp[y*w+x] = s / n
			}
		}
	})
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				var s float32
				var n float32
				for k := max(0, y-r); k <= min(h-1, y+r); k++ {
					s += tmp[k*w+x]
					n++
				}
				v[y*w+x] = s / n
			}
		}
	})
}

func (m *mask) at(x, y int) float32 {
	fx := (float32(x)+0.5)/m.cell - 0.5
	fy := (float32(y)+0.5)/m.cell - 0.5
	fx = max(0, min(fx, float32(m.gw-1)))
	fy = max(0, min(fy, float32(m.gh-1)))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, m.gw-1), min(y0+1, m.gh-1)
	tx, ty := fx-float32(x0), fy-float32(y0)
	a := m.v[y0*m.gw+x0] + (m.v[y0*m.gw+x1]-m.v[y0*m.gw+x0])*tx
	b := m.v[y1*m.gw+x0] + (m.v[y1*m.gw+x1]-m.v[y1*m.gw+x0])*tx
	return a + (b-a)*ty
}

// Base is a prepared source image: linear pixels plus its Fill Light mask.
type Base struct {
	*Linear
	m      *mask
	sa, sb *Surround // Fill Light surroundings (SurroundFracs)

	filmMu sync.Mutex
	halos  map[int][]float32 // halation light by spread (film.go)

	// Per-pixel Fill Light inputs, fixed for the image: the mask, and the
	// two surroundings minus the pixel's own brightness (computed once,
	// not interpolated from their grids on every render).
	fieldsOnce   sync.Once
	mPix, da, db []float32
	// fillMean is the whole frame's mean brightness (Linear.FillMean).
	fillMean float32
}

// fields computes the per-pixel Fill Light inputs once.
func (b *Base) fields() {
	b.fieldsOnce.Do(func() {
		n := b.W * b.H
		b.mPix, b.da, b.db = make([]float32, n), make([]float32, n), make([]float32, n)
		parallelRows(b.H, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < b.W; x++ {
					j := y*b.W + x
					i := j * 3
					v := enc(0.2126*b.Pix[i] + 0.7152*b.Pix[i+1] + 0.0722*b.Pix[i+2])
					b.mPix[j] = b.m.at(x, y)
					b.da[j], b.db[j] = b.sa.At(x, y)-v, b.sb.At(x, y)-v
				}
			}
		})
	})
}

// NewBase prepares a linear image for repeated rendering.
func NewBase(l *Linear) *Base {
	return &Base{Linear: l, m: newMask(l), sa: NewSurround(l, SurroundFracs[0]), sb: NewSurround(l, SurroundFracs[1]), fillMean: l.frameMean()}
}

// Compensation undoes the camera's own JPEG styling before a preset is
// applied, so presets start from something closer to SPP's neutral render.
type Compensation struct {
	Contrast   float64 // added to the preset's contrast
	Saturation float64 // added to the preset's saturation
	SatFactor  float64 // extra chroma multiplier (e.g. to cancel Vivid); 0 = 1
	// Family picks the measured model for this camera ("SD1", "SD15"; ""
	// = the default one).
	Family string
	// Model is the camera's short model name ("SD14", "SD9"): when a model
	// was measured on that body it wins over the family's (the SD14 and the
	// SD15 share a family but not their fill).
	Model string
	// Undo is the inverse of the camera's colour-mode matrix (from its
	// CAMF calibration), applied to each pixel first; nil = none.
	// UndoGamma applies it to gamma-encoded values instead of linear.
	Undo      *[9]float32
	UndoGamma bool
	// WBDeveloped: the base was developed with the preset's camera white
	// balance already (cameraWBName), so the measured WB mapping is skipped.
	WBDeveloped bool
	// CC is the photo's camera-to-linear-sRGB colour matrix (white
	// balanced camera layers in; rows sum to 1), for the layer filter;
	// nil = no layer filter.
	CC *[9]float64
	// Sensor is the Foveon generation for the physical lens filters
	// ("Merrill", "TRUEII", "SD9", "Quattro"); "" = no lens filters.
	Sensor string
	// ISO the raw was shot at, for the Monochrome path's high-ISO tone
	// (0 = unknown, no high-ISO term).
	ISO float64
	// MonoDefault is the photo's fresh-open Color Mixer, already resolved
	// from its X3F header (render.MonoFreshMix). nil = no header field, so
	// the Monochrome default is equal thirds.
	MonoDefault *[3]float64
	// GenericFilm: the source is a colour-managed linear RGB image with no
	// measured sensor sensitivity (another camera's raw), so a colour film
	// is fitted to the sRGB/CIE observer response instead of a Foveon layer
	// stack. The film's optics are then an approximation: its spectral layer
	// map cannot see a spectrum the file never carried.
	GenericFilm bool
	// NoModes says the camera has no in-camera colour modes (SD9, SD10,
	// SD14): SPP ignores the setting on those bodies, so the emulation must
	// too — its export with Colour mode 6/7 is identical to the neutral.
	NoModes bool
}

// CompensationFor derives the compensation from X3F PROP metadata:
// the in-camera colour mode (CM_DESC) and contrast/saturation settings,
// for a camera family ("SD1", "SD15").
func CompensationFor(props map[string]string, family string) Compensation {
	return CompensationWith(props, family, nil)
}

// CompensationWith is CompensationFor with the photo's own as-shot white
// balance colour matrix (from its CAMF; nil = the per-camera fallback).
func CompensationWith(props map[string]string, family string, cc *[9]float64) Compensation {
	c := Compensation{SatFactor: 1, Family: family}
	parse := func(k string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(props[k], ",", ".")), 64)
		if err != nil {
			return 0
		}
		return v
	}
	c.Contrast = -parse("CONT_DESC")
	c.Saturation = -parse("SATU_DESC")
	mode := strings.TrimSpace(props["CM_DESC"])
	if how := compModeFor(family); how != "old" {
		if inv, ct, ok := camfUndo(family, mode, how, cc); ok {
			var u [9]float32
			for i, v := range inv {
				u[i] = float32(v)
			}
			c.Undo = &u
			c.UndoGamma = how == "camf-gamma"
			c.Contrast -= ct * compContrast
			return c
		}
	}
	switch mode {
	case "Vivid":
		c.SatFactor = 0.8
		c.Contrast -= 0.12
	case "Portrait", "Neutral":
		c.SatFactor = 1.08
	case "Landscape":
		c.SatFactor = 0.9
	}
	return c
}

func hueWeight(h, center, half float64) float64 {
	d := math.Abs(h - center)
	if d > 180 {
		d = 360 - d
	}
	return math.Max(0, 1-d/half)
}

// Render applies p to the base image and returns 8-bit sRGB.
func Render(b *Base, p preset.Params, c Compensation) *image.RGBA {
	return RenderOpt(b, p, c, true)
}

// RenderOpt is Render; sharp false skips the sharpening (a quick preview
// while a slider is dragged).
func RenderOpt(b *Base, p preset.Params, c Compensation, sharp bool) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, b.W, b.H))
	t0 := time.Now()
	renderInto(b, p, c, func(i int, r, g, bl float32) {
		o := out.Pix[i*4:]
		o[0] = uint8(enc(r)*255 + 0.5)
		o[1] = uint8(enc(g)*255 + 0.5)
		o[2] = uint8(enc(bl)*255 + 0.5)
		o[3] = 255
	})
	stageLap("tone", t0)
	t0 = time.Now()
	if b.Raw && sharp {
		sppSharpen8(out, b.scale(), b.ISO, b.Sensor)
	}
	if p.Sharpness != 0 && sharp {
		sharpen(out, float32(math.Max(-1, math.Min(2, p.Sharpness*1.5*b.scale()))))
	}
	if !noImpulse && b.ISO >= 800 && (b.Sensor == "DPM" || b.Sensor == "Quattro") {
		impulseClean8(out, 80, 12)
	}
	stageLap("sharpening", t0)
	return out
}

// Render16 is Render with 16-bit sRGB output (for TIFF export): no 8-bit
// rounding, so edited gradients don't band.
func Render16(b *Base, p preset.Params, c Compensation) *image.RGBA64 {
	out := image.NewRGBA64(image.Rect(0, 0, b.W, b.H))
	t0 := time.Now()
	renderInto(b, p, c, func(i int, r, g, bl float32) {
		o := out.Pix[i*8:]
		for ch, v := range [3]float32{r, g, bl} {
			u := uint16(enc(v)*65535 + 0.5)
			o[ch*2], o[ch*2+1] = uint8(u>>8), uint8(u)
		}
		o[6], o[7] = 0xff, 0xff
	})
	stageLap("tone", t0)
	t0 = time.Now()
	if b.Raw {
		sppSharpen16(out, b.scale(), b.ISO, b.Sensor)
	}
	if p.Sharpness != 0 {
		sharpen16(out, float32(math.Max(-1, math.Min(2, p.Sharpness*1.5*b.scale()))))
	}
	if !noImpulse && b.ISO >= 800 && (b.Sensor == "DPM" || b.Sensor == "Quattro") {
		impulseClean16(out, 80*257, 12*257)
	}
	stageLap("sharpening", t0)
	return out
}

// renderInto runs the adjustment pipeline and hands every pixel (index,
// linear RGB clamped later by the sink's encoding) to put.
func renderInto(b *Base, p preset.Params, c Compensation, put func(i int, r, g, bl float32)) {
	w, h := b.W, b.H
	op := newPixOp(p, c)
	op.forBase(b)
	dens := densitySetupFor(p)
	// SPP's monochrome grain and the film library's/manual grain are two
	// different grains. When the mono one is on, it is the grain: the film
	// grain is skipped so a preset cannot show both at once.
	sg := b.newMonoGrain(p, op.monoNew)
	fp := p
	if sg != nil {
		fp.Grain = 0
	}
	film := b.newFilm(fp, op.mono || op.layerMono != nil)
	fill := op.fill != 0 || op.fillM != nil
	if fill {
		b.fields()
	}
	multi := op.fillM != nil && op.fillM.multi()
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				j := y*w + x
				i := j * 3
				var m, da, db float32
				if fill {
					m = b.mPix[j]
					if multi {
						da, db = b.da[j], b.db[j]
					}
				}
				r, g, bl := film.addHalo(y*w+x, b.Pix[i], b.Pix[i+1], b.Pix[i+2])
				r, g, bl = op.applyAt(y*w+x, r, g, bl, m, da, db)
				r, g, bl = op.monoShade(b, x, y, r, g, bl)
				r, g, bl = film.grain(y*w+x, r, g, bl)
				r, g, bl = sg.apply(y*w+x, r, g, bl)
				r, g, bl = op.applyMonoTone(r, g, bl)
				if gain := dens.gain(x, y, w, h); gain != 1 {
					r, g, bl = r*gain, g*gain, bl*gain
				}
				put(y*w+x, r, g, bl)
			}
		}
	})
}

// pixOp is the per-pixel part of a render (everything but sharpening);
// the Fill Light mask value comes from the caller.
type pixOp struct {
	cr, cg, cb, exp, maskScale, fill, hl, blk, ct float32
	satF                                          float64
	mono                                          bool
	mode                                          int
	undo                                          *[9]float32
	undoGamma                                     bool
	layer                                         *[9]float32 // layer filter, colour
	layerMono                                     *[3]float32 // layer filter, monochrome grey

	// Monochrome (SPP's Monochrome tab): the mixer, SPP's measured base tone,
	// toning and shading. A film has its own curve and grain and keeps the
	// rendering it always had, so none of this applies then.
	monoNew   bool
	monoGamma float32
	monoTone  [3]float32 // 8-bit sRGB offsets / 255
	// monoLayers: SPP's per-layer tone image (Linear.MonoLayers), mixed in
	// SPP's order; nil = the old path, which mixes in the sRGB base first.
	monoLayers []float32
	monoW      [3]float32 // the mixer's layer weights (bottom, middle, top)
	monoG      [9]float32 // lens filter and the layer sliders' gains, in layer space
	monoA      [3]float32 // high-ISO per-layer gain
	monoF      [3]float32 // high-ISO per-layer black floor
	monoB      float32    // high-ISO black lift before the tone power
	// monoShadeR/A: SPP's Shading Effect; A 0 = off.
	monoShadeR, monoShadeA float32

	// Measured emulation (model.go), when a model is embedded.
	measured   bool
	wb         *RawCorrection // measured WhiteBalance code mapping
	modeMap    *RawCorrection // measured colour-mode mapping (when used)
	tint       [3]float32     // colour adjust, brightness-neutral
	pre, post  []toneOp       // tone steps before / after Fill Light
	fillM      *fillOp
	satMeasure float64

	tone *filmTone // a black-and-white film's characteristic curve
	// A colour film (colorfilm.go): linear sRGB -> sensor layers (cfInv,
	// the inverse of the camera matrix) -> the film -> linear sRGB.
	cfilm *colorFilm
	cfInv [9]float32

	// The measured tone steps before / after Fill Light (pre includes the
	// film's curve), baked once per render: they depend on brightness only.
	preL, postL *toneLUT
	fillL       *fillLUT // the two-scale Fill Light, baked
}

// toneLUT: brightness x (sRGB 0..1) -> new brightness and chroma factor,
// sampled at toneLUTN+1 points and interpolated linearly.
const toneLUTN = 2048

type toneLUT struct{ x, c [toneLUTN + 1]float32 }

func bakeTone(f func(x float64) (float64, float64)) *toneLUT {
	t := &toneLUT{}
	for i := 0; i <= toneLUTN; i++ {
		x, c := f(float64(i) / toneLUTN)
		t.x[i], t.c[i] = float32(x), float32(c)
	}
	return t
}

func (t *toneLUT) at(x float32) (float32, float32) {
	if x < 0 {
		x = 0
	} else if x > 1 {
		x = 1
	}
	f := x * toneLUTN
	i := int(f)
	if i >= toneLUTN {
		i = toneLUTN - 1
	}
	fr := f - float32(i)
	return t.x[i] + fr*(t.x[i+1]-t.x[i]), t.c[i] + fr*(t.c[i+1]-t.c[i])
}

func newPixOp(p preset.Params, c Compensation) pixOp {
	// A black-and-white film renders in black and white.
	film, isFilm := FilmByKey(p.Film)
	if isFilm && film.BW {
		p.Mono = true
	}
	// Colour adjust: normalised so it tints without changing brightness.
	k := 1 / (0.2126*p.R + 0.7152*p.G + 0.0722*p.B)
	if p.R <= 0 || p.G <= 0 || p.B <= 0 || math.IsInf(k, 0) {
		p.R, p.G, p.B, k = 1, 1, 1, 1
	}
	expGain := math.Exp2(p.Exposure)
	op := pixOp{
		undo: c.Undo, undoGamma: c.UndoGamma,
		cr: float32(p.R * k * expGain), cg: float32(p.G * k * expGain), cb: float32(p.B * k * expGain),
		exp:       float32(expGain),
		maskScale: float32(math.Exp2(p.Exposure / 2.2)),
		fill:      float32(p.FillLight * 1.5),
		hl:        float32(p.Highlight * 0.35),
		blk:       float32(math.Max(-0.3, math.Min(0.3, p.Blackness*0.08))),
		ct:        float32(math.Max(-1, math.Min(1, (p.Contrast+c.Contrast)*1.2))),
		mode:      p.ColorMode,
	}

	// SPP's saturation slider spans -2..+2 and -1 is visibly still colour
	// (confirmed by eye in SPP; exact curve pending calibration steps A15 and
	// B13), so the negative side is scaled over the full -2 range. True B&W
	// is SPP's separate Monochrome mode, i.e. p.Mono.
	sat := p.Saturation + c.Saturation
	op.mono = p.Mono || p.Saturation <= -2
	if p.Mono && p.Film == "" {
		// SPP's Monochrome tab. The base tone is SPP's own: the linear layer
		// mix raised to monoBaseGamma, fitted to 01-base and 06-base (task 50).
		op.monoNew = true
		op.monoGamma = monoBaseGamma
		if v, ok := preset.MonoToneOffset(p.MonoTone); ok {
			for i := range v {
				op.monoTone[i] = float32(v[i] / 255)
			}
		}
		op.monoShadeR = float32(math.Max(0, math.Min(1, p.MonoShadeRadius)))
		op.monoShadeA = float32(math.Max(0, math.Min(1, p.MonoShadeAmount)))
		// Task 54: SPP applies its tone to each layer and mixes after; the
		// mixer's weights also go to the per-layer path. The high-ISO terms
		// are zero at base ISO, where the tone is the measured 1.084 curve.
		w := monoWeights(p, c)
		s := monoISORamp(c.ISO)
		op.monoW = [3]float32{float32(w[0]), float32(w[1]), float32(w[2])}
		op.monoA = [3]float32{float32(1 - 0.085*s), float32(1 - 0.085*s), 1}
		op.monoF = [3]float32{float32(0.028 * s), float32(0.028 * s), float32(monoTopFloor(s))}
		op.monoB = float32(0.005 * s)
		op.monoGamma = float32(monoBaseGamma + 0.116*s)
	}
	switch {
	case sat < 0:
		op.satF = math.Max(0, 1+sat/2)
	default:
		op.satF = 1 + 0.6*sat
	}
	if c.SatFactor > 0 {
		op.satF *= c.SatFactor
	}
	op.setLayerFilter(p, c)
	op.tone = newFilmTone(p)
	if c.CC != nil {
		if f := colorFilmOnPush(p.Film, p.Paper, c.Sensor, p.Push, ColorPrint{p.CCY, p.CCM, p.CCC, p.PrintExp}); f != nil {
			if inv, ok := invert3x3(*c.CC); ok {
				op.cfilm = f
				for i, v := range inv {
					op.cfInv[i] = float32(v)
				}
			}
		}
	} else if c.GenericFilm {
		// Another camera's raw is linear sRGB, not a Foveon layer stack. Fit
		// the film to the colourimetric sRGB response (spectralfilter.go,
		// srgbSens) and feed it the pixels directly: no Foveon matrix, no
		// pretence that the file carries silicon layer data.
		if f := colorFilmOnPush(p.Film, p.Paper, "sRGB", p.Push, ColorPrint{p.CCY, p.CCM, p.CCC, p.PrintExp}); f != nil {
			op.cfilm = f
			for i := range op.cfInv {
				op.cfInv[i] = 0
			}
			op.cfInv[0], op.cfInv[4], op.cfInv[8] = 1, 1, 1
		}
	}
	// A colour film is the look: SPP's measured rendering doesn't apply.
	if md := modelFor(c.Family, c.Model); md != nil && op.cfilm == nil {
		op.measured = true
		if !c.WBDeveloped {
			op.wb = md.wbFor(p.WhiteBalance)
		}
		op.tint = [3]float32{float32(p.R * k), float32(p.G * k), float32(p.B * k)}
		op.pre = md.newToneOp("Exposure", p.Exposure)
		op.fillM = md.newFillOp(p.FillLight)
		if op.fillM != nil {
			op.fillL = op.fillM.bake(0) // the frame mean comes with the Base (forBase)
		}
		for _, t := range []struct {
			param string
			v     float64
		}{{"Highlight", p.Highlight}, {"Blackness", p.Blackness}, {"Contrast", p.Contrast + c.Contrast}} {
			op.post = append(op.post, md.newToneOp(t.param, t.v)...)
		}
		if p.ColorMode != preset.ModeStandard && !c.NoModes {
			if m := md.modeFor(p.ColorMode); m != nil {
				op.modeMap = m
			} else {
				op.post = append(op.post, md.newToneOp(fmt.Sprintf("ColorMode%d", p.ColorMode), 1)...)
			}
		}
		pre, post, tone := op.pre, op.post, op.tone
		op.preL = bakeTone(func(x float64) (float64, float64) {
			c := 1.0
			var k float64
			for i := range pre {
				x, k = pre[i].apply(x)
				c *= k
			}
			if tone != nil {
				x = float64(enc(tone.at(dec(float32(x)))))
			}
			return x, c
		})
		op.postL = bakeTone(func(x float64) (float64, float64) {
			c := 1.0
			var k float64
			for i := range post {
				x, k = post[i].apply(x)
				c *= k
			}
			return x, c
		})
		// Measured: Saturation ±0.5 → ×1.25 / ×0.75, ±1 → ×1.5 / ×0.5.
		op.satMeasure = math.Max(0, 1+0.5*sat)
		if c.SatFactor > 0 {
			op.satMeasure *= c.SatFactor
		}
	}
	return op
}

// colourLayerScale: how much of the layer filter's stops apply in colour.
const colourLayerScale = 0.1

// Invert3 inverts a 3×3 row-major matrix (invert3x3 for other packages).
func Invert3(m [9]float64) ([9]float64, bool) { return invert3x3(m) }

// monoBaseGamma is SPP's Monochrome base tone: its neutral Monochrome render
// is our linear layer mix raised to this power before the sRGB encode. Fitted
// to 01-base (ISO 100) and 06-base (ISO 3200), whose four-times-reduced
// pictures agree on it: 1.084, with amplitude 0.993 and a +0.0013 black lift
// (task 50). The old mono path used the luminance with no such curve.
const monoBaseGamma = 1.084

// monoISORamp is how much of the measured high-ISO Monochrome term applies.
// Task 54 fitted a straight line in log2(ISO) clamped at ISO 3200; the new
// DP1 Merrill 400-6400 series says the response is convex: slower at 800
// and 1600, and still growing at 6400. The exponent keeps 3200 at 1, so the
// task-54 parameters there are unchanged, and has no upper clamp (the 6400
// point is measured; above it is an extrapolation).
func monoISORamp(iso float64) float64 {
	if iso <= 400 {
		return 0
	}
	r := math.Log2(iso/400) / 3
	return math.Pow(r, 1.25)
}

// monoTopFloor is the top (blue) layer's black floor above ISO 3200. The
// 06 layer exports at ISO 3200 need none (B is clean); the DP1M ISO 6400
// export needs a small one once the lower-layer floor and the tone power
// have been set for that ISO, so the term starts at 3200 and grows above it.
func monoTopFloor(s float64) float64 {
	return 0.09 * math.Max(0, s-1)
}

var (
	// monoEqualMix is SPP's Reset mixer and the fallback when the X3F header
	// has no Monochrome mixer field (the 2.x and Quattro headers).
	monoEqualMix = [3]float64{1.0 / 3, 1.0 / 3, 1.0 / 3}
	// monoTopMix is what SPP shows when the Merrill 3.0 header's mixer field
	// is all zero: the top (blue) layer alone.
	monoTopMix = [3]float64{0, 0, 1}
)

// MonoFreshMix resolves the Monochrome Color Mixer SPP shows on a fresh
// open. stored is the raw three floats from the X3F 3.0 header, or nil when
// the file has no such field. No field => equal thirds (SPP's Reset
// position). A stored all-zero field => pure B. A stored non-zero field is
// normalised; a negative or non-finite field is treated as absent. This is
// the one place the rule lives: CPU, GPU, presets and batch all obtain their
// unset mixer through it.
func MonoFreshMix(stored *[3]float64) [3]float64 {
	if stored == nil {
		return monoEqualMix
	}
	w := *stored
	if w == [3]float64{} {
		return monoTopMix
	}
	if w = normalMonoMix(w); w == ([3]float64{}) {
		return monoEqualMix
	}
	return w
}

// normalMonoMix normalises a non-negative mixer to sum 1; it returns the
// zero array for an invalid or all-zero one.
func normalMonoMix(w [3]float64) [3]float64 {
	sum := 0.0
	for _, v := range w {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return [3]float64{}
		}
		sum += v
	}
	if sum <= 0 {
		return [3]float64{}
	}
	for i := range w {
		w[i] /= sum
	}
	return w
}

// monoWeights is SPP's Color Mixer: the share of the bottom (red), middle
// (green) and top (blue) layer. An explicitly set mixer wins; otherwise the
// photo's own fresh-open mix is used (c.MonoDefault, resolved from its
// header by MonoFreshMix), and with no header field that is equal thirds.
// SPP's Reset button is an explicit equal thirds.
func monoWeights(p preset.Params, c Compensation) [3]float64 {
	w := monoEqualMix
	if c.MonoDefault != nil {
		w = MonoFreshMix(c.MonoDefault)
	}
	if p.MonoMixSet {
		if set := normalMonoMix([3]float64{p.MonoR, p.MonoG, p.MonoB}); set != ([3]float64{}) {
			w = set
		}
	}
	return w
}

// setMonoMix prepares SPP's monochrome grey: the mixer's layer weights with a
// lens filter and the layer sliders' gains in front of them (G), the camera's
// colour matrix undone to reach the layers (cc⁻¹), and the result normalised
// so a neutral stays the same grey. With no colour matrix (a camera JPEG) the
// mixer weights act on linear sRGB directly.
func (op *pixOp) setMonoMix(p preset.Params, c Compensation) {
	w := monoWeights(p, c)
	ev := [3]float64{p.LayerBottom, p.LayerMiddle, p.LayerTop}
	f := [3]float64{math.Exp2(ev[0]), math.Exp2(ev[1]), math.Exp2(ev[2])}
	G := [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	if lens := lensFilter(c.Sensor, p.Filter); lens != nil {
		G = *lens
	}
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			G[i*3+j] *= f[i]
		}
	}
	for i := range op.monoG {
		op.monoG[i] = float32(G[i])
	}
	if c.CC == nil {
		var v [3]float32
		sum := w[0] + w[1] + w[2]
		for j := 0; j < 3; j++ {
			v[j] = float32(w[j] / sum)
		}
		op.layerMono = &v
		return
	}
	inv, ok := invert3x3(*c.CC)
	if !ok {
		return
	}
	// grey = wᵀ·G·layers, layers = cc⁻¹·sRGB.
	var u [3]float64
	for k := 0; k < 3; k++ {
		for i := 0; i < 3; i++ {
			u[k] += w[i] * G[i*3+k]
		}
	}
	var v [3]float32
	var sum float64
	for j := 0; j < 3; j++ {
		for k := 0; k < 3; k++ {
			v[j] += float32(u[k] * inv[k*3+j])
		}
		sum += float64(v[j])
	}
	if sum > 0 {
		for j := range v {
			v[j] /= float32(sum)
		}
	}
	op.layerMono = &v
}

// setLayerFilter prepares the layer filter: back to the camera's
// (white-balanced) layers with the inverse of its colour matrix, a gain per
// layer, forward again; in colour scaled so white keeps its brightness, in
// monochrome the grey is the layers' gain-weighted mean (grey stays grey).
func (op *pixOp) setLayerFilter(p preset.Params, c Compensation) {
	// A black-and-white film from the library, with no layer mix of its
	// own: the grey is the film's spectral sensitivity (its data sheet's, as
	// layer weights), linear sRGB -> layers (cc⁻¹). A colour filter on the
	// lens sits in front of the film: the film sees the filtered layers
	// (w·G), and the photographer opens up by the filter factor, so white
	// keeps its grey.
	lensG := lensFilter(c.Sensor, p.Filter)
	if f, ok := FilmByKey(p.Film); ok && f.BW && c.CC != nil && (p.Filter == "" || lensG != nil) && p.LayerBottom == 0 && p.LayerMiddle == 0 && p.LayerTop == 0 {
		if w, ok := f.Mono[c.Sensor]; ok {
			if G := lensG; G != nil {
				var wf [3]float64
				for k := 0; k < 3; k++ {
					for i := 0; i < 3; i++ {
						wf[k] += w[i] * G[i*3+k]
					}
				}
				if sf := wf[0] + wf[1] + wf[2]; sf > 0 {
					s := (w[0] + w[1] + w[2]) / sf
					w = [3]float64{wf[0] * s, wf[1] * s, wf[2] * s}
				}
			}
			if inv, ok := invert3x3(*c.CC); ok {
				var v [3]float32
				for j := 0; j < 3; j++ {
					for k := 0; k < 3; k++ {
						v[j] += float32(w[k] * inv[k*3+j])
					}
				}
				op.layerMono = &v
				return
			}
		}
	}
	if ir, mono := irLook(c.Sensor, p.Filter); ir != nil && c.CC != nil {
		op.setIRLook(*ir, mono, p, *c.CC)
		return
	}
	if op.monoNew {
		op.setMonoMix(p, c)
		return
	}
	lens := lensFilter(c.Sensor, p.Filter)
	mode := camModeMatrix(c.Family, p.CamMode)
	if c.CC == nil || (lens == nil && mode == nil && p.LayerBottom == 0 && p.LayerMiddle == 0 && p.LayerTop == 0) {
		return
	}
	cc := *c.CC
	inv, ok := invert3x3(cc)
	if !ok {
		return
	}
	ev := [3]float64{p.LayerBottom, p.LayerMiddle, p.LayerTop}
	if !op.mono {
		// In colour the camera matrix amplifies a difference between the
		// layers about tenfold (they overlap spectrally; SD1 coefficients
		// reach ±4.8), so the sliders act at a tenth: 1 stop = a clear warm
		// or cool shift, as a mono filter's few stops are a clear filter.
		for i := range ev {
			ev[i] *= colourLayerScale
		}
	}
	f := [3]float64{math.Exp2(ev[0]), math.Exp2(ev[1]), math.Exp2(ev[2])}
	// G: the change in white-balanced layer space: the lens filter, then
	// the per-layer gains.
	G := [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	if lens != nil {
		G = *lens
	}
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			G[i*3+j] *= f[i]
		}
	}
	// The camera's colour mode works on the layers the sensor recorded,
	// after any filter (Sigma's matrix, in camera space, as SPP applies it).
	if mode != nil && !op.mono {
		G = mul33(*mode, G)
	}
	lum := [3]float64{0.2126, 0.7152, 0.0722}
	if op.mono && lens == nil {
		// Layer mix: grey = the layers' gain-weighted mean (grey stays grey).
		sum := f[0] + f[1] + f[2]
		var v [3]float32
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				v[j] += float32(f[k] * inv[k*3+j] / sum)
			}
		}
		op.layerMono = &v
		return
	}
	// m = cc · G · cc⁻¹ in linear sRGB; scaled so white keeps its
	// brightness (the photographer's filter-factor exposure).
	m := mul33(cc, mul33(G, inv))
	y := 0.0
	for i, wt := range lum {
		y += wt * (m[i*3] + m[i*3+1] + m[i*3+2])
	}
	if y <= 0 {
		return
	}
	if op.mono {
		// A lens filter in black and white: the grey is the filtered
		// image's luminance.
		var v [3]float32
		for j := 0; j < 3; j++ {
			for i, wt := range lum {
				v[j] += float32(wt * m[i*3+j] / y)
			}
		}
		op.layerMono = &v
		return
	}
	var mf [9]float32
	for i := range m {
		mf[i] = float32(m[i] / y)
	}
	op.layer = &mf
}

// setIRLook: linear sRGB -> white-balanced layers (cc⁻¹) -> the look's
// bands (M), the layer sliders acting on the layers in between.
func (op *pixOp) setIRLook(M [9]float64, mono bool, p preset.Params, cc [9]float64) {
	inv, ok := invert3x3(cc)
	if !ok {
		return
	}
	ev := [3]float64{p.LayerBottom, p.LayerMiddle, p.LayerTop}
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			M[i*3+j] *= math.Exp2(ev[j])
		}
	}
	m := mul33(M, inv)
	if mono {
		v := [3]float32{float32(m[0]), float32(m[1]), float32(m[2])}
		op.layerMono = &v
		return
	}
	var mf [9]float32
	for i := range m {
		mf[i] = float32(m[i])
	}
	op.layer = &mf
}

// layerFilter applies the prepared layer filter to one linear pixel.
func (op *pixOp) layerFilter(r, g, bl float32) (float32, float32, float32) {
	if v := op.layerMono; v != nil {
		y := max(0, v[0]*r+v[1]*g+v[2]*bl)
		return y, y, y
	}
	m := op.layer
	return m[0]*r + m[1]*g + m[2]*bl, m[3]*r + m[4]*g + m[5]*bl, m[6]*r + m[7]*g + m[8]*bl
}

// unstyle undoes the camera's colour-mode matrix.
func (op *pixOp) unstyle(r, g, bl float32) (float32, float32, float32) {
	u := op.undo
	if op.undoGamma {
		r, g, bl = enc(max(0, r)), enc(max(0, g)), enc(max(0, bl))
	}
	r, g, bl = u[0]*r+u[1]*g+u[2]*bl, u[3]*r+u[4]*g+u[5]*bl, u[6]*r+u[7]*g+u[8]*bl
	if op.undoGamma {
		r, g, bl = dec(max(0, r)), dec(max(0, g)), dec(max(0, bl))
	}
	return r, g, bl
}

// highlightDesat is how much a pixel's chroma is faded because it is clipping
// towards white: 0 below linear 0.9, 1 at 1. A pixel whose darkest channel is
// still nearly white is a clipped white, whatever tint the channels picked up
// on the way (raw base curves, JPEG clipping); keeping that tint would draw a
// coloured rim around every clipped patch.
func highlightDesat(r, g, b float32) float32 {
	mn := min(r, min(g, b))
	if mn <= 0.9 {
		return 0
	}
	if mn >= 1 {
		return 1
	}
	u := (mn - 0.9) / 0.1
	return u * u * (3 - 2*u)
}

// applyMeasured is apply for the measured model: tone steps work on
// perceptual brightness and scale colourfulness by their measured ratio.
func (op *pixOp) applyMeasured(r, g, bl, m, da, db float32) (float32, float32, float32) {
	if op.wb != nil {
		r, g, bl = op.wb.Pixel(r, g, bl)
	}
	if op.modeMap != nil {
		r, g, bl = op.modeMap.Pixel(r, g, bl)
	}
	r, g, bl = r*op.tint[0], g*op.tint[1], bl*op.tint[2]
	desat := highlightDesat(r, g, bl)
	Y := 0.2126*r + 0.7152*g + 0.0722*bl
	if op.monoNew {
		// SPP's monochrome base tone (see monoBaseGamma); on the per-layer
		// path the task-54 high-ISO black lift and exponent apply instead.
		if op.monoLayers != nil {
			Y = max(0, Y+op.monoB)
			if Y > 0 {
				Y = float32(math.Pow(float64(Y), float64(op.monoGamma)))
			}
		} else if Y > 0 {
			Y = float32(math.Pow(float64(Y), monoBaseGamma))
		}
	}
	cr, cg, cb := r-Y, g-Y, bl-Y
	x, chroma := op.preL.at(enc(Y))
	if op.fillL != nil {
		xd, k := op.fillL.apply(float64(x), float64(da), float64(db))
		x, chroma = float32(xd), chroma*float32(k)
	} else if op.fillM != nil {
		xd, k := op.fillM.apply(float64(x), float64(m), float64(da), float64(db))
		x, chroma = float32(xd), chroma*float32(k)
	}
	x, k := op.postL.at(x)
	chroma *= k
	Y2 := dec(x)
	if op.mono {
		return Y2, Y2, Y2
	}
	f := chroma * float32(op.satMeasure) * (1 - desat)
	return Y2 + cr*f, Y2 + cg*f, Y2 + cb*f
}

// applyAt is apply for a pixel of a prepared Base: on the per-layer
// Monochrome path it mixes the layer tone SPP would (each layer scaled and
// floored, the mixer's weights after that) and carries the result as the
// grey through the ordinary tone steps.
func (op *pixOp) applyAt(i int, r, g, bl, m, da, db float32) (float32, float32, float32) {
	if op.monoLayers != nil {
		j := i * 3
		q0, q1, q2 := op.monoLayers[j], op.monoLayers[j+1], op.monoLayers[j+2]
		// The lens filter and the layer sliders act on the camera layers
		// (G), then the high-ISO per-layer gain and floor, then the mixer.
		l0 := op.monoG[0]*q0 + op.monoG[1]*q1 + op.monoG[2]*q2
		l1 := op.monoG[3]*q0 + op.monoG[4]*q1 + op.monoG[5]*q2
		l2 := op.monoG[6]*q0 + op.monoG[7]*q1 + op.monoG[8]*q2
		y := op.monoW[0]*(op.monoA[0]*l0+op.monoF[0]) +
			op.monoW[1]*(op.monoA[1]*l1+op.monoF[1]) +
			op.monoW[2]*(op.monoA[2]*l2+op.monoF[2])
		return op.applyMeasured(max(0, y), max(0, y), max(0, y), m, da, db)
	}
	return op.apply(r, g, bl, m, da, db)
}

// apply maps one linear pixel; m is the blurred perceptual luminance around
// it (Fill Light's mask).
func (op *pixOp) apply(r, g, bl, m, da, db float32) (float32, float32, float32) {
	if op.undo != nil {
		r, g, bl = op.unstyle(r, g, bl)
	}
	if op.layer != nil || op.layerMono != nil {
		r, g, bl = op.layerFilter(r, g, bl)
	}
	if op.measured {
		return op.applyMeasured(r, g, bl, m, da, db)
	}
	if f := op.cfilm; f != nil {
		// The film is exposed by the scene, and the colour adjust tints the
		// result. Tinting first feeds the film layer ratios no scene has, and
		// a slide's contrast turns a small tint into a green picture.
		m := &op.cfInv
		r, g, bl = r*op.exp, g*op.exp, bl*op.exp
		r, g, bl = f.apply(m[0]*r+m[1]*g+m[2]*bl, m[3]*r+m[4]*g+m[5]*bl, m[6]*r+m[7]*g+m[8]*bl)
		r, g, bl = r*op.cr/op.exp, g*op.cg/op.exp, bl*op.cb/op.exp
	} else {
		r, g, bl = r*op.cr, g*op.cg, bl*op.cb
	}

	if op.fill != 0 {
		m := min(1, m*op.maskScale)
		wt := (1 - m) * (1 - m)
		gain := float32(math.Exp2(float64(op.fill * wt)))
		r, g, bl = r*gain, g*gain, bl*gain
	}

	Y := 0.2126*r + 0.7152*g + 0.0722*bl
	desat := highlightDesat(r, g, bl)
	if op.monoNew && Y > 0 {
		Yg := float32(math.Pow(float64(Y), monoBaseGamma))
		k := Yg / Y
		r, g, bl, Y = r*k, g*k, bl*k, Yg
	}
	if op.tone != nil && Y > 1e-6 {
		// The film: the exposure (above) is the camera's, the tone steps
		// (below) the print's.
		Yf := op.tone.at(Y)
		kk := Yf / Y
		r, g, bl, Y = r*kk, g*kk, bl*kk, Yf
	}
	if Y > 1e-6 {
		Y2 := Y
		if op.hl < 0 {
			// A shoulder, not a scale: SPP's Highlight darkens the upper
			// midtones and returns to white (its measured -1 curve has a
			// -0.03 peak and ends ~0 at 1), while the old Y/(1-hl·Y²) took
			// 26% off white and left clipped white a flat grey. This form is
			// monotonic over the slider's range and leaves 1 at 1.
			Y2 = Y * (1 + 2*op.hl*Y*(1-Y))
		} else if op.hl > 0 {
			Y2 = Y * (1 + op.hl*Y*Y)
		}
		v := enc(Y2)
		if op.blk != 0 {
			v = max(0, (v-op.blk)/(1-op.blk))
		}
		if op.ct != 0 {
			v = min(1, max(0, v))
			s := v * v * (3 - 2*v)
			v += op.ct * (s - v)
		}
		Y3 := dec(v)
		kk := Y3 / Y
		r, g, bl = r*kk, g*kk, bl*kk
		Y = Y3
	}

	f := op.satF * (1 - float64(desat))
	if op.mode == preset.ModePortrait || op.mode == preset.ModeLandscape {
		hx := float64(r) - 0.5*float64(g+bl)
		hy := 0.8660254 * float64(g-bl)
		hue := math.Atan2(hy, hx) * 180 / math.Pi
		if hue < 0 {
			hue += 360
		}
		if op.mode == preset.ModePortrait {
			// Softer colour overall, skin tones kept.
			skin := hueWeight(hue, 30, 35)
			f *= 0.86 + 0.12*skin
		} else {
			// Landscape: greens and blues pushed.
			gb := math.Max(hueWeight(hue, 110, 60), hueWeight(hue, 215, 45))
			f *= 1.05 + 0.15*gb
		}
	}
	if op.mono {
		r, g, bl = Y, Y, Y
	} else if f != 1 {
		ff := float32(f)
		r, g, bl = Y+(r-Y)*ff, Y+(g-Y)*ff, Y+(bl-Y)*ff
	}
	return r, g, bl
}

// monoShadeAmp is how much of the encoded grey SPP's Shading Effect takes
// away at the frame's corner at full amount, from the three exports (task
// 50: 0.56 at the corner, the same for radius .5 and 1).
const monoShadeAmp = 0.56

// monoShade applies SPP's Shading Effect: a radial gain about the WHOLE
// frame's centre (a crop is not the frame), falling from 1 at the radius
// SPP calls the shading radius to 1-monoShadeAmp at the corner, and a
// constant -1/255 that every measured shading export carries. The gain is
// multiplicative on the encoded value: across bright and dark corners the
// ratio was the same. Fitted to 01-shade-.5-.5, -.5-1 and -1-1; the amount
// is mapped 0 = off to 1 = SPP's full, because SPP's .5 measured as no
// vignette (only the -1), so its slider has a neutral in the middle.
func (op *pixOp) monoShade(b *Base, x, y int, r, g, bl float32) (float32, float32, float32) {
	if !op.monoNew || op.monoShadeA <= 0 {
		return r, g, bl
	}
	fw, fh := b.fullSize()
	cx, cy := fw/2, fh/2
	px, py := b.fullCoord(x, y)
	rn := math.Hypot(px-cx, py-cy) / math.Hypot(cx, cy)
	rad := float64(op.monoShadeR)
	inner := 0.72 * rad
	if inner > 0.95 {
		inner = 0.95
	}
	power := 6.06 - 3.32*rad
	t := (rn - inner) / (1 - inner)
	t = math.Max(0, math.Min(1, t))
	gain := 1 - monoShadeAmp*float64(op.monoShadeA)*math.Pow(t, power)
	sh := func(v float32) float32 {
		u := float64(enc(v))*gain - 1.0/255.0
		u = math.Max(0, math.Min(1, u))
		return dec(float32(u))
	}
	return sh(r), sh(g), sh(bl)
}

// applyMonoTone adds SPP's toning preset: the measured 8-bit sRGB offsets,
// clamped. They are the same at every tone band, so this is an offset and
// not a curve (task 50).
func (op *pixOp) applyMonoTone(r, g, bl float32) (float32, float32, float32) {
	if !op.monoNew || op.monoTone == [3]float32{} {
		return r, g, bl
	}
	out := [3]float32{r, g, bl}
	for i := range out {
		u := float64(enc(out[i])) + float64(op.monoTone[i])
		out[i] = dec(float32(math.Max(0, math.Min(1, u))))
	}
	return out[0], out[1], out[2]
}

// sharpen16 is sharpen for 16-bit images (big-endian samples).
func sharpen16(img *image.RGBA64, amount float32) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	src := make([]uint8, len(img.Pix))
	copy(src, img.Pix)
	at := func(i int) float32 { return float32(uint16(src[i])<<8 | uint16(src[i+1])) }
	parallelRows(h, func(y0, y1 int) {
		for y := max(1, y0); y < min(h-1, y1); y++ {
			for x := 1; x < w-1; x++ {
				i := (y*w + x) * 8
				for c := 0; c < 6; c += 2 {
					v := at(i + c)
					n := (at(i+c-8) + at(i+c+8) + at(i+c-w*8) + at(i+c+w*8)) / 4
					u := uint16(max(0, min(65535, v+amount*(v-n))) + 0.5)
					img.Pix[i+c], img.Pix[i+c+1] = uint8(u>>8), uint8(u)
				}
			}
		}
	})
}

// sharpen applies a 3x3 cross unsharp mask (amount < 0 softens).
func sharpen(img *image.RGBA, amount float32) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	src := make([]uint8, len(img.Pix))
	copy(src, img.Pix)
	parallelRows(h, func(y0, y1 int) {
		for y := max(1, y0); y < min(h-1, y1); y++ {
			for x := 1; x < w-1; x++ {
				i := (y*w + x) * 4
				for c := 0; c < 3; c++ {
					v := float32(src[i+c])
					n := (float32(src[i+c-4]) + float32(src[i+c+4]) + float32(src[i+c-w*4]) + float32(src[i+c+w*4])) / 4
					img.Pix[i+c] = uint8(max(0, min(255, v+amount*(v-n))) + 0.5)
				}
			}
		}
	})
}

// forBase finishes the parts of a render that depend on the image itself:
// Fill Light's frame-mean term.
func (op *pixOp) forBase(b *Base) {
	if op.monoNew {
		op.monoLayers = b.MonoLayers
	}
	if op.fillM != nil && op.fillL != nil && op.fillM.usesMean() && b.fillMean > 0 {
		// In the space Fill Light sees a pixel's brightness: after the
		// exposure step (applyMeasured's preL), so a flat frame's mean is
		// exactly its pixels' brightness.
		m := b.fillMean
		if op.preL != nil {
			m, _ = op.preL.at(m)
		}
		op.fillL = op.fillM.bake(float64(m))
	}
}
