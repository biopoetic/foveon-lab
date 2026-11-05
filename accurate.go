package main

// Accurate mode: for camera families where it was measured to help (SD1
// Merrill so far, see render/rawbase.json), previews start from the raw data
// mapped onto SPP's neutral rendering instead of the camera's JPEG, which
// carries the in-camera colour mode. A raw decode takes ~10–25 s, so the
// JPEG preview shows at once and the raw one replaces it when ready
// (decoded in the background, one at a time, cached on disk).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

// rawDecodeCount counts raw developments started (native or x3f_extract), so
// "no raw decode behind a cull run" is a number, not a claim.
var rawDecodeCount atomic.Int64

const (
	rawNone     = ""
	rawDecoding = "decoding" // nothing usable yet
	rawFast     = "fast"     // the quick decode is ready; the full one may follow
	rawReady    = "ready"    // the full (denoised) decode is ready
	rawFailed   = "failed"
)

// rawFastColor is the quick preview decode: x3f_extract without its denoise,
// 7x faster (SD1 32 s -> 4.5 s). Held out vs SPP it scores dE2000 6.10
// against 5.83 for the full decode (Oct 2026), so it is shown first and
// replaced by the full decode; exports always use the full one.
const rawFastColor = "sRGB-nodenoise"

// rawFullQueued marks photos whose full decode is queued (guarded by rawMu).
var rawFullQueued = map[string]bool{}

// rawCorrectionFor is the raw→SPP mapping for ph, or nil.
func rawCorrectionFor(ph *Photo) *render.RawCorrection {
	if ph.Kind != "x3f" || ph.RawDecoded || ph.Unsupported != "" || !rawDecodable(ph.props) {
		return nil
	}
	if c := render.RawBaseFor(calibGroup(ph), wbDesc(ph)); c != nil {
		return c
	}
	// At high ISO the camera JPEG is noisy and colour-shifted (Merrill high
	// ISO is pushed in processing), while the raw path is denoised by the
	// x3f package: use the raw path with the family's fitted correction even
	// where it did not beat the JPEG at base ISO.
	if goRaw && highISO(ph) {
		return render.RawBaseFitted(calibGroup(ph), wbDesc(ph))
	}
	return nil
}

// highISO: ISO 800 and above.
func highISO(ph *Photo) bool {
	iso, err := strconv.Atoi(strings.TrimSpace(ph.props["ISO"]))
	return err == nil && iso >= 800
}

// rawDecodable reports whether the raw base handles the file's size mode.
// An SD1 MED file is read out with rows binned in pairs (raw 4704×1568 for
// a 3264×2176 frame); rawLinear stretches it back. Measured against SPP
// (tools/rescmp, Oct 2026): same framing, no crop, and its colour offset
// from SPP lies in the HI photos' range. LOW has no sample yet.
func rawDecodable(props map[string]string) bool {
	r := props["RESOLUTION"]
	return r == "" || r == "HI" || r == "MED"
}

func (s *server) wantRaw(ph *Photo) bool {
	if !rawAvailable() {
		return false
	}
	// SD9/SD10 have no full-size preview: their raw data is the picture, not
	// an optional "accurate" extra, so the accurate switch does not hide it.
	if ph.RawDecoded {
		return !ph.rawFailed.Load()
	}
	return s.accurate.Load() && ph.Accurate
}

// goRaw: decode and develop raw data with the x3f package (no x3f_extract);
// FOVEON_RAW=x3f switches back to x3f_extract.
var goRaw = os.Getenv("FOVEON_RAW") != "x3f"

// rawAvailable reports whether raw-based rendering is possible at all.
func rawAvailable() bool { return goRaw || rawTool != "" }

// nativeRaw: the x3f package decodes this file's raw data (every body,
// Quattro included).
func nativeRaw(ph *Photo) bool {
	f, err := x3f.Open(ph.path)
	if err != nil {
		return false
	}
	_, ok := f.RawSection()
	return ok
}

