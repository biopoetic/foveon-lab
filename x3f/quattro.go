package x3f

import (
	"errors"
	"math"
)

// Quattro development: the bottom and middle layers at quarter resolution,
// the top layer at full resolution. A Go port of x3fuse-core's Quattro path
// (Copyright 2026 Sang Lee, Apache-2.0; from the Kalpanika x3f tools,
// BSD-style; licences and notices in third_party/x3fuse-core/): black level
// from the dark shield rows and columns, the F23 bad pixel lists and the
// sd Quattro autofocus pixel grids, and the "Y is 4T" expansion (the full
// resolution top layer carries the detail; the low resolution colour
// differences bottom-top and middle-top are upsampled bicubically).
// Spatial gain is not applied, as x3fuse does not by default for Quattro,
// nor the WB colour shading (x3fuse applies it to Merrill bodies only).

const (
	cameraIDSDQ  = 40 // sd Quattro
	cameraIDSDQH = 41 // sd Quattro H
)

// keepRect is x3fuse's x3f_transform_rect_to_keep_image: a CAMF rectangle
// (inclusive, sensor coordinates) clipped to KeepImageArea and made
// relative to it, then scaled to a w×h image (rescale) or required to fit
// in it.
func (c *CAMF) keepRect(r []float64, w, h int, rescale bool) (x0, y0, x1, y1 int, ok bool) {
	k := c.Matrices["KeepImageArea"]
	if len(r) != 4 || len(k) != 4 {
		return 0, 0, 0, 0, false
	}
	if r[0] > k[2] || r[1] > k[3] || r[2] < k[0] || r[3] < k[1] {
		return 0, 0, 0, 0, false
	}
	a := [4]int{int(math.Max(r[0], k[0]) - k[0]), int(math.Max(r[1], k[1]) - k[1]), int(math.Min(r[2], k[2]) - k[0]), int(math.Min(r[3], k[3]) - k[1])}
	kw, kh := int(k[2]-k[0])+1, int(k[3]-k[1])+1
	if rescale {
		a = [4]int{a[0] * w / kw, a[1] * h / kh, a[2] * w / kw, a[3] * h / kh}
	} else if kw > w || kh > h {
		return 0, 0, 0, 0, false
	}
	if a[0] > a[2] || a[1] > a[3] || a[2] >= w || a[3] >= h {
		return 0, 0, 0, 0, false
	}
	return a[0], a[1], a[2], a[3], true
}

// darkRects are the black-level areas of a w×h image: DarkShieldTop and
// DarkShieldBottom (not on the sd Quattro H) and the left and right
// DarkShieldColRange columns over the whole height.
func (c *CAMF) darkRects(w, h int, rescale bool) [][4]int {
	var rs [][]float64
	rs = append(rs, c.Matrices["DarkShieldTop"])
	if id := c.Matrices["CAMERAID"]; len(id) != 1 || id[0] != cameraIDSDQH {
		rs = append(rs, c.Matrices["DarkShieldBottom"])
	}
	if col := c.Matrices["DarkShieldColRange"]; len(col) == 4 {
		const all = 1 << 30
		rs = append(rs, []float64{col[0], 0, col[1], all}, []float64{col[2], 0, col[3], all})
	}
	var out [][4]int
	for _, r := range rs {
		if x0, y0, x1, y1, ok := c.keepRect(r, w, h, rescale); ok {
			out = append(out, [4]int{x0, y0, x1, y1})
		}
	}
	return out
}

// meanOver is the mean of channels 0..n-1 of an interleaved nch-channel
// plane over rects.
func meanOver(pix []uint16, w, nch, n int, rects [][4]int) ([3]float64, bool) {
	var sum [3]float64
	cnt := 0
	for _, r := range rects {
		for y := r[1]; y <= r[3]; y++ {
			for x := r[0]; x <= r[2]; x++ {
				p := pix[(y*w+x)*nch:]
				for k := 0; k < n; k++ {
					sum[k] += float64(p[k])
				}
				cnt++
			}
		}
	}
	if cnt == 0 {
		return sum, false
	}
	for k := range sum {
		sum[k] /= float64(cnt)
	}
	return sum, true
}

// afGrid is an sd Quattro autofocus pixel pattern: from (ci, ri) to
// (cf, rf), every cp columns and rp rows, blocks of cs×rs pixels.
type afGrid struct{ ci, cf, cp, cs, ri, rf, rp, rs int }

func quattroAFGrid(id int, luma bool) *afGrid {
	switch {
	case id == cameraIDSDQ && luma:
		return &afGrid{217, 5641, 16, 1, 464, 3312, 32, 2}
	case id == cameraIDSDQ:
		return &afGrid{108, 2820, 8, 1, 232, 1656, 16, 1}
	case id == cameraIDSDQH && luma:
		return &afGrid{233, 6425, 16, 1, 592, 3888, 32, 2}
	case id == cameraIDSDQH:
		return &afGrid{116, 2820, 8, 1, 296, 1944, 16, 1}
	}
	return nil
}

