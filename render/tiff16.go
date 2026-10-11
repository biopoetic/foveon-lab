package render

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"io"
)

// WriteTIFF16 writes img as a baseline 16-bit-per-channel RGB TIFF (no alpha
// channel), Deflate-compressed in strips — the format Photoshop, Lightroom,
// Affinity and GIMP open as plain 16-bit RGB.
func WriteTIFF16(w io.Writer, img *image.RGBA64) error {
	const rowsPerStrip = 32
	width, height := img.Rect.Dx(), img.Rect.Dy()
	le := binary.LittleEndian

	// Compress the pixel data strip by strip (little-endian samples).
	var strips [][]byte
	row := make([]byte, width*6)
	for y0 := 0; y0 < height; y0 += rowsPerStrip {
		var buf bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
		for y := y0; y < min(height, y0+rowsPerStrip); y++ {
			src := img.Pix[y*img.Stride:]
			for x := 0; x < width; x++ {
				for c := 0; c < 3; c++ {
					// RGBA64 stores big-endian; TIFF here is little-endian.
					row[x*6+c*2] = src[x*8+c*2+1]
					row[x*6+c*2+1] = src[x*8+c*2]
				}
			}
			if _, err := zw.Write(row); err != nil {
				return err
			}
		}
		if err := zw.Close(); err != nil {
			return err
		}
		strips = append(strips, buf.Bytes())
	}

	type entry struct {
		tag, typ uint16
		count    uint32
		value    []byte // stored inline when <= 4 bytes, else at an offset
	}
	u16 := func(vs ...uint16) []byte {
		b := make([]byte, 2*len(vs))
		for i, v := range vs {
			le.PutUint16(b[i*2:], v)
		}
		return b
	}
	u32 := func(vs ...uint32) []byte {
		b := make([]byte, 4*len(vs))
		for i, v := range vs {
			le.PutUint32(b[i*4:], v)
		}
		return b
	}
	const (
		tShort = 3
		tLong  = 4
		tASCII = 2
	)
	n := uint32(len(strips))
	offsets := make([]uint32, n)
	counts := make([]uint32, n)
	software := []byte("Foveon Lab\x00")
	entries := []entry{
		{256, tLong, 1, u32(uint32(width))},
		{257, tLong, 1, u32(uint32(height))},
		{258, tShort, 3, u16(16, 16, 16)},
		{259, tShort, 1, u16(8)}, // Deflate
		{262, tShort, 1, u16(2)}, // RGB
		{273, tLong, n, nil},     // StripOffsets, filled below
		{277, tShort, 1, u16(3)},
		{278, tLong, 1, u32(rowsPerStrip)},
		{279, tLong, n, nil}, // StripByteCounts, filled below
		{284, tShort, 1, u16(1)},
		{305, tASCII, uint32(len(software)), software},
	}

	// Layout: header(8) | IFD | out-of-line values | strips.
	ifdSize := 2 + 12*len(entries) + 4
	extraOff := 8 + ifdSize
	extraSize := 0
	sizeOf := func(e entry) int {
		if e.tag == 273 || e.tag == 279 {
			return int(n) * 4
		}
		return len(e.value)
	}
	for _, e := range entries {
		if s := sizeOf(e); s > 4 {
			extraSize += s + s%2
		}
	}
	dataOff := uint32(extraOff + extraSize)
	for i, s := range strips {
		offsets[i] = dataOff
		counts[i] = uint32(len(s))
		dataOff += uint32(len(s))
	}
	for i := range entries {
		switch entries[i].tag {
		case 273:
			entries[i].value = u32(offsets...)
		case 279:
			entries[i].value = u32(counts...)
		}
	}

	var out bytes.Buffer
	out.WriteString("II*\x00")
	out.Write(u32(8))
	out.Write(u16(uint16(len(entries))))
	var extra bytes.Buffer
	for _, e := range entries {
		out.Write(u16(e.tag, e.typ))
		out.Write(u32(e.count))
		if len(e.value) <= 4 {
			v := make([]byte, 4)
			copy(v, e.value)
			out.Write(v)
		} else {
			out.Write(u32(uint32(extraOff + extra.Len())))
			extra.Write(e.value)
			if len(e.value)%2 == 1 {
				extra.WriteByte(0)
			}
		}
	}
	out.Write(u32(0)) // no next IFD
	out.Write(extra.Bytes())
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