// defaultNR is the noise reduction for previews and for exports left at
// the default: "Low". Side by side with SPP (SD1 ISO 100-6400, 2026-10-14)
// Sigma Photo Pro's own noise reduction looks better than Full; Low keeps
// the photo's grain and detail and only calms the colour noise.
const defaultNR = 0.3

// baseNR is the default denoise strength at base ISO (<= 400) on the DPM and
// Quattro bodies. SPP keeps more pixel-scale texture there than our 0.3
// does; task 37 measured the <0.7 px band at 0.78-0.93 of SPP's energy and
// the held-out base-ISO scenes improved with less smoothing. High ISO keeps
// the 0.3: there the missing energy is SPP's noise, which we deliberately
// remove, and lower smoothing made those frames worse. The two bodies above
// ISO 400, and every other sensor generation, are unchanged.
const baseNR = 0.15

// denoiseStrengthFor is the strength the app's default development uses:
// baseNR at base ISO on the DPM and Quattro bodies, the caller's strength
// everywhere else (including an explicit -dn/-nodenoise, which is passed as
// explicit). An unknown ISO is left alone, so a file without the field keeps
// the old 0.3. explicit matters because -dn3 is numerically the old default:
// a user who asked for 0.3 must still get 0.3.
func denoiseStrengthFor(ph *Photo, strength float64, explicit bool) float64 {
	if explicit || strength != defaultNR {
		return strength
	}
	if iso := photoISO(ph); iso > 0 && iso <= 400 {
		if g := calibGroup(ph); g == "DPM" || g == "Quattro" {
			return baseNR
		}
	}
	return strength
}

// explicitStrength reports whether a decode name asks for a particular noise
// reduction ("-dnN" or "-nodenoise") rather than leaving the default.
func explicitStrength(color string) bool {
	_, opts, _ := strings.Cut(color, "-")
	return strings.Contains(opts, "nodenoise") || strings.HasPrefix(opts, "dn")
}

// goStrength maps a decode name to the native denoiser's strength: the
// default decode defaultNR, "-nodenoise" 0, "-dnN" N/10 (NR Full: -dn10).
func goStrength(color string) float64 {
	_, opts, _ := strings.Cut(color, "-")
	switch {
	case strings.Contains(opts, "nodenoise"):
		return 0
	case strings.HasPrefix(opts, "dn"):
		if n, err := strconv.Atoi(strings.TrimPrefix(opts, "dn")); err == nil {
			return float64(max(0, min(10, n))) / 10
		}
	}
	return defaultNR
}

