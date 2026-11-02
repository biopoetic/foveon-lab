package main

import (
	"fmt"
	"image"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

// noiseAwareFix: the noise-aware detail restore is fitted and held out on
// the DPM and Quattro sensor families. Other generations keep the old
// restore until their own ladders are measured (SD14/DP2x high ISO lost up
// to 0.47 in spp-check with it).
func noiseAwareFix(group string) bool { return group == "DPM" || group == "Quattro" }

// noDespeckle is the research switch that keeps the pre-fix speckle path
// (FOVEON_DN_NOSPECK=1), for before/after crops.
var noDespeckle = os.Getenv("FOVEON_DN_NOSPECK") != ""

// noShadowChroma switches the low-frequency shadow colour fix off
// (FOVEON_DN_NOSC=1), for isolating the stage that paints a colour patch.
var noShadowChroma = os.Getenv("FOVEON_DN_NOSC") != ""

// mottleFix enables the large-radius Quattro chroma pass (on by default;
// FOVEON_DN_MOTTLE=0 renders without it). It is applied from ISO 6400 only:
// at 800-1600 it made the 8-32 px blotch worse (2.48 -> 3.03 at 800) and at
// 3200 sd Quattro H's rose 16%, so it earns a place only at the top ISO,
// where on dp2 Quattro it now takes the dark-block spread 8.68 -> 7.39, the
// dark b* 4.9 -> 4.2 and the blotch 5.60 -> 5.36 against SPP's 4.91.
var mottleFix = os.Getenv("FOVEON_DN_MOTTLE") != "0"

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
// cachedRaw holds one photo's decoded, bad-pixel-fixed sensor data. The
// preset grid develops the same file several times (one per infrared filter,
// a colour-film base, a Max Detail base), and each development used to
// decode the raw data again — 2 s a time for the same 15 MP. The raw pixels
// are read-only after FixBadPixels; CameraLinear applies white balance and
// gains to its own output, so every development still sees exactly the same
// data it did before. One entry only: the grid is for one photo.
type devRawEntry struct {
	key string
	raw *x3f.RawImage
}

var devRawMu sync.Mutex
var devRawOne *devRawEntry
var devRawBuild = map[string]chan struct{}{}

func cachedRaw(ph *Photo, f *x3f.File, c *x3f.CAMF) (*x3f.RawImage, error) {
	st, err := os.Stat(ph.path)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s|%d|%d", ph.path, st.Size(), st.ModTime().UnixNano())
	timing := os.Getenv("FOVEON_TIMING") != ""
	tStart := time.Now()
	for {
		devRawMu.Lock()
		if devRawOne != nil && devRawOne.key == key {
			e := devRawOne
			devRawMu.Unlock()
			if timing {
				fmt.Fprintf(os.Stderr, "  %-16s %s\n", "raw decode", "cached (0 ms)")
			}
			return e.raw, nil
		}
		if ch, ok := devRawBuild[key]; ok {
			devRawMu.Unlock()
			<-ch // another request is already decoding this file
			continue
		}
		ch := make(chan struct{})
		devRawBuild[key] = ch
		devRawMu.Unlock()

		raw, derr := f.DecodeRaw()
		tDecode := time.Now()
		if derr == nil {
			x3f.FixBadPixels(raw, c)
		}
		if timing {
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", "raw decode", tDecode.Sub(tStart).Round(time.Millisecond))
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", "bad pixels", time.Since(tDecode).Round(time.Millisecond))
		}
		devRawMu.Lock()
		if derr == nil {
			devRawOne = &devRawEntry{key: key, raw: raw}
		}
		delete(devRawBuild, key)
		close(ch)
		devRawMu.Unlock()
		return raw, derr
	}
}

func goDevelopFor(ph *Photo, layers, sceneWB bool, wbName string) (*x3f.CamImage, render.CamTransform, error) {
	var t render.CamTransform
	timing := os.Getenv("FOVEON_TIMING") != ""
	memTiming := os.Getenv("FOVEON_TIMING_MEM") != ""
	mark := time.Now()
	lap := func(name string) {
		if timing {
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", name, time.Since(mark).Round(time.Millisecond))
			mark = time.Now()
		}
		if timing && memTiming {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			fmt.Fprintf(os.Stderr, "  mem after %-16s heap %4d MB\n", name, ms.HeapAlloc>>20)
		}
	}
	f, err := x3f.Open(ph.path)
	if err != nil {
		return nil, t, err
	}
	c, err := f.CAMF()
	if err != nil {
		return nil, t, err
	}
	lap("open+camf")
	raw, err := cachedRaw(ph, f, c)
	if err != nil {
		return nil, t, err
	}
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
	lap("CameraLinear")
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
	return goLinearFor(ph, maxW, strength, nil, false, "", false)
}

// goLinearFor is goLinear, as the sensor's layers when irWhite is set
// (clipped highlights then go to that white): Sunlight-balanced, or with
// sceneWB balanced like the photo itself (goDevelopFor). rebuild restores
// clipped layers from the ones still holding signal first (Max Detail).
func goLinearFor(ph *Photo, maxW int, strength float64, irWhite *[3]float64, sceneWB bool, wbName string, rebuild bool) (*render.Linear, error) {
	l, _, err := goLinearForClass(ph, maxW, strength, irWhite, sceneWB, wbName, rebuild)
	return l, err
}

// goLinearForClass is goLinearFor with a per-pixel flag marking the
// highlights RecoverHighlights actually rebuilt, at the returned image's
// geometry, so Max Detail can shoulder those and leave open highlights
// alone (task 29).
func goLinearForClass(ph *Photo, maxW int, strength float64, irWhite *[3]float64, sceneWB bool, wbName string, rebuild bool) (*render.Linear, []bool, error) {
	l, rebuilt, _, err := goLinearForGates(ph, maxW, strength, irWhite, sceneWB, wbName, rebuild)
	return l, rebuilt, err
}

// goLinearForGates is goLinearForClass and also marks the pixels no layer of
// which was clipped, at the returned image's geometry. Max Detail uses that
// to compress open over-exposed tones only: a fully clipped area has no
// texture to show and must stay white.
func goLinearForGates(ph *Photo, maxW int, strength float64, irWhite *[3]float64, sceneWB bool, wbName string, rebuild bool) (*render.Linear, []bool, []bool, error) {
	return goLinearForGatesExplicit(ph, maxW, strength, irWhite, sceneWB, wbName, rebuild, false)
}

// goLinearForExplicit is goLinearFor for a caller that knows whether the
// strength was asked for explicitly (a "-dn" export) or is the app default.
func goLinearForExplicit(ph *Photo, maxW int, strength float64, irWhite *[3]float64, sceneWB bool, wbName string, rebuild, explicit bool) (*render.Linear, error) {
	l, _, _, err := goLinearForGatesExplicit(ph, maxW, strength, irWhite, sceneWB, wbName, rebuild, explicit)
	return l, err
}

// goLinearForGatesExplicit is goLinearForGates with the explicit-strength
// flag: an explicit -dnN keeps its number even when it equals defaultNR.
func goLinearForGatesExplicit(ph *Photo, maxW int, strength float64, irWhite *[3]float64, sceneWB bool, wbName string, rebuild, explicit bool) (*render.Linear, []bool, []bool, error) {
	rawDecodeCount.Add(1)
	timing := os.Getenv("FOVEON_TIMING") != ""
	memTiming := os.Getenv("FOVEON_TIMING_MEM") != ""
	mark := time.Now()
	lap := func(name string) {
		if timing {
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", name, time.Since(mark).Round(time.Millisecond))
			mark = time.Now()
		}
		if timing && memTiming {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			fmt.Fprintf(os.Stderr, "  mem after %-16s heap %4d MB\n", name, ms.HeapAlloc>>20)
		}
	}
	img, t, err := goDevelopFor(ph, irWhite != nil, sceneWB, wbName)
	if err != nil {
		return nil, nil, nil, err
	}
	strength = denoiseStrengthFor(ph, strength, explicit)
	lap("develop")
	// The gate is taken at the camera image's full resolution and brought
	// through the same binning/resampling as the image, so its entries stay
	// aligned with the output pixels.
	classW, classH := img.W, img.H
	var rebuilt, clipped []bool
	if rebuild {
		var st x3f.RecoverStats
		var class []x3f.HighlightClass
		st, class = x3f.RecoverHighlightsClass(img, t.Gain, 0.995)
		rebuilt = make([]bool, len(class))
		clipped = make([]bool, len(class))
		for i, c := range class {
			rebuilt[i] = c == x3f.HLRecovered
			clipped[i] = c != x3f.HLNone
		}
		lap("highlight rebuild")
		if os.Getenv("FOVEON_MD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "maxdetail %s: recovered %v lost %d partial %d\n", ph.Name, st.Recovered, st.Lost, st.Partial)
		}
	}
	// Before the denoiser: its chroma filter would spread the clipped
	// highlights' false colour into their neighbours.
	if irWhite != nil {
		x3f.ClipHighlightsTo(img, t.Gain, *irWhite)
	} else {
		x3f.ClipHighlights(img, t.Gain)
	}
	lap("clip highlights")
	// Single-pixel hot/dead samples that the CAMF lists do not cover survive
	// the denoiser (it smooths, but one sample far off its neighbourhood is
	// not noise) and stand out as black/white dots once the grain around them
	// is gone. Repair them in camera-linear RGB, where tools/speck measures
	// them and the thresholds below were fitted, before the denoiser. Only at
	// high ISO, only on the DPM/Quattro families and at the working
	// resolution of both the export and the preview: a preview binned 2-4x
	// dilutes the same hot sample rather than moving it, so the same pass and
	// the same thresholds keep the two paths consistent (before this the
	// export had no dots and the preview did). The flat-neighbour test keeps
	// stars and specular points, whose PSF lights the neighbours. Research
	// switch FOVEON_DN_NOSPECK=1 renders the "before" for the speckle crops.
	fullW := img.W
	// The binning factor is chosen for both paths, so a denoised view and the
	// plain one come out at the same size: with noise reduction the frame is
	// binned first (binning averages noise), and either way the source is
	// capped at that width - a 4704 px frame binned to 1600 gives 1568, and
	// the plain path follows it instead of rendering 1600 and shifting on the
	// swap.
	origW := img.W
	dw, dh, bx, by := reducedView(ph, img.W, img.H, maxW)
	if strength > 0 {
		if bx > 1 || by > 1 {
			img = binCam2(img, bx, by)
		}
		lap(fmt.Sprintf("bin %dx%d -> %dx%d", bx, by, img.W, img.H))
		if !noDespeckle && photoISO(ph) >= 800 && noiseAwareFix(calibGroup(ph)) {
			lin := &render.Linear{W: img.W, H: img.H, Pix: img.Pix}
			if n := render.Despeckle(lin, 0.5, 0.01, 0.05); os.Getenv("FOVEON_DN_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "despeckle %s (%dx%d): %d samples\n", ph.Name, img.W, img.H, n)
			}
			img.Pix = lin.Pix
		}
		lap("despeckle")
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: strength, Scale: 1 / float64(bx), ISO: photoISO(ph), Grey: greyOf(t), NoiseAware: noiseAwareFix(calibGroup(ph)), Quattro: calibGroup(ph) == "Quattro"})
	}
	lap("denoise")
	if dw == 0 && bx > 1 {
		if binnedW := origW / bx; maxW > binnedW {
			maxW = binnedW
		}
	}
	_ = origW
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
	// ISO before the shadow fix: that filter uses the high-ISO ratio formula
	// only from ISO 800 (render/shadowchroma.go), so low ISO stays
	// bit-identical.
	l.ISO, _ = strconv.ParseFloat(strings.TrimSpace(ph.props["ISO"]), 64)
	l.Sensor = render.SharpKey(calibGroup(ph))
	if strength > 0 && !noShadowChroma && (irWhite == nil || sceneWB) {
		noise := render.ShadowChromaFix(l, fullW, t.Colour)
		if os.Getenv("FOVEON_DN_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "shadow chroma noise %s: %.4f\n", ph.Name, noise)
		}
	}
	lap("shadow chroma")
	// Quattro chroma mottle: the per-layer denoiser removes the fine chroma
	// noise, but 8-32 px blotches survive and SPP's band is lower. A wide,
	// edge-aware pass on low-chroma pixels only (render/mottle.go), guided by
	// the luminance, which on Quattro is dominated by the clean top layer.
	// Both paths run it, at their scale; ISO 6400 only (see mottleFix).
	if strength > 0 && mottleFix && calibGroup(ph) == "Quattro" && photoISO(ph) >= 6400 {
		render.MottleChroma(l, render.MottleSigma(fullW, l.W), 0.02, 0.10)
	}
	lap("mottle")
	// The body's brightness relative to its calibration group (not for the
	// IR looks, which model the sensor layers themselves).
	if g := render.ModelExposure(modelOf(ph)); g != 1 && irWhite == nil {
		render.ParallelRows(l.H, func(y0, y1 int) {
			for i := y0 * l.W * 3; i < y1*l.W*3; i++ {
				l.Pix[i] *= g
			}
		})
	}
	lap("model exposure")
	if rebuilt != nil {
		if img.W != classW || img.H != classH {
			rebuilt = maxPoolGate(rebuilt, classW, classH, img.W, img.H)
			clipped = maxPoolGate(clipped, classW, classH, img.W, img.H)
			classW, classH = img.W, img.H
		}
		if l.W != classW || l.H != classH {
			rebuilt = nearestGate(rebuilt, classW, classH, l.W, l.H)
			clipped = nearestGate(clipped, classW, classH, l.W, l.H)
		}
	}
	var open []bool
	if clipped != nil {
		open = make([]bool, len(clipped))
		for i, c := range clipped {
			open[i] = !c
		}
	}
	return l, rebuilt, open, nil
}

