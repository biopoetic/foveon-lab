package dng

// Colour and geometry: the camera's white balance and matrices take the raw
// values to linear sRGB, then the default crop and scale are applied and the
// image is reduced to the requested width. All of it is linear light: the
// film stage is the only place a tone curve is applied.

import (
	"math"
	"runtime"
	"sync"
)

// Image is a developed DNG in linear sRGB (D50 white, as the DNG matrices
// define it), not yet turned for the file's Orientation.
type Image struct {
	W, H         int
	Pix          []float32 // interleaved RGB, row-major
	FullW, FullH int       // before the optional reduction to MaxWidth
	Meta         Meta
	// ApproxColour: no usable colour matrix was present, so the data was
	// taken as linear sRGB.
	ApproxColour bool
}

// Develop decodes and develops the image, reduced to at most maxW pixels wide
// (maxW <= 0 keeps full size). The result is unrotated; the caller applies
// Meta.Rotation (or the Photo's rotation) as it does for every other source.
func (f *File) Develop(maxW int) (*Image, error) {
	im, err := f.image()
	if err != nil {
		return nil, err
	}
	raw, err := f.readRaw(im)
	if err != nil {
		return nil, err
	}
	var rgb []float32
	if im.samples == 1 && im.mono {
		// One plane, no colour filter: a monochrome camera. Show it as grey.
		rgb = make([]float32, im.width*im.height*3)
		for i, v := range raw {
			rgb[i*3], rgb[i*3+1], rgb[i*3+2] = v, v, v
		}
		raw = nil
	} else if im.samples == 1 {
		rgb = Demosaic(raw, im.width, im.height, im.cfa, im.cfaPW, im.cfaPH)
		raw = nil
	} else {
		rgb = raw
	}
	m, neutral, be, approx, err := f.colour(im)
	if err != nil {
		return nil, err
	}
	// Camera / AsShotNeutral, then camera -> XYZ D50, then XYZ D50 ->
	// linear sRGB. Folding the three matrices into one saves a pass.
	var e [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			e[i*3+j] = m[i*3+j] / neutral[j] * math.Exp2(be)
		}
	}
	srgb := mul33(xyzD50ToSRGB, e)
	parallelRows(im.height, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < im.width; x++ {
				i := (y*im.width + x) * 3
				r, g, b := float64(rgb[i]), float64(rgb[i+1]), float64(rgb[i+2])
				o := [3]float64{
					srgb[0]*r + srgb[1]*g + srgb[2]*b,
					srgb[3]*r + srgb[4]*g + srgb[5]*b,
					srgb[6]*r + srgb[7]*g + srgb[8]*b,
				}
				for c := 0; c < 3; c++ {
					// Keep highlight headroom if it survives the matrix,
					// but never a negative light: the render's tone stage
					// and Fill Light assume non-negative linear values.
					if o[c] < 0 {
						o[c] = 0
					}
					rgb[i+c] = float32(o[c])
				}
			}
		}
	})
	// Output-stage opcodes (FixVignetteRadial, WarpRectilinear) are applied
	// to the developed linear image, in list order, before the geometry.
	rgb = im.applyStage3(rgb, im.width, im.height)
	fullW, fullH := im.width, im.height
	if im.cropX != 0 || im.cropY != 0 || im.cropW != im.width || im.cropH != im.height {
		rgb = crop(rgb, im.width, im.height, im.cropX, im.cropY, im.cropW, im.cropH)
		fullW, fullH = im.cropW, im.cropH
	}
	if im.scaleX != 1 || im.scaleY != 1 {
		nw := max(1, int(math.Round(float64(fullW)*im.scaleX)))
		nh := max(1, int(math.Round(float64(fullH)*im.scaleY)))
		rgb = resizeBilinear(rgb, fullW, fullH, nw, nh)
		fullW, fullH = nw, nh
	}
	out := &Image{W: fullW, H: fullH, Pix: rgb, FullW: fullW, FullH: fullH, Meta: f.Meta, ApproxColour: approx}
	if maxW > 0 && fullW > maxW {
		dw := maxW
		dh := max(1, int(math.Round(float64(fullH)*float64(dw)/float64(fullW))))
		out.Pix = areaResample(rgb, fullW, fullH, dw, dh)
		out.W, out.H = dw, dh
	}
	return out, nil
}