func cachedAs(ph *Photo, color string) bool {
	p, err := rawCachePathFor(ph, color)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func (s *server) rawStateOf(ph *Photo) string {
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	st := s.rawState[ph.ID]
	if !goRaw && (st == rawNone || st == rawFast) {
		switch {
		case cachedAs(ph, "sRGB"):
			st = rawReady
		case cachedAs(ph, rawFastColor):
			st = rawFast
		}
		s.rawState[ph.ID] = st
	}
	return st
}

// useRaw: render ph from its corrected raw decode right now.
func (s *server) useRaw(ph *Photo) bool {
	if !s.wantRaw(ph) {
		return false
	}
	st := s.rawStateOf(ph)
	return st == rawReady || st == rawFast
}

// rawMode is the source mode of a photo shown from raw data: "raw" (full
// decode) or "rawfast" (the quick one, until the full one is ready).
func (s *server) rawMode(ph *Photo) string {
	if s.rawStateOf(ph) == rawReady {
		return "raw"
	}
	return "rawfast"
}

// requestRaw queues ph's raw decode (quick, then full) if accurate mode
// wants it; the photo on screen goes before prefetched neighbours. A click
// also drops prefetches the user has moved past, so they never delay the
// photo now on screen.
func (s *server) requestRaw(ph *Photo) {
	s.drainRawLow()
	s.queueRaw(ph, true)
}

// queueRaw: urgent = the photo on screen (quick then full decode);
// otherwise a neighbour prefetch (quick decode only).
func (s *server) queueRaw(ph *Photo, urgent bool) {
	if !s.wantRaw(ph) || s.cullActive.Load() {
		return
	}
	st := s.rawStateOf(ph)
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	if s.rawBusy[ph.ID] {
		return
	}
	switch {
	case st == rawReady || st == rawFailed:
		return
	case st == rawFast && (!urgent || rawFullQueued[ph.ID]):
		return
	case st == rawDecoding && (!urgent || rawFullQueued[ph.ID]):
		return
	}
	if st == rawNone {
		s.rawState[ph.ID] = rawDecoding
	}
	if urgent {
		rawFullQueued[ph.ID] = true
		go func() { s.rawQueue <- ph }()
	} else {
		go func() { s.rawQueueLow <- ph }()
	}
}

// rawWorker decodes queued photos one at a time (x3f_extract is heavy):
// the quick decode first, then, for the photo on screen, the full one.
func (s *server) rawWorker() {
	for {
		var ph *Photo
		urgent := true
		select {
		case ph = <-s.rawQueue:
		default:
			select {
			case ph = <-s.rawQueue:
			case ph = <-s.rawQueueLow:
				urgent = false
			}
		}
		if ph == nil {
			continue
		}
		// A job that was queued before the user entered the cull is dropped
		// here; it never starts a decode. (One already inside a decode is
		// allowed to finish into the source cache: see cullActive.)
		if s.cullActive.Load() {
			s.dropQueuedRaw(ph)
			continue
		}
		func() {
			s.rawMu.Lock()
			s.rawBusy[ph.ID] = true
			s.rawMu.Unlock()
			defer func() {
				s.rawMu.Lock()
				delete(s.rawBusy, ph.ID)
				s.rawMu.Unlock()
			}()
			if goRaw {
				// Native: one pass (seconds), straight into the source cache.
				if _, err := s.sourceAs(ph, "raw"); err != nil {
					log.Printf("accurate preview for %s: %v", ph.Name, err)
					s.rawMu.Lock()
					s.rawState[ph.ID] = rawFailed
					s.rawMu.Unlock()
					return
				}
				s.rawMu.Lock()
				s.rawState[ph.ID] = rawReady
				delete(rawFullQueued, ph.ID)
				s.rawMu.Unlock()
				return
			}
			if !cachedAs(ph, "sRGB") && !cachedAs(ph, rawFastColor) {
				if _, err := decodeRawAs(ph, rawFastColor); err != nil {
					log.Printf("accurate preview for %s: %v", ph.Name, err)
					s.rawMu.Lock()
					s.rawState[ph.ID] = rawFailed
					s.rawMu.Unlock()
					return
				}
				s.rawMu.Lock()
				s.rawState[ph.ID] = rawFast
				s.rawMu.Unlock()
			}
			s.rawMu.Lock()
			full := urgent || rawFullQueued[ph.ID]
			s.rawMu.Unlock()
			if !full {
				return
			}
			if s.cullActive.Load() {
				// The quick decode is cached and enough for a rating; do not
				// start the full pass while the user is culling.
				s.rawMu.Lock()
				delete(rawFullQueued, ph.ID)
				s.rawMu.Unlock()
				return
			}
			_, err := decodeRaw(ph)
			s.rawMu.Lock()
			delete(rawFullQueued, ph.ID)
			if err != nil {
				log.Printf("accurate preview (full) for %s: %v", ph.Name, err)
			} else {
				s.rawState[ph.ID] = rawReady
			}
			s.rawMu.Unlock()
		}()
	}
}

// rawLinear decodes ph's raw data (blocking), fixes the aspect of
// reduced-resolution files, rotates it and maps it onto SPP's neutral look.
func rawLinear(ph *Photo, maxW int, color string) (*render.Linear, error) {
	return rawLinearFor(ph, maxW, color, "")
}

// splitFilterWB splits a raw-source filter spec into its filter and the
// camera white balance it names: "wb:Shade" (the normal rawwb path) and
// "film@wb:Shade" (a colour film developed under that balance). A spec with
// no balance returns it unchanged.
func splitFilterWB(spec string) (filter, wb string) {
	if base, w, ok := strings.Cut(spec, "@wb:"); ok {
		return base, w
	}
	if w, ok := strings.CutPrefix(spec, "wb:"); ok {
		return "", w
	}
	return spec, ""
}

// rawLinearFor: for an infrared look (irFilter "ir-..."), the
// Sunlight-balanced sensor layers (native decode only), with no raw base
// on top and clipped highlights going to that look's white.
func rawLinearFor(ph *Photo, maxW int, color, irFilter string) (*render.Linear, error) {
	var l *render.Linear
	// "wb:<name>": the normal raw rendering, developed with that camera
	// white balance (a preset's WhiteBalance code, cameraWBName).
	// "film@wb:<name>": a colour film's layers, developed with that balance
	// (layersSource): the light is balanced before the emulsion.
	wbName := ""
	irFilter, wbName = splitFilterWB(irFilter)
	irWhite := render.IRWhite(render.SensorGeneration(calibGroup(ph)), irFilter)
	if irFilter == "film" {
		// Colour film: the layers, clipped highlights to white.
		irWhite = &[3]float64{1, 1, 1}
	}
	layers := irWhite != nil
	if goRaw {
		var err error
		if l, err = goLinearForExplicit(ph, maxW, goStrength(color), irWhite, irFilter == "film", wbName, false, explicitStrength(color)); err != nil {
			return nil, err
		}
	} else {
		img, err := decodeRawAs(ph, color)
		if err != nil {
			return nil, err
		}
		l = render.FromImage(img, maxW, 0)
	}
	l = uprightRaw(ph, l)
	if !layers {
		// wbName: the user chose this camera white balance. The DP2 balance
		// corrects the as-shot Auto development; applying it here would pull
		// the chosen balance back towards the camera JPEG.
		rawCorrectWith(ph, l, wbName == "")
	}
	// Single-pixel impulse dots that survived the denoiser are removed on
	// the finished frame in render.ImpulseClean (render/render.go), where
	// the per-photo raw correction has already lifted them out of the
	// near-black neighbourhood and the tone curve has made them visible.
	return l, nil
}

// rawCorrect applies the per-body correction to a colour raw development, as
// the camera shot it.
func rawCorrect(ph *Photo, l *render.Linear) {
	rawCorrectWith(ph, l, true)
}

// rawCorrectWith: balance=false leaves out the DP2 camera-JPEG balance. A
// development made with an explicit camera white balance (the preset's
// WhiteBalance code) is the user's balance, and the camera-JPEG correction
// must not pull it back to the as-shot colour.
func rawCorrectWith(ph *Photo, l *render.Linear, balance bool) {
	c := rawCorrectionFor(ph)
	if c == nil {
		return
	}
	// SPP's Monochrome mixes layers after their own tone. The camera layers
	// are what the raw development holds before the colour raw base; take
	// them through the raw base's neutral response now, while this image is
	// still linear camera data (task 54: mixing the raw base's per-channel
	// output instead rectifies per-channel noise and is what went wrong at
	// high ISO).
	timing := os.Getenv("FOVEON_TIMING") != ""
	mark := time.Now()
	lap := func(name string) {
		if timing {
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", name, time.Since(mark).Round(time.Millisecond))
			mark = time.Now()
		}
	}
	if l.MonoLayers == nil {
		if cc := layerCC(ph); cc != nil {
			l.MonoLayers = monoNeutralLayers(l, c, cc)
		}
	}
	lap("mono layers")
	c.Apply(l)
	lap("correct matrix")
	if balance {
		rawJPEGBalance(ph, l)
	}
	lap("jpeg balance")
	// SPP's chroma noise reduction also removes the bright near-neutral
	// low-frequency mottling the per-pixel denoiser leaves; do it here so
	// every raw-based render (default, Max Detail, exports) has it.
	render.BrightChromaDenoiseFor(l, calibGroup(ph))
	lap("bright chroma")
	// SPP renders a Merrill file marked AdobeRGB differently even in
	// sRGB (render/adobergb.go).
	if ph.props["EXIF_COLORSPACE"] == "AdobeRGB" && render.SensorGeneration(calibGroup(ph)) == "Merrill" {
		render.ApplySPPAdobeRGB(l)
	}
	lap("adobe rgb")
}

// balanceRefW is the width of the one reference the DP2 balance is measured
// at. A single fixed width makes its gains a property of the photo, not of
// the view: fit, 100%, a tile, Max Detail and an export all apply the same
// three numbers.
const balanceRefW = 1200

// balanceMeasures counts DP2 reference measurements (one per photo), so a
// regression test can prove the per-call measurement is gone.
var balanceMeasures atomic.Int64

// monoNeutralLayers fills Linear.MonoLayers: SPP's monochrome layer tone.
//
// SPP's Monochrome mixes the three sensor layers after each has had its own
// tone. Our colour raw base is a 3×3 matrix plus a curve per sRGB channel,
// so applying it and then unmixing the layers (the old path) makes the
// curve act on the channels instead of the layers. At base ISO that was
// invisible; at ISO 3200 the curve's toe rectifies the raw noise per
// channel, and the layer means then depend on the mix — the task-54 error.
//
// The layer tone is the raw base's measured response to a neutral layer:
// feed (L,L,L) through the correction, unmix with the camera matrix, and
// average with SPP's Reset weights. That curve is the same shape as the
// base's own neutral tone (so ISO 100 is still the measured 1.084 curve),
// but it is evaluated per layer on clean, denoised layer values.
func monoNeutralLayers(l *render.Linear, c *render.RawCorrection, cc *[9]float64) []float32 {
	inv, ok := render.Invert3(*cc)
	if !ok {
		return nil
	}
	// SPP's Reset mixer in sRGB: (1,1,1)/3 · cc⁻¹, normalised so a neutral
	// stays the same grey.
	var v [3]float64
	for j := 0; j < 3; j++ {
		v[j] = (inv[j] + inv[3+j] + inv[6+j]) / 3
	}
	if s := v[0] + v[1] + v[2]; s > 0 {
		v[0], v[1], v[2] = v[0]/s, v[1]/s, v[2]/s
	} else {
		return nil
	}
	const n = 4097
	var lut [n]float32
	for i := 0; i < n; i++ {
		x := float32(i) / (n - 1)
		r, g, b := c.Pixel(x, x, x)
		lut[i] = float32(v[0]*float64(r) + v[1]*float64(g) + v[2]*float64(b))
	}
	at := func(x float32) float32 {
		if x <= 0 {
			return 0
		}
		if x >= 1 {
			return lut[n-1]
		}
		f := x * (n - 1)
		i := int(f)
		t := f - float32(i)
		return lut[i]*(1-t) + lut[i+1]*t
	}
	q := make([]float32, len(l.Pix))
	render.ParallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			j := i * 3
			r, g, b := l.Pix[j], l.Pix[j+1], l.Pix[j+2]
			q[j] = at(float32(inv[0]*float64(r) + inv[1]*float64(g) + inv[2]*float64(b)))
			q[j+1] = at(float32(inv[3]*float64(r) + inv[4]*float64(g) + inv[5]*float64(b)))
			q[j+2] = at(float32(inv[6]*float64(r) + inv[7]*float64(g) + inv[8]*float64(b)))
		}
	})
	return q
}

