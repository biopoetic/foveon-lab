// Package render applies an approximation of Sigma Photo Pro's adjustments
// to an already-rendered sRGB image (the JPEG preview embedded in an X3F).
//
// It is an EMULATION: SPP's own algorithms (X3 Fill Light, colour modes,
// highlight recovery) are not public, so the aim is to show the direction
// and relative strength of each preset, not to reproduce SPP pixel for pixel.
package render

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
)

// Linear is an RGB image in linear light, 3 float32 per pixel.
type Linear struct {
	W, H int
	Pix  []float32
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

	colMap := make([]int, sw)
	for x := range colMap {
		colMap[x] = x * dw / sw
	}
	get := pixelGetter(img)
	l := &Linear{W: dw, H: dh, Pix: make([]float32, dw*dh*3)}
	parallelRows(dh, func(y0, y1 int) {
		sum := make([]float32, dw*3)
		cnt := make([]float32, dw)
		for dy := y0; dy < y1; dy++ {
			clear(sum)
			clear(cnt)
			for sy := dy * sh / dh; sy < (dy+1)*sh/dh; sy++ {
				for sx := 0; sx < sw; sx++ {
					r, g, bb := get(b.Min.X+sx, b.Min.Y+sy)
					d := colMap[sx]
					sum[d*3] += dec8[r]
					sum[d*3+1] += dec8[g]
					sum[d*3+2] += dec8[bb]
					cnt[d]++
				}
			}
			row := l.Pix[dy*dw*3:]
			for x := 0; x < dw; x++ {
				if c := cnt[x]; c > 0 {
					row[x*3], row[x*3+1], row[x*3+2] = sum[x*3]/c, sum[x*3+1]/c, sum[x*3+2]/c
				}
			}
		}
	})
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
func (l *Linear) Rotate(deg int) *Linear {
	if deg != 90 && deg != 180 && deg != 270 {
		return l
	}
	w, h := l.W, l.H
	o := &Linear{W: w, H: h, Pix: make([]float32, len(l.Pix))}
	if deg != 180 {
		o.W, o.H = h, w
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch deg {
			case 90:
				nx, ny = h-1-y, x
			case 180:
				nx, ny = w-1-x, h-1-y
			case 270:
				nx, ny = y, w-1-x
			}
			copy(o.Pix[(ny*o.W+nx)*3:][:3], l.Pix[(y*w+x)*3:][:3])
		}
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
	o := &Linear{W: dw, H: dh, Pix: make([]float32, dw*dh*3)}
	parallelRows(dh, func(y0, y1 int) {
		for dy := y0; dy < y1; dy++ {
			sy0, sy1 := dy*l.H/dh, (dy+1)*l.H/dh
			for dx := 0; dx < dw; dx++ {
				sx0, sx1 := dx*l.W/dw, (dx+1)*l.W/dw
				var r, g, b float32
				for sy := sy0; sy < sy1; sy++ {
					row := l.Pix[(sy*l.W)*3:]
					for sx := sx0; sx < sx1; sx++ {
						r += row[sx*3]
						g += row[sx*3+1]
						b += row[sx*3+2]
					}
				}
				n := float32((sy1 - sy0) * (sx1 - sx0))
				i := (dy*dw + dx) * 3
				o.Pix[i], o.Pix[i+1], o.Pix[i+2] = r/n, g/n, b/n
			}
		}
	})
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
	for y := 0; y < l.H; y++ {
		for x := 0; x < l.W; x++ {
			i := (y*l.W + x) * 3
			Y := 0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2]
			g := (y/cell)*gw + x/cell
			m.v[g] += enc(Y)
			cnt[g]++
		}
	}
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
	for y := 0; y < h; y++ {
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
	for y := 0; y < h; y++ {
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
	m *mask
}

// NewBase prepares a linear image for repeated rendering.
func NewBase(l *Linear) *Base { return &Base{Linear: l, m: newMask(l)} }

// Compensation undoes the camera's own JPEG styling before a preset is
// applied, so presets start from something closer to SPP's neutral render.
type Compensation struct {
	Contrast   float64 // added to the preset's contrast
	Saturation float64 // added to the preset's saturation
	SatFactor  float64 // extra chroma multiplier (e.g. to cancel Vivid); 0 = 1
}

// CompensationFor derives the compensation from X3F PROP metadata:
// the in-camera colour mode (CM_DESC) and contrast/saturation settings.
func CompensationFor(props map[string]string) Compensation {
	c := Compensation{SatFactor: 1}
	parse := func(k string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(props[k], ",", ".")), 64)
		if err != nil {
			return 0
		}
		return v
	}
	c.Contrast = -parse("CONT_DESC")
	c.Saturation = -parse("SATU_DESC")
	switch props["CM_DESC"] {
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
	w, h := b.W, b.H
	out := image.NewRGBA(image.Rect(0, 0, w, h))

	// Colour adjust: normalised so it tints without changing brightness.
	k := 1 / (0.2126*p.R + 0.7152*p.G + 0.0722*p.B)
	if p.R <= 0 || p.G <= 0 || p.B <= 0 || math.IsInf(k, 0) {
		p.R, p.G, p.B, k = 1, 1, 1, 1
	}
	expGain := math.Exp2(p.Exposure)
	cr, cg, cb := float32(p.R*k*expGain), float32(p.G*k*expGain), float32(p.B*k*expGain)
	maskScale := float32(math.Exp2(p.Exposure / 2.2))
	fill := float32(p.FillLight * 1.5)
	hl := float32(p.Highlight * 0.35)
	blk := float32(math.Max(-0.3, math.Min(0.3, p.Blackness*0.08)))
	ct := float32(math.Max(-1, math.Min(1, (p.Contrast+c.Contrast)*1.2)))

	sat := p.Saturation + c.Saturation
	var satF float64
	mono := p.Saturation <= -1
	switch {
	case sat < 0:
		satF = math.Max(0, 1+sat)
	default:
		satF = 1 + 0.6*sat
	}
	if c.SatFactor > 0 {
		satF *= c.SatFactor
	}
	mode := p.ColorMode

	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				r, g, bl := b.Pix[i]*cr, b.Pix[i+1]*cg, b.Pix[i+2]*cb

				if fill != 0 {
					m := min(1, b.m.at(x, y)*maskScale)
					wt := (1 - m) * (1 - m)
					gain := float32(math.Exp2(float64(fill * wt)))
					r, g, bl = r*gain, g*gain, bl*gain
				}

				Y := 0.2126*r + 0.7152*g + 0.0722*bl
				if Y > 1e-6 {
					Y2 := Y
					if hl < 0 {
						Y2 = Y / (1 - hl*Y*Y)
					} else if hl > 0 {
						Y2 = Y * (1 + hl*Y*Y)
					}
					v := enc(Y2)
					if blk != 0 {
						v = max(0, (v-blk)/(1-blk))
					}
					if ct != 0 {
						v = min(1, max(0, v))
						s := v * v * (3 - 2*v)
						v += ct * (s - v)
					}
					Y3 := dec(v)
					kk := Y3 / Y
					r, g, bl = r*kk, g*kk, bl*kk
					Y = Y3
				}

				f := satF
				if mode == preset.ModePortrait || mode == preset.ModeLandscape {
					hx := float64(r) - 0.5*float64(g+bl)
					hy := 0.8660254 * float64(g-bl)
					hue := math.Atan2(hy, hx) * 180 / math.Pi
					if hue < 0 {
						hue += 360
					}
					if mode == preset.ModePortrait {
						// Softer colour overall, skin tones kept.
						skin := hueWeight(hue, 30, 35)
						f *= 0.86 + 0.12*skin
					} else {
						// Landscape: greens and blues pushed.
						gb := math.Max(hueWeight(hue, 110, 60), hueWeight(hue, 215, 45))
						f *= 1.05 + 0.15*gb
					}
				}
				if mono {
					r, g, bl = Y, Y, Y
				} else if f != 1 {
					ff := float32(f)
					r, g, bl = Y+(r-Y)*ff, Y+(g-Y)*ff, Y+(bl-Y)*ff
				}

				o := out.Pix[(y*w+x)*4:]
				o[0] = uint8(enc(r)*255 + 0.5)
				o[1] = uint8(enc(g)*255 + 0.5)
				o[2] = uint8(enc(bl)*255 + 0.5)
				o[3] = 255
			}
		}
	})

	if p.Sharpness != 0 {
		sharpen(out, float32(math.Max(-1, math.Min(2, p.Sharpness*1.5))))
	}
	return out
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
