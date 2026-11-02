package dng

// Known-colour tests: a Bayer or LinearRaw DNG built here, developed by the
// reader, compared with the linear sRGB the file was made from. If these
// pass, the strip/tile reader, the CFA pattern, the demosaic, the white
// balance and the colour matrices all agree on a colour we chose.

import (
	"bytes"
	"image"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/render"
)

func developBytes(t *testing.T, b []byte, maxW int) *Image {
	t.Helper()
	f, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	img, err := f.Develop(maxW)
	if err != nil {
		t.Fatalf("Develop: %v", err)
	}
	return img
}

// mosaic makes a Bayer image of flat patches whose camera values are the
// linear sRGB colours in cols. The camera model in the test DNG is linear
// sRGB, so the developed patch must come back as that colour.
func mosaic(w, h int, cols [][3]float64, pat []byte) []uint32 {
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// A 4x1 grid of patches.
			pi := min(len(cols)-1, x*len(cols)/w)
			c := pat[(y%2)*2+x%2]
			v := cols[pi][c]
			vals[y*w+x] = uint32(math.Round(v * 65535))
		}
	}
	return vals
}

func TestBayerFlatColours(t *testing.T) {
	cols := [][3]float64{
		{0.18, 0.18, 0.18},
		{0.60, 0.15, 0.10},
		{0.10, 0.45, 0.12},
		{0.12, 0.16, 0.55},
	}
	patterns := []struct {
		name string
		pat  []byte
	}{
		{"RGGB", []byte{0, 1, 1, 2}},
		{"BGGR", []byte{2, 1, 1, 0}},
		{"GRBG", []byte{1, 0, 2, 1}},
		{"GBRG", []byte{1, 2, 0, 1}},
	}
	for _, p := range patterns {
		t.Run(p.name, func(t *testing.T) {
			const w, h = 64, 32
			d := &testDNG{w: w, h: h, bits: 16, samples: 1, cfa: p.pat, cfaW: 2, cfaH: 2}
			d.vals = mosaic(w, h, cols, p.pat)
			img := developBytes(t, d.build(t), 0)
			if img.W != w || img.H != h {
				t.Fatalf("size %dx%d, want %dx%d", img.W, img.H, w, h)
			}
			// Sample the middle of each patch, away from its edges.
			for pi, want := range cols {
				x := (pi*2 + 1) * w / (len(cols) * 2)
				y := h / 2
				i := (y*w + x) * 3
				for c := 0; c < 3; c++ {
					got := float64(img.Pix[i+c])
					if math.Abs(got-want[c]) > 0.012 {
						t.Errorf("patch %d channel %d: got %.4f want %.4f", pi, c, got, want[c])
					}
				}
			}
		})
	}
}

func TestBayerPacked12Deflate(t *testing.T) {
	cols := [][3]float64{{0.25, 0.10, 0.05}, {0.05, 0.30, 0.20}}
	const w, h = 32, 16
	const black, white = 128, 4095
	vals := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pi := min(1, x*2/w)
			c := []byte{0, 1, 1, 2}[(y%2)*2+x%2]
			vals[y*w+x] = uint32(black) + uint32(math.Round(cols[pi][c]*float64(white-black)))
		}
	}
	d := &testDNG{w: w, h: h, bits: 12, samples: 1, cfa: []byte{0, 1, 1, 2}, cfaW: 2, cfaH: 2, comp: compressionDeflate}
	d.vals = vals
	d.tag(50714, 4, lo(black), 1)
	d.tag(50717, 4, lo(white), 1)
	img := developBytes(t, d.build(t), 0)
	for pi, want := range cols {
		x, y := (pi*2+1)*w/4, h/2
		i := (y*w + x) * 3
		for c := 0; c < 3; c++ {
			got := float64(img.Pix[i+c])
			// 12-bit quantisation at the black/white scale costs about
			// 1/4000; leave a little for the demosaic.
			if math.Abs(got-want[c]) > 0.01 {
				t.Errorf("patch %d channel %d: got %.4f want %.4f", pi, c, got, want[c])
			}
		}
	}
}

func TestLinearRawRenderedDNG(t *testing.T) {
	// WriteRenderedDNG stores linear light behind a linear tone curve and
	// the sRGB matrices; the reader must hand that linear light back.
	img := image.NewRGBA64(image.Rect(0, 0, 4, 2))
	srgb := []uint16{0, 8192, 32768, 65535, 16384, 40000, 50000, 12000}
	for i := 0; i < 8; i++ {
		img.Pix[i*8+0], img.Pix[i*8+1] = byte(srgb[i]>>8), byte(srgb[i])
		img.Pix[i*8+2], img.Pix[i*8+3] = byte(srgb[(i+1)%8]>>8), byte(srgb[(i+1)%8])
		img.Pix[i*8+4], img.Pix[i*8+5] = byte(srgb[(i+2)%8]>>8), byte(srgb[(i+2)%8])
		img.Pix[i*8+6], img.Pix[i*8+7] = 0xff, 0xff
	}
	var buf bytes.Buffer
	if err := render.WriteRenderedDNG(&buf, img, "TEST", "synthetic", "test"); err != nil {
		t.Fatal(err)
	}
	out := developBytes(t, buf.Bytes(), 0)
	if out.W != 4 || out.H != 2 {
		t.Fatalf("size %dx%d", out.W, out.H)
	}
	for i := 0; i < 8; i++ {
		for c := 0; c < 3; c++ {
			s := srgb[(i+c)%8]
			want := float64(render.ToLinear(float32(s) / 65535))
			got := float64(out.Pix[i*3+c])
			if math.Abs(got-want) > 0.004 {
				t.Errorf("pixel %d channel %d: got %.5f want %.5f", i, c, got, want)
			}
		}
	}
}