// rawJPEGBalance applies the DP2 camera-JPEG balance to a developed raw
// image. The gains are measured once per photo from a fixed reference
// (measureJPEGBalance) and cached on the Photo; l's own size or content is
// never measured, so every path gets one answer and a refused frame is
// refused everywhere.
func rawJPEGBalance(ph *Photo, l *render.Linear) {
	if l == nil {
		return
	}
	g, ok := jpegBalanceFor(ph)
	if !ok {
		return
	}
	for i := 0; i < len(l.Pix); i += 3 {
		l.Pix[i] *= g[0]
		l.Pix[i+1] *= g[1]
		l.Pix[i+2] *= g[2]
	}
}

// jpegBalanceFor is the photo's cached DP2 balance (ok=false for every other
// body and for a refused frame). The DP2 (original) stores an Auto white
// balance that matches neither the camera JPEG nor SPP: its render is far
// too red, while the DP2X (same calibration group) is neutral. The JPEG is
// the camera's own per-shot balance, so matching channel means restores the
// colour without a fitted table.
func jpegBalanceFor(ph *Photo) ([3]float32, bool) {
	if ph.Kind != "x3f" || modelOf(ph) != "SIGMA DP2" {
		return [3]float32{}, false
	}
	ph.balanceOnce.Do(func() {
		balanceMeasures.Add(1)
		ph.balanceG, ph.balanceOK = measureJPEGBalance(ph)
	})
	return ph.balanceG, ph.balanceOK
}

