package render

import (
	"math"

	"github.com/biopoetic/foveon-lab/preset"
)

// The lab's automatic exposure for colour negatives.
//
// A colour negative is never printed or scanned at one fixed exposure: a
// minilab printer measures each frame's integrated density and sets the print
// exposure from it, and a lab scanner does the same before it inverts. That
// is why a dark, underexposed frame still comes back from the lab as a
// normal-looking print. Foveon Lab printed every negative at the exposure
// that makes the scene's 18% grey print as 18% grey, so a frame whose subject
// is mostly shadow (a portrait against a dark background) came out as dark
// as the scene, a stop or more under what a lab would hand back.
//
// The correction here acts after the negative, as a lab's does: the film
// still records the scene at the camera's exposure, with its toe and its
// shoulder, and only the print (or the scan) is exposed per frame. A frame
// too thin for the film's toe stays thin and grainy; the lab can only print
// what the negative holds.
//
// The frame is read as its log-average luminance (the "key", as M-Film's
// darkroom reads it, ml-dev/DEVELOPING.md step 5), and the correction is the
// difference to the key of an ordinary frame, negKeyRef, measured on the
// sample photos so that an ordinary frame keeps the print it had. Slides
// are not corrected: a slide is the camera's exposure, as it was projected.
// Scans are not corrected yet: the server sets NegEV for prints only (see
// negAuto in main.go for why), though negPrintFor handles a scan's exposure.

// negKeyRef is the log-average luminance of an ordinary frame, in the
// linear units the colour-film path renders from: the median of the 183
// sample photos' frames, measured through the app on 23 November 2026
// (X-Neg-Key on /api/render with Portra 400; by body SD1 0.069, SD15 0.101,
// SD10 0.080, SD9 0.043, Quattro 0.057). The median photo keeps its print.
const negKeyRef = 0.070

// negAutoMax limits the correction, as a lab's printer has a range.
const negAutoMax = 2.5

// negAutoStep is the correction's step: the tables are cached per exposure,
// so the correction is held to a twelfth of a stop.
const negAutoStep = 1.0 / 12

// FrameLogKey is l's log-average luminance, read on a grid of about 200
// samples across, so the full frame and a reduced copy of it read alike.
func FrameLogKey(l *Linear) float64 {
	if l == nil || l.W < 1 || l.H < 1 {
		return 0
	}
	step := max(1, l.W/200)
	var s float64
	n := 0
	for y := step / 2; y < l.H; y += step {
		for x := step / 2; x < l.W; x += step {
			i := (y*l.W + x) * 3
			lum := 0.2126*float64(l.Pix[i]) + 0.7152*float64(l.Pix[i+1]) + 0.0722*float64(l.Pix[i+2])
			s += math.Log(math.Max(0, math.Min(1, lum)) + 1e-4)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Exp(s / float64(n))
}

// NegAutoEV is the lab's correction in stops of scene light for a frame of
// log-average luminance key, read on the raw layers the film renders from:
// positive brightens a dark frame. 0 for an unknown key.
func NegAutoEV(key float64) float64 {
	if key <= 0 {
		return 0
	}
	ev := math.Log2(negKeyRef / key)
	ev = math.Max(-negAutoMax, math.Min(negAutoMax, ev))
	return math.Round(ev/negAutoStep) * negAutoStep
}

// negPrintFor is the enlarger (or scanner) setting a colour negative is
// developed with: the user's colour head and print exposure, plus the lab's
// automatic exposure. A print's exposure moves the other way to the scene's
// and by the negative's slope (a stop more scene light makes the negative
// scanNegGamma stops denser), so a frame brightened by e stops is printed
// scanNegGamma*e stops shorter. A scan is linear in the scene's light after
// its inversion, so its exposure moves by e itself. Other films keep their
// setting as it was.
func negPrintFor(p preset.Params, c Compensation) ColorPrint {
	cp := ColorPrint{p.CCY, p.CCM, p.CCC, p.PrintExp}
	if c.NegEV == 0 || !IsColorNegative(p.Film) {
		if isScanPaper(p.Paper) {
			return ColorPrint{}
		}
		return cp
	}
	if isScanPaper(p.Paper) {
		// A scan has no colour head; its one setting is the exposure.
		return ColorPrint{Exp: -c.NegEV}
	}
	cp.Exp -= scanNegGamma * c.NegEV
	return cp
}
