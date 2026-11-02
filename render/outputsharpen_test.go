package render

import (
	"image"
	"testing"
)

// TestOutputSharpen16: Publish's final-size sharpening must add acutance at
// an edge and leave a flat field alone.
func TestOutputSharpen16(t *testing.T) {
	img := image.NewRGBA64(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			v := uint16(10000)
			if x >= 4 {
				v = 50000
			}
			i := y*img.Stride + x*8
			for c := 0; c < 3; c++ {
				img.Pix[i+2*c], img.Pix[i+2*c+1] = byte(v>>8), byte(v)
			}
			img.Pix[i+6], img.Pix[i+7] = 0xff, 0xff
		}
	}
	at := func(x, y int) int {
		i := y*img.Stride + x*8
		return int(img.Pix[i])<<8 | int(img.Pix[i+1])
	}
	before3, before4 := at(3, 1), at(4, 1)
	OutputSharpen16(img, 0.5)
	if got := at(3, 1); got >= before3 {
		t.Errorf("dark side of the edge %d -> %d, want darker (acutance)", before3, got)
	}
	if got := at(4, 1); got <= before4 {
		t.Errorf("bright side of the edge %d -> %d, want brighter (acutance)", before4, got)
	}
	if at(7, 1) != 50000 {
		// The last column is not touched (the mask keeps a one-pixel border).
		t.Errorf("flat field changed: %d", at(7, 1))
	}
}