// measureJPEGBalance computes the DP2 balance from one fixed reference: the
// camera JPEG at balanceRefW, and the raw development the app's preview and
// spp-check start from (sourceW, the default noise reduction) reduced to
// balanceRefW and mapped through the raw base. Nothing here depends on the
// image a caller happens to hold.
func measureJPEGBalance(ph *Photo) ([3]float32, bool) {
	c := rawCorrectionFor(ph)
	if c == nil {
		return [3]float32{}, false
	}
	img, err := stillPreview(ph)
	if err != nil {
		return [3]float32{}, false
	}
	jp := render.FromImage(img, balanceRefW, 0)
	l, err := goLinearFor(ph, sourceW, defaultNR, nil, false, "", false)
	if err != nil {
		return [3]float32{}, false
	}
	l = l.Downscale(balanceRefW)
	c.Apply(l)
	mo, meanY, clipO := balanceMeans(l)
	mj, _, clipJ := balanceMeans(jp)
	return jpegBalanceGains(mo, mj, meanY, clipO, clipJ)
}

// balanceMeans is the unclipped channel mean, the mean luminance of those
// pixels, and the share above 0.95 (too dark or too clipped is not a colour
// measurement).
func balanceMeans(l *render.Linear) (m [3]float64, meanY, clip float64) {
	var n, c int
	var sumY float64
	for i := 0; i+2 < len(l.Pix); i += 3 {
		if max(l.Pix[i], l.Pix[i+1], l.Pix[i+2]) > 0.95 {
			c++
			continue
		}
		for k := 0; k < 3; k++ {
			m[k] += float64(l.Pix[i+k])
		}
		sumY += float64(0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2])
		n++
	}
	if n == 0 {
		return m, 0, 1
	}
	for k := range m {
		m[k] /= float64(n)
	}
	return m, sumY / float64(n), float64(c) / float64(n+c)
}

