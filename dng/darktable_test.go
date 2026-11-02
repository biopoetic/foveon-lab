package dng

// TestWriteDarktableSamples is not a correctness test: it writes the
// synthetic files used for the independent darktable-cli comparison into
// zz-out/task46. It is skipped unless TASK46_DARKTABLE_DIR names that folder,
// so an ordinary `go test` writes nothing.

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDarktableSamples(t *testing.T) {
	dir := os.Getenv("TASK46_DARKTABLE_DIR")
	if dir == "" {
		t.Skip("set TASK46_DARKTABLE_DIR to write the darktable comparison samples")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const w, h = 96, 48
	cols := [][3]float64{
		{0.18, 0.18, 0.18},
		{0.60, 0.15, 0.10},
		{0.10, 0.45, 0.12},
		{0.12, 0.16, 0.55},
	}
	pat := []byte{0, 1, 1, 2}
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pi := min(len(cols)-1, x*len(cols)/w)
			c := int(pat[(y%2)*2+x%2])
			vals[y*w+x] = uint32(math.Round(cols[pi][c] * 65535))
		}
	}
	d := sdir{tags: baseRawTags(w, h, 16, 1, compressionNone, photometricCFA, raw16(vals))}
	d.tags = append(d.tags, cfaTags(pat)...)
	d.tags = append(d.tags, colourTags()...)
	d.tags = append(d.tags, tgASCII(tagMake, "Foveon Lab"), tgASCII(tagModel, "Synthetic chart"), tgASCII(tagSoftware, "foveon-lab task 46 test"))
	if err := os.WriteFile(filepath.Join(dir, "chart.dng"), buildTIFF(t, []sdir{d}, [][]byte{raw16(vals)}), 0o644); err != nil {
		t.Fatal(err)
	}
	// The same chart packed at 12 bits.
	v12 := make([]uint32, len(vals))
	for i, v := range vals {
		v12[i] = v * 4095 / 65535
	}
	d12 := sdir{tags: baseRawTags(w, h, 12, 1, compressionNone, photometricCFA, packSamples(v12, 12, h, w))}
	d12.tags = append(d12.tags, cfaTags(pat)...)
	d12.tags = append(d12.tags, colourTags()...)
	for i := range d12.tags {
		if d12.tags[i].tag == tagWhiteLevel {
			d12.tags[i] = tgLong(tagWhiteLevel, 4095)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "chart12.dng"), buildTIFF(t, []sdir{d12}, [][]byte{packSamples(v12, 12, h, w)}), 0o644); err != nil {
		t.Fatal(err)
	}
	// Lossless JPEG in one tile.
	lv := make([]int, w*h)
	for i, v := range vals {
		lv[i] = int(v)
	}
	lj := losslessJPEGTest(t, w, h, 1, lv, 0, 1)
	dl := sdir{tags: baseRawTags(w, h, 16, 1, compressionJPEG, photometricCFA, lj)}
	dl.tags = append(dl.tags, cfaTags(pat)...)
	dl.tags = append(dl.tags, colourTags()...)
	for i := range dl.tags {
		switch dl.tags[i].tag {
		case tagStripOffsets:
			dl.tags[i] = tgLong(tagTileOffsets, 0)
		case tagRowsPerStrip:
			dl.tags[i] = tgLong(tagTileLength, h)
		case tagStripByteCounts:
			dl.tags[i] = tgLong(tagTileByteCounts, uint32(len(lj)))
		}
	}
	dl.tags = append(dl.tags, tgLong(tagTileWidth, w))
	if err := os.WriteFile(filepath.Join(dir, "chartljpeg.dng"), buildTIFF(t, []sdir{dl}, [][]byte{lj}), 0o644); err != nil {
		t.Fatal(err)
	}
}
