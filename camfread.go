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
		if ph.Kind != "x3f" || rawTool == "" {
			return
		}
		if t, err := camfTransform(ph); err == nil {
			cc := t.CC
			ph.camfCC = &cc
		}
	})
	return ph.camfCC
}

// goCameraRGB decodes and develops ph's raw data natively (no x3f_extract)
// to linear camera RGB, with its CAMF white balance + colour matrix.
func goCameraRGB(ph *Photo) (image.Image, render.CamTransform, error) {
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
	wb := camfWBName[strings.TrimSpace(ph.props["WB_DESC"])]
	img, err := x3f.CameraRGB(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return nil, t, err
	}
	if t, err = camfTransform(ph); err != nil {
		return nil, t, err
	}
	// As x3fuse-core's x3f_get_gain / get_conv: the WB gains times the
	// sensor, temperature and aperture factors, and CaptureISO/SensorISO.
	for _, k := range []string{"SensorAdjustmentGainFact", "TempGainFact", "FNumberGainFact"} {
		if v := c.Matrices[k]; len(v) == 3 {
			for i := range t.Gain {
				t.Gain[i] *= v[i]
			}
		}
	}
	if s, cap := c.Matrices["SensorISO"], c.Matrices["CaptureISO"]; len(s) == 1 && len(cap) == 1 && s[0] > 0 {
		for i := range t.Gain {
			t.Gain[i] *= cap[0] / s[0]
		}
	}
	return img, t, nil
}
