package main

import (
	"fmt"
	"image"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
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
const (
	despeckleFraction = 0.6
	despeckleFloor    = 0.0015
)

func goDevelop(ph *Photo) (*x3f.CamImage, render.CamTransform, error) {
	return goDevelopFor(ph, false, false, "")
}

// identity33 is the 3×3 identity.
var identity33 = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}

// goDevelopFor: layers = the sensor's own layers balanced by the camera's
// Sunlight gains, with no colour matrix (for the infrared looks, which
// model the layers themselves; the matrix assumes the IR cut).
// goDevelopFor develops ph; layers: as the sensor's own layers (no colour
// matrix), balanced for Sunlight (the IR looks are calibrated on it) or,
// with sceneWB, for the photo's own white balance like the normal path
// (colour film starts from the scene as the photographer balanced it).
// wbName, when set, is the camera white balance to develop with instead
// (a preset's WhiteBalance code: SPP develops its codes with the camera's
// own balance for that light — checked against SPP's exports).
func goDevelopFor(ph *Photo, layers, sceneWB bool, wbName string) (*x3f.CamImage, render.CamTransform, error) {
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
	wb := developWB(c, ph)
	if layers && !sceneWB {
		wb = "Sunlight"
	}
	if wbName != "" {
		wb = wbName
	}
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return nil, t, err
	}
	// (A single-sample outlier rejection ran here while the coloured dots in
	// dark areas were being chased, but the dots turned out to be the guided
	// filter's runaway coefficient, fixed in x3f/denoise.go: the pass cost ten
	// seconds per decode on a 15 MP frame and purchased nothing.)
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return nil, t, err
	}
	t.Gain, t.CC = gain, cc
	if layers {
		t.Colour, t.CC = &cc, identity33
	}
	return img, t, nil
}

// goLinear is ph's raw data as linear output RGB at most maxW wide (0 =
// full size, unrotated), with Foveon noise reduction of the given strength
// (0 = off). Previews are binned in camera space first, then denoised at
// that scale (binning already averages noise; the denoiser measures what
// is left).
func goLinear(ph *Photo, maxW int, strength float64) (*render.Linear, error) {
	return goLinearFor(ph, maxW, strength, nil, false, "")
}

