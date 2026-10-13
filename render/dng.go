package render

// WriteRenderedDNG writes an already-rendered image (the SPP emulation with
// a preset applied) as a 16-bit linear DNG: LinearRaw in linear sRGB with a
// linear profile tone curve, so Lightroom, Capture One, darktable or Apple
// Photos open it looking like the render, while white balance and exposure
// stay editable on 16-bit linear data. (The raw DNG export keeps the
// sensor data instead and cannot carry the look.)

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"sort"
)

const (
	tByte      = 1
	tASCII     = 2
	tShort     = 3
	tLong      = 4
	tRational  = 5
	tSRational = 10
	tFloat     = 11
)

type dngEntry struct {
	tag, typ uint16
	count    uint32
	data     []byte // little-endian payload
}

func dngShorts(v ...uint16) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], x)
	}
	return b
}

func dngLongs(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func dngSRationals(v ...float64) []byte {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[8*i:], uint32(int32(math.Round(x*10000))))
		binary.LittleEndian.PutUint32(b[8*i+4:], 10000)
	}
	return b
}

func dngRationals(v ...float64) []byte {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[8*i:], uint32(math.Round(x*10000)))
		binary.LittleEndian.PutUint32(b[8*i+4:], 10000)
	}
	return b
}

func dngFloats(v ...float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func dngASCII(s string) []byte { return append([]byte(s), 0) }

// xyzD65ToSRGB: ColorMatrix1 (XYZ → camera, here linear sRGB).
var xyzD65ToSRGB = []float64{
	3.2404542, -1.5371385, -0.4985314,
	-0.9692660, 1.8760108, 0.0415560,
	0.0556434, -0.2040259, 1.0572252,
}

// sRGBToXYZD50: ForwardMatrix1 (camera = linear sRGB → XYZ D50, Bradford).
var sRGBToXYZD50 = []float64{
	0.4360747, 0.3850649, 0.1430804,
	0.2225045, 0.7168786, 0.0606169,
	0.0139322, 0.0971045, 0.7141733,
}

// WriteRenderedDNG writes img (sRGB-encoded 16-bit, as Render16 returns)
// as a linear DNG. model names the source camera for the metadata.
func WriteRenderedDNG(w io.Writer, img *image.RGBA64, make_, model, software string) error {
	width, height := img.Rect.Dx(), img.Rect.Dy()
	// sRGB-encoded 16-bit → linear 16-bit (lookup table).
	var lin [65536]uint16
	for i := range lin {
		lin[i] = uint16(math.Round(float64(ToLinear(float32(i)/65535)) * 65535))
	}
	const rowsPerStrip = 32
	var strips [][]byte
	row := make([]byte, width*6)
	for y0 := 0; y0 < height; y0 += rowsPerStrip {
		// Uncompressed: DNG readers only promise Deflate for floating-point data.
		var buf bytes.Buffer
		for y := y0; y < min(height, y0+rowsPerStrip); y++ {
			src := img.Pix[y*img.Stride:]
			for x := 0; x < width; x++ {
				for c := 0; c < 3; c++ {
					v := uint16(src[x*8+c*2])<<8 | uint16(src[x*8+c*2+1])
					binary.LittleEndian.PutUint16(row[x*6+c*2:], lin[v])
				}
			}
			buf.Write(row)
		}
		strips = append(strips, buf.Bytes())
	}

	return writeDNG(w, width, height, rowsPerStrip, strips, dngMeta{
		make: make_, model: model, software: software, unique: "Foveon Lab render",
		colorMatrix: xyzD65ToSRGB, forward: sRGBToXYZD50, neutral: [3]float64{1, 1, 1},
		profile: "Foveon Lab (as rendered)", linearCurve: true,
	})
}

// dngMeta is what differs between the DNGs Foveon Lab writes.
type dngMeta struct {
	make, model, software, unique string
	colorMatrix, forward          []float64 // XYZ(D65) -> camera; camera -> XYZ(D50)
	neutral                       [3]float64
	baselineExposure              float64
	profile                       string
	linearCurve                   bool      // ProfileToneCurve: linear (show the data as is)
	tone                          []float32 // ProfileToneCurve pairs (camera profile)
}

// writeDNG writes a 3-channel 16-bit LinearRaw DNG from prepared strips.
func writeDNG(w io.Writer, width, height, rowsPerStrip int, strips [][]byte, m dngMeta) error {
	stripCounts := make([]uint32, len(strips))
	for i, s := range strips {
		stripCounts[i] = uint32(len(s))
	}
	entries := []dngEntry{
		{254, tLong, 1, dngLongs(0)},                                // NewSubFileType: main image
		{256, tLong, 1, dngLongs(uint32(width))},                    // ImageWidth
		{257, tLong, 1, dngLongs(uint32(height))},                   // ImageLength
		{258, tShort, 3, dngShorts(16, 16, 16)},                     // BitsPerSample
		{259, tShort, 1, dngShorts(1)},                              // Compression: none
		{262, tShort, 1, dngShorts(34892)},                          // PhotometricInterpretation: LinearRaw
		{271, tASCII, 0, dngASCII(m.make)},                          // Make
		{272, tASCII, 0, dngASCII(m.model)},                         // Model
		{273, tLong, uint32(len(strips)), nil},                      // StripOffsets (filled in below)
		{274, tShort, 1, dngShorts(1)},                              // Orientation
		{277, tShort, 1, dngShorts(3)},                              // SamplesPerPixel
		{278, tLong, 1, dngLongs(uint32(rowsPerStrip))},             // RowsPerStrip
		{279, tLong, uint32(len(strips)), dngLongs(stripCounts...)}, // StripByteCounts
		{284, tShort, 1, dngShorts(1)},                              // PlanarConfiguration: chunky
		{305, tASCII, 0, dngASCII(m.software)},                      // Software
		{50706, tByte, 4, []byte{1, 4, 0, 0}},                       // DNGVersion
		{50707, tByte, 4, []byte{1, 2, 0, 0}},                       // DNGBackwardVersion
		{50708, tASCII, 0, dngASCII(m.unique)},                      // UniqueCameraModel
		{50714, tLong, 3, dngLongs(0, 0, 0)},                        // BlackLevel
		{50717, tLong, 3, dngLongs(65535, 65535, 65535)},            // WhiteLevel
		{50721, tSRational, 9, dngSRationals(m.colorMatrix...)},     // ColorMatrix1
		{50728, tRational, 3, dngRationals(m.neutral[:]...)},        // AsShotNeutral
		{50730, tSRational, 1, dngSRationals(m.baselineExposure)},   // BaselineExposure
		{50778, tShort, 1, dngShorts(21)},                           // CalibrationIlluminant1: D65
		{50936, tASCII, 0, dngASCII(m.profile)},                     // ProfileName
		{50941, tLong, 1, dngLongs(3)},                              // ProfileEmbedPolicy: no restrictions
		{50964, tSRational, 9, dngSRationals(m.forward...)},         // ForwardMatrix1
	}
	if m.linearCurve {
		entries = append(entries, dngEntry{50940, tFloat, 4, dngFloats(0, 0, 1, 1)}) // ProfileToneCurve: linear
	} else if len(m.tone) >= 4 {
		entries = append(entries,
			dngEntry{50940, tFloat, uint32(len(m.tone)), dngFloats(m.tone...)}, // ProfileToneCurve
			dngEntry{51110, tLong, 1, dngLongs(1)})                             // DefaultBlackRender: none
	}
	for i := range entries {
		if entries[i].typ == tASCII {
			entries[i].count = uint32(len(entries[i].data))
		}
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].tag < entries[b].tag })

	// Layout: header (8) | IFD | out-of-line values | strips.
	ifdSize := 2 + 12*len(entries) + 4
	off := 8 + ifdSize
	var extra bytes.Buffer
	valueOff := make([]uint32, len(entries))
	for i, e := range entries {
		if e.tag == 273 {
			continue
		}
		if len(e.data) > 4 {
			if (off+extra.Len())%2 == 1 {
				extra.WriteByte(0)
			}
			valueOff[i] = uint32(off + extra.Len())
			extra.Write(e.data)
		}
	}
	if (off+extra.Len())%2 == 1 {
		extra.WriteByte(0)
	}
	// Strip offsets go out of line too (one LONG each).
	stripOffPos := off + extra.Len()
	extra.Write(make([]byte, 4*len(strips)))
	dataStart := uint32(off + extra.Len())
	offsets := make([]uint32, len(strips))
	pos := dataStart
	for i, s := range strips {
		offsets[i] = pos
		pos += uint32(len(s))
	}
	eb := extra.Bytes()
	for i, o := range offsets {
		binary.LittleEndian.PutUint32(eb[stripOffPos-off+4*i:], o)
	}

	var out bytes.Buffer
	out.Write([]byte{'I', 'I', 42, 0})
	binary.Write(&out, binary.LittleEndian, uint32(8))
	binary.Write(&out, binary.LittleEndian, uint16(len(entries)))
	for i, e := range entries {
		binary.Write(&out, binary.LittleEndian, e.tag)
		binary.Write(&out, binary.LittleEndian, e.typ)
		binary.Write(&out, binary.LittleEndian, e.count)
		switch {
		case e.tag == 273:
			if len(strips) == 1 {
				binary.Write(&out, binary.LittleEndian, offsets[0])
			} else {
				binary.Write(&out, binary.LittleEndian, uint32(stripOffPos))
			}
		case len(e.data) > 4:
			binary.Write(&out, binary.LittleEndian, valueOff[i])
		default:
			var v [4]byte
			copy(v[:], e.data)
			out.Write(v[:])
		}
	}
	binary.Write(&out, binary.LittleEndian, uint32(0)) // no next IFD
	out.Write(eb)
	if _, err := w.Write(out.Bytes()); err != nil {
		return err
	}
	for _, s := range strips {
		if _, err := w.Write(s); err != nil {
			return err
		}
	}
	return nil
}

