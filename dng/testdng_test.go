package dng

// Synthetic DNGs for the tests: a TIFF writer small enough to read, with a
// CFA or LinearRaw image, the colour tags, and optional Deflate packing.
// Nothing here is used by the app; it exists so the reader can be tested
// against known colours without a third-party raw file.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"sort"
	"testing"
)

type tent struct {
	tag, typ uint16
	count    uint32
	data     []byte
}

func sh(v ...uint16) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], x)
	}
	return b
}

func lo(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func rat(v ...float64) []byte {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[8*i:], uint32(x*1000000+0.5))
		binary.LittleEndian.PutUint32(b[8*i+4:], 1000000)
	}
	return b
}

func srat(v ...float64) []byte {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[8*i:], uint32(int32(x*1000000)))
		binary.LittleEndian.PutUint32(b[8*i+4:], 1000000)
	}
	return b
}

func ascii(s string) []byte { return append([]byte(s), 0) }

// packSamples packs integer samples MSB-first, padding every row to a byte.
func packSamples(vals []uint32, bits, rows, rowSamples int) []byte {
	stride := (rowSamples*bits + 7) / 8
	out := make([]byte, stride*rows)
	for r := 0; r < rows; r++ {
		for c := 0; c < rowSamples; c++ {
			v := vals[r*rowSamples+c]
			for k := 0; k < bits; k++ {
				if v&(1<<uint(bits-1-k)) != 0 {
					pos := r*stride*8 + c*bits + k
					out[pos/8] |= 1 << uint(7-pos%8)
				}
			}
		}
	}
	return out
}

type testDNG struct {
	w, h, bits int
	samples    int
	vals       []uint32 // row-major, samples interleaved
	cfa        []byte
	cfaW, cfaH int
	comp       int
	tiled      bool
	extra      []tent
	noMatrix   bool
	noForward  bool
	noNeutral  bool
	whiteXY    [2]float64
}

func (d *testDNG) tag(tag, typ uint16, data []byte, count int) {
	if count == 0 {
		count = len(data)
		if typ == 3 {
			count /= 2
		}
	}
	d.extra = append(d.extra, tent{tag: tag, typ: typ, count: uint32(count), data: data})
}

