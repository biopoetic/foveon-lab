package dng

// Feature proofs: every case builds a synthetic file to the DNG tags it
// exercises, with a scene whose expected developed pixels are known exactly.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
)

func opcodeEntry(id, flags uint32, data []byte) []byte {
	b := make([]byte, 16+len(data))
	binary.BigEndian.PutUint32(b[0:], id)
	binary.BigEndian.PutUint32(b[4:], 0x01040000) // version, informational
	binary.BigEndian.PutUint32(b[8:], flags)
	binary.BigEndian.PutUint32(b[12:], uint32(len(data)))
	copy(b[16:], data)
	return b
}

func opcodeList(entries ...[]byte) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(len(entries)))
	for _, e := range entries {
		b = append(b, e...)
	}
	return b
}

func gainMapData(top, left, bottom, right, rowPitch, colPitch, pointsV, pointsH int, spacingV, spacingH, originV, originH float64, gains []float32) []byte {
	b := make([]byte, 76+4*len(gains))
	put := func(i int, v uint32) { binary.BigEndian.PutUint32(b[i:], v) }
	put(0, uint32(top))
	put(4, uint32(left))
	put(8, uint32(bottom))
	put(12, uint32(right))
	put(16, 0) // plane
	put(20, 1) // planes
	put(24, uint32(rowPitch))
	put(28, uint32(colPitch))
	put(32, uint32(pointsV))
	put(36, uint32(pointsH))
	binary.BigEndian.PutUint64(b[40:], math.Float64bits(spacingV))
	binary.BigEndian.PutUint64(b[48:], math.Float64bits(spacingH))
	binary.BigEndian.PutUint64(b[56:], math.Float64bits(originV))
	binary.BigEndian.PutUint64(b[64:], math.Float64bits(originH))
	put(72, 1) // map planes
	for i, g := range gains {
		binary.BigEndian.PutUint32(b[76+4*i:], math.Float32bits(g))
	}
	return b
}

func TestBlackLevelPerCFAAndDeltas(t *testing.T) {
	const w, h = 32, 16
	cols := [][3]float64{{0.40, 0.20, 0.10}, {0.10, 0.30, 0.50}}
	pat := []byte{0, 1, 1, 2}
	base := [4]float64{100, 110, 120, 130}
	deltaH := make([]float64, w)
	deltaV := make([]float64, h)
	for x := range deltaH {
		deltaH[x] = float64(x%3) - 1
	}
	for y := range deltaV {
		deltaV[y] = float64(y%5) - 2
	}
	blackAt := func(x, y int) float64 {
		return base[(y%2)*2+x%2] + deltaH[x] + deltaV[y]
	}
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := int(pat[(y%2)*2+x%2])
			pi := min(1, x*2/w)
			b := blackAt(x, y)
			vals[y*w+x] = uint32(math.Round(b + cols[pi][c]*(4095-b)))
		}
	}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags(pat)...)
	d.tags = append(d.tags,
		tgShort(tagBlackLevelRepeat, 2, 2),
		tgRational(tagBlackLevel, base[0], base[1], base[2], base[3]),
		tgSRational(tagBlackLevelDeltaH, deltaH...),
		tgSRational(tagBlackLevelDeltaV, deltaV...),
		tgLong(tagWhiteLevel, 4095),
	)
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	for pi, want := range cols {
		x, y := (pi*2+1)*w/4, h/2
		wantPatch(t, img, x, y, want, 0.015)
	}
}

func TestLinearizationTable(t *testing.T) {
	const w, h = 32, 16
	const black, white = 0, 8190
	pat := []byte{0, 1, 1, 2}
	cols := [][3]float64{{0.25, 0.10, 0.05}, {0.05, 0.30, 0.20}}
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := int(pat[(y%2)*2+x%2])
			pi := min(1, x*2/w)
			vals[y*w+x] = uint32(math.Round(cols[pi][c] * 4095))
		}
	}
	lookup := make([]uint16, 4096)
	for i := range lookup {
		lookup[i] = uint16(i * 2)
	}
	d := sdir{tags: baseRawTags(w, h, 12, 1, compressionNone, photometricCFA, packSamples(vals, 12, h, w))}
	d.tags = append(d.tags, cfaTags(pat)...)
	d.tags = append(d.tags,
		tgLong(tagBlackLevel, black),
		tgLong(tagWhiteLevel, white),
		st{tagLinearization, 3, uint32(len(lookup)), func() []byte {
			b := make([]byte, 2*len(lookup))
			for i, v := range lookup {
				binary.LittleEndian.PutUint16(b[2*i:], v)
			}
			return b
		}()},
	)
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{packSamples(vals, 12, h, w)}), 0)
	for pi, want := range cols {
		x, y := (pi*2+1)*w/4, h/2
		wantPatch(t, img, x, y, want, 0.015)
	}
}

