package dng

// FuzzOpen is the bad-input proof. Its committed seeds are one small file of
// each storage shape this reader now accepts; the fuzzer mutates them for a
// few minutes and every iteration must either return an error or develop a
// consistent image, never panic and never reserve gigabytes. The pixel cap is
// lowered for fuzzing so a mutated header cannot make the corpus itself an
// out-of-memory test.

import (
	"bytes"
	"testing"
)

func fuzzSeedCFA(t testing.TB) []byte {
	const w, h = 8, 4
	vals := make([]uint32, w*h)
	for i := range vals {
		vals[i] = uint32(1000 + i*97)
	}
	pat := []byte{0, 1, 1, 2}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags(pat)...)
	d.tags = append(d.tags, colourTags()...)
	return buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
}

func fuzzSeedLinear(t testing.TB) []byte {
	const w, h = 4, 2
	vals := make([]uint32, w*h*3)
	for i := range vals {
		vals[i] = uint32(3000 + i*211)
	}
	d := sdir{tags: baseRawTags(w, h, 16, 3, compressionNone, photometricLinearRaw, raw16(vals))}
	d.tags = append(d.tags, colourTags()...)
	return buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
}

func fuzzSeedFloat(t testing.TB) []byte {
	const w, h = 3, 2
	raw := make([]byte, w*h*3*4)
	for i := 0; i < w*h*3; i++ {
		raw[4*i] = byte(i * 13)
		raw[4*i+1] = byte(i * 7)
	}
	d := sdir{tags: baseRawTags(w, h, 32, 3, compressionNone, photometricLinearRaw, raw)}
	d.tags = append(d.tags, tgShort(tagSampleFormat, 3))
	for i := range d.tags {
		if d.tags[i].tag == tagWhiteLevel {
			d.tags[i] = tgRational(tagWhiteLevel, 1)
		}
	}
	d.tags = append(d.tags, colourTags()...)
	return buildTIFF(t, []sdir{d}, [][]byte{raw})
}

func fuzzSeedOpcode(t testing.TB) []byte {
	const w, h = 6, 4
	vals := make([]uint32, w*h)
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags([]byte{0, 1, 1, 2})...)
	g := gainMapData(0, 0, h, w, 2, 2, 2, 2, 1, 1, 0, 0, []float32{1, 1, 1, 1})
	d.tags = append(d.tags, tgUndef(tagOpcodeList1, opcodeList(opcodeEntry(opGainMap, 0, g))))
	d.tags = append(d.tags, colourTags()...)
	return buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)})
}

func FuzzOpen(f *testing.F) {
	for _, s := range [][]byte{fuzzSeedCFA(f), fuzzSeedLinear(f), fuzzSeedFloat(f), fuzzSeedOpcode(f)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		old := maxPixels
		maxPixels = 1_000_000
		defer func() { maxPixels = old }()
		f, err := NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		if err := f.Meta.Readable(); err != nil {
			return
		}
		img, err := f.Develop(0)
		if err != nil {
			return
		}
		if img.W <= 0 || img.H <= 0 || len(img.Pix) != img.W*img.H*3 {
			t.Fatalf("developed image is inconsistent: %dx%d, %d samples", img.W, img.H, len(img.Pix))
		}
	})
}