// goLinearFor is goLinear, as the sensor's layers when irWhite is set
// (clipped highlights then go to that white): Sunlight-balanced, or with
// sceneWB balanced like the photo itself (goDevelopFor).
func goLinearFor(ph *Photo, maxW int, strength float64, irWhite *[3]float64, sceneWB bool, wbName string) (*render.Linear, error) {
	timing := os.Getenv("FOVEON_TIMING") != ""
	mark := time.Now()
	lap := func(name string) {
		if timing {
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", name, time.Since(mark).Round(time.Millisecond))
			mark = time.Now()
		}
	}
	img, t, err := goDevelopFor(ph, irWhite != nil, sceneWB, wbName)
	if err != nil {
		return nil, err
	}
	lap("develop")
	// Before the denoiser: its chroma filter would spread the clipped
	// highlights' false colour into their neighbours.
	if irWhite != nil {
		x3f.ClipHighlightsTo(img, t.Gain, *irWhite)
	} else {
		x3f.ClipHighlights(img, t.Gain)
	}
	fullW := img.W
	// A reduced-height readout (SD1 MED: rows binned in pairs, 4704×1568
	// for a 3:2 frame) is brought to the frame's shape per axis on a
	// reduced view. Shrinking both axes alike, then stretching the rows
	// (rawLinearFor), kept 533 of the 1568 rows on a 1600 view: MED
	// previews lost two thirds of their vertical detail.
	dw, dh := 0, 0
	if maxW > 0 && maxW < img.W && ph.previewW > 0 && ph.previewH > 0 {
		if r := float64(img.W*ph.previewH) / float64(img.H*ph.previewW); r >= 1.5 || r <= 0.67 {
			dw, dh = maxW, int(math.Round(float64(maxW*ph.previewH)/float64(ph.previewW)))
			if dh > img.H {
				dw, dh = 0, 0
			}
		}
	}
	// The binning factor is chosen for both paths, so a denoised view and the
	// plain one come out at the same size: with noise reduction the frame is
	// binned first (binning averages noise), and either way the source is
	// capped at that width - a 4704 px frame binned to 1600 gives 1568, and
	// the plain path follows it instead of rendering 1600 and shifting on the
	// swap.
	origW := img.W
	bx, by := 1, 1
	if dw > 0 {
		bx, by = max(1, (img.W+dw-1)/dw), max(1, (img.H+dh-1)/dh)
	} else if maxW > 0 {
		// Round up: 4704/1600 truncates to 2, which leaves a 2352 px
		// preview - 2.25x the pixels to denoise for a view shown at 1600.
		bx = max(1, (img.W+maxW-1)/maxW)
		by = bx
	}
	if strength > 0 {
		if bx > 1 || by > 1 {
			img = binCam2(img, bx, by)
		}
		lap(fmt.Sprintf("bin %dx%d -> %dx%d", bx, by, img.W, img.H))
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: strength, Scale: 1 / float64(bx), ISO: photoISO(ph), Grey: greyOf(t)})
	}
	lap("denoise")
	if dw == 0 && bx > 1 {
		if binnedW := origW / bx; maxW > binnedW {
			maxW = binnedW
		}
	}
	var l *render.Linear
	if dw > 0 {
		l = render.FromCamLinearSize(img.Pix, img.W, img.H, dw, dh, 0, t)
	} else {
		l = render.FromCamLinear(img.Pix, img.W, img.H, maxW, 0, t)
	}
	lap("to linear")
	// The low-frequency shadow colour noise the layer denoiser misses
	// (render/shadowchroma.go), with noise reduction on: the colour path
	// and colour film (its layers, fixed through the colour they make);
	// not the IR looks, which model the layers themselves.
	lap("develop+bin")
	if strength > 0 && (irWhite == nil || sceneWB) {
		noise := render.ShadowChromaFix(l, fullW, t.Colour)
		if os.Getenv("FOVEON_DN_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "shadow chroma noise %s: %.4f\n", ph.Name, noise)
		}
	}
	// The body's brightness relative to its calibration group (not for the
	// IR looks, which model the sensor layers themselves).
	if g := render.ModelExposure(modelOf(ph)); g != 1 && irWhite == nil {
		for i := range l.Pix {
			l.Pix[i] *= g
		}
	}
	l.ISO, _ = strconv.ParseFloat(strings.TrimSpace(ph.props["ISO"]), 64)
	l.Sensor = render.SharpKey(calibGroup(ph))
	return l, nil
}

// binCam averages b×b blocks of camera RGB.
func binCam(c *x3f.CamImage, b int) *x3f.CamImage { return binCam2(c, b, b) }

// binCam2 averages bx×by blocks of camera RGB.
func binCam2(c *x3f.CamImage, bx, by int) *x3f.CamImage {
	w, h := c.W/bx, c.H/by
	out := &x3f.CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	inv := 1 / float32(bx*by)
	render.ParallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				var s [3]float32
				for yy := y * by; yy < y*by+by; yy++ {
					row := c.Pix[(yy*c.W+x*bx)*3:]
					for xx := 0; xx < bx; xx++ {
						s[0] += row[xx*3]
						s[1] += row[xx*3+1]
						s[2] += row[xx*3+2]
					}
				}
				o := (y*w + x) * 3
				out.Pix[o], out.Pix[o+1], out.Pix[o+2] = s[0]*inv, s[1]*inv, s[2]*inv
			}
		}
	})
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