func TestWhiteLevelPerChannel(t *testing.T) {
	const w, h = 16, 8
	white := [3]float64{3000, 4000, 5000}
	want := [3]float64{0.40, 0.25, 0.15}
	vals := make([]uint32, w*h*3)
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			vals[i*3+c] = uint32(math.Round(want[c] * white[c]))
		}
	}
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags,
		tgLong(tagWhiteLevel, uint32(white[0]), uint32(white[1]), uint32(white[2])),
	)
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	// A LinearRaw image has no demosaic, so every pixel is the same.
	for _, p := range [][2]int{{0, 0}, {w / 2, h / 2}, {w - 1, h - 1}} {
		wantPatch(t, img, p[0], p[1], want, 0.001)
	}
}

func TestFloatHalfAndFloat32(t *testing.T) {
	const w, h = 8, 4
	want := [3]float64{0.35, 0.60, 0.15}
	for _, bits := range []int{16, 32} {
		vals := make([]float64, w*h*3)
		for i := range vals {
			vals[i] = want[i%3]
		}
		var raw []byte
		if bits == 16 {
			raw = make([]byte, len(vals)*2)
			for i, v := range vals {
				binary.LittleEndian.PutUint16(raw[2*i:], floatToHalf(v))
			}
		} else {
			raw = make([]byte, len(vals)*4)
			for i, v := range vals {
				binary.LittleEndian.PutUint32(raw[4*i:], math.Float32bits(float32(v)))
			}
		}
		d := sdir{tags: baseRawTags(w, h, bits, 3, compressionNone, photometricLinearRaw, raw)}
		d.tags = append(d.tags, tgShort(tagSampleFormat, 3))
		// For 32-bit float, drop WhiteLevel entirely: the DNG default for a
		// float raw is 1.0. For 16-bit half, state it explicitly.
		if bits == 16 {
			for i := range d.tags {
				if d.tags[i].tag == tagWhiteLevel {
					d.tags[i] = tgRational(tagWhiteLevel, 1)
				}
			}
		} else {
			var tags []st
			for _, tg := range d.tags {
				if tg.tag != tagWhiteLevel {
					tags = append(tags, tg)
				}
			}
			d.tags = tags
		}
		d.tags = append(d.tags, colourTags()...)
		img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw}), 0)
		wantPatch(t, img, w/2, h/2, want, 0.002)
	}
}

func floatToHalf(v float64) uint16 {
	f := float32(v)
	bits := math.Float32bits(f)
	sign := uint16(bits>>16) & 0x8000
	exp := int((bits >> 23) & 0xff)
	mant := bits & 0x7fffff
	if exp == 0 {
		return sign
	}
	e := exp - 127 + 15
	if e >= 31 {
		return sign | 0x7c00
	}
	if e <= 0 {
		if e < -10 {
			return sign
		}
		mant |= 0x800000
		shift := uint(14 - e)
		return sign | uint16(mant>>shift)
	}
	return sign | uint16(e<<10) | uint16(mant>>13)
}

func TestMaskedAreasGiveBlack(t *testing.T) {
	const w, h = 32, 24
	const black, white = 512, 4095
	want := [3]float64{0.45, 0.20, 0.10}
	pat := []byte{0, 1, 1, 2}
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 4 {
				vals[y*w+x] = black
				continue
			}
			c := int(pat[(y%2)*2+x%2])
			vals[y*w+x] = uint32(math.Round(black + want[c]*(white-black)))
		}
	}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags(pat)...)
	for i := range d.tags {
		switch d.tags[i].tag {
		case tagBlackLevel:
			d.tags[i] = tgShortLong(tagBlackLevel) // present but empty: no black value
		case tagWhiteLevel:
			d.tags[i] = tgLong(tagWhiteLevel, white)
		}
	}
	d.tags = append(d.tags,
		tgLong(tagActiveArea, 4, 0, h, w),
		tgLong(tagMaskedAreas, 0, 0, 4, w),
	)
	// A zero-count BlackLevel is not written at all: drop it.
	var tags []st
	for _, tg := range d.tags {
		if tg.tag == tagBlackLevel && tg.count == 0 {
			continue
		}
		tags = append(tags, tg)
	}
	d.tags = tags
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	if img.W != w || img.H != h-4 {
		t.Fatalf("crop is %dx%d, want %dx%d", img.W, img.H, w, h-4)
	}
	wantPatch(t, img, w/2, (h-4)/2, want, 0.015)
}

