package main

import (
	"image"
	"testing"
)

// TestNormalizeBrightness checks that the comparison normalisation brings a
// flat dark render and a flat bright one to the same upper-midtones.
func TestNormalizeBrightness(t *testing.T) {
	for _, v := range []uint8{40, 64, 200} {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
		}
		normalizeBrightness(img, 128)
		got := int(img.Pix[0])
		if got < 126 || got > 130 {
			t.Errorf("flat %d: became %d, want ~128", v, got)
		}
		if img.Pix[3] != 255 {
			t.Errorf("flat %d: alpha changed to %d", v, img.Pix[3])
		}
	}
}

// A mid-grey image is left alone.
func TestNormalizeMidGrey(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 128, 128, 128, 255
	}
	normalizeBrightness(img, 128)
	if img.Pix[0] != 128 {
		t.Errorf("mid-grey became %d", img.Pix[0])
	}
}

// A small, near-white highlight in a dark frame must not be pushed over the
// top by the comparison's own brightness match: the cap from the 99th
// percentile keeps it below 254. The old median match made it white.
func TestNormalizeKeepsHighlights(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		v := uint8(40)
		if y < 5 {
			v = 250
		}
		for x := 0; x < 100; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
		}
	}
	normalizeBrightness(img, 128)
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] >= 254 || img.Pix[i+1] >= 254 || img.Pix[i+2] >= 254 {
			t.Fatalf("highlight was clipped to %d,%d,%d", img.Pix[i], img.Pix[i+1], img.Pix[i+2])
		}
	}
}
