package x3f

import (
	"errors"
	"image"
	"math"
)

// CameraRGB turns decoded raw data into linear camera RGB scaled to 0-65535:
// cropped to the sensor's ActiveImageArea, the black level (mean of the
// DarkShieldTop/Bottom rows) subtracted and each layer divided by its white
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
	var sum [3]float64
	n := 0
	for _, name := range []string{"DarkShieldTop", "DarkShieldBottom"} {
		x0, y0, x1, y1, ok := rect(name)
		if !ok {
			continue
		}
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				p := raw.Pix[(y*raw.W+x)*3:]
				sum[0] += float64(p[0])
				sum[1] += float64(p[1])
				sum[2] += float64(p[2])
				n++
			}
		}
	}
	var black [3]float64
	if n > 0 {
		for k := range black {
			black[k] = sum[k] / float64(n)
		}
	}
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
	gain := make([]float64, ow*3)
	for i := range gain {
		gain[i] = 1
	}
	for y := y0; y <= y1; y++ {
		src := raw.Pix[(y*raw.W+x0)*3:]
		dst := out.Pix[(y-y0)*ow*3:]
		if sg != nil {
			for x := 0; x < ow; x++ {
				for k := 0; k < 3; k++ {
					gain[x*3+k] = sg.At(y-y0, x, k, oh, ow)
				}
			}
		}
		for x := 0; x <= x1-x0; x++ {
			fb, ft := 1.0, 1.0
			if shade != nil {
				dx, dy := float64(x+x0)-cx, float64(y)-cy
				r2 := (dx*dx + dy*dy) / den2
				fb = 1 + shade[0]*r2*r2 + shade[1]*r2
				ft = 1 + shade[2]*r2*r2 + shade[3]*r2
			}
			for k := 0; k < 3; k++ {
				v := (float64(src[x*3+k]) - black[k]) * scale[k] * gain[x*3+k]
				if k == 0 {
					v *= fb
				} else if k == 2 {
					v *= ft
				}
				dst[x*3+k] = float32(v)
			}
		}
	}
	return out, nil
}