func TestActiveAreaAndDefaultCrop(t *testing.T) {
	const w, h = 32, 24
	vals := make([]uint32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			vals[i] = uint32(math.Round(float64(x) / (w - 1) * 65535))
			vals[i+1] = uint32(math.Round(float64(y) / (h - 1) * 65535))
			vals[i+2] = 16384
		}
	}
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags,
		tgLong(tagActiveArea, 4, 2, h-2, w-2),
		tgLong(tagDefaultCropOrig, 2, 1),
		tgLong(tagDefaultCropSize, 20, 16),
	)
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	if img.W != 20 || img.H != 16 {
		t.Fatalf("crop is %dx%d, want 20x16", img.W, img.H)
	}
	// DefaultCropOrigin is relative to ActiveArea: first source pixel is
	// (left 2 + 2, top 4 + 1) = (4, 5).
	wantPatch(t, img, 0, 0, [3]float64{4.0 / (w - 1), 5.0 / (h - 1), 0.2500}, 0.002)
}

func TestActiveAreaShiftsCFAPhase(t *testing.T) {
	const w, h = 32, 16
	const ax, ay = 3, 1 // both odd: the CFA pattern starts here
	want := [3]float64{0.50, 0.20, 0.10}
	black := [4]float64{100, 200, 300, 400}
	pat := []byte{0, 1, 1, 2}
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			xx, yy := posMod(x-ax, 2), posMod(y-ay, 2)
			c := int(pat[yy*2+xx])
			b := black[yy*2+xx]
			vals[y*w+x] = uint32(math.Round(b + want[c]*(4095-b)))
		}
	}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags(pat)...)
	d.tags = append(d.tags,
		tgLong(tagActiveArea, ay, ax, h, w),
		tgShort(tagBlackLevelRepeat, 2, 2),
		tgRational(tagBlackLevel, black[0], black[1], black[2], black[3]),
		tgLong(tagWhiteLevel, 4095),
	)
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	if img.W != w-ax || img.H != h-ay {
		t.Fatalf("ActiveArea output %dx%d, want %dx%d", img.W, img.H, w-ax, h-ay)
	}
	wantPatch(t, img, img.W/2, img.H/2, want, 0.015)
}

func TestTrimBoundsOpcode(t *testing.T) {
	const w, h = 32, 24
	vals := make([]uint32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			vals[i] = uint32(math.Round(float64(x) / (w - 1) * 65535))
			vals[i+1] = uint32(math.Round(float64(y) / (h - 1) * 65535))
			vals[i+2] = 16384
		}
	}
	trim := make([]byte, 16)
	binary.BigEndian.PutUint32(trim[0:], 1)
	binary.BigEndian.PutUint32(trim[4:], 2)
	binary.BigEndian.PutUint32(trim[8:], 20)
	binary.BigEndian.PutUint32(trim[12:], 30)
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags, tgUndef(tagOpcodeList3, opcodeList(opcodeEntry(opTrimBounds, 0, trim))))
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	if img.W != 28 || img.H != 19 {
		t.Fatalf("TrimBounds output is %dx%d, want 28x19", img.W, img.H)
	}
	wantPatch(t, img, 0, 0, [3]float64{2.0 / (w - 1), 1.0 / (h - 1), 0.2500}, 0.002)
}

