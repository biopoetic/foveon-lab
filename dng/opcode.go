package dng

// DNG opcode lists (DNG 1.6, "Opcode List Processing"). The lists are stored
// big-endian whatever the TIFF byte order is. Each entry is
//
//	uint32 id, uint32 version, uint32 flags, uint32 byteCount, byteCount bytes
//
// Flags bit 0 says the opcode is optional: a reader that does not know it may
// skip it. An unknown opcode with that bit clear is mandatory, and because
// ignoring it would silently produce a wrong picture this reader applies what
// it can and reports a plain sentence to the caller (Meta.Notice). This file
// implements GainMap (9), TrimBounds (6), FixVignetteRadial (3) and
// WarpRectilinear (1). MapTable, MapPolynomial, the delta/scale opcodes,
// FixBadPixels* and WarpFisheye are reported, not guessed at.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	opGainMap           = 9
	opTrimBounds        = 6
	opFixVignetteRadial = 3
	opWarpRectilinear   = 1
)

type opcode struct {
	id    uint32
	flags uint32
	data  []byte
}

func (o opcode) optional() bool { return o.flags&1 != 0 }

func knownOpcode(id uint32) bool {
	switch id {
	case opGainMap, opTrimBounds, opFixVignetteRadial, opWarpRectilinear:
		return true
	}
	return false
}

// parseOpcodeList splits one OpcodeList and returns its entries plus the
// mandatory ids it does not know. count is never used to preallocate, so a
// corrupt 4-byte count cannot make the reader reserve gigabytes.
func parseOpcodeList(b []byte) ([]opcode, []uint32, error) {
	if len(b) < 4 {
		return nil, nil, errors.New("dng: opcode list shorter than its count")
	}
	count := binary.BigEndian.Uint32(b[:4])
	if count > 1<<20 {
		return nil, nil, fmt.Errorf("dng: opcode list claims %d entries", count)
	}
	var out []opcode
	var required []uint32
	pos := 4
	for i := uint32(0); i < count; i++ {
		if pos+16 > len(b) {
			return nil, nil, errors.New("dng: truncated opcode header")
		}
		id := binary.BigEndian.Uint32(b[pos:])
		flags := binary.BigEndian.Uint32(b[pos+8:])
		size := binary.BigEndian.Uint32(b[pos+12:])
		pos += 16
		if uint64(size) > uint64(len(b)-pos) {
			return nil, nil, fmt.Errorf("dng: opcode %d claims %d bytes, %d left", id, size, len(b)-pos)
		}
		o := opcode{id: id, flags: flags, data: b[pos : pos+int(size)]}
		pos += int(size)
		out = append(out, o)
		if !knownOpcode(id) && !o.optional() {
			required = append(required, id)
		}
	}
	return out, required, nil
}

func noticeFor(ids []uint32) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	seen := map[uint32]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return "DNG opcode(s) " + strings.Join(parts, ", ") + " must be applied but this reader does not implement them; the picture may be wrong"
}

// opcodeTag returns an opcode list from the raw IFD, or from any IFD when the
// writer put it on IFD0 (Adobe DNG Converter does this when the raw is in a
// SubIFD).
func (f *File) opcodeTag(tg uint16) (tag, bool) {
	if f.raw != nil {
		if t, ok := f.raw.tags[tg]; ok && !t.empty() {
			return t, true
		}
	}
	for _, d := range f.ifds {
		if t, ok := d.tags[tg]; ok && !t.empty() {
			return t, true
		}
	}
	return tag{}, false
}

func (f *File) opcodeLists() (list1, list2, list3 []opcode, required []uint32, err error) {
	for i, tg := range []uint16{tagOpcodeList1, tagOpcodeList2, tagOpcodeList3} {
		t, ok := f.opcodeTag(tg)
		if !ok {
			continue
		}
		ops, req, perr := parseOpcodeList(t.data)
		if perr != nil {
			return nil, nil, nil, nil, perr
		}
		required = append(required, req...)
		switch i {
		case 0:
			list1 = ops
		case 1:
			list2 = ops
		case 2:
			list3 = ops
		}
	}
	return list1, list2, list3, required, nil
}

// opcodeNotice is the cheap form used by Meta: it does not decode any opcode
// payload, only reports mandatory ids this reader does not implement.
func (f *File) opcodeNotice() string {
	_, _, _, req, err := f.opcodeLists()
	if err != nil {
		return "DNG opcode list is malformed: " + err.Error()
	}
	return noticeFor(req)
}

func (f *File) hasGainMap() bool {
	l1, l2, _, _, err := f.opcodeLists()
	if err != nil {
		return false
	}
	for _, list := range [][]opcode{l1, l2} {
		for _, o := range list {
			if o.id == opGainMap {
				return true
			}
		}
	}
	return false
}