// maxDetailAvailable reports whether ph can be developed from raw layers
// for the Max Detail mode. It does not depend on Accurate mode: the mode is
// its own reason to decode the raw data.
func maxDetailAvailable(ph *Photo) bool {
	return ph != nil && ph.Kind == "x3f" && ph.Unsupported == "" && rawAvailable() && rawDecodable(ph.props) && nativeRaw(ph)
}

// reducedView is how a raw frame reaches a maxW view: the binning per axis
// and, for a reduced-height readout, the frame-shaped target size (dw is 0
// otherwise). SD1 MED bins rows in pairs, 4704×1568 for a 3:2 frame.
// Shrinking both axes alike and then stretching the rows (rawLinearFor) kept
// 533 of the 1568 rows on a 1600 view: MED previews lost two thirds of their
// vertical detail.
func reducedView(ph *Photo, w, h, maxW int) (dw, dh, bx, by int) {
	if maxW > 0 && maxW < w && ph.previewW > 0 && ph.previewH > 0 {
		if r := float64(w*ph.previewH) / float64(h*ph.previewW); r >= 1.5 || r <= 0.67 {
			dw, dh = maxW, int(math.Round(float64(maxW*ph.previewH)/float64(ph.previewW)))
			if dh > h {
				dw, dh = 0, 0
			}
		}
	}
	bx, by = 1, 1
	if dw > 0 {
		bx, by = max(1, (w+dw-1)/dw), max(1, (h+dh-1)/dh)
	} else if maxW > 0 {
		// Truncating on purpose: a 4704 px frame asked for a 1600 px preview
		// bins by 2 and denoises at 2352 px. Rounding up to 3 made the preview
		// smaller and faster but binned 3x3 before the denoiser, and the
		// calibration caught it: 81 photos worse by more than 0.3 dE, DP
		// 6.14 -> 6.70, DPM 6.85 -> 7.83 (2026-10-23). A sharper source
		// denoises closer to what SPP does.
		bx = max(1, w/maxW)
		by = bx
	}
	return dw, dh, bx, by
}