func TestGainMapFlattensVignettedScene(t *testing.T) {
	const w, h = 32, 32
	const black, white = 0, 4095
	want := [3]float64{0.5, 0.4, 0.3}
	gain := func(x, y int) float64 {
		return 1 - 0.4*float64(x)/float64(w) - 0.2*float64(y)/float64(h)
	}
	pat := []byte{0, 1, 1, 2}
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := int(pat[(y%2)*2+x%2])
			vals[y*w+x] = uint32(math.Round(want[c] / gain(x, y) * white))
		}
	}
	// Four maps, one for each 2x2 CFA phase. Gains are linear in x and y, so
	// bilinear interpolation on two map points per axis is exact.
	var entries [][]byte
	for phase := 0; phase < 4; phase++ {
		top, left := phase>>1, phase&1
		g := make([]float32, 4)
		for j := 0; j < 2; j++ {
			for i := 0; i < 2; i++ {
				g[j*2+i] = float32(1 - 0.4*float64(i) - 0.2*float64(j))
			}
		}
		data := gainMapData(top, left, h, w, 2, 2, 2, 2, 1, 1, 0, 0, g)
		entries = append(entries, opcodeEntry(opGainMap, 0, data))
	}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags(pat)...)
	for i := range d.tags {
		if d.tags[i].tag == tagWhiteLevel {
			d.tags[i] = tgLong(tagWhiteLevel, white)
		}
	}
	d.tags = append(d.tags, tgUndef(tagOpcodeList1, opcodeList(entries...)))
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	for _, p := range [][2]int{{8, 8}, {w / 2, h / 2}, {w - 9, h - 9}, {5, h - 6}} {
		wantPatch(t, img, p[0], p[1], want, 0.02)
	}
}

func TestFixVignetteRadial(t *testing.T) {
	const w, h = 32, 32
	const black, white = 0, 4095
	want := [3]float64{0.4, 0.3, 0.2}
	cx, cy := 0.5*float64(w), 0.5*float64(h)
	maxR := math.Hypot(math.Max(cx, float64(w)-cx), math.Max(cy, float64(h)-cy))
	gain := func(x, y int) float64 {
		dx, dy := (float64(x)-cx)/maxR, (float64(y)-cy)/maxR
		r2 := dx*dx + dy*dy
		return 1 - 0.5*r2
	}
	vals := make([]uint32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			for c := 0; c < 3; c++ {
				vals[i+c] = uint32(math.Round(want[c] / gain(x, y) * white))
			}
		}
	}
	vig := make([]byte, 56)
	for i, k := range []float64{-0.5, 0, 0, 0, 0} {
		binary.BigEndian.PutUint64(vig[8*i:], math.Float64bits(k))
	}
	binary.BigEndian.PutUint64(vig[40:], math.Float64bits(0.5))
	binary.BigEndian.PutUint64(vig[48:], math.Float64bits(0.5))
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	for i := range d.tags {
		if d.tags[i].tag == tagWhiteLevel {
			d.tags[i] = tgLong(tagWhiteLevel, white)
		}
	}
	d.tags = append(d.tags, tgUndef(tagOpcodeList3, opcodeList(opcodeEntry(opFixVignetteRadial, 0, vig))))
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	for _, p := range [][2]int{{16, 16}, {1, 1}, {w - 2, h - 2}} {
		wantPatch(t, img, p[0], p[1], want, 0.01)
	}
}

func TestWarpRectilinearIdentity(t *testing.T) {
	const w, h = 24, 16
	vals := make([]uint32, w*h*3)
	for i := 0; i < w*h; i++ {
		vals[i*3] = uint32(10000 + i*37)
		vals[i*3+1] = uint32(20000 + i*11)
		vals[i*3+2] = uint32(30000 - i*13)
	}
	warp := make([]byte, 4+48+16)
	binary.BigEndian.PutUint32(warp[0:], 1)
	coef := []float64{1, 0, 0, 0, 0, 0}
	for i, v := range coef {
		binary.BigEndian.PutUint64(warp[4+8*i:], math.Float64bits(v))
	}
	binary.BigEndian.PutUint64(warp[4+48:], math.Float64bits(0.5))
	binary.BigEndian.PutUint64(warp[4+48+8:], math.Float64bits(0.5))
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags, tgUndef(tagOpcodeList3, opcodeList(opcodeEntry(opWarpRectilinear, 0, warp))))
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	plain := developFile(t, buildTIFF(t, []sdir{{tags: append(baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals)), colourTags()...)}}, [][]byte{raw16(vals)}), 0)
	for i := range plain.Pix {
		if math.Abs(float64(img.Pix[i]-plain.Pix[i])) > 0.0005 {
			t.Fatalf("identity warp changed sample %d: %v vs %v", i, img.Pix[i], plain.Pix[i])
		}
	}
}

