package main

import (
	"fmt"
	"image"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

// camfWBName maps the X3F's WB_DESC to the illuminant name CAMF uses.
var camfWBName = map[string]string{
	"Daylight": "Sunlight", "Sunlight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast",
	"Incandescent": "Incandescent", "Fluorescent": "Fluorescent", "Flash": "Flash",
	"Auto": "Auto", "Custom": "Custom",
}

// camfTransform reads the photo's white-balance gains and colour matrix
// from its CAMF block, for its in-camera white
// balance — what Sigma Photo Pro applies to the raw data.
func camfTransform(ph *Photo) (render.CamTransform, error) {
	var t render.CamTransform
	wb := camfWBName[strings.TrimSpace(ph.props["WB_DESC"])]
	if wb == "" {
		wb = "Auto"
	}
	m, err := camfMatrices(ph.path)
	if err != nil {
		return t, err
	}
	g, cc := m[wb+"WBGain"], m[wb+"CCMatrix"]
	if len(g) != 3 || len(cc) != 9 {
		return t, fmt.Errorf("%s: no %s white balance in CAMF", ph.Name, wb)
	}
	copy(t.Gain[:], g)
	copy(t.CC[:], cc)
	return t, nil
}

// camfMatrices returns every numeric CAMF matrix of an X3F by name.
func camfMatrices(path string) (map[string][]float64, error) {
	f, err := x3f.Open(path)
	if err != nil {
		return nil, err
	}
	c, err := f.CAMF()
	if err != nil {
		return nil, err
	}
	return c.Matrices, nil
}

// photoCC is ph's own as-shot white-balance colour matrix from its CAMF
// (nil if it can't be read): the colour-mode undo needs the camera body's
// matrix, which differs from body to body.
func photoCC(ph *Photo) *[9]float64 {
	ph.camfOnce.Do(func() {
		if ph.Kind != "x3f" {
			return
		}
		if t, err := camfTransform(ph); err == nil {
			cc := t.CC
			ph.camfCC = &cc
		}
	})
	return ph.camfCC
}

// goDevelop decodes and develops ph's raw data natively (no x3f_extract):
// linear camera RGB (noise below black kept) and the transform to linear
// output RGB — its CAMF white balance + colour matrix (x3f.WBTransform,
// which also covers the SD9/SD10/SD14/DP1's camera-to-XYZ form).
func goDevelop(ph *Photo) (*x3f.CamImage, render.CamTransform, error) {
	var t render.CamTransform
	f, err := x3f.Open(ph.path)
	if err != nil {
		return nil, t, err
	}
	c, err := f.CAMF()
	if err != nil {
		return nil, t, err
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		return nil, t, err
	}
	x3f.FixBadPixels(raw, c)
	wb := camfWBName[strings.TrimSpace(ph.props["WB_DESC"])]
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return nil, t, err
	}
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return nil, t, err
	}
	t.Gain, t.CC = gain, cc
	return img, t, nil
}

// goLinear is ph's raw data as linear output RGB at most maxW wide (0 =
// full size, unrotated), with Foveon noise reduction of the given strength
// (0 = off). Previews are binned in camera space first, then denoised at
// that scale (binning already averages noise; the denoiser measures what
// is left).
func goLinear(ph *Photo, maxW int, strength float64) (*render.Linear, error) {
	img, t, err := goDevelop(ph)
	if err != nil {
		return nil, err
	}
	if strength > 0 {
		b := 1
		if maxW > 0 {
			b = max(1, img.W/maxW)
		}
		if b > 1 {
			img = binCam(img, b)
		}
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: strength, Scale: 1 / float64(b)})
	}
	return render.FromCamLinear(img.Pix, img.W, img.H, maxW, 0, t), nil
}

// binCam averages b×b blocks of camera RGB.
func binCam(c *x3f.CamImage, b int) *x3f.CamImage {
	w, h := c.W/b, c.H/b
	out := &x3f.CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	inv := 1 / float32(b*b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s [3]float32
			for yy := y * b; yy < y*b+b; yy++ {
				row := c.Pix[(yy*c.W+x*b)*3:]
				for xx := 0; xx < b; xx++ {
					s[0] += row[xx*3]
					s[1] += row[xx*3+1]
					s[2] += row[xx*3+2]
				}
			}
			o := (y*w + x) * 3
			out.Pix[o], out.Pix[o+1], out.Pix[o+2] = s[0]*inv, s[1]*inv, s[2]*inv
		}
	}
	return out
}

// linearToSRGB16 encodes linear RGB as a 16-bit sRGB image (clipped).
func linearToSRGB16(l *render.Linear) *image.RGBA64 {
	out := image.NewRGBA64(image.Rect(0, 0, l.W, l.H))
	for i := 0; i < l.W*l.H; i++ {
		o := (i/l.W)*out.Stride + (i%l.W)*8
		for k := 0; k < 3; k++ {
			v := render.ToSRGB(max(0, min(1, l.Pix[i*3+k])))
			u := uint16(v*65535 + 0.5)
			out.Pix[o+2*k], out.Pix[o+2*k+1] = byte(u>>8), byte(u)
		}
		out.Pix[o+6], out.Pix[o+7] = 0xff, 0xff
	}
	return out
}
