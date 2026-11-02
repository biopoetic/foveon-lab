package x3f

import (
	"math"
	"testing"
)

func cbTestImage() *CamImage {
	const w, h = 96, 96
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	seed := uint32(12345)
	rnd := func() float32 {
		seed = seed*1664525 + 1013904223
		return float32(seed>>8)/float32(1<<24)*2 - 1
	}
	// A smooth field with a colour step and noise, so both the guide and the
	// filter have something real to work on.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := float32(0.12)
			if x > w/2 {
				base = 0.14
			}
			i := (y*w + x) * 3
			img.Pix[i] = base + 0.010*rnd()
			img.Pix[i+1] = base + 0.006*rnd()
			img.Pix[i+2] = base*0.9 + 0.008*rnd()
		}
	}
	return img
}

// Task 43: the wide bilateral must not reach a base-ISO frame. Turning it on
// and off has to leave ISO 400 and below byte for byte the same.
func TestChromaBilateralBaseISOIdentical(t *testing.T) {
	for _, iso := range []float64{100, 400} {
		dnCB = 0
		old := cbTestImage()
		Denoise(old, DenoiseOptions{Strength: 0.3, ISO: iso})
		dnCB = 1
		neu := cbTestImage()
		Denoise(neu, DenoiseOptions{Strength: 0.3, ISO: iso})
		dnCB = 1
		for i := range old.Pix {
			if old.Pix[i] != neu.Pix[i] {
				t.Fatalf("ISO %g pixel %d changed when the task 43 filter was enabled: %g -> %g", iso, i, old.Pix[i], neu.Pix[i])
			}
		}
	}
}

// A saturated patch smaller than the wide support must keep its chroma: the
// range weight on the smoothed guide is what stops the average at its edge.
func TestChromaBilateralKeepsPatch(t *testing.T) {
	const w, h = 192, 192
	mk := func() *CamImage {
		img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
		seed := uint32(12345)
		rnd := func() float32 {
			seed = seed*1664525 + 1013904223
			return float32(seed>>8)/float32(1<<24)*2 - 1
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 0.060, 0.060, 0.060
				if x >= 72 && x < 120 && y >= 72 && y < 120 {
					img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 0.120, 0.030, 0.030
				}
				for k := 0; k < 3; k++ {
					img.Pix[i+k] += 0.0015 * rnd()
				}
			}
		}
		return img
	}
	patch := func(img *CamImage) float64 {
		var r, g, b float64
		var n int
		for y := 80; y < 112; y++ {
			for x := 80; x < 112; x++ {
				i := (y*w + x) * 3
				r += float64(img.Pix[i])
				g += float64(img.Pix[i+1])
				b += float64(img.Pix[i+2])
				n++
			}
		}
		r, g, b = r/float64(n), g/float64(n), b/float64(n)
		return math.Max(r, math.Max(g, b)) - math.Min(r, math.Min(g, b))
	}
	dnCB = 0
	old := mk()
	Denoise(old, DenoiseOptions{Strength: 0.3, ISO: 6400})
	dnCB = 1
	neu := mk()
	Denoise(neu, DenoiseOptions{Strength: 0.3, ISO: 6400})
	dnCB = 1
	if got, want := patch(neu), patch(old); got < want*0.90 {
		t.Fatalf("patch chroma %.4f, old pipeline %.4f: the wide average is eating the patch", got, want)
	}
}

func TestChromaBilateralHighISO(t *testing.T) {
	iso := 1600.0
	dnCB = 0
	old := cbTestImage()
	Denoise(old, DenoiseOptions{Strength: 0.3, ISO: iso})
	dnCB = 1
	neu := cbTestImage()
	Denoise(neu, DenoiseOptions{Strength: 0.3, ISO: iso})
	dnCB = 1
	same := true
	var ma, mb, na, nb float64
	for i := 0; i < len(neu.Pix); i += 3 {
		if old.Pix[i] != neu.Pix[i] || old.Pix[i+1] != neu.Pix[i+1] || old.Pix[i+2] != neu.Pix[i+2] {
			same = false
		}
		for k := 0; k < 3; k++ {
			v := float64(neu.Pix[i+k])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite output at pixel %d", i/3)
			}
		}
		ma += float64(old.Pix[i]) - float64(old.Pix[i+1])
		mb += float64(old.Pix[i+2]) - float64(old.Pix[i+1])
		na += float64(neu.Pix[i]) - float64(neu.Pix[i+1])
		nb += float64(neu.Pix[i+2]) - float64(neu.Pix[i+1])
	}
	if same {
		t.Fatal("the task 43 filter changed nothing at ISO 1600; the new path did not run")
	}
	n := float64(len(neu.Pix) / 3)
	if math.Abs(na/n-ma/n) > 0.002 || math.Abs(nb/n-mb/n) > 0.002 {
		t.Fatalf("frame mean chroma moved by %.4f/%.4f", na/n-ma/n, nb/n-mb/n)
	}
}
