package main

import (
	"image"
	"testing"
)

// TestNormalizeMedian checks that the comparison normalisation brings a dark
// render and a bright one to the same median luminance.
func TestNormalizeMedian(t *testing.T) {
	for _, v := range []uint8{40, 64, 200} {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
		}
		normalizeMean(img, 128)
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
	normalizeMean(img, 128)
	if img.Pix[0] != 128 {
		t.Errorf("mid-grey became %d", img.Pix[0])
	}
}
