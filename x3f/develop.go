package x3f

import (
	"errors"
	"image"
	"math"
)

// CameraRGB turns decoded raw data into linear camera RGB scaled to 0-65535:
// cropped to the sensor's ActiveImageArea, the black level (blackLevel:
// the sensor's masked rows and columns) subtracted and each layer divided by its white
// level (2^ImageDepth-1, else RawSaturationLevel / SaturationLevel), then
// multiplied by the spatial gain sg (lens colour shading; nil = none) and,
// on Merrill files, the radial WB colour shading for white balance wb
// (WhiteBalanceColorShadingFactor; as x3fuse-core's apply_wb_color_shading).
// No bad pixel repair or denoise. Values are clipped to 0..65535; use
// CameraLinear to keep the negative noise below black (needed whenever the
// image is averaged or denoised later: clipping it biases dark areas).
func CameraRGB(raw *RawImage, c *CAMF, sg *SpatialGain, wb string) (*image.RGBA64, error) {
	l, err := CameraLinear(raw, c, sg, wb)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA64(image.Rect(0, 0, l.W, l.H))
	for i := 0; i < l.W*l.H; i++ {
		y, x := i/l.W, i%l.W
		dst := out.Pix[y*out.Stride+x*8:]
		for k := 0; k < 3; k++ {
			u := uint16(math.Max(0, math.Min(65535, float64(l.Pix[i*3+k])*65535)))
			dst[k*2], dst[k*2+1] = byte(u>>8), byte(u)
		}
		dst[6], dst[7] = 0xff, 0xff
	}
	return out, nil
}

// CamImage is linear camera RGB: 0 = black, 1 = each layer's white level;
// noise may take values below 0 (and highlights above 1).
type CamImage struct {
	W, H int
	Pix  []float32 // W*H*3
}

// CameraLinear is CameraRGB without clipping or quantising.
func CameraLinear(raw *RawImage, c *CAMF, sg *SpatialGain, wb string) (*CamImage, error) {
	if raw == nil || c == nil {
		return nil, errors.New("x3f: no raw data or CAMF")
	}
	if raw.Top != nil {
		return cameraLinearQuattro(raw, c, wb, true)
	}
	// CAMF rectangles are inclusive (x0, y0, x1, y1) in KeepImageArea
	// coordinates; a reduced-size readout scales them.
	kx0, ky0, kw, kh := 0.0, 0.0, float64(raw.W), float64(raw.H)
	if k := c.Matrices["KeepImageArea"]; len(k) == 4 {
		kx0, ky0, kw, kh = k[0], k[1], k[2]-k[0]+1, k[3]-k[1]+1
	}
	sx, sy := float64(raw.W)/kw, float64(raw.H)/kh
	rect := func(name string) (x0, y0, x1, y1 int, ok bool) {
		r := c.Matrices[name]
		if len(r) != 4 {
			return 0, 0, 0, 0, false
		}
		x0 = int(math.Round((r[0] - kx0) * sx))
		y0 = int(math.Round((r[1] - ky0) * sy))
		x1 = int(math.Round((r[2]-kx0+1)*sx)) - 1
		y1 = int(math.Round((r[3]-ky0+1)*sy)) - 1
		x0, y0 = max(x0, 0), max(y0, 0)
		x1, y1 = min(x1, raw.W-1), min(y1, raw.H-1)
		return x0, y0, x1, y1, x1 >= x0 && y1 >= y0
	}
	black := blackLevel(raw, c, rect, func(x float64) int { return int(math.Round((x - kx0) * sx)) })
	white := [3]float64{65535, 65535, 65535}
	if d := c.Matrices["ImageDepth"]; len(d) == 1 && d[0] > 0 && d[0] <= 16 {
		v := math.Exp2(d[0]) - 1
		white = [3]float64{v, v, v}
	} else if s := c.Matrices["RawSaturationLevel"]; len(s) == 3 && c.trueEngine() {
		copy(white[:], s)
	} else if s := c.Matrices["SaturationLevel"]; len(s) == 3 {
		copy(white[:], s)
	}
	x0, y0, x1, y1, ok := rect("ActiveImageArea")
	if !ok {
		x0, y0, x1, y1 = 0, 0, raw.W-1, raw.H-1
	}
	var scale [3]float64
	for k := range scale {
		scale[k] = 1 / math.Max(1, white[k]-black[k])
	}
	// Radial shading of the bottom and top layers: 1 + a*r^4 + b*r^2 and
	// 1 + c*r^4 + d*r^2, r relative to the active area's half diagonal.
	var shade []float64
	if name := c.Props["WhiteBalanceColorShadingFactor"][wb]; name != "" {
		shade = c.Matrices[name]
		if len(shade) != 4 {
			shade = nil
		}
	}
	hw, hh := float64(x1-x0)/2, float64(y1-y0)/2
	cx, cy := float64(x0)+hw, float64(y0)+hh
	den2 := hw*hw + hh*hh
	ow, oh := x1-x0+1, y1-y0+1
	out := &CamImage{W: ow, H: oh, Pix: make([]float32, ow*oh*3)}
	// Rows in parallel (each worker its own gain row): serial, this was a
	// sixth of a full-size development.
	var sgr *SGRows
	if sg != nil {
		sgr = sg.NewRows(oh, ow)
	}
	b0, b1, b2 := black[0], black[1], black[2]
	s0, s1, s2 := scale[0], scale[1], scale[2]
	parallel(oh, func(r0, r1 int) {
		gain := make([]float64, ow*3)
		for i := range gain {
			gain[i] = 1
		}
		for y := y0 + r0; y < y0+r1; y++ {
			src := raw.Pix[(y*raw.W+x0)*3:]
			dst := out.Pix[(y-y0)*ow*3:]
			if sgr != nil {
				sgr.Fill(y-y0, gain)
			}
			// No per-channel branch and no shade test inside the pixel loop:
			// the two cases are separate and the three layers are unrolled, so
			// the body is straight arithmetic over contiguous slices.
			if shade == nil {
				for x := 0; x <= x1-x0; x++ {
					i := x * 3
					dst[i] = float32((float64(src[i]) - b0) * s0 * gain[i])
					dst[i+1] = float32((float64(src[i+1]) - b1) * s1 * gain[i+1])
					dst[i+2] = float32((float64(src[i+2]) - b2) * s2 * gain[i+2])
				}
				continue
			}
			for x := 0; x <= x1-x0; x++ {
				i := x * 3
				dx, dy := float64(x+x0)-cx, float64(y)-cy
				r2 := (dx*dx + dy*dy) / den2
				fb := 1 + shade[0]*r2*r2 + shade[1]*r2
				ft := 1 + shade[2]*r2*r2 + shade[3]*r2
				dst[i] = float32((float64(src[i]) - b0) * s0 * gain[i] * fb)
				dst[i+1] = float32((float64(src[i+1]) - b1) * s1 * gain[i+1])
				dst[i+2] = float32((float64(src[i+2]) - b2) * s2 * gain[i+2] * ft)
			}
		}
	})
	return out, nil
}