// build returns a complete TIFF/DNG file.
func (d *testDNG) build(t *testing.T) []byte {
	t.Helper()
	if d.bits == 0 {
		d.bits = 16
	}
	if d.samples == 0 {
		d.samples = 1
	}
	var raw []byte
	if d.bits == 16 {
		raw = make([]byte, len(d.vals)*2)
		for i, v := range d.vals {
			binary.LittleEndian.PutUint16(raw[2*i:], uint16(v))
		}
	} else if d.bits == 8 {
		raw = make([]byte, len(d.vals))
		for i, v := range d.vals {
			raw[i] = byte(v)
		}
	} else {
		raw = packSamples(d.vals, d.bits, d.h, d.w*d.samples)
	}
	if d.comp == 0 {
		d.comp = compressionNone
	}
	if d.comp == compressionDeflate {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		zw.Write(raw)
		zw.Close()
		raw = buf.Bytes()
	}
	ents := []tent{
		{254, 4, 1, lo(0)},
		{256, 4, 1, lo(uint32(d.w))},
		{257, 4, 1, lo(uint32(d.h))},
		{258, 3, uint32(d.samples), sh(repeat16(uint16(d.bits), d.samples)...)},
		{259, 3, 1, sh(uint16(d.comp))},
		{262, 3, 1, sh(photometricCFA)},
		{273, 4, 1, lo(0)}, // strip offset, filled below
		{277, 3, 1, sh(uint16(d.samples))},
		{278, 4, 1, lo(uint32(d.h))},
		{279, 4, 1, lo(uint32(len(raw)))},
		{284, 3, 1, sh(1)},
		{50706, 1, 4, []byte{1, 4, 0, 0}},
		{50714, 4, 1, lo(0)},
		{50717, 4, 1, lo(uint32((1 << d.bits) - 1))},
	}
	if d.samples == 1 {
		pw, ph := d.cfaW, d.cfaH
		if pw == 0 {
			pw, ph = 2, 2
			d.cfa = []byte{0, 1, 1, 2}
		}
		ents[5].data = sh(photometricCFA)
		ents = append(ents,
			tent{33421, 3, 2, sh(uint16(pw), uint16(ph))},
			tent{33422, 1, uint32(len(d.cfa)), append([]byte(nil), d.cfa...)},
			tent{50710, 1, 3, []byte{0, 1, 2}},
			tent{50711, 3, 1, sh(1)},
		)
	} else {
		ents[5].data = sh(photometricLinearRaw)
	}
	if d.tiled {
		for i := range ents {
			switch ents[i].tag {
			case 273:
				ents[i].tag = 324
			case 278:
				ents[i].tag = 323
			case 279:
				ents[i].tag = 325
			}
		}
		ents = append(ents, tent{322, 4, 1, lo(uint32(d.w))})
	}
	if !d.noMatrix {
		// A camera that is linear sRGB: ForwardMatrix = sRGB -> XYZ D50.
		ents = append(ents,
			tent{50721, 10, 9, srat(
				3.2404542, -1.5371385, -0.4985314,
				-0.9692660, 1.8760108, 0.0415560,
				0.0556434, -0.2040259, 1.0572252)},
			tent{50778, 3, 1, sh(21)},
		)
		if d.noNeutral {
			ents = append(ents, tent{50729, 5, 2, rat(d.whiteXY[0], d.whiteXY[1])})
		} else {
			ents = append(ents, tent{50728, 5, 3, rat(1, 1, 1)})
		}
		if !d.noForward {
			ents = append(ents, tent{50964, 10, 9, srat(
				0.4360747, 0.3850649, 0.1430804,
				0.2225045, 0.7168786, 0.0606169,
				0.0139322, 0.0971045, 0.7141733)})
		}
	}
	ents = append(ents, d.extra...)
	sort.SliceStable(ents, func(i, j int) bool {
		if ents[i].tag == ents[j].tag {
			return i < j
		}
		return ents[i].tag < ents[j].tag
	})
	// Deduplicate tags: keep the last (the specific test value wins).
	var uniq []tent
	for _, e := range ents {
		if len(uniq) > 0 && uniq[len(uniq)-1].tag == e.tag {
			uniq[len(uniq)-1] = e
		} else {
			uniq = append(uniq, e)
		}
	}
	ents = uniq

	ifdSize := 2 + 12*len(ents) + 4
	off := 8 + ifdSize
	var extra bytes.Buffer
	valOff := make([]uint32, len(ents))
	for i, e := range ents {
		if e.tag == 273 {
			continue
		}
		if len(e.data) > 4 {
			if (off+extra.Len())%2 == 1 {
				extra.WriteByte(0)
			}
			valOff[i] = uint32(off + extra.Len())
			extra.Write(e.data)
		}
	}
	if (off+extra.Len())%2 == 1 {
		extra.WriteByte(0)
	}
	dataStart := uint32(off + extra.Len())
	var out bytes.Buffer
	out.Write([]byte{'I', 'I', 42, 0})
	binary.Write(&out, binary.LittleEndian, uint32(8))
	binary.Write(&out, binary.LittleEndian, uint16(len(ents)))
	for i, e := range ents {
		binary.Write(&out, binary.LittleEndian, e.tag)
		binary.Write(&out, binary.LittleEndian, e.typ)
		binary.Write(&out, binary.LittleEndian, e.count)
		switch {
		case e.tag == 273 || e.tag == 324:
			binary.Write(&out, binary.LittleEndian, dataStart)
		case len(e.data) > 4:
			binary.Write(&out, binary.LittleEndian, valOff[i])
		default:
			var v [4]byte
			copy(v[:], e.data)
			out.Write(v[:])
		}
	}
	binary.Write(&out, binary.LittleEndian, uint32(0))
	out.Write(extra.Bytes())
	out.Write(raw)
	return out.Bytes()
}

func repeat16(v uint16, n int) []uint16 {
	o := make([]uint16, n)
	for i := range o {
		o[i] = v
	}
	return o
}
