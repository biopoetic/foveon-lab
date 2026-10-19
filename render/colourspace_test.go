package render

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"testing"
)

// Each generated profile is a well-formed ICC v2.1 display profile with the
// tags a colour-managed viewer needs.
func TestColourSpaceICC(t *testing.T) {
	be := binary.BigEndian
	for _, c := range ColourSpaces {
		p := c.ICC()
		if int(be.Uint32(p)) != len(p) {
			t.Errorf("%s: header size %d, profile is %d bytes", c.ID, be.Uint32(p), len(p))
		}
		if string(p[36:40]) != "acsp" || string(p[12:24]) != "mntrRGB XYZ " {
			t.Errorf("%s: bad header", c.ID)
		}
		tags := map[string]bool{}
		n := int(be.Uint32(p[128:]))
		for i := 0; i < n; i++ {
			e := 132 + 12*i
			off, size := int(be.Uint32(p[e+4:])), int(be.Uint32(p[e+8:]))
			if off%4 != 0 || off+size > len(p) {
				t.Errorf("%s: tag %s at %d+%d", c.ID, p[e:e+4], off, size)
			}
			tags[string(p[e:e+4])] = true
		}
		for _, want := range []string{"desc", "cprt", "wtpt", "rXYZ", "gXYZ", "bXYZ", "rTRC", "gTRC", "bTRC"} {
			if !tags[want] {
				t.Errorf("%s: no %s tag", c.ID, want)
			}
		}
	}
}

// White and black stay white and black in every space, and a mid grey stays
// neutral (all channels equal) - the spaces share the sRGB/D50 white.
func TestConvertFromSRGBNeutrals(t *testing.T) {
	for _, c := range ColourSpaces {
		img := image.NewRGBA64(image.Rect(0, 0, 3, 1))
		for x, v := range []uint16{65535, 0, 30000} {
			for k := 0; k < 3; k++ {
				img.Pix[x*8+2*k], img.Pix[x*8+2*k+1] = byte(v>>8), byte(v)
			}
		}
		ConvertFromSRGB(img, c)
		px := func(x, k int) int { return int(img.Pix[x*8+2*k])<<8 | int(img.Pix[x*8+2*k+1]) }
		for k := 0; k < 3; k++ {
			if px(0, k) < 65500 || px(1, k) != 0 {
				t.Errorf("%s: white/black channel %d -> %d / %d", c.ID, k, px(0, k), px(1, k))
			}
			if d := px(2, k) - px(2, 1); d > 40 || d < -40 {
				t.Errorf("%s: grey not neutral: %d %d %d", c.ID, px(2, 0), px(2, 1), px(2, 2))
			}
		}
	}
}

// The ICC segment goes right after SOI and the JPEG still decodes.
func TestJPEGWithICC(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	c, _ := ColourSpaceByID("prophoto")
	out := JPEGWithICC(buf.Bytes(), c.ICC())
	if out[2] != 0xFF || out[3] != 0xE2 || string(out[6:18]) != "ICC_PROFILE\x00" {
		t.Fatalf("no APP2 ICC segment after SOI: % x", out[:20])
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("does not decode: %v", err)
	}
}