// DNGProfile is a camera profile embedded in a camera DNG: a 3×3 matrix
// on the camera's linear sRGB (as RawCorrection.Matrix) and a tone curve
// (ProfileToneCurve: linear in, linear out, on ProPhoto RGB with the
// reader's hue-preserving RGB tone). Lightroom and Camera Raw show it as
// the "Embedded" profile.
type DNGProfile struct {
	Name   string
	Matrix [9]float64
	Tone   []float64 // x0, y0, x1, y1, …; (0,0) first, (1,1) last
}

// DNGProfile is the raw base as a DNG camera profile: its matrix, and its
// tone for grey (the mean of the three channel curves, made monotone).
// Held out against SPP's exports it stays within 0.2 dE2000 of the full
// per-channel raw base on every camera group (fit-raw -dngprofile,
// 2026-10-13: SD1 6.96 vs 6.75, SD15 8.49 vs 8.44, DP Merrill 6.24 vs
// 6.39, Quattro 6.14 vs 6.14).
func (c *RawCorrection) DNGProfile(name string) *DNGProfile {
	id := *c
	id.Matrix = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	const n = 256
	tone := make([]float64, 0, 2*(n+1))
	prev := 0.0
	for i := 0; i <= n; i++ {
		x := float64(i) / n
		r, g, b := id.Pixel(float32(x), float32(x), float32(x))
		y := math.Min(1, math.Max(prev, (float64(r)+float64(g)+float64(b))/3))
		if i == 0 {
			y = 0
		} else if i == n {
			y = 1
		}
		tone = append(tone, x, y)
		prev = y
	}
	return &DNGProfile{Name: name, Matrix: c.Matrix, Tone: tone}
}