// ClipHighlights makes clipped highlights neutral. Where a layer reaches
// its white level the sensor stopped counting, so its ratio to the other
// layers (the colour) is wrong; the top layer clips first and a white sky
// or lamp comes out yellow-red (blue-green in black and white). Pixels
// with a layer from 0.9 to 1 of white are blended towards a neutral at the
// brightest white-balanced layer (Sigma Photo Pro renders them white).
// gain is the white balance (WBTransform's).
func ClipHighlights(img *CamImage, gain [3]float64) {
	ClipHighlightsTo(img, gain, [3]float64{1, 1, 1})
}

// ClipHighlightsTo is ClipHighlights towards a given white (as the
// white-balanced layers of a white surface): with the IR cut out a white
// surface is not equal on the Sunlight-balanced layers (the bottom one
// gets ~2.4x), and a neutral fill made blown skies cyan in the infrared
// looks.
func ClipHighlightsTo(img *CamImage, gain, white [3]float64) {
	const lo, hi = 0.9, 1.0
	for k := range gain {
		gain[k] /= white[k]
	}
	g := [3]float32{float32(gain[0]), float32(gain[1]), float32(gain[2])}
	parallel(len(img.Pix)/3, func(i0, i1 int) {
		for i := i0 * 3; i < i1*3; i += 3 {
			p := img.Pix[i : i+3]
			m := max(p[0], p[1], p[2])
			if m <= lo {
				continue
			}
			h := min(1, (m-lo)/(hi-lo))
			h = h * h * (3 - 2*h)
			l := max(p[0]*g[0], p[1]*g[1], p[2]*g[2])
			for k := 0; k < 3; k++ {
				p[k] += h * (l/g[k] - p[k])
			}
		}
	})
}

// blackLevel is the black of each layer, from the sensor's masked areas: the
// DarkShieldTop/Bottom rows and the DarkShieldColRange columns (two at each
// side, over the active rows). A masked area can only read above the true
// black (light reaching it, or other data), never below it, so the black
// is the mean of the areas that agree with the lowest one. Not every area
// holds the black: on the SD15 from ISO 400 the masked rows read ~100-127
// while the masked columns and the image's darkest pixels read ~4 (taking
// the rows crushed every high-ISO SD15 photo to near black); on the SD1 and
// SD14 the right-hand columns read ~20 above the rest.
func blackLevel(raw *RawImage, c *CAMF, rect func(string) (int, int, int, int, bool), col func(float64) int) [3]float64 {
	type ref struct{ m [3]float64 }
	var refs []ref
	mean := func(x0, y0, x1, y1 int) (r ref, ok bool) {
		n := 0.0
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				p := raw.Pix[(y*raw.W+x)*3:]
				r.m[0] += float64(p[0])
				r.m[1] += float64(p[1])
				r.m[2] += float64(p[2])
				n++
			}
		}
		if n == 0 {
			return r, false
		}
		for k := range r.m {
			r.m[k] /= n
		}
		return r, true
	}
	for _, name := range []string{"DarkShieldTop", "DarkShieldBottom"} {
		if x0, y0, x1, y1, ok := rect(name); ok {
			if r, ok := mean(x0, y0, x1, y1); ok {
				refs = append(refs, r)
			}
		}
	}
	if dc := c.Matrices["DarkShieldColRange"]; len(dc) == 4 {
		if _, y0, _, y1, ok := rect("ActiveImageArea"); ok {
			for _, pr := range [][2]float64{{dc[0], dc[1]}, {dc[2], dc[3]}} {
				x0, x1 := max(0, col(pr[0])), min(raw.W-1, col(pr[1]))
				if x1 >= x0 {
					if r, ok := mean(x0, y0, x1, y1); ok {
						refs = append(refs, r)
					}
				}
			}
		}
	}
	var black [3]float64
	if len(refs) == 0 {
		return black
	}
	level := func(r ref) float64 { return (r.m[0] + r.m[1] + r.m[2]) / 3 }
	low := level(refs[0])
	for _, r := range refs[1:] {
		low = math.Min(low, level(r))
	}
	tol := 3 + 0.05*low
	n := 0.0
	for _, r := range refs {
		if level(r) <= low+tol {
			for k := range black {
				black[k] += r.m[k]
			}
			n++
		}
	}
	for k := range black {
		black[k] /= n
	}
	return black
}