func TestLinearRawCameraDNG(t *testing.T) {
	// The app's own camera DNG: linear camera RGB plus gain and colour
	// matrix. With an identity matrix and unit gain the developed values
	// are the raw values.
	const w, h = 8, 4
	pix := make([]float32, w*h*3)
	for i := range pix {
		pix[i] = float32((i*37)%1000) / 1000
	}
	var buf bytes.Buffer
	gain := [3]float64{1, 1, 1}
	cc := [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	if err := render.WriteCameraDNG(&buf, pix, w, h, gain, cc, nil, 1, [2]float64{1, 1}, "TEST", "synthetic", "test"); err != nil {
		t.Fatal(err)
	}
	out := developBytes(t, buf.Bytes(), 0)
	for i, want := range pix {
		// 16-bit storage rounds to 1/65535.
		if math.Abs(float64(out.Pix[i])-float64(want)) > 0.0006 {
			t.Fatalf("sample %d: got %.6f want %.6f", i, out.Pix[i], want)
		}
	}
}

func TestAsShotWhiteXY(t *testing.T) {
	// A file with AsShotWhiteXY but no AsShotNeutral. For the D65 white this
	// reader's ForwardMatrix describes, the derived neutral is 1,1,1 and the
	// patches come back unchanged; this also exercises the derive path.
	cols := [][3]float64{{0.18, 0.18, 0.18}, {0.50, 0.20, 0.10}}
	const w, h = 32, 16
	d := &testDNG{w: w, h: h, bits: 16, samples: 1, cfa: []byte{0, 1, 1, 2}, cfaW: 2, cfaH: 2,
		noNeutral: true, whiteXY: [2]float64{0.3127, 0.3290}}
	d.vals = mosaic(w, h, cols, d.cfa)
	img := developBytes(t, d.build(t), 0)
	for pi, want := range cols {
		x, y := (pi*2+1)*w/4, h/2
		i := (y*w + x) * 3
		for c := 0; c < 3; c++ {
			if math.Abs(float64(img.Pix[i+c])-want[c]) > 0.015 {
				t.Errorf("patch %d channel %d: got %.4f want %.4f", pi, c, img.Pix[i+c], want[c])
			}
		}
	}
}

func TestBayerTiled(t *testing.T) {
	cols := [][3]float64{{0.18, 0.18, 0.18}, {0.55, 0.20, 0.10}, {0.10, 0.35, 0.50}}
	const w, h = 48, 32
	d := &testDNG{w: w, h: h, bits: 16, samples: 1, cfa: []byte{0, 1, 1, 2}, cfaW: 2, cfaH: 2, tiled: true}
	d.vals = mosaic(w, h, cols, d.cfa)
	img := developBytes(t, d.build(t), 0)
	for pi, want := range cols {
		x, y := (pi*2+1)*w/(len(cols)*2), h/2
		i := (y*w + x) * 3
		for c := 0; c < 3; c++ {
			if math.Abs(float64(img.Pix[i+c])-want[c]) > 0.012 {
				t.Errorf("patch %d channel %d: got %.4f want %.4f", pi, c, img.Pix[i+c], want[c])
			}
		}
	}
}

func TestColorMatrixOnly(t *testing.T) {
	// Adobe DNG Converter often writes ColorMatrix1/2 and no ForwardMatrix.
	// The ColorMatrix is XYZ -> camera under its calibration illuminant; the
	// reader must invert it and adapt D65 to D50.
	cols := [][3]float64{{0.18, 0.18, 0.18}, {0.55, 0.20, 0.10}, {0.12, 0.25, 0.50}}
	const w, h = 48, 24
	d := &testDNG{w: w, h: h, bits: 16, samples: 1, cfa: []byte{0, 1, 1, 2}, cfaW: 2, cfaH: 2, noForward: true}
	d.vals = mosaic(w, h, cols, d.cfa)
	img := developBytes(t, d.build(t), 0)
	for pi, want := range cols {
		x, y := (pi*2+1)*w/(len(cols)*2), h/2
		i := (y*w + x) * 3
		for c := 0; c < 3; c++ {
			got := float64(img.Pix[i+c])
			if math.Abs(got-want[c]) > 0.03 {
				t.Errorf("patch %d channel %d: got %.4f want %.4f", pi, c, got, want[c])
			}
		}
	}
}

func TestNoColourMatrixIsApproximate(t *testing.T) {
	d := &testDNG{w: 4, h: 4, bits: 16, samples: 3, noMatrix: true}
	d.vals = make([]uint32, 4*4*3)
	for i := range d.vals {
		d.vals[i] = uint32(10000 + i)
	}
	img := developBytes(t, d.build(t), 0)
	if !img.ApproxColour {
		t.Error("a DNG without a colour matrix should be marked approximate")
	}
}

func TestUnsupportedCompression(t *testing.T) {
	d := &testDNG{w: 4, h: 4, bits: 16, samples: 3, comp: 6}
	d.vals = make([]uint32, 4*4*3)
	f, err := NewReader(bytes.NewReader(d.build(t)), int64(len(d.build(t))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Develop(0); err == nil {
		t.Fatal("compression 6 should not decode")
	}
}