func crop(pix []float32, w, h, x0, y0, cw, ch int) []float32 {
	out := make([]float32, cw*ch*3)
	for y := 0; y < ch; y++ {
		copy(out[y*cw*3:(y+1)*cw*3], pix[((y+y0)*w+x0)*3:((y+y0)*w+x0+cw)*3])
	}
	return out
}

// areaResample is an exact box reduction at integer edges: every output
// pixel is the area-weighted mean of the source it covers.
func areaResample(pix []float32, sw, sh, dw, dh int) []float32 {
	out := make([]float32, dw*dh*3)
	rows := runtime.GOMAXPROCS(0)
	if rows > dh {
		rows = dh
	}
	var wg sync.WaitGroup
	step := (dh + rows - 1) / rows
	for y0 := 0; y0 < dh; y0 += step {
		y1 := min(y0+step, dh)
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for dy := y0; dy < y1; dy++ {
				sy0 := dy * sh / dh
				sy1 := (dy + 1) * sh / dh
				if sy1 <= sy0 {
					sy1 = sy0 + 1
				}
				for dx := 0; dx < dw; dx++ {
					sx0 := dx * sw / dw
					sx1 := (dx + 1) * sw / dw
					if sx1 <= sx0 {
						sx1 = sx0 + 1
					}
					var sum [3]float64
					var n float64
					for y := sy0; y < sy1; y++ {
						for x := sx0; x < sx1; x++ {
							i := (y*sw + x) * 3
							sum[0] += float64(pix[i])
							sum[1] += float64(pix[i+1])
							sum[2] += float64(pix[i+2])
							n++
						}
					}
					o := (dy*dw + dx) * 3
					if n > 0 {
						out[o], out[o+1], out[o+2] = float32(sum[0]/n), float32(sum[1]/n), float32(sum[2]/n)
					}
				}
			}
		}(y0, y1)
	}
	wg.Wait()
	return out
}

// resizeBilinear is for DefaultScale, which is normally an integer
// enlargement of a stretched readout.
func resizeBilinear(pix []float32, sw, sh, dw, dh int) []float32 {
	out := make([]float32, dw*dh*3)
	for dy := 0; dy < dh; dy++ {
		fy := (float64(dy)+0.5)*float64(sh)/float64(dh) - 0.5
		y0 := int(math.Floor(fy))
		ty := float32(fy - float64(y0))
		y0 = max(0, min(sh-1, y0))
		y1 := min(sh-1, y0+1)
		for dx := 0; dx < dw; dx++ {
			fx := (float64(dx)+0.5)*float64(sw)/float64(dw) - 0.5
			x0 := int(math.Floor(fx))
			tx := float32(fx - float64(x0))
			x0 = max(0, min(sw-1, x0))
			x1 := min(sw-1, x0+1)
			o := (dy*dw + dx) * 3
			for c := 0; c < 3; c++ {
				a := pix[(y0*sw+x0)*3+c]*(1-tx) + pix[(y0*sw+x1)*3+c]*tx
				b := pix[(y1*sw+x0)*3+c]*(1-tx) + pix[(y1*sw+x1)*3+c]*tx
				out[o+c] = a*(1-ty) + b*ty
			}
		}
	}
	return out
}

