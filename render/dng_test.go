package render

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"testing"
)

// TestRenderedDNG reads the DNG back: the tags a raw converter needs are
// there, sorted, and the pixels are the render's values, linearised.
func TestRenderedDNG(t *testing.T) {
	img := image.NewRGBA64(image.Rect(0, 0, 37, 70)) // 3 strips, odd width
	for y := 0; y < 70; y++ {
		for x := 0; x < 37; x++ {
			img.Set(x, y, color.RGBA64{uint16(x * 1700), uint16(y * 900), 30000, 65535})
		}
	}
	var buf bytes.Buffer
	if err := WriteRenderedDNG(&buf, img, "SIGMA", "SIGMA SD1 Merrill", "Foveon Lab test"); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	le := binary.LittleEndian
	if string(b[:4]) != "II*\x00" {
		t.Fatalf("bad header %q", b[:4])
	}
	ifd := le.Uint32(b[4:])
	n := int(le.Uint16(b[ifd:]))
	tags := map[uint16][2]uint32{} // tag → count, value/offset
	last := uint16(0)
	for i := 0; i < n; i++ {
		e := b[int(ifd)+2+12*i:]
		tag := le.Uint16(e)
		if tag <= last {
			t.Fatalf("tags not ascending at %d", tag)
		}
		last = tag
		tags[tag] = [2]uint32{le.Uint32(e[4:]), le.Uint32(e[8:])}
	}
	for _, need := range []uint16{256, 257, 258, 262, 273, 279, 50706, 50708, 50721, 50728, 50778, 50940, 50964} {
		if _, ok := tags[need]; !ok {
			t.Errorf("missing tag %d", need)
		}
	}
	if tags[262][1] != 34892 {
		t.Errorf("photometric = %d, want LinearRaw", tags[262][1])
	}
	// Pixels: follow StripOffsets.
	cnt, off := tags[273][0], tags[273][1]
	var data []byte
	for i := uint32(0); i < cnt; i++ {
		so := le.Uint32(b[off+4*i:])
		sc := le.Uint32(b[tags[279][1]+4*i:])
		data = append(data, b[so:so+sc]...)
	}
	if len(data) != 37*70*6 {
		t.Fatalf("pixel bytes = %d, want %d", len(data), 37*70*6)
	}
	for _, p := range [][2]int{{0, 0}, {36, 69}, {12, 33}} {
		x, y := p[0], p[1]
		i := (y*37 + x) * 6
		got := le.Uint16(data[i:])
		want := math.Round(float64(ToLinear(float32(x*1700)/65535)) * 65535)
		if math.Abs(float64(got)-want) > 1 {
			t.Errorf("pixel (%d,%d) R = %d, want %v", x, y, got, want)
		}
	}
}
