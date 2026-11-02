package dng

// A minimal lossless-JPEG writer for the decoder test. It uses one canonical
// Huffman table (category 0-16, all five bits) and predictor 1; the point is
// to exercise the marker parser, the Huffman reader, the predictor and the
// point transform on known samples, not to compress well.

import (
	"math"
	"testing"
)

type jenc struct {
	b   []byte
	bit int
}

func (e *jenc) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		if e.bit%8 == 0 {
			e.b = append(e.b, 0)
		}
		if v&(1<<uint(i)) != 0 {
			e.b[len(e.b)-1] |= 1 << uint(7-e.bit%8)
		}
		e.bit++
	}
}

func (e *jenc) pad() {
	for e.bit%8 != 0 {
		e.put(1, 1)
	}
}

func losslessJPEGTest(t *testing.T, w, h, comp int, vals []int, pt int) []byte {
	t.Helper()
	e := &jenc{}
	e.b = append(e.b, 0xFF, 0xD8) // SOI
	// DHT: class 0, table 0, one code of length 5 for each of 0..16.
	dht := []byte{0x00}
	var counts [16]byte
	counts[4] = 17 // five-bit codes
	dht = append(dht, counts[:]...)
	for i := 0; i < 17; i++ {
		dht = append(dht, byte(i))
	}
	e.b = append(e.b, 0xFF, 0xC4, byte((len(dht)+2)>>8), byte(len(dht)+2))
	e.b = append(e.b, dht...)

	sof := []byte{16, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(comp)}
	for c := 0; c < comp; c++ {
		sof = append(sof, byte(c+1), 0x11, 0)
	}
	e.b = append(e.b, 0xFF, 0xC3, byte((len(sof)+2)>>8), byte(len(sof)+2))
	e.b = append(e.b, sof...)

	sos := []byte{byte(comp)}
	for c := 0; c < comp; c++ {
		sos = append(sos, byte(c+1), 0x00)
	}
	sos = append(sos, 1, 0, byte(pt))
	e.b = append(e.b, 0xFF, 0xDA, byte((len(sos)+2)>>8), byte(len(sos)+2))
	e.b = append(e.b, sos...)
	entropyStart := len(e.b)

	// Predictor 1: Ra (the sample to the left); the first sample of a line
	// uses the sample above, and the very first uses 2^(P-Pt-1).
	up := make([][]int, comp)
	cur := make([][]int, comp)
	first := 1 << (16 - pt - 1)
	for c := range up {
		up[c] = make([]int, w)
		cur[c] = make([]int, w)
		for x := 0; x < w; x++ {
			up[c][x] = first
			cur[c][x] = first
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < comp; c++ {
				pred := first
				switch {
				case y == 0 && x == 0:
					pred = first
				case y == 0:
					pred = cur[c][x-1]
				case x == 0:
					pred = up[c][x]
				default:
					pred = cur[c][x-1]
				}
				v := vals[(y*w+x)*comp+c] >> pt
				diff := v - pred
				cat := 0
				if diff != 0 {
					cat = int(math.Ceil(math.Log2(math.Abs(float64(diff)) + 1)))
				}
				e.put(cat, 5)
				if cat > 0 {
					amp := diff
					if diff < 0 {
						amp = diff + (1 << cat) - 1
					}
					e.put(amp, cat)
				}
				cur[c][x] = v
			}
		}
		for c := range up {
			up[c], cur[c] = cur[c], up[c]
		}
	}
	e.pad()
	// JPEG byte stuffing: every FF in the entropy data is followed by 00.
	ent := e.b[entropyStart:]
	var stuffed []byte
	for _, c := range ent {
		stuffed = append(stuffed, c)
		if c == 0xFF {
			stuffed = append(stuffed, 0)
		}
	}
	e.b = append(e.b[:entropyStart], stuffed...)
	e.b = append(e.b, 0xFF, 0xD9) // EOI
	return e.b
}

func TestLosslessJPEGDecode(t *testing.T) {
	const w, h = 19, 7
	for _, comp := range []int{1, 3} {
		vals := make([]int, w*h*comp)
		for i := range vals {
			vals[i] = (i*7919 + 1234) & 0xFFFF
		}
		src := losslessJPEGTest(t, w, h, comp, vals, 0)
		out, err := decodeLosslessJPEG(src, comp)
		if err != nil {
			t.Fatalf("%d components: %v", comp, err)
		}
		if len(out) != len(vals) {
			t.Fatalf("%d components: %d samples, want %d", comp, len(out), len(vals))
		}
		for i, want := range vals {
			if int(out[i]) != want {
				t.Fatalf("%d components sample %d: got %d want %d", comp, i, out[i], want)
			}
		}
	}
}

func TestLosslessJPEGPointTransform(t *testing.T) {
	const w, h = 8, 4
	vals := make([]int, w*h)
	for i := range vals {
		vals[i] = (i * 4096) & 0xFFFF
	}
	// With Pt, the encoder stores values shifted down; the decoder shifts
	// the difference back up, so the result is the stored value << Pt.
	src := losslessJPEGTest(t, w, h, 1, vals, 2)
	out, err := decodeLosslessJPEG(src, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range vals {
		if int(out[i]) != (want>>2)<<2 {
			t.Fatalf("sample %d: got %d want %d", i, out[i], (want>>2)<<2)
		}
	}
}
