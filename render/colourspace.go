package render

// Export colour spaces, as Sigma Photo Pro offers them: sRGB, Apple RGB,
// Adobe RGB (1998), ColorMatch RGB and ProPhoto RGB. SPP renders in sRGB
// and re-encodes into the chosen space (docs/HOW_SPP_WORKS.md section 8:
// its ProPhoto export holds no colour outside sRGB, even on a photo 94%
// pinned at the sRGB gamut edge), so an export here is the same: the sRGB
// render, converted through XYZ (D50) into the space, with its ICC profile
// embedded. The primaries, white points and tone curves are the ones in
// the profiles SPP embeds; the profiles themselves are generated here.

import (
	"bytes"
	"encoding/binary"
	"image"
	"math"
)

// ColourSpace is an RGB export space: colourant XYZ (D50-adapted, the ICC
// connection space) and its tone curve.
type ColourSpace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// m: linear RGB -> XYZ (D50); columns are the r, g, b colourants.
	m [9]float64
	// white: the profile's media white point (wtpt).
	white [3]float64
	// gamma: the tone curve's exponent; 0 = the sRGB curve.
	gamma float64
}

var (
	d65White = [3]float64{0.950455, 1.0, 1.08905}
	d50White = [3]float64{0.964203, 1.0, 0.82489}
)

// col builds a colourant matrix from the r, g, b XYZ triples.
func col(r, g, b [3]float64) [9]float64 {
	return [9]float64{r[0], g[0], b[0], r[1], g[1], b[1], r[2], g[2], b[2]}
}

// ColourSpaces are the export spaces, in SPP's order.
var ColourSpaces = []ColourSpace{
	{ID: "srgb", Name: "sRGB IEC61966-2.1", white: d65White,
		m: col([3]float64{0.436066, 0.222488, 0.013916}, [3]float64{0.385147, 0.716873, 0.097076}, [3]float64{0.143066, 0.060608, 0.714096})},
	{ID: "apple", Name: "Apple RGB compatible", white: d65White, gamma: 461.0 / 256,
		m: col([3]float64{0.47554, 0.255157, 0.018448}, [3]float64{0.339722, 0.672592, 0.113327}, [3]float64{0.148956, 0.07225, 0.693115})},
	{ID: "adobe", Name: "Adobe RGB (1998) compatible", white: d65White, gamma: 563.0 / 256,
		m: col([3]float64{0.609741, 0.311111, 0.01947}, [3]float64{0.205276, 0.625671, 0.060867}, [3]float64{0.149185, 0.063217, 0.744568})},
	{ID: "colormatch", Name: "ColorMatch RGB compatible", white: [3]float64{0.964294, 1.0, 0.825104}, gamma: 461.0 / 256,
		m: col([3]float64{0.509445, 0.274948, 0.024261}, [3]float64{0.320847, 0.658096, 0.108749}, [3]float64{0.133911, 0.066971, 0.691895})},
	{ID: "prophoto", Name: "ProPhoto RGB compatible", white: d50White, gamma: 461.0 / 256,
		m: col([3]float64{0.797668, 0.28804, 0.0}, [3]float64{0.135193, 0.711884, 0.0}, [3]float64{0.031342, 0.000092, 0.824905})},
}

// ColourSpaceByID returns the export space with that id ("" = sRGB).
func ColourSpaceByID(id string) (ColourSpace, bool) {
	if id == "" {
		id = "srgb"
	}
	for _, c := range ColourSpaces {
		if c.ID == id {
			return c, true
		}
	}
	return ColourSpace{}, false
}

// encode is the space's tone curve: linear [0,1] -> encoded.
func (c ColourSpace) encode(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	if c.gamma == 0 {
		return linearToSrgb(v)
	}
	return math.Pow(v, 1/c.gamma)
}

// ConvertFromSRGB re-encodes an sRGB-encoded 16-bit image into space c, in
// place. Every sRGB colour fits inside the wider spaces, so nothing clips.
func ConvertFromSRGB(img *image.RGBA64, c ColourSpace) {
	if c.ID == "srgb" {
		return
	}
	s, _ := ColourSpaceByID("srgb")
	m := mul33f(invert33f(c.m), s.m) // linear sRGB -> linear target
	var dec [65536]float64
	for i := range dec {
		dec[i] = srgbToLinear(float64(i) / 65535)
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := img.Pix[y*img.Stride:]
			for x := 0; x < w; x++ {
				p := row[x*8:]
				r := dec[int(p[0])<<8|int(p[1])]
				g := dec[int(p[2])<<8|int(p[3])]
				b := dec[int(p[4])<<8|int(p[5])]
				for k := 0; k < 3; k++ {
					v := uint16(math.Round(c.encode(m[k*3]*r+m[k*3+1]*g+m[k*3+2]*b) * 65535))
					p[k*2], p[k*2+1] = byte(v>>8), byte(v)
				}
			}
		}
	})
}

func mul33f(a, b [9]float64) [9]float64 {
	var o [9]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			for k := 0; k < 3; k++ {
				o[r*3+c] += a[r*3+k] * b[k*3+c]
			}
		}
	}
	return o
}

