package dng

// A small general TIFF/DNG writer for the tests. Unlike testdng_test.go,
// which writes one flat IFD, this one can put a preview in IFD0 and the raw
// in a SubIFD, write several strips or tiles, and carry the DNG tags this
// task added (MaskedAreas, ActiveArea, black deltas, float samples, opcode
// lists). It exists only in _test files.

import (
	"bytes"
	"encoding/binary"
	"math"
	"sort"
	"testing"
)

type st struct {
	tag, typ uint16
	count    uint32
	data     []byte
}

type sdir struct {
	tags []st
	next int
	sub  []int
}

func tgShort(tag uint16, v ...uint16) st {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], x)
	}
	return st{tag, 3, uint32(len(v)), b}
}

func tgLong(tag uint16, v ...uint32) st {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return st{tag, 4, uint32(len(v)), b}
}

func tgShortLong(tag uint16, v ...uint32) st {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return st{tag, 4, uint32(len(v)), b}
}

func tgByte(tag uint16, v ...byte) st { return st{tag, 1, uint32(len(v)), append([]byte(nil), v...)} }

func tgUndef(tag uint16, b []byte) st { return st{tag, 7, uint32(len(b)), append([]byte(nil), b...)} }

func tgASCII(tag uint16, s string) st { return st{tag, 2, uint32(len(s) + 1), append([]byte(s), 0)} }

func tgRational(tag uint16, v ...float64) st {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[8*i:], uint32(x*1000000+0.5))
		binary.LittleEndian.PutUint32(b[8*i+4:], 1000000)
	}
	return st{tag, 5, uint32(len(v)), b}
}

func tgSRational(tag uint16, v ...float64) st {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[8*i:], uint32(int32(x*1000000)))
		binary.LittleEndian.PutUint32(b[8*i+4:], 1000000)
	}
	return st{tag, 10, uint32(len(v)), b}
}

func tgDouble(tag uint16, v ...float64) st {
	b := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint64(b[8*i:], mathFloat64bits(x))
	}
	return st{tag, 12, uint32(len(v)), b}
}

// buildTIFF lays out the directories, then their out-of-line values, then the
// blobs. StripOffsets/TileOffsets entries are filled with the blob offsets;
// SubIFDs entries with the directory offsets.
func buildTIFF(t testing.TB, dirs []sdir, blobs [][]byte) []byte {
	t.Helper()
	// Copy the dirs so a test's slices are not mutated.
	d := make([]sdir, len(dirs))
	for i := range dirs {
		d[i] = sdir{next: dirs[i].next, sub: append([]int(nil), dirs[i].sub...)}
		for _, tg := range dirs[i].tags {
			tg.data = append([]byte(nil), tg.data...)
			d[i].tags = append(d[i].tags, tg)
		}
		sort.SliceStable(d[i].tags, func(a, b int) bool { return d[i].tags[a].tag < d[i].tags[b].tag })
	}
	offs := make([]int, len(d))
	pos := 8
	for i := range d {
		offs[i] = pos
		pos += 2 + 12*len(d[i].tags) + 4
	}
	// SubIFD offsets are known now; make sure the tag data exists.
	for i := range d {
		for j := range d[i].tags {
			if d[i].tags[j].tag == tagSubIFDs {
				d[i].tags[j].data = append([]byte(nil), tgLong(0, make([]uint32, len(d[i].sub))...).data...)
				for k, s := range d[i].sub {
					binary.LittleEndian.PutUint32(d[i].tags[j].data[4*k:], uint32(offs[s]))
				}
			}
		}
	}
	// Allocate out-of-line values.
	type patch struct{ dir, tag, off int }
	var patches []patch
	extra := pos
	if extra%2 == 1 {
		extra++
	}
	extraStart := extra
	valOff := make([][]int, len(d))
	for i := range d {
		valOff[i] = make([]int, len(d[i].tags))
		for j, tg := range d[i].tags {
			if len(tg.data) > 4 {
				if extra%2 == 1 {
					extra++
				}
				valOff[i][j] = extra
				extra += len(tg.data)
			}
		}
	}
	blobStart := extra
	if blobStart%2 == 1 {
		blobStart++
	}
	blobOff := make([]int, len(blobs))
	p := blobStart
	for i, b := range blobs {
		blobOff[i] = p
		p += len(b)
		if p%2 == 1 {
			p++
		}
	}
	// Fill offset arrays now that the blob offsets are known. Each offset tag
	// (one per directory) consumes the next block of blobs.
	nextBlob := 0
	for i := range d {
		for j := range d[i].tags {
			tg := &d[i].tags[j]
			if tg.tag != tagStripOffsets && tg.tag != tagTileOffsets {
				continue
			}
			if nextBlob+int(tg.count) > len(blobOff) {
				t.Fatalf("tag %d asks for %d blobs, have %d left", tg.tag, tg.count, len(blobOff)-nextBlob)
			}
			if tg.count == 1 {
				binary.LittleEndian.PutUint32(tg.data, uint32(blobOff[nextBlob]))
			} else {
				for k := 0; k < int(tg.count); k++ {
					binary.LittleEndian.PutUint32(tg.data[4*k:], uint32(blobOff[nextBlob+k]))
				}
			}
			nextBlob += int(tg.count)
			patches = append(patches, patch{i, j, valOff[i][j]})
		}
	}
	// Byte counts are real lengths, consumed in the same order.
	nextBlob = 0
	for i := range d {
		for j := range d[i].tags {
			tg := &d[i].tags[j]
			if tg.tag != tagStripByteCounts && tg.tag != tagTileByteCounts {
				continue
			}
			if nextBlob+int(tg.count) > len(blobs) {
				t.Fatalf("byte-count tag %d asks for %d blobs", tg.tag, tg.count)
			}
			for k := 0; k < int(tg.count); k++ {
				binary.LittleEndian.PutUint32(tg.data[4*k:], uint32(len(blobs[nextBlob+k])))
			}
			nextBlob += int(tg.count)
		}
	}
	out := make([]byte, p)
	copy(out, []byte{'I', 'I', 42, 0})
	binary.LittleEndian.PutUint32(out[4:], uint32(offs[0]))
	for i := range d {
		base := offs[i]
		binary.LittleEndian.PutUint16(out[base:], uint16(len(d[i].tags)))
		for j, tg := range d[i].tags {
			e := out[base+2+12*j:]
			binary.LittleEndian.PutUint16(e[0:], tg.tag)
			binary.LittleEndian.PutUint16(e[2:], tg.typ)
			binary.LittleEndian.PutUint32(e[4:], tg.count)
			if len(tg.data) <= 4 {
				copy(e[8:12], tg.data)
			} else {
				binary.LittleEndian.PutUint32(e[8:], uint32(valOff[i][j]))
			}
		}
		next := d[i].next
		if next > 0 {
			binary.LittleEndian.PutUint32(out[base+2+12*len(d[i].tags):], uint32(offs[next]))
		}
	}
	for i := range d {
		for j, tg := range d[i].tags {
			if len(tg.data) > 4 {
				copy(out[valOff[i][j]:], tg.data)
			}
		}
	}
	for i, b := range blobs {
		copy(out[blobOff[i]:], b)
	}
	_ = extraStart
	return out
}