// colour returns the camera -> XYZ D50 matrix, the as-shot neutral, and the
// baseline exposure. approx is true when no colour matrix was usable.
//
// The order is the DNG specification's: Chapter 5, "Mapping Raw Values to
// Linear Reference Values" (black/white, LinearizationTable, then
// AnalogBalance) and Chapter 6, "Mapping Camera Color Space to CIE XYZ Space"
// (CameraCalibration, ColorMatrix or ForwardMatrix, AsShotNeutral, then
// BaselineExposure). The matrices are folded into one 3x3 for the pixel pass;
// the per-channel neutral is divided out first, as AsShotNeutral requires.
func (f *File) colour(im *rawImage) ([9]float64, [3]float64, float64, bool, error) {
	neutral := [3]float64{1, 1, 1}
	hasNeutral := false
	if t, ok := f.findTag(tagAsShotNeutral); ok {
		if v := f.floatValues(t); len(v) >= 3 {
			ok := true
			for i := 0; i < 3; i++ {
				neutral[i] = v[i]
				if !(neutral[i] > 0) {
					ok = false
				}
			}
			if ok {
				hasNeutral = true
			} else {
				neutral = [3]float64{1, 1, 1}
			}
		}
	}
	be := 0.0
	if t, ok := f.findTag(tagBaselineExposure); ok {
		if v := f.floatValues(t); len(v) > 0 {
			be = v[0]
		}
	}
	// AnalogBalance: a per-channel multiplier before the colour matrix.
	if t, ok := f.findTag(tagAnalogBalance); ok {
		if v := f.floatValues(t); len(v) >= 3 {
			for i := 0; i < 3; i++ {
				if v[i] > 0 {
					neutral[i] /= v[i]
				}
			}
		}
	}
	whiteX, whiteY, hasWhiteXY := 0.0, 0.0, false
	whiteTemp := 0.0
	if t, ok := f.findTag(tagAsShotWhiteXY); ok {
		if v := f.floatValues(t); len(v) >= 2 && v[1] != 0 {
			whiteX, whiteY, hasWhiteXY = v[0], v[1], true
			whiteTemp = cctFromXY(v[0], v[1])
		}
	}
	if !hasWhiteXY {
		whiteTemp = f.estimateWhiteTemp(neutral)
	}

	t1, t2 := f.illumTemp(tagCalibIllum1), f.illumTemp(tagCalibIllum2)
	w := 0.0
	if hasWhiteXY && t1 > 0 && t2 > 0 && t1 != t2 {
		w = math.Max(0, math.Min(1, (whiteTemp-t1)/(t2-t1)))
	}
	havePair := t1 > 0 && t2 > 0 && t1 != t2

	fm1, ok1 := f.matrix(tagForwardMatrix1)
	fm2, ok2 := f.matrix(tagForwardMatrix2)
	if ok1 {
		m := fm1
		if ok2 && havePair && hasWhiteXY {
			m = lerpMatrix(fm1, fm2, w)
		}
		m = f.withCalibration(m, w)
		if !hasNeutral {
			if n, ok := f.neutralFromWhite(m, whiteX, whiteY, hasWhiteXY); ok {
				neutral = n
			}
		}
		return m, neutral, be, false, nil
	}

	cm1, ok1 := f.matrix(tagColorMatrix1)
	cm2, ok2 := f.matrix(tagColorMatrix2)
	if ok1 || ok2 {
		var m1, m2 [9]float64
		if ok1 {
			m1 = f.cameraToXYZ(cm1, t1)
		}
		if ok2 {
			m2 = f.cameraToXYZ(cm2, t2)
		}
		m := m1
		switch {
		case ok1 && ok2 && havePair && hasWhiteXY:
			// Both illuminants and the as-shot white point: interpolate
			// between the two camera-to-XYZ matrices by the white point's
			// position between the calibration illuminants (Chapter 6).
			m = lerpMatrix(m1, m2, w)
		case ok1 && ok2 && hasNeutral:
			// No AsShotWhiteXY: pick the matrix whose own calibration
			// white best explains the as-shot neutral.
			if d1, d2 := f.matrixFit(cm1, t1, neutral), f.matrixFit(cm2, t2, neutral); d2 < d1 {
				m, w = m2, 1
			}
		}
		m = f.withCalibration(m, w)
		if !hasNeutral {
			if n, ok := f.neutralFromWhite(m, whiteX, whiteY, hasWhiteXY); ok {
				neutral = n
			}
		}
		return m, neutral, be, false, nil
	}

	// No matrix at all: the file is taken as linear sRGB. This is the only
	// invented colour in the reader, and the caller says so.
	return identity33, neutral, be, true, nil
}

// neutralFromWhite derives the camera's as-shot neutral from AsShotWhiteXY
// when the file has no AsShotNeutral. The white is adapted to D50 and taken
// through the inverse of the chosen camera -> XYZ D50 matrix, then scaled
// so its largest channel is 1, which is the DNG's convention for
// AsShotNeutral.
func (f *File) neutralFromWhite(m [9]float64, whiteX, whiteY float64, hasWhiteXY bool) ([3]float64, bool) {
	if !hasWhiteXY || whiteY == 0 {
		return [3]float64{}, false
	}
	w := xyzFromXY(whiteX, whiteY)
	w = mulVec(adaptToD50(w), w)
	inv, ok := invert3x3(m)
	if !ok {
		return [3]float64{}, false
	}
	n := mulVec(inv, w)
	top := 0.0
	for _, x := range n {
		if x <= 0 {
			return [3]float64{}, false
		}
		if x > top {
			top = x
		}
	}
	if top <= 0 {
		return [3]float64{}, false
	}
	for i := range n {
		n[i] /= top
	}
	return n, true
}