// jpegBalanceGains is the DP2 balance's numeric half: the luminance-preserving
// per-channel gains that put l's mean colour on the camera JPEG's. Frames too
// dark or too clipped for a reliable mean are refused, as are implausible
// gains.
func jpegBalanceGains(mo, mj [3]float64, meanY, clipO, clipJ float64) ([3]float32, bool) {
	var g [3]float32
	if meanY < 0.01 || clipO > 0.05 || clipJ > 0.05 {
		return g, false
	}
	var lum float64
	for k := 0; k < 3; k++ {
		if mo[k] <= 0 || mj[k] <= 0 {
			return g, false
		}
		g[k] = float32(mj[k] / mo[k])
		lum += float64(g[k]) * [3]float64{0.2126, 0.7152, 0.0722}[k]
	}
	if lum <= 0 {
		return g, false
	}
	for k := 0; k < 3; k++ {
		g[k] /= float32(lum)
		if g[k] < 0.5 || g[k] > 1.6 {
			return g, false
		}
	}
	return g, true
}

// rawCorrectApply is the per-body matrix and curves without the bright-chroma
// denoise. Max Detail needs them apart: its shoulder must run before the
// curves (so the ramp does not clip the recovered values), but the denoise is
// spatial and must see the un-shouldered development, or it pulls the chroma
// of unclipped pixels towards a nearby recovered highlight. Max Detail keeps
// the normal rawCorrect result for every pixel the shoulder did not move.
func rawCorrectApply(ph *Photo, l *render.Linear) {
	c := rawCorrectionFor(ph)
	if c == nil {
		return
	}
	c.Apply(l)
	rawJPEGBalance(ph, l)
	if ph.props["EXIF_COLORSPACE"] == "AdobeRGB" && render.SensorGeneration(calibGroup(ph)) == "Merrill" {
		render.ApplySPPAdobeRGB(l)
	}
}