func TestUnknownOpcodeNotice(t *testing.T) {
	const w, h = 8, 4
	vals := make([]uint32, w*h*3)
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags, colourTags()...)
	d.tags = append(d.tags, tgUndef(tagOpcodeList1, opcodeList(opcodeEntry(999, 1, nil))))
	f, err := NewReader(bytes.NewReader(buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})), int64(len(buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}))))
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.Notice != "" {
		t.Errorf("optional unknown opcode produced a notice: %q", f.Meta.Notice)
	}
	// Required unknown: same list with flags 0.
	d.tags = d.tags[:len(d.tags)-1]
	d.tags = append(d.tags, tgUndef(tagOpcodeList1, opcodeList(opcodeEntry(999, 0, nil))))
	b := buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
	f, err = NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.Notice == "" || !bytes.Contains([]byte(f.Meta.Notice), []byte("999")) {
		t.Errorf("required unknown opcode notice is %q", f.Meta.Notice)
	}
	if _, err := f.Develop(0); err != nil {
		t.Errorf("a required unknown opcode should still develop with a notice: %v", err)
	}
}

func TestLossyJPEGCompression(t *testing.T) {
	const w, h = 32, 24
	want := [3]float64{0.60, 0.35, 0.20}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(want[0] * 255), uint8(want[1] * 255), uint8(want[2] * 255), 255})
		}
	}
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	jpg := jb.Bytes()
	d := sdir{tags: baseRawTags(w, h, 8, 3, compressionLossyJPEG, photometricLinearRaw, jpg)}
	d.tags = append(d.tags, colourTags()...)
	b := buildTIFF(t, []sdir{d}, [][]byte{jpg})
	out := developFile(t, b, 0)
	if out.W != w || out.H != h {
		t.Fatalf("lossy output %dx%d", out.W, out.H)
	}
	wantPatch(t, out, w/2, h/2, want, 0.08)
}

func TestJPEGXLRefused(t *testing.T) {
	m := Meta{Compression: compressionJPEGXL, Samples: 3, Bits: 8, SampleFormat: 1}
	err := m.Readable()
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("JPEG XL")) {
		t.Fatalf("JPEG XL error is %v", err)
	}
}

func TestMonochrome(t *testing.T) {
	const w, h = 16, 8
	const level = 0.42
	for _, cfa := range []bool{false, true} {
		vals := make([]uint32, w*h)
		for i := range vals {
			vals[i] = uint32(math.Round(level * 65535))
		}
		ph := photometricLinearRaw
		samples := 1
		d := sdir{tags: baseRawTags(w, h, 16, samples, compressionNone, ph, raw16(vals))}
		if cfa {
			for i := range d.tags {
				if d.tags[i].tag == tagPhotometric {
					d.tags[i] = tgShort(tagPhotometric, photometricCFA)
				}
			}
			d.tags = append(d.tags, monoCFATags()...)
		}
		d.tags = append(d.tags, colourTags()...)
		img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
		i := (h/2*w + w/2) * 3
		for c := 0; c < 3; c++ {
			if math.Abs(float64(img.Pix[i+c])-level) > 0.001 {
				t.Errorf("mono cfa=%v channel %d: got %.4f want %.4f", cfa, c, img.Pix[i+c], level)
			}
		}
	}
}

func TestNonBayerCFARefused(t *testing.T) {
	const w, h = 24, 24
	vals := make([]uint32, w*h)
	pat := make([]byte, 36)
	for i := range pat {
		pat[i] = byte(i % 3)
	}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, tgShort(tagCFARepeatPattern, 6, 6), tgByte(tagCFAPattern, pat...), tgByte(tagCFAPlaneColor, 0, 1, 2), tgShort(tagCFALayout, 1))
	d.tags = append(d.tags, colourTags()...)
	b := buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Develop(0); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("6x6 CFA error is %v, want unsupported", err)
	}
}

func TestSubTileLayoutRefused(t *testing.T) {
	const w, h = 8, 4
	vals := make([]uint32, w*h*3)
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags, tgLong(tagSubTileBlockSize, 2, 2))
	d.tags = append(d.tags, colourTags()...)
	b := buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Develop(0); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SubTileBlockSize error is %v, want unsupported", err)
	}
}