// estimateWhiteTemp finds the calibration illuminant whose ColorMatrix best
// explains the as-shot neutral, for a file that gives no AsShotWhiteXY. It
// lets the two ForwardMatrices (D65 and A, say) be chosen sensibly instead
// of always taking the first.
func (f *File) estimateWhiteTemp(neutral [3]float64) float64 {
	cm1, ok1 := f.matrix(tagColorMatrix1)
	cm2, ok2 := f.matrix(tagColorMatrix2)
	if !ok1 && !ok2 {
		return 0
	}
	t1, t2 := f.illumTemp(tagCalibIllum1), f.illumTemp(tagCalibIllum2)
	switch {
	case ok1 && ok2 && t1 > 0 && t2 > 0:
		if f.matrixFit(cm2, t2, neutral) < f.matrixFit(cm1, t1, neutral) {
			return t2
		}
		return t1
	case ok1:
		return t1
	}
	return t2
}

// withCalibration folds the per-camera CameraCalibration matrix into the
// camera -> XYZ matrix, as Chapter 6 places it before ColorMatrix. The two
// calibration matrices belong to the two calibration illuminants; when both
// exist they are interpolated with the same weight as the colour matrices.
func (f *File) withCalibration(m [9]float64, w float64) [9]float64 {
	c1, ok1 := f.matrix(tagCameraCalib1)
	c2, ok2 := f.matrix(tagCameraCalib2)
	var cm [9]float64
	switch {
	case ok1 && ok2:
		cm = lerpMatrix(c1, c2, w)
	case ok1:
		cm = c1
	case ok2:
		cm = c2
	default:
		return m
	}
	if cm == identity33 {
		return m
	}
	return mul33(m, cm)
}

func (f *File) matrix(tag uint16) ([9]float64, bool) {
	t, ok := f.findTag(tag)
	if !ok {
		return identity33, false
	}
	v := f.floatValues(t)
	if len(v) < 9 {
		return identity33, false
	}
	var m [9]float64
	copy(m[:], v[:9])
	if det3(m) == 0 || math.IsNaN(det3(m)) {
		return identity33, false
	}
	return m, true
}

// matrixFit scores how well a camera-to-XYZ matrix explains the as-shot
// neutral: under the right matrix, the neutral's XYZ chromaticity is the
// matrix's own calibration illuminant.
func (f *File) matrixFit(cm [9]float64, temp float64, neutral [3]float64) float64 {
	w, ok := illumWhite(temp)
	if !ok {
		return math.Inf(1)
	}
	inv, ok := invert3x3(cm)
	if !ok {
		return math.Inf(1)
	}
	xyz := mulVec(inv, neutral)
	s := xyz[0] + xyz[1] + xyz[2]
	ws := w[0] + w[1] + w[2]
	if xyz[1] <= 0 || s <= 0 || ws <= 0 {
		return math.Inf(1)
	}
	// Chromaticity distance to the calibration illuminant.
	sx, sy := xyz[0]/s, xyz[1]/s
	wx, wy := w[0]/ws, w[1]/ws
	return math.Hypot(sx-wx, sy-wy)
}

// cameraToXYZ converts a ColorMatrix (XYZ -> camera) to camera -> XYZ D50,
// adapting the calibration illuminant to D50 with Bradford.
func (f *File) cameraToXYZ(cm [9]float64, temp float64) [9]float64 {
	inv, ok := invert3x3(cm)
	if !ok {
		return identity33
	}
	w, ok := illumWhite(temp)
	if !ok {
		return inv
	}
	a := adaptToD50(w)
	return mul33(a, inv)
}

func (f *File) illumTemp(tag uint16) float64 {
	t, ok := f.findTag(tag)
	if !ok {
		return 0
	}
	v := f.uintValues(t)
	if len(v) == 0 {
		return 0
	}
	switch v[0] {
	case 17:
		return 2856 // Standard Light A
	case 18:
		return 4874 // B
	case 19:
		return 6774 // C
	case 20:
		return 5503 // D55
	case 21:
		return 6504 // D65
	case 22:
		return 7504 // D75
	case 23:
		return 5000 // D50
	}
	return 0
}

