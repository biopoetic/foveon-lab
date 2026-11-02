package main

// A DNG from another camera as a source for the film stage. It is developed
// to linear sRGB with the file's own matrices (dng/), then treated exactly
// like the linear light the app already gets from an X3F development or from
// a JPEG/TIFF: the film stage -- presets, tone, films, grain, halation --
// runs on it. It has no Foveon layers, so the layer filter, the layer mixes
// and the infrared looks (which are matrices on those layers) are hidden.

import (
	"math"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/dng"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// dngDevelop decodes and develops ph at most maxW wide (0 = full size),
// turns it, and wraps it as the app's Linear source.
func dngDevelop(ph *Photo, maxW int) (*render.Linear, error) {
	return dngDevelopRot(ph, maxW, true)
}

// dngDevelopUnrotated is the same development without applying the file's
// rotation: the cull and the list thumbnail turn it themselves, exactly as
// they do a camera JPEG.
func dngDevelopUnrotated(ph *Photo, maxW int) (*render.Linear, error) {
	return dngDevelopRot(ph, maxW, false)
}

func dngDevelopRot(ph *Photo, maxW int, rotate bool) (*render.Linear, error) {
	f, err := dng.Open(ph.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := f.Develop(maxW)
	if err != nil {
		return nil, err
	}
	l := &render.Linear{
		W: img.W, H: img.H, Pix: img.Pix,
		Scale: float64(img.W) / float64(img.FullW),
		Frame: max(img.FullW, img.FullH),
	}
	if rotate && ph.Rotation != 0 {
		l = l.Rotate(ph.Rotation)
	}
	if iso, err := strconv.ParseFloat(strings.TrimSpace(ph.props["ISO"]), 64); err == nil && iso > 0 {
		l.ISO = iso
	}
	return l, nil
}

// dngHasLayers reports whether ph has Foveon sensor layers the layer maths
// can use. Only an X3F does. The UI uses the Photo's kind directly; this
// helper keeps the server-side wording in one place.
func dngHasLayers(ph *Photo) bool { return ph != nil && ph.Kind == "x3f" }

// sourceParams removes the settings that only mean something on Foveon
// layers from a DNG or an ordinary image. The UI hides those controls, but a
// saved preset, an old edit or a direct API call could still carry them; the
// server must not quietly run Foveon layer maths on another camera's data.
func sourceParams(ph *Photo, p preset.Params) preset.Params {
	if ph == nil || ph.Kind == "x3f" {
		return p
	}
	p.Filter = ""
	p.LayerBottom, p.LayerMiddle, p.LayerTop = 0, 0, 0
	p.WhiteBalance = 0
	p.CamMode = ""
	return p
}

// dngPixelScale is the size of the developed image relative to the raw
// frame; kept here for the report and the tests.
func dngPixelScale(ph *Photo) float64 {
	f, err := dng.Open(ph.path)
	if err != nil {
		return 0
	}
	defer f.Close()
	if f.Meta.Width == 0 {
		return 0
	}
	return math.Min(1, float64(sourceW)/float64(f.Meta.Width))
}