// uprightRaw gives a raw development its frame: the embedded preview's shape
// and ph's rotation. Every raw path needs it; Max Detail once skipped it and
// came out stretched on SD1 medium files and sideways on portrait frames.
func uprightRaw(ph *Photo, l *render.Linear) *render.Linear {
	if ph.previewW > 0 && ph.previewH > 0 {
		// e.g. an SD1 medium-size file decodes at 4704×1568 for a 3:2 frame
		// (stretched); a Quattro shot at 16:9 decodes 3:2 (cropped).
		wantH := l.W * ph.previewH / ph.previewW
		if d := wantH - l.H; d > l.H/30 || -d > l.H/30 {
			l = fitAspect(l, l.W, wantH)
		}
	}
	if ph.Rotation != 0 {
		l = l.Rotate(ph.Rotation)
	}
	return l
}

// irLayers: the params ask for an infrared look and ph's raw data can give
// it the sensor layers it models.
func irLayers(ph *Photo, p preset.Params) bool {
	return strings.HasPrefix(p.Filter, "ir-") && goRaw && ph.Kind == "x3f" && ph.Unsupported == "" && rawDecodable(ph.props)
}

// layersSource says whether p renders from the sensor's own layers rather
// than the SPP look, and which: an IR look (its filter name), or a colour
// film ("film": its physics starts from the scene, white-balanced as the
// photo is — camera Auto, as SPP — not from SPP's rendering).
//
// A colour film with a preset camera white balance is "film@wb:<name>": the
// balance belongs to the light before the emulsion, so the raw layers are
// developed with that balance and the film sees balanced light. Without it
// the film branch would keep only the as-shot Auto balance: the measured
// model (which holds SPP's WhiteBalance mapping) is off under a colour film.
func layersSource(ph *Photo, p preset.Params) (string, bool) {
	if irLayers(ph, p) {
		return p.Filter, true
	}
	if render.IsColorFilm(p.Film) && goRaw && ph.Kind == "x3f" && ph.Unsupported == "" && rawDecodable(ph.props) {
		src := "film"
		if n := cameraWBName(p.WhiteBalance); n != "" {
			src += "@wb:" + n
		}
		return src, true
	}
	return "", false
}

// wbSource is the raw development for p's camera white balance
// ("wb:Shade" etc.), when the native decoder can develop ph with it.
func wbSource(ph *Photo, p preset.Params) (string, bool) {
	n := cameraWBName(p.WhiteBalance)
	if n == "" || !goRaw || ph.Kind != "x3f" || ph.Unsupported != "" || !rawDecodable(ph.props) {
		return "", false
	}
	return "wb:" + n, true
}

// modeDevelopedWB reports whether the source mode already developed the raw
// with the preset's white balance (the measured WB mapping must then not run
// again): a rawwb source, or a colour film's @wb suffix.
func modeDevelopedWB(mode string) bool {
	return strings.HasPrefix(mode, "rawwb:") || strings.Contains(mode, "@wb:")
}

// layerCompensation is the compensation for a render from the layers: the
// layers are the output space (no colour matrix, nothing to undo).
func layerCompensation(c render.Compensation) render.Compensation {
	id := identity33
	return render.Compensation{SatFactor: 1, Family: c.Family, CC: &id, Sensor: c.Sensor}
}