// Illuminant whites (XYZ, Y = 1).
func illumWhite(temp float64) ([3]float64, bool) {
	switch {
	case math.Abs(temp-2856) < 1:
		return [3]float64{1.09850, 1, 0.35585}, true
	case math.Abs(temp-4874) < 1:
		return [3]float64{0.9642, 1, 0.8249}, true
	case math.Abs(temp-5000) < 1:
		return [3]float64{0.9642, 1, 0.8249}, true
	case math.Abs(temp-5503) < 1:
		return [3]float64{0.95682, 1, 0.92149}, true
	case math.Abs(temp-6504) < 1:
		return [3]float64{0.95047, 1, 1.08883}, true
	case math.Abs(temp-6774) < 1:
		return [3]float64{0.98074, 1, 1.18232}, true
	case math.Abs(temp-7504) < 1:
		return [3]float64{0.94972, 1, 1.22638}, true
	}
	return [3]float64{}, false
}

func xyzFromXY(x, y float64) [3]float64 {
	if y <= 0 {
		return [3]float64{0, 1, 0}
	}
	return [3]float64{x / y, 1, (1 - x - y) / y}
}

// cctFromXY is McCamy's approximation, enough to choose between two
// calibration illuminants.
func cctFromXY(x, y float64) float64 {
	n := (x - 0.3320) / (0.1858 - y)
	return 449*n*n*n + 3525*n*n + 6823.3*n + 5520.33
}

var d50 = [3]float64{0.96422, 1, 0.82521}

var bradford = [9]float64{
	0.8951, 0.2664, -0.1614,
	-0.7502, 1.7135, 0.0367,
	0.0389, -0.0685, 1.0296,
}

// adaptToD50 is a Bradford chromatic adaptation from white to D50.
func adaptToD50(white [3]float64) [9]float64 {
	binv, ok := invert3x3(bradford)
	if !ok {
		return identity33
	}
	src := mulVec(bradford, white)
	dst := mulVec(bradford, d50)
	var d [9]float64
	for i := 0; i < 3; i++ {
		if src[i] == 0 {
			return identity33
		}
		d[i*3+i] = dst[i] / src[i]
	}
	return mul33(binv, mul33(d, bradford))
}

var (
	srgbD50ToXYZ = [9]float64{
		0.4360747, 0.3850649, 0.1430804,
		0.2225045, 0.7168786, 0.0606169,
		0.0139322, 0.0971045, 0.7141733,
	}
	xyzD50ToSRGB = func() [9]float64 {
		m, _ := invert3x3(srgbD50ToXYZ)
		return m
	}()
	identity33 = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
)

func det3(m [9]float64) float64 {
	return m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
}

func mulVec(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

func mul33(a, b [9]float64) (c [9]float64) {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				c[i*3+j] += a[i*3+k] * b[k*3+j]
			}
		}
	}
	return c
}

func invert3x3(m [9]float64) ([9]float64, bool) {
	d := det3(m)
	if d == 0 || math.IsNaN(d) {
		return identity33, false
	}
	var r [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			i1, i2, j1, j2 := (j+1)%3, (j+2)%3, (i+1)%3, (i+2)%3
			r[i*3+j] = (m[i1*3+j1]*m[i2*3+j2] - m[i1*3+j2]*m[i2*3+j1]) / d
		}
	}
	return r, true
}

func lerpMatrix(a, b [9]float64, w float64) [9]float64 {
	var o [9]float64
	for i := range o {
		o[i] = a[i] + (b[i]-a[i])*w
	}
	return o
}

func parallelRows(h int, fn func(y0, y1 int)) {
	n := runtime.GOMAXPROCS(0)
	if n > h {
		n = h
	}
	if n <= 1 {
		fn(0, h)
		return
	}
	var wg sync.WaitGroup
	step := (h + n - 1) / n
	for y := 0; y < h; y += step {
		y1 := min(y+step, h)
		wg.Add(1)
		go func(a, b int) { defer wg.Done(); fn(a, b) }(y, y1)
	}
	wg.Wait()
}

// ApplyOrientation returns a new image turned upright for a TIFF
// Orientation (1-8). The app uses this for standalone tools; the app itself
// keeps the image unrotated and applies the Photo's rotation as for an X3F.
func ApplyOrientation(img *Image, orientation int) *Image {
	w, h := img.W, img.H
	out := &Image{W: w, H: h, Pix: make([]float32, len(img.Pix)), FullW: img.FullW, FullH: img.FullH, Meta: img.Meta, ApproxColour: img.ApproxColour}
	swap := orientation >= 5
	if swap {
		out.W, out.H = h, w
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch orientation {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			default:
				nx, ny = x, y
			}
			copy(out.Pix[(ny*out.W+nx)*3:][:3], img.Pix[(y*w+x)*3:][:3])
		}
	}
	return out
}

// Ensure imports stay honest for the small helpers used above.