type gainMap struct {
	top, left, bottom, right int
	plane, planes            int
	rowPitch, colPitch       int
	pointsV, pointsH         int
	spacingV, spacingH       float64
	originV, originH         float64
	mapPlanes                int
	gain                     []float32
}

type vignetteRadial struct {
	k  [5]float64
	cx float64
	cy float64
}

type warpRectilinear struct {
	planes int
	kr     [][4]float64
	kt     [][2]float64
	cx, cy float64
}

const (
	stage3Vignette = iota
	stage3Warp
)

type stage3op struct {
	kind int
	vig  *vignetteRadial
	warp *warpRectilinear
}

func parseGainMap(b []byte) (*gainMap, error) {
	const hdr = 76
	if len(b) < hdr {
		return nil, errors.New("dng: GainMap shorter than its header")
	}
	g := &gainMap{
		top: int(binary.BigEndian.Uint32(b[0:])), left: int(binary.BigEndian.Uint32(b[4:])),
		bottom: int(binary.BigEndian.Uint32(b[8:])), right: int(binary.BigEndian.Uint32(b[12:])),
		plane: int(binary.BigEndian.Uint32(b[16:])), planes: int(binary.BigEndian.Uint32(b[20:])),
		rowPitch: int(binary.BigEndian.Uint32(b[24:])), colPitch: int(binary.BigEndian.Uint32(b[28:])),
		pointsV: int(binary.BigEndian.Uint32(b[32:])), pointsH: int(binary.BigEndian.Uint32(b[36:])),
		spacingV:  math.Float64frombits(binary.BigEndian.Uint64(b[40:])),
		spacingH:  math.Float64frombits(binary.BigEndian.Uint64(b[48:])),
		originV:   math.Float64frombits(binary.BigEndian.Uint64(b[56:])),
		originH:   math.Float64frombits(binary.BigEndian.Uint64(b[64:])),
		mapPlanes: int(binary.BigEndian.Uint32(b[72:])),
	}
	if g.pointsV <= 0 || g.pointsH <= 0 || g.mapPlanes <= 0 {
		return nil, errors.New("dng: GainMap has zero map dimensions")
	}
	if g.rowPitch <= 0 || g.colPitch <= 0 {
		return nil, errors.New("dng: GainMap has a zero pitch")
	}
	n := int64(g.pointsV) * int64(g.pointsH) * int64(g.mapPlanes)
	if n <= 0 || n > 1<<24 || int64(hdr)+4*n != int64(len(b)) {
		return nil, fmt.Errorf("dng: GainMap has %d gain values, header says %d", (len(b)-hdr)/4, n)
	}
	g.gain = make([]float32, n)
	for i := range g.gain {
		g.gain[i] = math.Float32frombits(binary.BigEndian.Uint32(b[hdr+4*i:]))
	}
	return g, nil
}

func parseTrimBounds(b []byte) (area, error) {
	if len(b) < 16 {
		return area{}, errors.New("dng: TrimBounds shorter than 16 bytes")
	}
	a := area{
		top: int(binary.BigEndian.Uint32(b[0:])), left: int(binary.BigEndian.Uint32(b[4:])),
		bottom: int(binary.BigEndian.Uint32(b[8:])), right: int(binary.BigEndian.Uint32(b[12:])),
	}
	if a.empty() {
		return area{}, errors.New("dng: TrimBounds rectangle is empty or inverted")
	}
	return a, nil
}

func parseVignette(b []byte) (*vignetteRadial, error) {
	if len(b) != 7*8 {
		return nil, fmt.Errorf("dng: FixVignetteRadial has %d bytes, want 56", len(b))
	}
	v := &vignetteRadial{}
	for i := range v.k {
		v.k[i] = math.Float64frombits(binary.BigEndian.Uint64(b[8*i:]))
	}
	v.cx = math.Float64frombits(binary.BigEndian.Uint64(b[40:]))
	v.cy = math.Float64frombits(binary.BigEndian.Uint64(b[48:]))
	return v, nil
}

