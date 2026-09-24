package render

import (
	"image"
	"image/color"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func testImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 6), uint8(255 - x*3), 255})
		}
	}
	return img
}

func noComp() Compensation { return Compensation{SatFactor: 1} }

func TestNeutralIsIdentity(t *testing.T) {
	src := testImage()
	out := Render(NewBase(FromImage(src, 0, 0)), preset.Neutral(), noComp())
	for i := range src.Pix {
		d := int(src.Pix[i]) - int(out.Pix[i])
		if d < -1 || d > 1 {
			t.Fatalf("pixel byte %d: in %d out %d", i, src.Pix[i], out.Pix[i])
		}
	}
}

func lum(img *image.RGBA, x, y int) float64 {
	c := img.RGBAAt(x, y)
	return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
}

func TestDirections(t *testing.T) {
	base := NewBase(FromImage(testImage(), 0, 0))
	ref := Render(base, preset.Neutral(), noComp())

	p := preset.Neutral()
	p.Exposure = 1
	if brighter := Render(base, p, noComp()); lum(brighter, 20, 10) <= lum(ref, 20, 10) {
		t.Error("exposure +1 did not brighten")
	}

	p = preset.Neutral()
	p.Saturation = -1
	mono := Render(base, p, noComp())
	for _, pt := range [][2]int{{5, 5}, {40, 30}} {
		c := mono.RGBAAt(pt[0], pt[1])
		if c.R != c.G || c.G != c.B {
			t.Errorf("saturation -1 not monochrome at %v: %v", pt, c)
		}
	}

	p = preset.Neutral()
	p.FillLight = 1
	if lifted := Render(base, p, noComp()); lum(lifted, 2, 2) <= lum(ref, 2, 2) {
		t.Error("fill light did not lift the dark corner")
	}
}

func TestRotateAndDownscale(t *testing.T) {
	l := FromImage(testImage(), 0, 90)
	if l.W != 40 || l.H != 64 {
		t.Fatalf("rotated size %dx%d", l.W, l.H)
	}
	d := FromImage(testImage(), 32, 0)
	if d.W != 32 || d.H != 20 {
		t.Fatalf("downscaled size %dx%d", d.W, d.H)
	}
	if s := d.Downscale(16); s.W != 16 || s.H != 10 {
		t.Fatalf("second downscale %dx%d", s.W, s.H)
	}
}

func TestCompensation(t *testing.T) {
	c := CompensationFor(map[string]string{"CM_DESC": "Vivid", "SATU_DESC": "0.5", "CONT_DESC": "-0.3"})
	if c.SatFactor >= 1 || c.Saturation != -0.5 || c.Contrast > 0.3-0.1 {
		t.Errorf("compensation = %+v", c)
	}
}
