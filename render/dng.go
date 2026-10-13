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
		{271, tASCII, 0, dngASCII(make_)},                           // Make
		{272, tASCII, 0, dngASCII(model)},                           // Model
		{273, tLong, uint32(len(strips)), nil},                      // StripOffsets (filled in below)
		{274, tShort, 1, dngShorts(1)},                              // Orientation
		{277, tShort, 1, dngShorts(3)},                              // SamplesPerPixel
		{278, tLong, 1, dngLongs(rowsPerStrip)},                     // RowsPerStrip
		{279, tLong, uint32(len(strips)), dngLongs(stripCounts...)}, // StripByteCounts
		{284, tShort, 1, dngShorts(1)},                              // PlanarConfiguration: chunky
		{305, tASCII, 0, dngASCII(software)},                        // Software
		{50706, tByte, 4, []byte{1, 4, 0, 0}},                       // DNGVersion
		{50707, tByte, 4, []byte{1, 2, 0, 0}},                       // DNGBackwardVersion
		{50708, tASCII, 0, dngASCII("Foveon Lab render")},           // UniqueCameraModel
		{50714, tLong, 3, dngLongs(0, 0, 0)},                        // BlackLevel
		{50717, tLong, 3, dngLongs(65535, 65535, 65535)},            // WhiteLevel
		{50721, tSRational, 9, dngSRationals(xyzD65ToSRGB...)},      // ColorMatrix1
		{50728, tRational, 3, dngRationals(1, 1, 1)},                // AsShotNeutral
		{50730, tSRational, 1, dngSRationals(0)},                    // BaselineExposure
		{50778, tShort, 1, dngShorts(21)},                           // CalibrationIlluminant1: D65
		{50936, tASCII, 0, dngASCII("Foveon Lab (as rendered)")},    // ProfileName
		{50940, tFloat, 4, dngFloats(0, 0, 1, 1)},                   // ProfileToneCurve: linear
		{50941, tLong, 1, dngLongs(3)},                              // ProfileEmbedPolicy: no restrictions
		{50964, tSRational, 9, dngSRationals(sRGBToXYZD50...)},      // ForwardMatrix1
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
