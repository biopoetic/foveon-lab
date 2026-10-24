package x3f

// A golden test for the denoiser, on a synthetic frame: no sample photos
// (they are other people's and cannot be committed) and no SPP, so any
// machine can run it. If a change to the pipeline alters the pixels, this
// fails and the reference has to be updated deliberately - which is what
// the coarser preview binning should have forced (2026-10-23: it cost
// 0.4-1.5 dE on most camera groups and only the calibration noticed).
//
// Update the reference with:
//
//	FL_GOLDEN_UPDATE=1 go test -run TestDenoiseGolden -v ./x3f/
//
// and paste the printed value into goldenData.

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticFrame builds a deterministic frame: a gradient, a sine texture in
// one channel, fine noise everywhere and a few single-pixel spikes that the
// denoiser must not turn into coloured dots.
func syntheticFrame(w, h int) *CamImage {
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	s := uint32(12345)
	rnd := func() float64 {
		s = s*1664525 + 1013904223
		return float64(s>>8) / float64(1<<24)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			base := 0.02 + 0.30*fx + 0.10*fy
			img.Pix[i] = float32(base + 0.01*rnd())
			img.Pix[i+1] = float32(base*1.05 + 0.02*math.Sin(6*fx*math.Pi) + 0.01*rnd())
			img.Pix[i+2] = float32(base*0.9 + 0.01*rnd())
		}
	}
	for _, s := range [][3]int{{7, 5, 0}, {20, 9, 1}, {31, 22, 2}, {40, 3, 1}} {
		i := (s[1]*w + s[0]) * 3
		img.Pix[i+s[2]] = 0.95
	}
	return img
}

func encodePix(p []float32) string {
	b := make([]byte, 4*len(p))
	for i, v := range p {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodePix(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("golden: %d bytes is not a float32 slice", len(b))
	}
	p := make([]float32, len(b)/4)
	for i := range p {
		p[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return p, nil
}

func TestDenoiseGolden(t *testing.T) {
	const w, h = 48, 32
	img := syntheticFrame(w, h)
	Denoise(img, DenoiseOptions{Strength: 0.3, Scale: 1, ISO: 100})
	got := encodePix(img.Pix)
	if os.Getenv("FL_GOLDEN_UPDATE") != "" {
		fmt.Printf("GOLDEN_DENOISE=%s\n", got)
		return
	}
	want, err := decodePix(goldenDenoise48x32(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != len(img.Pix) {
		t.Fatalf("golden holds %d samples, the frame has %d", len(want), len(img.Pix))
	}
	worst, at := 0.0, 0
	for i := range want {
		d := math.Abs(float64(img.Pix[i] - want[i]))
		if d > worst {
			worst, at = d, i
		}
	}
	// Deterministic float32 arithmetic, so the tolerance only has to cover
	// architecture differences in the order of operations.
	if worst > 1e-4 {
		t.Fatalf("denoised output changed: worst %.6g at sample %d (x %d, channel %d); "+
			"if this is intended, regenerate with FL_GOLDEN_UPDATE=1", worst, at, (at/3)%w, at%3)
	}
}

// goldenDenoise48x32 is the reference output, generated with
// FL_GOLDEN_UPDATE=1 (see goldenData).
func goldenDenoise48x32(t *testing.T) string {
	data, err := os.ReadFile(filepath.Join("testdata", "golden-denoise-48x32.txt"))
	if err != nil {
		t.Fatalf("golden reference missing (%v); generate it with FL_GOLDEN_UPDATE=1", err)
	}
	return strings.TrimSpace(string(data))
}