// handleRawPrefetch queues the raw decode of the given photos (the ones
// next to the selected one), so stepping to them is instant.
func (s *server) handleRawPrefetch(w http.ResponseWriter, r *http.Request) {
	// The page sends the neighbours after every selection. Drop the previous
	// selection's prefetches first: the user has moved on, and decoding a
	// photo nobody is looking at keeps the one they are looking at waiting.
	s.drainRawLow()
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if ph := s.lookup(id); ph != nil {
			s.queueRaw(ph, false)
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// drainRawLow drops every queued neighbour prefetch, without starting it.
// One already inside a decode has no cancellation point and is allowed to
// finish into the source cache (as in the cull).
func (s *server) drainRawLow() {
	for {
		select {
		case ph := <-s.rawQueueLow:
			s.dropQueuedRaw(ph)
		default:
			return
		}
	}
}

// handleRawState: GET ?id= → {"state": "", "decoding", "ready" or "failed",
// "accurate": bool} so the page can swap in the accurate preview.
func (s *server) handleRawState(w http.ResponseWriter, r *http.Request) {
	ph := s.lookup(r.URL.Query().Get("id"))
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	st := s.rawStateOf(ph)
	// The page polls this after the picture is on screen; that is the safe
	// moment to start a decode without competing with the first render.
	if s.wantRaw(ph) && st == rawNone {
		s.requestRaw(ph)
	}
	mode := "jpg"
	if s.useRaw(ph) {
		mode = s.rawMode(ph)
	}
	writeJSON(w, map[string]any{"state": st, "accurate": s.useRaw(ph), "mode": mode, "wanted": s.wantRaw(ph)})
}

// handleAccurate switches accurate mode (POST {"on": bool}); remembered.
func (s *server) handleAccurate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, 400, err)
			return
		}
		s.accurate.Store(req.On)
		cfg, _ := loadConfig()
		cfg.Accurate = &req.On
		if err := saveConfig(cfg); err != nil {
			httpErr(w, 500, err)
			return
		}
	}
	writeJSON(w, map[string]any{"on": s.accurate.Load(), "available": rawAvailable()})
}

// calibGroup is the group ph's learned raw base and JPEG balance belong to
// (x3f.CalibGroup of the camera model; ph.Camera already holds the family).
// wbDesc is the photo's in-camera white balance name ("Auto", "Overcast",
// ...), which selects a per-balance raw base when one was fitted.
func wbDesc(ph *Photo) string { return strings.TrimSpace(ph.props["WB_DESC"]) }

func calibGroup(ph *Photo) string {
	m := ph.props["CAMMODEL"]
	if m == "" {
		m = ph.props["CAMNAME"]
	}
	if m == "" {
		return ph.Camera
	}
	return x3f.CalibGroup(m)
}

// ---------- cull mode: embedded previews only ----------
//
// The cull is a decision loop over a folder. It must never compete with a
// multi-second raw development: no decode may start while it is open, and
// queued decodes are dropped. A decode already inside the raw decoder has no
// cancellation point, so it finishes into the source cache (bounded, six
// entries) and the worker does not start another; the cull is served from the
// embedded preview throughout.

// dropQueuedRaw forgets a queued job without decoding it.
func (s *server) dropQueuedRaw(ph *Photo) {
	s.rawMu.Lock()
	if s.rawState[ph.ID] == rawDecoding {
		s.rawState[ph.ID] = rawNone
	}
	delete(rawFullQueued, ph.ID)
	s.rawMu.Unlock()
}

// drainRawQueues empties both queues when the cull opens. A worker already
// blocked on a send may add one more after this; the worker's own cull check
// drops it.
func (s *server) drainRawQueues() {
	for {
		select {
		case ph := <-s.rawQueue:
			s.dropQueuedRaw(ph)
		default:
			goto low
		}
	}
low:
	for {
		select {
		case ph := <-s.rawQueueLow:
			s.dropQueuedRaw(ph)
		default:
			return
		}
	}
}

// handleCullMode is POST {on}: the cull view opened or closed. GET reports
// the mode and how many raw decodes the server has started, which is how the
// "100 cull moves, zero raw decodes" check is counted.
func (s *server) handleCullMode(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			httpErr(w, 400, err)
			return
		}
		s.cullActive.Store(req.On)
		if req.On {
			s.drainRawQueues()
		}
	}
	writeJSON(w, map[string]any{"on": s.cullActive.Load(), "decodes": rawDecodeCount.Load()})
}