// maxDetailRenderParams marks p as the Max Detail mode and leaves its
// sliders alone: they are the user's adjustments on top of the development
// and start at zero because the preset's own values were moved into
// SPPFallback when it was loaded (main.go, maxDetailPreset). Task 19 zeroed
// them here, which made moving a slider do nothing.
func maxDetailRenderParams(p preset.Params) preset.Params {
	p.MaxDetail = true
	return p
}

// rawMaxDetail develops ph for the Max Detail mode: the normal raw development
// with the clipped layers rebuilt first and the shadows opened last
// (render.MaxDetailDevelop). It runs the same stages as rawLinearFor on
// purpose: a separate copy of them lost the frame shape and the rotation, and
// drifted to other colours. It costs more than a normal preview because it
// always decodes raw data, as the mode promises.
func rawMaxDetail(ph *Photo, maxW int, strength float64) (*render.Linear, error) {
	return rawMaxDetailExplicit(ph, maxW, strength, false)
}

// rawMaxDetailExplicit is rawMaxDetail for an export with an explicit NR.
func rawMaxDetailExplicit(ph *Photo, maxW int, strength float64, explicit bool) (*render.Linear, error) {
	l, rebuilt, open, err := goLinearForGatesExplicit(ph, maxW, strength, nil, false, "", true, explicit)
	if err != nil {
		return nil, err
	}
	preW, preH := l.W, l.H
	l = uprightRaw(ph, l)
	gate := uprightGate(ph, rebuilt, preW, preH)
	openGate := uprightGate(ph, open, preW, preH)
	top := whiteInput(ph)
	// The normal development, kept for every pixel neither highlight pass
	// moves. The shoulder is per-pixel and exact, but the bright-chroma
	// denoise inside rawCorrect is spatial: without this, a moved highlight
	// changes the chroma of its unclipped neighbours (task 28: 87,004
	// below-knee pixels, up to 0.25 linear). The copied pixels are the normal
	// development's, bit for bit.
	base := &render.Linear{W: l.W, H: l.H, Pix: append([]float32(nil), l.Pix...)}
	rawCorrect(ph, base)
	// Task 30: open over-exposed tones are pulled down before the correction
	// so it does not run them into flat white. Then the rebuilt highlights
	// keep task 29's shoulder. The two sets are disjoint by construction.
	movedOpen := render.MaxDetailOpenHighlights(l, base, openGate, render.MaxDetailOpenOptions{Top: top})
	mask := render.MaxDetailHighlightsGate(l, top, gate)
	switch {
	case movedOpen == nil && mask == nil:
		l = base
	case movedOpen == nil:
		// Task 29's path, unchanged: the rebuilt pixels keep the curves
		// without the bright-chroma denoise, which is spatial and must see
		// the un-shouldered development.
		rawCorrectApply(ph, l)
		for i, moved := range mask {
			if !moved {
				copy(l.Pix[i*3:i*3+3], base.Pix[i*3:i*3+3])
			}
		}
	default:
		rawCorrectApply(ph, l)
		// The denoise may see the open gain (a smooth exposure change) but
		// not the shoulder: keep the rebuilt pixels' pre-denoise values and
		// put them back after it.
		var sIdx []int
		var sVal []float32
		if mask != nil {
			for i, moved := range mask {
				if moved {
					sIdx = append(sIdx, i)
					sVal = append(sVal, l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2])
				}
			}
		}
		render.BrightChromaDenoiseFor(l, calibGroup(ph))
		for j, i := range sIdx {
			copy(l.Pix[i*3:i*3+3], sVal[j*3:j*3+3])
		}
		for i := 0; i < l.W*l.H; i++ {
			if (mask != nil && mask[i]) || movedOpen[i] {
				continue
			}
			copy(l.Pix[i*3:i*3+3], base.Pix[i*3:i*3+3])
		}
	}
	render.MaxDetailDevelop(l, render.MaxDetailOptions{ISO: photoISO(ph)})
	return l, nil
}