func parseWarp(b []byte) (*warpRectilinear, error) {
	if len(b) < 4 {
		return nil, errors.New("dng: WarpRectilinear shorter than its plane count")
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	if n != 1 && n != 3 {
		return nil, fmt.Errorf("dng: WarpRectilinear has %d planes (1 or 3)", n)
	}
	if len(b) != 4+48*n+16 {
		return nil, fmt.Errorf("dng: WarpRectilinear has %d bytes for %d planes", len(b), n)
	}
	w := &warpRectilinear{planes: n, kr: make([][4]float64, n), kt: make([][2]float64, n)}
	p := 4
	for i := 0; i < n; i++ {
		for j := 0; j < 4; j++ {
			w.kr[i][j] = math.Float64frombits(binary.BigEndian.Uint64(b[p:]))
			p += 8
		}
		for j := 0; j < 2; j++ {
			w.kt[i][j] = math.Float64frombits(binary.BigEndian.Uint64(b[p:]))
			p += 8
		}
	}
	w.cx = math.Float64frombits(binary.BigEndian.Uint64(b[p:]))
	w.cy = math.Float64frombits(binary.BigEndian.Uint64(b[p+8:]))
	return w, nil
}

// parseOpcodes turns the two raw-stage lists and the output-stage list into
// the operations this reader can apply, and records any mandatory opcode it
// cannot. GainMaps from OpcodeList2 are applied in raw space, as darktable
// and RawTherapee do: they are lens-shading gains and belong before the
// demosaic even when the writer chose list 2.
func (f *File) parseOpcodes(im *rawImage) error {
	l1, l2, l3, required, err := f.opcodeLists()
	if err != nil {
		return err
	}
	im.opcodeNotice = noticeFor(required)
	im.opcodes1, im.opcodes2, im.opcodes3 = l1, l2, l3
	// GainMaps, TrimBounds, vignette and warp are read from every list: the
	// stage a writer picks is not a reliable guide to their meaning, and they
	// are all harmless when absent.
	for _, list := range [][]opcode{l1, l2, l3} {
		for _, o := range list {
			switch o.id {
			case opGainMap:
				g, gerr := parseGainMap(o.data)
				if gerr != nil {
					if o.optional() {
						continue
					}
					return gerr
				}
				if err := im.checkGainMap(g); err != nil {
					if o.optional() {
						continue
					}
					return err
				}
				im.gainMaps = append(im.gainMaps, *g)
			case opTrimBounds:
				a, aerr := parseTrimBounds(o.data)
				if aerr != nil {
					if o.optional() {
						continue
					}
					return aerr
				}
				a.top, a.left = max(0, a.top), max(0, a.left)
				a.bottom, a.right = min(im.height, a.bottom), min(im.width, a.right)
				if !a.empty() {
					im.trim = append(im.trim, a)
				}
			case opFixVignetteRadial:
				v, verr := parseVignette(o.data)
				if verr != nil {
					if o.optional() {
						continue
					}
					return verr
				}
				im.stage3 = append(im.stage3, stage3op{kind: stage3Vignette, vig: v})
			case opWarpRectilinear:
				w, werr := parseWarp(o.data)
				if werr != nil {
					if o.optional() {
						continue
					}
					return werr
				}
				im.stage3 = append(im.stage3, stage3op{kind: stage3Warp, warp: w})
			}
		}
	}
	return nil
}

func (im *rawImage) checkGainMap(g *gainMap) error {
	if g.bottom > im.height || g.right > im.width || g.top < 0 || g.left < 0 || g.bottom <= g.top || g.right <= g.left {
		return fmt.Errorf("dng: GainMap rectangle %d,%d-%d,%d is outside the %dx%d image", g.left, g.top, g.right, g.bottom, im.width, im.height)
	}
	if g.planes < 1 || g.plane < 0 || g.plane+g.planes > im.samples {
		return fmt.Errorf("dng: GainMap covers planes %d+%d of %d", g.plane, g.planes, im.samples)
	}
	if !(g.spacingH > 0) || !(g.spacingV > 0) {
		return errors.New("dng: GainMap has a zero or negative spacing")
	}
	return nil
}

// gainAt samples the map bilinearly. Map coordinates are normalized to the
// whole raw image, matching the DNG SDK's GainMap use: the rectangle says
// where to apply a phase's map, not how to scale its coordinates.
func (g *gainMap) gainAt(x, y, width, height, plane int) float64 {
	fx := (float64(x)/float64(width) - g.originH) / g.spacingH
	fy := (float64(y)/float64(height) - g.originV) / g.spacingV
	fx = math.Max(0, math.Min(float64(g.pointsH-1), fx))
	fy = math.Max(0, math.Min(float64(g.pointsV-1), fy))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, g.pointsH-1), min(y0+1, g.pointsV-1)
	tx, ty := fx-float64(x0), fy-float64(y0)
	mp := 0
	if g.mapPlanes > 1 {
		mp = min(plane, g.mapPlanes-1)
	}
	at := func(i, j int) float64 { return float64(g.gain[(j*g.pointsH+i)*g.mapPlanes+mp]) }
	a := at(x0, y0)*(1-tx) + at(x1, y0)*tx
	b := at(x0, y1)*(1-tx) + at(x1, y1)*tx
	return a*(1-ty) + b*ty
}

