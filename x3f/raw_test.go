package x3f

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
	"testing"
)

// TestCodes12 checks the 12-bit fast table against the bit-level rules it
// stands for: a window either does not resolve (the slow path runs), or it
// consumes and produces exactly what reading the prefix code and then its
// payload bit by bit does.
func TestCodes12(t *testing.T) {
	// A prefix table shaped like a file's: canonical codes of 1..8 bits for
	// categories 0..7 (category i = i magnitude bits).
	var pre prefix
	next := 0
	for s := 1; s <= 8; s++ {
		code := uint8(next << (8 - s))
		size := uint8(s)
		lo := int(code) &^ (1<<(8-s) - 1)
		for j := lo; j < lo+1<<(8-s); j++ {
			pre[j].n, pre[j].bits = size, uint8(s-1)
		}
		next = (next + 1) << 1
	}
	codes := buildCodes12(&pre)
	resolved := 0
	for w := 0; w < 1<<12; w++ {
		e := pre[uint8(w>>4)]
		n := int(e.n) + int(e.bits)
		if e.n == 0 || n > 12 {
			if codes[w].bits != 0 {
				t.Fatalf("window %03x: resolved to %d bits, the code runs past the window", w, codes[w].bits)
			}
			continue
		}
		// Reference: read the payload MSB-first, one bit at a time.
		pos := int(e.n)
		var u int32
		for i := 0; i < int(e.bits); i++ {
			u = u<<1 | int32(w>>(11-pos)&1)
			pos++
		}
		if e.bits > 0 && u>>(e.bits-1) == 0 {
			u -= 1<<e.bits - 1
		}
		if got := codes[w]; int(got.bits) != n || int32(got.diff) != u {
			t.Fatalf("window %03x: table %d bits %d, want %d bits %d", w, got.bits, got.diff, n, u)
		}
		resolved++
	}
	if resolved < 1<<11 {
		t.Fatalf("only %d of 4096 windows resolved", resolved)
	}
}

// TestDecodeTRUESynthetic decodes a hand-built two-plane-row stream: code
// "0" = difference 0, code "1" + one magnitude bit = ±1.
func TestDecodeTRUESynthetic(t *testing.T) {
	w, h := 4, 2
	var hdr []byte
	put16 := func(v uint16) { hdr = binary.LittleEndian.AppendUint16(hdr, v) }
	put16(100) // seeds
	put16(200)
	put16(300)
	put16(0)                                  // unknown
	hdr = append(hdr, 1, 0x00, 1, 0x80, 0, 0) // len 0 -> "0", len 1 -> "1", end
	// Every plane: row 0 = +1, 0, -1, +1 ; row 1 = 0, 0, 0, 0.
	// bits: 11 0 10 11 | 0 0 0 0 = 1101 0110 0000 0000
	plane := []byte{0xd6, 0x00, 0x00, 0x00}
	for i := 0; i < 3; i++ {
		hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(plane)))
	}
	data := hdr
	for i := 0; i < 3; i++ {
		data = append(data, plane...)
		data = append(data, make([]byte, 16-len(plane))...)
	}
	raw, err := decodeTRUE(data, w, h, false)
	if err != nil {
		t.Fatal(err)
	}
	for c, seed := range []int{100, 200, 300} {
		// Row 0: even columns seed+1, then +1-1 = seed+0; odd seed+0, then +1.
		want := []int{seed + 1, seed, seed, seed + 1, seed, seed, seed, seed}
		for i, v := range want {
			if got := int(raw.Pix[i*3+c]); got != v {
				t.Errorf("plane %d pixel %d = %d, want %d", c, i, got, v)
			}
		}
	}
}

// rawHashes are SHA-256 digests of DecodeRaw's samples for the files in
// ../testdata, each verified value-for-value against x3fuse-core's
// x3f_extract -unprocessed -no-crop (tools/rawcheck, October 2026).
var rawHashes = map[string]string{
	"DP1/RAW_SIGMA_DP1.X3F":      "8cfbb29b663d0342c30e2fa56f578d223bbae175a050d3c8cd75f03ffa4c8c5a",
	"DP1-Merrill/SDIM0555.X3F":   "bfbe61bb478e41a51bac95f979b2c0c431d0bb39c2c50049ac369b89e1040e57",
	"SD10/RAW_SIGMA_SD10.X3F":    "44a331ab98772b5c41c5670963d73975cbedf962ac752feeaaacaaede86c7b35",
	"SD14/20120818-SDIM4579.X3F": "e0aec47bba0e65c2fac2e1704c60a2ca9a6ba495677d410bd7125a61c035827e",
	"SD9/RAW_SIGMA_SD9_SRGB.X3F": "57b7407cf3b96c78272ef1563b73c58c01031c49e5b34db257eb96601155adf2",
}

// TestSampleRaw decodes the raw data and CAMF of every sample in
// ../testdata (git-ignored; skipped when absent).
func TestSampleRaw(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "testdata", "*", "*.[Xx]3[Ff]"))
	if len(files) == 0 {
		t.Skip("no samples in ../testdata")
	}
	for _, p := range files {
		name := filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p)
		f, err := Open(p)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		c, err := f.CAMF()
		if err != nil {
			t.Errorf("%s: CAMF: %v", name, err)
			continue
		}
		if len(c.Matrices) == 0 {
			t.Errorf("%s: CAMF has no matrices", name)
		}
		if _, ok := f.RawSection(); !ok {
			t.Logf("%s: no supported raw data (Quattro)", name)
			continue
		}
		raw, err := f.DecodeRaw()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		hs := sha256.New()
		buf := make([]byte, 2*len(raw.Pix))
		for i, v := range raw.Pix {
			binary.LittleEndian.PutUint16(buf[2*i:], v)
		}
		hs.Write(buf)
		got := hex.EncodeToString(hs.Sum(nil))
		if want, ok := rawHashes[name]; !ok {
			t.Logf("%s: %dx%d %s (no reference)", name, raw.W, raw.H, got)
		} else if got != want {
			t.Errorf("%s: decode changed: %s, want %s", name, got, want)
		}
		if _, err := CameraRGB(raw, c, NewSpatialGain(c, f.Props, "Sunlight"), "Sunlight"); err != nil {
			t.Errorf("%s: CameraRGB: %v", name, err)
		}
	}
}