// uprightGate brings the rebuilt-pixel gate through the same frame and
// rotation as uprightRaw. A nil or wrong-sized gate means "no gate" and the
// shoulder keeps its old global behaviour.
func uprightGate(ph *Photo, gate []bool, w, h int) []bool {
	if gate == nil || len(gate) != w*h {
		return nil
	}
	if ph.previewW > 0 && ph.previewH > 0 {
		pw := w
		wantH := w * ph.previewH / ph.previewW
		if d := wantH - h; d > h/30 || -d > h/30 {
			// The crop and stretch of fitAspect, mirrored for the gate.
			r := float64(wantH) / float64(h)
			switch {
			case r > 1.02 && r < 1.5:
				cw := int(math.Round(float64(h) * float64(w) / float64(wantH)))
				gate = cropGate(gate, w, h, (w-cw)/2, 0, cw, h)
				w = cw
			case r < 0.98 && r > 0.67:
				ch := wantH
				gate = cropGate(gate, w, h, 0, (h-ch)/2, w, ch)
				h = ch
			}
			gate = stretchGate(gate, w, h, pw, wantH)
			w, h = pw, wantH
		}
	}
	return rotateGate(gate, w, h, ph.Rotation)
}

func cropGate(g []bool, w, h, x0, y0, cw, ch int) []bool {
	o := make([]bool, cw*ch)
	for y := 0; y < ch; y++ {
		copy(o[y*cw:(y+1)*cw], g[(y+y0)*w+x0:(y+y0)*w+x0+cw])
	}
	return o
}