// fixQuattroBadPixels repairs an interleaved nch-channel w×h plane from the
// F23 list (rows of: row, then col and a flag per bad pixel, then 0) and
// the body's autofocus grid. luma = the full-resolution top layer.
func fixQuattroBadPixels(pix []uint16, w, h, nch int, c *CAMF, luma bool) int {
	bad := make([]bool, w*h)
	var list []int32
	mark := func(col, row int) {
		if col < 0 || row < 0 || col >= w || row >= h || bad[row*w+col] {
			return
		}
		bad[row*w+col] = true
		list = append(list, int32(row*w+col))
	}
	name := "BadPixelsChromaF23"
	if luma {
		name = "BadPixelsLumaF23"
	}
	row := -1
	m := c.Matrices[name]
	for i := 0; i < len(m); i++ {
		v := int(m[i])
		switch {
		case row == -1:
			row = v
		case v == 0:
			row = -1
		default:
			mark(v, row)
			i++ // the flag after each column
		}
	}
	id := -1
	if v := c.Matrices["CAMERAID"]; len(v) == 1 {
		id = int(v[0])
	}
	if g := quattroAFGrid(id, luma); g != nil {
		for r := g.ri; r <= g.rf; r += g.rp {
			for col := g.ci; col <= g.cf; col += g.cp {
				for dr := 0; dr < g.rs; dr++ {
					for dc := 0; dc < g.cs; dc++ {
						mark(col+dc, r+dr)
					}
				}
			}
		}
	}
	if !luma {
		markHighlightPixels(c, w, h, mark)
	}
	return fixMarked(pix, w, h, nch, bad, list)
}

// cameraLinearQuattro is CameraLinear for a Quattro RawImage: full
// resolution (2× the bottom/middle layers), cropped to ActiveImageArea.
func cameraLinearQuattro(raw *RawImage, c *CAMF, wb string, fixBad bool) (*CamImage, error) {
	w, h := raw.W, raw.H
	ow, oh := 2*w, 2*h
	if raw.TopW < ow || raw.TopH < oh {
		return nil, errors.New("x3f: Quattro top layer smaller than 2× the others")
	}
	top := raw.Top
	if fixBad {
		top = append([]uint16(nil), raw.Top...)
		fixQuattroBadPixels(top, raw.TopW, raw.TopH, 1, c, true)
	}
	// Bottom, middle and the top binned 2×2 (on the raw scale), as one
	// quarter-resolution image.
	low := make([]uint16, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			low[i], low[i+1] = raw.Pix[i], raw.Pix[i+1]
			t0 := (2*y)*raw.TopW + 2*x
			s := uint32(top[t0]) + uint32(top[t0+1]) + uint32(top[t0+raw.TopW]) + uint32(top[t0+raw.TopW+1])
			low[i+2] = uint16((s + 2) / 4)
		}
	}
	lb, ok1 := meanOver(low, w, 3, 2, c.darkRects(w, h, true))
	tb, ok2 := meanOver(top, raw.TopW, 1, 1, c.darkRects(raw.TopW, raw.TopH, false))
	if !ok1 || !ok2 {
		return nil, errors.New("x3f: no Quattro dark shield for the black level")
	}
	black := [3]float64{lb[0], lb[1], tb[0]}
	black[1] += quattroMiddleBlack(c)
	if fixBad {
		fixQuattroBadPixels(low, w, h, 3, c, false)
	}
	white := 65535.0
	if d := c.Matrices["ImageDepth"]; len(d) == 1 && d[0] > 0 && d[0] <= 16 {
		white = math.Exp2(d[0]) - 1
	}
	var scale [3]float64
	for k := range scale {
		scale[k] = 1 / math.Max(1, white-black[k])
	}
	// Colour differences at quarter resolution, bottom-top and middle-top,
	// taken white balanced (as x3fuse's intermediate levels are): the top
	// layer's detail then adds to every layer in neutral proportion.
	sb, sm := 1.0, 1.0
	if g, _, err := c.WBTransform(wb); err == nil && g[0] > 0 && g[1] > 0 {
		sb, sm = g[2]/g[0], g[2]/g[1]
	}
	db := make([]float32, w*h)
	dm := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			b := (float64(low[i*3]) - black[0]) * scale[0]
			m := (float64(low[i*3+1]) - black[1]) * scale[1]
			t := (float64(low[i*3+2]) - black[2]) * scale[2]
			db[i], dm[i] = float32(b-sb*t), float32(m-sm*t)
		}
	}
	ub := upBicubic2x(db, w, h)
	um := upBicubic2x(dm, w, h)
	x0, y0, x1, y1, ok := c.keepRect(c.Matrices["ActiveImageArea"], ow, oh, false)
	if !ok {
		x0, y0, x1, y1 = 0, 0, ow-1, oh-1
	}
	cw, ch := x1-x0+1, y1-y0+1
	out := &CamImage{W: cw, H: ch, Pix: make([]float32, cw*ch*3)}
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			sx, sy := x+x0, y+y0
			t := float32((float64(top[sy*raw.TopW+sx]) - black[2]) * scale[2])
			j := sy*ow + sx
			d := out.Pix[(y*cw+x)*3:]
			d[0], d[1], d[2] = float32(sb)*t+ub[j], float32(sm)*t+um[j], t
		}
	}
	return out, nil
}