func TestSubIFDPreviewNotTheRaw(t *testing.T) {
	const pw, ph = 64, 64
	const rw, rh = 16, 16
	preview := make([]uint32, pw*ph*3)
	for i := range preview {
		preview[i] = uint32(50000)
	}
	rawv := make([]uint32, rw*rh)
	for i := range rawv {
		rawv[i] = uint32(30000)
	}
	pat := []byte{0, 1, 1, 2}
	previewIFD := sdir{tags: baseRawTags(pw, ph, 16, 3, compressionNone, photometricLinearRaw, raw16(preview))}
	previewIFD.tags = append(previewIFD.tags, tgShort(tagPhotometric, photometricLinearRaw))
	for i := range previewIFD.tags {
		if previewIFD.tags[i].tag == tagNewSubFileType {
			previewIFD.tags[i] = tgLong(tagNewSubFileType, 1)
		}
	}
	rawIFD := sdir{tags: baseRawTags(rw, rh, 16, 1, compressionNone, photometricCFA, raw16(rawv))}
	rawIFD.tags = append(rawIFD.tags, cfaTags(pat)...)
	rawIFD.tags = append(rawIFD.tags, colourTags()...)
	dirs := []sdir{previewIFD, rawIFD}
	dirs[0].sub = []int{1}
	dirs[0].tags = append(dirs[0].tags, tgLong(tagSubIFDs, 0))
	b := buildTIFF(t, dirs, [][]byte{raw16(preview), raw16(rawv)})
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	img, err := f.Develop(0)
	if err != nil {
		t.Fatal(err)
	}
	if img.W != rw || img.H != rh {
		t.Fatalf("chose %dx%d, want the %dx%d raw", img.W, img.H, rw, rh)
	}
}

func TestTwoIlluminantsAndCameraCalibration(t *testing.T) {
	const w, h = 8, 4
	vals := make([]uint32, w*h*3)
	for i := 0; i < w*h; i++ {
		vals[i*3] = 10000
		vals[i*3+1] = 20000
		vals[i*3+2] = 30000
	}
	// Camera matrices: the same XYZ->camera map scaled by two, so the two
	// illuminant matrices are easy to tell apart. CameraCalibration2 doubles
	// the red camera channel.
	cm := [9]float64{3.2404542, -1.5371385, -0.4985314, -0.9692660, 1.8760108, 0.0415560, 0.0556434, -0.2040259, 1.0572252}
	cm2 := [9]float64{}
	for i, v := range cm {
		cm2[i] = 2 * v
	}
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags,
		tgSRational(tagColorMatrix1, cm[:]...),
		tgSRational(tagColorMatrix2, cm2[:]...),
		tgShort(tagCalibIllum1, 21),
		tgShort(tagCalibIllum2, 17),
		tgSRational(tagCameraCalib2, 2, 0, 0, 0, 1, 0, 0, 0, 1),
		tgRational(tagAsShotWhiteXY, 0.44757, 0.40745), // Standard Light A
	)
	b := buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	im, err := f.image()
	if err != nil {
		t.Fatal(err)
	}
	m, _, _, _, err := f.colour(im)
	if err != nil {
		t.Fatal(err)
	}
	m2 := f.cameraToXYZ(cm2, 2856)
	// White point A selects the second illuminant, so the result must be
	// cameraToXYZ2 with CameraCalibration2 folded in.
	cal2 := [9]float64{2, 0, 0, 0, 1, 0, 0, 0, 1}
	want := mul33(m2, cal2)
	for i := range want {
		// McCamy's CCT for the A white point is a few kelvin off 2856, so
		// the interpolation weight is just under 1; the matrix is the second
		// one to within 0.1 %.
		if math.Abs(m[i]-want[i]) > 0.001 {
			t.Errorf("colour matrix %d: got %.8f want %.8f", i, m[i], want[i])
		}
	}
}

