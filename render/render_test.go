package render

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/tiff"

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
	p.Saturation = -2
	mono := Render(base, p, noComp())
	for _, pt := range [][2]int{{5, 5}, {40, 30}} {
		c := mono.RGBAAt(pt[0], pt[1])
		if c.R != c.G || c.G != c.B {
			t.Errorf("saturation -2 not monochrome at %v: %v", pt, c)
		}
	}

	// -1 is muted colour in SPP, not black & white.
	spread := func(img *image.RGBA, x, y int) int {
		c := img.RGBAAt(x, y)
		return int(max(c.R, c.G, c.B)) - int(min(c.R, c.G, c.B))
	}
	p.Saturation = -1
	muted := Render(base, p, noComp())
	if s, full := spread(muted, 40, 30), spread(ref, 40, 30); s == 0 || s >= full {
		t.Errorf("saturation -1 chroma spread %d, want between 0 and %d", s, full)
	}

	p = preset.Neutral()
	p.FillLight = 1
	if lifted := Render(base, p, noComp()); lum(lifted, 2, 2) <= lum(ref, 2, 2) {
		t.Error("fill light did not lift the dark corner")
	}
}

// Render16 must agree with Render to within 8-bit rounding, and carry more
// distinct levels (that's the point of the 16-bit TIFF export).
func TestRender16MatchesRender(t *testing.T) {
	base := NewBase(FromImage(testImage(), 0, 0))
	p := preset.Neutral()
	p.Exposure, p.Contrast, p.Saturation, p.FillLight, p.Sharpness = 0.3, 0.4, 0.5, 0.5, 0.2
	a, b := Render(base, p, noComp()), Render16(base, p, noComp())
	levels8, levels16 := map[uint8]bool{}, map[uint16]bool{}
	for y := 0; y < 40; y++ {
		for x := 0; x < 64; x++ {
			c8, c16 := a.RGBAAt(x, y), b.RGBA64At(x, y)
			for _, pair := range [][2]float64{{float64(c8.R), float64(c16.R)}, {float64(c8.G), float64(c16.G)}, {float64(c8.B), float64(c16.B)}} {
				if d := pair[0] - pair[1]/257; d > 1.6 || d < -1.6 {
					t.Fatalf("pixel %d,%d: 8-bit %v vs 16-bit %v", x, y, c8, c16)
				}
			}
			levels8[c8.G], levels16[c16.G] = true, true
		}
	}
	if len(levels16) <= len(levels8) {
		t.Errorf("16-bit render has %d levels vs %d at 8 bits", len(levels16), len(levels8))
	}
}

// WriteTIFF16 output must decode back (with an independent decoder) to the
// exact same 16-bit pixels, without an alpha channel.
func TestWriteTIFF16RoundTrip(t *testing.T) {
	base := NewBase(FromImage(testImage(), 0, 0))
	p := preset.Neutral()
	p.Contrast = 0.3
	img := Render16(base, p, noComp())
	var buf bytes.Buffer
	if err := WriteTIFF16(&buf, img); err != nil {
		t.Fatal(err)
	}
	back, err := tiff.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if back.Bounds() != img.Bounds() {
		t.Fatalf("bounds %v vs %v", back.Bounds(), img.Bounds())
	}
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			r1, g1, b1, _ := img.At(x, y).RGBA()
			r2, g2, b2, a2 := back.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a2 != 0xffff {
				t.Fatalf("pixel %d,%d: wrote %d,%d,%d read %d,%d,%d,%d", x, y, r1, g1, b1, r2, g2, b2, a2)
			}
		}
	}
	// 16-bit RGB without alpha decodes as RGBA64 (opaque); with an alpha
	// channel x/image/tiff would report NRGBA64.
	if cfg, _ := tiff.DecodeConfig(bytes.NewReader(buf.Bytes())); cfg.ColorModel != color.RGBA64Model {
		t.Errorf("colour model %v, want 16-bit RGB without alpha", cfg.ColorModel)
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