func mathFloat64bits(f float64) uint64 { return math.Float64bits(f) }

// patchTagValue overwrites the 4-byte value/offset field of the first entry
// with the given tag in IFD0. It is used by the bad-input tests.
func patchTagValue(b []byte, tag uint16, val uint32) bool {
	if len(b) < 10 {
		return false
	}
	n := int(binary.LittleEndian.Uint16(b[8:]))
	for i := 0; i < n; i++ {
		off := 10 + 12*i
		if off+12 > len(b) {
			return false
		}
		if binary.LittleEndian.Uint16(b[off:]) == tag {
			binary.LittleEndian.PutUint32(b[off+8:], val)
			return true
		}
	}
	return false
}

// dngTags is the common colour profile of the synthetic files: a camera
// whose ColorMatrix is the inverse sRGB matrix and whose ForwardMatrix is
// sRGB -> XYZ D50, so a set of raw camera values equal to a linear sRGB
// colour comes back as that colour.
func colourTags() []st {
	return []st{
		tgSRational(tagColorMatrix1,
			3.2404542, -1.5371385, -0.4985314,
			-0.9692660, 1.8760108, 0.0415560,
			0.0556434, -0.2040259, 1.0572252),
		tgShort(tagCalibIllum1, 21),
		tgRational(tagAsShotNeutral, 1, 1, 1),
		tgSRational(tagForwardMatrix1,
			0.4360747, 0.3850649, 0.1430804,
			0.2225045, 0.7168786, 0.0606169,
			0.0139322, 0.0971045, 0.7141733),
	}
}

// baseRawTags builds the common IFD of a one-strip raw image.
func baseRawTags(w, h, bits, samples, comp, photometric int, raw []byte) []st {
	return []st{
		tgLong(tagNewSubFileType, 0),
		tgLong(tagWidth, uint32(w)),
		tgLong(tagHeight, uint32(h)),
		tgShort(tagBitsPerSample, repeat16(uint16(bits), samples)...),
		tgShort(tagCompression, uint16(comp)),
		tgShort(tagPhotometric, uint16(photometric)),
		tgLong(tagStripOffsets, uint32(0)), // patched to the blob offset
		tgShort(tagSamplesPerPixel, uint16(samples)),
		tgLong(tagRowsPerStrip, uint32(h)),
		tgLong(tagStripByteCounts, uint32(len(raw))),
		tgShort(tagPlanarConfig, 1),
		tgByte(tagDNGVersion, 1, 6, 0, 0),
		tgLong(tagBlackLevel, 0),
		tgLong(tagWhiteLevel, uint32((1<<bits)-1)),
	}
}

func cfaTags(pat []byte) []st {
	return []st{
		tgShort(tagCFARepeatPattern, 2, 2),
		tgByte(tagCFAPattern, pat...),
		tgByte(tagCFAPlaneColor, 0, 1, 2),
		tgShort(tagCFALayout, 1),
	}
}

func monoCFATags() []st {
	return []st{
		tgShort(tagCFARepeatPattern, 1, 1),
		tgByte(tagCFAPattern, 0),
		tgByte(tagCFAPlaneColor, 0, 1, 2),
		tgShort(tagCFALayout, 1),
	}
}

func raw16(vals []uint32) []byte {
	b := make([]byte, 2*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

func raw8(vals []uint32) []byte {
	b := make([]byte, len(vals))
	for i, v := range vals {
		b[i] = byte(v)
	}
	return b
}

func developFile(t *testing.T, b []byte, maxW int) *Image {
	t.Helper()
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	img, err := f.Develop(maxW)
	if err != nil {
		t.Fatalf("Develop: %v", err)
	}
	return img
}

func wantPatch(t *testing.T, img *Image, x, y int, want [3]float64, tol float64) {
	t.Helper()
	i := (y*img.W + x) * 3
	for c := 0; c < 3; c++ {
		if math.Abs(float64(img.Pix[i+c])-want[c]) > tol {
			t.Errorf("pixel (%d,%d) channel %d: got %.5f want %.5f", x, y, c, img.Pix[i+c], want[c])
		}
	}
}