// exportCameraDNG writes ph's raw data as a camera DNG natively: decoded,
// bad pixels repaired, black level, white level and colour shading
// applied, optionally denoised (NR as for raw-based exports), with the
// file's own white balance + colour matrix as the DNG colour metadata and,
// where a raw base exists, SPP's neutral look as its embedded profile.
func (s *server) exportCameraDNG(w http.ResponseWriter, ph *Photo, dir, stem string, p preset.Params, label string, denoise *int) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	start := time.Now()
	img, t, err := goDevelop(ph)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	nr := defaultNR
	if denoise != nil {
		nr = float64(max(0, min(10, *denoise))) / 10
	}
	if nr > 0 {
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: nr, Scale: 1, ISO: photoISO(ph), Grey: greyOf(t)})
	}
	path := filepath.Join(dir, stem+".dng")
	f, err := os.Create(path)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	// SPP's neutral colours as the DNG's own profile, where a raw base was
	// fitted for this camera group; else the camera's matrix.
	var prof *render.DNGProfile
	if c := render.RawBaseFitted(calibGroup(ph), wbDesc(ph)); c != nil {
		prof = c.DNGProfile("Foveon Lab SPP Neutral (" + calibGroup(ph) + ")")
	}
	err = render.WriteCameraDNG(f, img.Pix, img.W, img.H, t.Gain, t.CC, prof, "SIGMA", dngModel(ph), "Foveon Lab "+version)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		httpErr(w, 500, err)
		return
	}
	log.Printf("exported %s (camera DNG, NR %.1f, %v)", path, nr, time.Since(start).Round(100*time.Millisecond))
	cube := filepath.Join(dir, stem+"_"+safeName(label)+".cube")
	if cf, err := os.Create(cube); err == nil {
		err = render.WriteCube(cf, label, p, 33)
		if cerr := cf.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			cube = ""
		}
	} else {
		cube = ""
	}
	writeJSON(w, map[string]string{"path": path, "lut": cube})
}

// dngModel is the camera model for a DNG: CAMMODEL without "SIGMA ", else
// (Quattro files carry no property list) the folder's camera.
func dngModel(ph *Photo) string {
	if m := strings.TrimPrefix(strings.TrimSpace(ph.props["CAMMODEL"]), "SIGMA "); m != "" {
		return m
	}
	return ph.Camera
}

// layerCC is ph's camera-to-linear-sRGB colour matrix for its in-camera
// white balance (x3f.WBTransform, every body), for the layer filter; nil
// when the CAMF can't be read. Read once per photo.
func layerCC(ph *Photo) *[9]float64 {
	ph.layerOnce.Do(func() {
		if ph.Kind != "x3f" {
			return
		}
		f, err := x3f.Open(ph.path)
		if err != nil {
			return
		}
		c, err := f.CAMF()
		if err != nil {
			return
		}
		if _, cc, err := c.WBTransform(developWB(c, ph)); err == nil {
			ph.layerCC = &cc
		}
	})
	return ph.layerCC
}

// developWB is the white balance a raw development uses: the camera's own
// Auto estimate of the light (stored with every shot), as Sigma Photo
// Pro's "as shot" does, not the WB set on the camera. Measured 2026-10-14
// on exports with every SPP WB code: SPP's as-shot equals codes 0/1/9 and
// differs from its Sunlight even on a Sunlight shot; with the camera's
// Auto WB the held-out raw base vs SPP fell SD1 6.87 -> 3.97, DP Merrill
// 6.42 -> 4.21, SD15 8.44 -> 6.03 dE2000. FOVEON_WB=camera uses the set WB.
func developWB(c *x3f.CAMF, ph *Photo) string {
	if !wbCamera && c.Props["WhiteBalanceGains"]["Auto"] != "" {
		return "Auto"
	}
	return c.CameraWB(strings.TrimSpace(ph.props["WB_DESC"]))
}

var wbCamera = os.Getenv("FOVEON_WB") == "camera"

// cameraWBName is the camera white balance SPP develops a preset's
// WhiteBalance code with ("" = as shot): 2 Sunlight, 3 Shade, 4 Overcast,
// 5 Incandescent, 6 Fluorescent, 7 Flash. Developing the raw with the
// camera's own balance for that light reproduces SPP's exports of each
// code to about the as-shot error (exports\wb-codes, two SD1 photos).
func cameraWBName(code int) string {
	return map[int]string{2: "Sunlight", 3: "Shade", 4: "Overcast", 5: "Incandescent", 6: "Fluorescent", 7: "Flash"}[code]
}

// photoISO is ph's ISO (0 if unknown).
func photoISO(ph *Photo) float64 {
	iso, _ := strconv.ParseFloat(strings.TrimSpace(ph.props["ISO"]), 64)
	return iso
}

// greyOf is a neutral grey's camera layer values before the white balance:
// the inverse of its gains (zero when a gain is missing).
func greyOf(t render.CamTransform) [3]float64 {
	var g [3]float64
	for i, v := range t.Gain {
		if v <= 0 {
			return [3]float64{}
		}
		g[i] = 1 / v
	}
	return g
}