func TestAsShotNeutralAndAnalogBalance(t *testing.T) {
	const w, h = 4, 2
	vals := make([]uint32, w*h*3)
	for i := 0; i < w*h; i++ {
		vals[i*3] = 32768
		vals[i*3+1] = 16384
		vals[i*3+2] = 8192
	}
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags,
		tgRational(tagAsShotNeutral, 2, 1, 0.5),
		tgRational(tagAnalogBalance, 1, 2, 1),
	)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0)
	// No colour matrix: the data is taken as XYZ D50, then XYZ -> sRGB.
	// White balance divides by neutral/analog, i.e. R: /2, G: *2, B: /0.5.
	xyz := [3]float64{0.5 / 2, 0.25 * 2, 0.125 / 0.5}
	want := mulVec(xyzD50ToSRGB, xyz)
	for c := range want {
		if want[c] < 0 {
			want[c] = 0 // the reader never hands negative light to the next stage
		}
	}
	wantPatch(t, img, 1, 1, want, 0.001)
}

func TestDefaultScaleAndOrientationMeta(t *testing.T) {
	const w, h = 8, 4
	vals := make([]uint32, w*h*3)
	for i := 0; i < w*h; i++ {
		vals[i*3] = 20000
		vals[i*3+1] = 30000
		vals[i*3+2] = 40000
	}
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags, tgRational(tagDefaultScale, 2, 1), tgShort(tagOrientation, 2))
	d.tags = append(d.tags, colourTags()...)
	b := buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.Orientation != 2 || !f.Meta.Mirror || f.Meta.Rotation != 0 {
		t.Errorf("orientation meta = %+v, want 2/mirror/no rotation", f.Meta)
	}
	img, err := f.Develop(0)
	if err != nil {
		t.Fatal(err)
	}
	if img.W != 2*w || img.H != h {
		t.Fatalf("DefaultScale output %dx%d, want %dx%d", img.W, img.H, 2*w, h)
	}
	wantPatch(t, img, w, h/2, [3]float64{20000.0 / 65535, 30000.0 / 65535, 40000.0 / 65535}, 0.002)
}

func TestLosslessJPEGTile(t *testing.T) {
	const w, h = 20, 12
	vals := make([]int, w*h*3)
	for i := range vals {
		vals[i] = (i*1237 + 500) & 0xFFFF
	}
	src := losslessJPEGTest(t, w, h, 3, vals, 0, 4)
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionJPEG, photometricLinearRaw, src)}
	for i := range d.tags {
		switch d.tags[i].tag {
		case tagStripOffsets:
			d.tags[i] = tgLong(tagTileOffsets, 0)
		case tagRowsPerStrip:
			d.tags[i] = tgLong(tagTileLength, h)
		case tagStripByteCounts:
			d.tags[i] = tgLong(tagTileByteCounts, uint32(len(src)))
		}
	}
	d.tags = append(d.tags,
		tgLong(tagTileWidth, w),
		tgLong(tagWhiteLevel, 65535),
	)
	d.tags = append(d.tags, colourTags()...)
	img := developFile(t, buildTIFF(t, []sdir{d}, [][]byte{src}), 0)
	if img.W != w || img.H != h {
		t.Fatalf("LJPEG tile output %dx%d", img.W, img.H)
	}
	for _, p := range [][2]int{{0, 0}, {w / 2, h / 2}, {w - 1, h - 1}} {
		i := (p[1]*w + p[0]) * 3
		for c := 0; c < 3; c++ {
			want := float64(vals[i+c]) / 65535
			if math.Abs(float64(img.Pix[i+c])-want) > 0.001 {
				t.Errorf("pixel %v channel %d: got %.5f want %.5f", p, c, img.Pix[i+c], want)
			}
		}
	}
}