// WriteCameraDNG writes linear camera RGB (0 = black, 1 = white per layer;
// w×h×3) as a raw-like LinearRaw DNG that raw editors develop themselves.
// The colour metadata comes from the camera's own white balance and colour
// matrix: linear sRGB = cc · diag(gain) · camera, then prof's matrix and
// tone when prof is not nil. Values above white clip.
func WriteCameraDNG(out io.Writer, pix []float32, w, h int, gain [3]float64, cc [9]float64, prof *DNGProfile, make_, model, software string) error {
	const rowsPerStrip = 32
	var strips [][]byte
	row := make([]byte, w*6)
	for y0 := 0; y0 < h; y0 += rowsPerStrip {
		var buf bytes.Buffer
		for y := y0; y < min(h, y0+rowsPerStrip); y++ {
			src := pix[y*w*3:]
			for i := 0; i < w*3; i++ {
				v := math.Max(0, math.Min(1, float64(src[i])))
				binary.LittleEndian.PutUint16(row[i*2:], uint16(math.Round(v*65535)))
			}
			buf.Write(row)
		}
		strips = append(strips, buf.Bytes())
	}
	// E = the profile matrix · cc: white-balanced camera -> linear sRGB.
	// The neutral (AsShotNeutral) is the camera value E·diag(gain) takes to
	// grey: diag(gain)^-1 · E^-1 · 1, scaled to a top of 1. ColorMatrix1 is
	// the inverse of sRGB->XYZ(D65) · E · diag(gain); ForwardMatrix1 is the
	// D50 sRGB matrix · E · diag(gain · neutral), which takes 1,1,1 to D50
	// white as readers require (so a profile matrix that tints grey turns
	// into the white balance, not into a cast the reader would normalise
	// away). The brightness of that grey, k, a reader won't apply: it goes
	// into BaselineExposure (e.g. +6 stops for a Merrill ISO 6400).
	e := cc
	name := "Foveon Lab camera matrix"
	if prof != nil {
		e = mul33(prof.Matrix, cc)
		name = prof.Name
	}
	for _, g := range gain {
		if g <= 0 {
			return errors.New("dng: bad white balance gains")
		}
	}
	einv, ok := invert3x3(e)
	if !ok {
		return errors.New("dng: singular colour matrix")
	}
	var neutral [3]float64
	top := 0.0
	for i := 0; i < 3; i++ {
		neutral[i] = (einv[i*3] + einv[i*3+1] + einv[i*3+2]) / gain[i]
		top = math.Max(top, neutral[i])
	}
	for i := range neutral {
		neutral[i] /= top
		if neutral[i] <= 0 {
			return errors.New("dng: the colour matrix takes grey outside the camera's range")
		}
	}
	srgbToXYZD65 := [9]float64{0.4124, 0.3576, 0.1805, 0.2126, 0.7152, 0.0722, 0.0193, 0.1192, 0.9505}
	var d50 [9]float64
	copy(d50[:], sRGBToXYZD50)
	var eg, egn [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			eg[i*3+j] = e[i*3+j] * gain[j]
			egn[i*3+j] = eg[i*3+j] * neutral[j]
		}
	}
	k := egn[3] + egn[4] + egn[5] // the grey's level (every row gives it)
	camToXYZ, fwd := mul33(srgbToXYZD65, eg), mul33(d50, egn)
	for i := range fwd {
		fwd[i] /= k
	}
	cm, ok := invert3x3(camToXYZ)
	if !ok {
		return errors.New("dng: singular colour matrix")
	}
	meta := dngMeta{
		make: make_, model: model, software: software, unique: make_ + " " + model,
		colorMatrix: cm[:], forward: fwd[:], neutral: neutral,
		baselineExposure: math.Log2(k), profile: name,
	}
	if prof != nil {
		for _, v := range prof.Tone {
			meta.tone = append(meta.tone, float32(v))
		}
	}
	return writeDNG(out, w, h, rowsPerStrip, strips, meta)
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
	det := m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
	if det == 0 {
		return m, false
	}
	var r [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			i1, i2, j1, j2 := (j+1)%3, (j+2)%3, (i+1)%3, (i+2)%3
			r[i*3+j] = (m[i1*3+j1]*m[i2*3+j2] - m[i1*3+j2]*m[i2*3+j1]) / det
		}
	}
	return r, true
}