func stretchGate(g []bool, sw, sh, w, h int) []bool {
	if sw == w && sh == h {
		return g
	}
	o := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o[y*w+x] = g[(y*sh/h)*sw+x*sw/w]
		}
	}
	return o
}

func rotateGate(g []bool, w, h, deg int) []bool {
	if deg != 90 && deg != 180 && deg != 270 {
		return g
	}
	ow, oh := w, h
	if deg != 180 {
		ow, oh = h, w
	}
	o := make([]bool, ow*oh)
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
			o[ny*ow+nx] = g[y*w+x]
		}
	}
	return o
}

// maxPoolGate shrinks the gate: a binned pixel is rebuilt if any of its
// source pixels was.
func maxPoolGate(g []bool, sw, sh, w, h int) []bool {
	o := make([]bool, w*h)
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, (x+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			found := false
			for yy := y0; yy < y1 && !found; yy++ {
				for xx := x0; xx < x1; xx++ {
					if g[yy*sw+xx] {
						found = true
						break
					}
				}
			}
			o[y*w+x] = found
		}
	}
	return o
}

// nearestGate resamples the gate to an output size.
func nearestGate(g []bool, sw, sh, w, h int) []bool {
	if sw == w && sh == h {
		return g
	}
	o := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o[y*w+x] = g[(y*sh/h)*sw+x*sw/w]
		}
	}
	return o
}

// whiteInput is the grey level the per-body correction brings to just under
// white: the most a raw frame can carry through it and still hold detail.
func whiteInput(ph *Photo) float32 {
	const n = 256
	l := &render.Linear{W: n, H: 1, Pix: make([]float32, n*3)}
	for i := 0; i < n; i++ {
		v := float32(i) / (n - 1) * 1.8
		l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = v, v, v
	}
	rawCorrect(ph, l)
	for i := 0; i < n; i++ {
		if l.Pix[i*3+1] >= 0.97 {
			return float32(i) / (n - 1) * 1.8
		}
	}
	return 1.8
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
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: nr, Scale: 1, ISO: photoISO(ph), Grey: greyOf(t), NoiseAware: noiseAwareFix(calibGroup(ph)), Quattro: calibGroup(ph) == "Quattro"})
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
