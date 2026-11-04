package main

// A DNG from another camera as a source for the film stage. It is developed
// to linear sRGB with the file's own matrices (dng/), then treated exactly
// like the linear light the app already gets from an X3F development or from
// a JPEG/TIFF: the film stage -- presets, tone, films, grain, halation --
// runs on it. It has no Foveon layers, so the layer filter, the layer mixes
// and the infrared looks (which are matrices on those layers) are hidden.

import (
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
	if rotate && img.Meta.Mirror {
		// With the turn TIFF orientations 2/4/5/7 are given (0/180/90/270
		// clockwise), every one of them is a horizontal mirror AFTER the
		// turn: 4 = 180 + flip H, 5 = 90 + flip H, 7 = 270 + flip H.
		// TestDNGOrientationMatchesSpec checks all eight against
		// dng.ApplyOrientation.
		mirrorLinearH(l)
	}
	if iso, err := strconv.ParseFloat(strings.TrimSpace(ph.props["ISO"]), 64); err == nil && iso > 0 {
		l.ISO = iso
	}
	return l, nil
}

// mirrorLinearH flips an already-turned linear image left to right in place. A DNG with a
// mirrored TIFF Orientation is rare, but applying it is a pure copy and the
// alternative (a note saying the picture is the wrong way round) is worse.
func mirrorLinearH(l *render.Linear) {
	for y := 0; y < l.H; y++ {
		row := l.Pix[y*l.W*3 : (y+1)*l.W*3]
		for x := 0; x < l.W/2; x++ {
			a, b := x*3, (l.W-1-x)*3
			row[a], row[b] = row[b], row[a]
			row[a+1], row[b+1] = row[b+1], row[a+1]
			row[a+2], row[b+2] = row[b+2], row[a+2]
		}
	}
}

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