// applyGainMaps multiplies stored raw samples by the interpolated gain before
// black/white normalisation. The raw plane is still in its sensor units, so
// this is the correct place for a lens-shading map.
func (im *rawImage) applyGainMaps(raw []float32) {
	for _, g := range im.gainMaps {
		yp := max(1, g.rowPitch)
		xp := max(1, g.colPitch)
		for y := g.top; y < g.bottom; y += yp {
			for x := g.left; x < g.right; x += xp {
				i := (y*im.width + x) * im.samples
				if im.samples == 1 {
					raw[i] *= float32(g.gainAt(x, y, im.width, im.height, 0))
					continue
				}
				for s := 0; s < im.samples; s++ {
					if g.planes == 1 && s != g.plane {
						continue
					}
					raw[i+s] *= float32(g.gainAt(x, y, im.width, im.height, s))
				}
			}
		}
	}
}

// applyStage3 applies the output-stage lens corrections (vignette and warp)
// in list order to the developed linear image.
func (im *rawImage) applyStage3(pix []float32, w, h int) []float32 {
	for _, op := range im.stage3 {
		switch op.kind {
		case stage3Vignette:
			applyVignette(pix, w, h, op.vig)
		case stage3Warp:
			pix = applyWarp(pix, w, h, op.warp)
		}
	}
	return pix
}

func applyVignette(pix []float32, w, h int, v *vignetteRadial) {
	cx := v.cx * float64(w)
	cy := v.cy * float64(h)
	maxR := math.Hypot(math.Max(math.Abs(cx), math.Abs(float64(w)-cx)), math.Max(math.Abs(cy), math.Abs(float64(h)-cy)))
	if maxR <= 0 {
		return
	}
	for y := 0; y < h; y++ {
		dy := (float64(y) - cy) / maxR
		for x := 0; x < w; x++ {
			dx := (float64(x) - cx) / maxR
			r2 := dx*dx + dy*dy
			gain := 1 + v.k[0]*r2 + v.k[1]*r2*r2 + v.k[2]*r2*r2*r2 + v.k[3]*r2*r2*r2*r2 + v.k[4]*r2*r2*r2*r2*r2
			i := (y*w + x) * 3
			pix[i] *= float32(gain)
			pix[i+1] *= float32(gain)
			pix[i+2] *= float32(gain)
		}
	}
}

// applyWarp resamples the image with the inverse of the WarpRectilinear
// mapping (destination -> source), as the DNG SDK does. The optical centre is
// in normalized image coordinates; a normalized radius of 1 is the distance
// from it to the farthest corner.
func applyWarp(src []float32, w, h int, wr *warpRectilinear) []float32 {
	out := make([]float32, len(src))
	cx := wr.cx * float64(w)
	cy := wr.cy * float64(h)
	maxDist := math.Hypot(math.Max(math.Abs(cx), math.Abs(float64(w)-cx)), math.Max(math.Abs(cy), math.Abs(float64(h)-cy)))
	if maxDist <= 0 {
		return src
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := (float64(x) - cx) / maxDist
			dy := (float64(y) - cy) / maxDist
			r2 := dx*dx + dy*dy
			o := (y*w + x) * 3
			for c := 0; c < 3; c++ {
				p := 0
				if wr.planes == 3 {
					p = c
				}
				k := wr.kr[p]
				ratio := k[0] + r2*(k[1]+r2*(k[2]+r2*k[3]))
				k0, k1 := wr.kt[p][0], wr.kt[p][1]
				tx := 2*k0*dx*dy + k1*(r2+2*dx*dx)
				ty := 2*k1*dx*dy + k0*(r2+2*dy*dy)
				sx := cx + (dx*ratio+tx)*maxDist
				sy := cy + (dy*ratio+ty)*maxDist
				out[o+c] = sampleBilinear1(src, w, h, sx, sy, c)
			}
		}
	}
	return out
}

func sampleBilinear1(pix []float32, w, h int, x, y float64, c int) float32 {
	if x < 0 || y < 0 || x > float64(w-1) || y > float64(h-1) {
		return 0
	}
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	tx, ty := float32(x-float64(x0)), float32(y-float64(y0))
	a := pix[(y0*w+x0)*3+c]*(1-tx) + pix[(y0*w+x1)*3+c]*tx
	b := pix[(y1*w+x0)*3+c]*(1-tx) + pix[(y1*w+x1)*3+c]*tx
	return a*(1-ty) + b*ty
}