func TestOrientationAll(t *testing.T) {
	const w, h = 4, 3
	img := &Image{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[(y*w+x)*3] = float32(y*w + x)
		}
	}
	// ApplyOrientation's mappings, written out independently.
	want := map[int]func(x, y int) (int, int, int, int){
		1: func(x, y int) (int, int, int, int) { return w, h, x, y },
		2: func(x, y int) (int, int, int, int) { return w, h, w - 1 - x, y },
		3: func(x, y int) (int, int, int, int) { return w, h, w - 1 - x, h - 1 - y },
		4: func(x, y int) (int, int, int, int) { return w, h, x, h - 1 - y },
		5: func(x, y int) (int, int, int, int) { return h, w, y, x },
		6: func(x, y int) (int, int, int, int) { return h, w, h - 1 - y, x },
		7: func(x, y int) (int, int, int, int) { return h, w, h - 1 - y, w - 1 - x },
		8: func(x, y int) (int, int, int, int) { return h, w, y, w - 1 - x },
	}
	for o := 1; o <= 8; o++ {
		out := ApplyOrientation(img, o)
		ow, oh := out.W, out.H
		ew, eh, _, _ := want[o](0, 0)
		if ow != ew || oh != eh {
			t.Fatalf("orientation %d output is %dx%d, want %dx%d", o, ow, oh, ew, eh)
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				_, _, nx, ny := want[o](x, y)
				if int(out.Pix[(ny*ow+nx)*3]) != y*w+x {
					t.Fatalf("orientation %d: (%d,%d) -> (%d,%d) = %v, want %d", o, x, y, nx, ny, out.Pix[(ny*ow+nx)*3], y*w+x)
				}
			}
		}
	}
}

func TestBadInputNeverPanicsAndErrors(t *testing.T) {
	const w, h = 8, 4
	vals := make([]uint32, w*h*3)
	for i := range vals {
		vals[i] = uint32(i * 1000)
	}
	good := buildTIFF(t, []sdir{{tags: append(baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals)), colourTags()...)}}, [][]byte{raw16(vals)})
	for n := 0; n < len(good); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic at truncation %d: %v", n, r)
				}
			}()
			f, err := NewReader(bytes.NewReader(good[:n]), int64(n))
			if err != nil {
				return
			}
			_, _ = f.Develop(0)
		}()
	}
	// A valid IFD that claims absurd dimensions.
	bad := buildTIFF(t, []sdir{{tags: append(baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals)), colourTags()...)}}, [][]byte{raw16(vals)})
	// Width at 2^31 in the first IFD.
	if !patchTagValue(bad, tagWidth, 0x7fffffff) {
		t.Fatal("no Width tag to corrupt")
	}
	f, err := NewReader(bytes.NewReader(bad), int64(len(bad)))
	if err != nil {
		return
	}
	if _, err := f.Develop(0); err == nil {
		t.Fatal("2^31-pixel-wide image developed without an error")
	}
	// A zero-byte-count strip.
	zero := buildTIFF(t, []sdir{{tags: append(baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals)), colourTags()...)}}, [][]byte{raw16(vals)})
	if !patchTagValue(zero, tagStripByteCounts, 0) {
		t.Fatal("no StripByteCounts tag to corrupt")
	}
	f, err = NewReader(bytes.NewReader(zero), int64(len(zero)))
	if err != nil {
		return
	}
	if _, err := f.Develop(0); err == nil {
		t.Fatal("zero-size strip developed without an error")
	}
	// A strip offset past the end of the file.
	past := buildTIFF(t, []sdir{{tags: append(baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals)), colourTags()...)}}, [][]byte{raw16(vals)})
	if !patchTagValue(past, tagStripOffsets, uint32(len(past)+1000)) {
		t.Fatal("no StripOffsets tag to corrupt")
	}
	f, err = NewReader(bytes.NewReader(past), int64(len(past)))
	if err != nil {
		return
	}
	if _, err := f.Develop(0); err == nil {
		t.Fatal("strip offset past the end developed without an error")
	}
	// A zero-size tile.
	tv := make([]uint32, w*h*3)
	td := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(tv))}
	for i := range td.tags {
		switch td.tags[i].tag {
		case tagStripOffsets:
			td.tags[i] = tgLong(tagTileOffsets, 0)
		case tagRowsPerStrip:
			td.tags[i] = tgLong(tagTileLength, h)
		case tagStripByteCounts:
			td.tags[i] = tgLong(tagTileByteCounts, uint32(len(raw16(tv))))
		}
	}
	td.tags = append(td.tags, tgLong(tagTileWidth, w))
	td.tags = append(td.tags, colourTags()...)
	tb := buildTIFF(t, []sdir{td}, [][]byte{raw16(tv)})
	if !patchTagValue(tb, tagTileByteCounts, 0) {
		t.Fatal("no TileByteCounts tag to corrupt")
	}
	f, err = NewReader(bytes.NewReader(tb), int64(len(tb)))
	if err != nil {
		return
	}
	if _, err := f.Develop(0); err == nil {
		t.Fatal("zero-size tile developed without an error")
	}
}