func invert33f(m [9]float64) [9]float64 {
	d := m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
	return [9]float64{
		(m[4]*m[8] - m[5]*m[7]) / d, (m[2]*m[7] - m[1]*m[8]) / d, (m[1]*m[5] - m[2]*m[4]) / d,
		(m[5]*m[6] - m[3]*m[8]) / d, (m[0]*m[8] - m[2]*m[6]) / d, (m[2]*m[3] - m[0]*m[5]) / d,
		(m[3]*m[7] - m[4]*m[6]) / d, (m[1]*m[6] - m[0]*m[7]) / d, (m[0]*m[4] - m[1]*m[3]) / d,
	}
}

// ICC is the space's ICC v2.1 display profile: description, copyright,
// white point, the three colourants and their tone curves.
func (c ColourSpace) ICC() []byte {
	be := binary.BigEndian
	s15 := func(v float64) []byte {
		b := make([]byte, 4)
		be.PutUint32(b, uint32(int32(math.Round(v*65536))))
		return b
	}
	xyz := func(v [3]float64) []byte {
		b := []byte("XYZ \x00\x00\x00\x00")
		for _, x := range v {
			b = append(b, s15(x)...)
		}
		return b
	}
	desc := func(s string) []byte {
		var b bytes.Buffer
		b.WriteString("desc\x00\x00\x00\x00")
		binary.Write(&b, be, uint32(len(s)+1))
		b.WriteString(s)
		b.WriteByte(0)
		b.Write(make([]byte, 4+4+2+1+67)) // no Unicode or ScriptCode text
		return b.Bytes()
	}
	text := func(s string) []byte { return append([]byte("text\x00\x00\x00\x00"+s), 0) }
	var trc []byte
	if c.gamma == 0 {
		var b bytes.Buffer
		b.WriteString("curv\x00\x00\x00\x00")
		binary.Write(&b, be, uint32(1024))
		for i := 0; i < 1024; i++ {
			binary.Write(&b, be, uint16(math.Round(srgbToLinear(float64(i)/1023)*65535)))
		}
		trc = b.Bytes()
	} else {
		trc = []byte("curv\x00\x00\x00\x00\x00\x00\x00\x01")
		trc = append(trc, byte(int(math.Round(c.gamma*256))>>8), byte(int(math.Round(c.gamma*256))), 0, 0)
	}
	col := func(k int) [3]float64 { return [3]float64{c.m[k], c.m[3+k], c.m[6+k]} }
	type tag struct {
		sig  string
		data []byte
	}
	tags := []tag{
		{"desc", desc(c.Name)},
		{"cprt", text("No copyright, use freely (generated by Foveon Lab)")},
		{"wtpt", xyz(c.white)},
		{"rXYZ", xyz(col(0))},
		{"gXYZ", xyz(col(1))},
		{"bXYZ", xyz(col(2))},
		{"rTRC", trc},
	}
	// Tag table, with gTRC and bTRC sharing rTRC's data.
	n := len(tags) + 2
	off := 128 + 4 + 12*n
	var table, data bytes.Buffer
	binary.Write(&table, be, uint32(n))
	var trcOff, trcLen int
	for _, t := range tags {
		for off%4 != 0 {
			data.WriteByte(0)
			off++
		}
		table.WriteString(t.sig)
		binary.Write(&table, be, uint32(off))
		binary.Write(&table, be, uint32(len(t.data)))
		if t.sig == "rTRC" {
			trcOff, trcLen = off, len(t.data)
		}
		data.Write(t.data)
		off += len(t.data)
	}
	for _, sig := range []string{"gTRC", "bTRC"} {
		table.WriteString(sig)
		binary.Write(&table, be, uint32(trcOff))
		binary.Write(&table, be, uint32(trcLen))
	}
	size := 128 + table.Len() + data.Len()
	h := make([]byte, 128)
	be.PutUint32(h[0:], uint32(size))
	be.PutUint32(h[8:], 0x02100000) // version 2.1
	copy(h[12:], "mntrRGB XYZ ")
	for i, v := range []uint16{2026, 1, 1, 0, 0, 0} {
		be.PutUint16(h[24+2*i:], v)
	}
	copy(h[36:], "acsp")
	copy(h[68:], append(append(s15(0.9642), s15(1.0)...), s15(0.8249)...)) // PCS illuminant D50
	return append(append(h, table.Bytes()...), data.Bytes()...)
}

// JPEGWithICC inserts profile into a JPEG stream as an APP2 ICC_PROFILE
// segment right after its SOI marker (one segment: the profiles here are
// far below the 64 KB limit).
func JPEGWithICC(jpg, profile []byte) []byte {
	if len(jpg) < 2 || jpg[0] != 0xFF || jpg[1] != 0xD8 || len(profile) > 65519 {
		return jpg
	}
	seg := []byte{0xFF, 0xE2, 0, 0}
	seg = append(seg, "ICC_PROFILE\x00"...)
	seg = append(seg, 1, 1) // chunk 1 of 1
	seg = append(seg, profile...)
	binary.BigEndian.PutUint16(seg[2:], uint16(len(seg)-2))
	out := make([]byte, 0, len(jpg)+len(seg))
	out = append(out, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// To8 reduces a 16-bit image to 8 bits per channel (for JPEG).
func To8(img *image.RGBA64) *image.RGBA {
	out := image.NewRGBA(img.Rect)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			src, dst := img.Pix[y*img.Stride:], out.Pix[y*out.Stride:]
			for x := 0; x < w; x++ {
				for k := 0; k < 4; k++ {
					v := uint32(src[x*8+k*2])<<8 | uint32(src[x*8+k*2+1])
					dst[x*4+k] = uint8((v*255 + 32767) / 65535)
				}
			}
		}
	})
	return out
}