// upBicubic2x doubles a plane both ways with the a = -0.75 cubic kernel and
// mirrored edges (OpenCV's INTER_CUBIC with BORDER_REFLECT_101), as x3fuse.
func upBicubic2x(src []float32, w, h int) []float32 {
	weights := func(n int) ([][4]int, [][4]float32) {
		taps := make([][4]int, 2*n)
		ws := make([][4]float32, 2*n)
		for d := range taps {
			s := (float64(d)+0.5)*0.5 - 0.5
			i := int(math.Floor(s))
			t := s - float64(i)
			for k := 0; k < 4; k++ {
				taps[d][k] = reflect101(i-1+k, n)
				ws[d][k] = float32(cubicKernel(t + 1 - float64(k)))
			}
		}
		return taps, ws
	}
	ct, cwt := weights(w)
	rt, rwt := weights(h)
	ow := 2 * w
	tmp := make([]float32, h*ow)
	for y := 0; y < h; y++ {
		row := src[y*w:]
		for x := 0; x < ow; x++ {
			t, k := ct[x], cwt[x]
			tmp[y*ow+x] = row[t[0]]*k[0] + row[t[1]]*k[1] + row[t[2]]*k[2] + row[t[3]]*k[3]
		}
	}
	out := make([]float32, 2*h*ow)
	for y := 0; y < 2*h; y++ {
		t, k := rt[y], rwt[y]
		r0, r1, r2, r3 := tmp[t[0]*ow:], tmp[t[1]*ow:], tmp[t[2]*ow:], tmp[t[3]*ow:]
		o := out[y*ow:]
		for x := 0; x < ow; x++ {
			o[x] = r0[x]*k[0] + r1[x]*k[1] + r2[x]*k[2] + r3[x]*k[3]
		}
	}
	return out
}

// cubicKernel is the cubic convolution kernel with a = -0.75.
func cubicKernel(d float64) float64 {
	const a = -0.75
	d = math.Abs(d)
	switch {
	case d <= 1:
		return ((a+2)*d-(a+3))*d*d + 1
	case d < 2:
		return ((a*d-5*a)*d+8*a)*d - 4*a
	}
	return 0
}

// reflect101 mirrors i into 0..n-1 without repeating the edge pixel.
func reflect101(i, n int) int {
	if n == 1 {
		return 0
	}
	p := 2 * (n - 1)
	r := ((i % p) + p) % p
	if r >= n {
		r = p - r
	}
	return r
}

// quattroWB maps CAMF's WhiteBalance code (Quattro files leave WB_DESC
// empty) to the illuminant name CAMF's tables use.
func (c *CAMF) quattroWB() string {
	v := c.Matrices["WhiteBalance"]
	if len(v) != 1 {
		return ""
	}
	return map[int]string{1: "Auto", 2: "Sunlight", 3: "Shade", 4: "Overcast", 5: "Incandescent",
		6: "Fluorescent", 7: "Flash", 8: "Custom", 11: "ColorTemp", 12: "AutoLSP"}[int(v[0])]
}

// CameraWB is the illuminant name CAMF uses for the photo's in-camera white
// balance: from its WB_DESC property, else (Quattro) CAMF's WhiteBalance.
func (c *CAMF) CameraWB(wbDesc string) string {
	if wb := map[string]string{"Daylight": "Sunlight", "Sunlight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast",
		"Incandescent": "Incandescent", "Fluorescent": "Fluorescent", "Flash": "Flash",
		"Auto": "Auto", "Custom": "Custom"}[wbDesc]; wb != "" {
		return wb
	}
	return c.quattroWB()
}

// quattroMiddleBlack is what the middle layer's black sits above its dark
// shields, in raw units: zero at the sensor's base gain, then growing with
// its analog gain (SensorISO; CaptureISO above it is digital). Missed, the
// middle layer read a little high at high ISO, and once the denoiser took
// away the noise that had masked it the shadows turned green-yellow
// (ISO 6400 vs the frame's own ISO 100, a*/b*: dp2 Quattro -15.0/+10.6,
// sd Quattro H -10.3/+5.7; SPP -3.1/+1.1 and -2.4/+2.0). Swept per ISO
// against SPP's exports of both studio series (October 2026), the best
// offset was SensorISO/100 on the dp2 Quattro (2 at 200, 4 at 400) and half
// that on the sd Quattro H; ISO 6400 error vs SPP 24.7 -> 9.9 and
// 15.4 -> 11.6, with the drift as small as SPP's. The other Quattro bodies
// share the dp2's sensor and take its value (not measured: no ISO series).
func quattroMiddleBlack(c *CAMF) float64 {
	s := c.Matrices["SensorISO"]
	if len(s) != 1 || s[0] < 200 {
		return 0
	}
	k := 1.0
	if id := c.Matrices["CAMERAID"]; len(id) == 1 && id[0] == cameraIDSDQH {
		k = 0.5
	}
	return k * s[0] / 100
}
