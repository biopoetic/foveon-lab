package render

// TASK-78: the GPU film tone must equal the CPU's. web/gpu.js filmTone reads
// the same LUT texture the server uploads, with x = Y^(1/4)*scale and a
// linear interpolation; this mirrors that shader for the scan and print LUTs.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func gpuFilmTone(t *filmTone, y float32) float32 {
	x := float32(math.Sqrt(math.Sqrt(float64(y)))) * t.scale
	if x >= filmToneN {
		return t.lut[filmToneN]
	}
	i := int(x)
	fr := x - float32(i)
	return t.lut[i] + fr*(t.lut[i+1]-t.lut[i])
}

// Task 78: guards GPU = CPU for the B&W film-tone LUT (x = Y^(1/4)*scale, linear interpolation).
// A failure means web/gpu.js's filmTone path or the LUT layout drifted from the CPU.
// Change only when the shader's film-tone path changes; update this mirror in the same commit.
func TestGPUFilmToneMirror(t *testing.T) {
	for _, film := range []string{
		"Foma Fomapan 400 Action", "Fujifilm Neopan Acros 100",
		"Rollei Retro 400S", "Kodak Technical Pan",
	} {
		for _, paper := range []string{"", "Ilford Multigrade IV RC"} {
			p := preset.Params{Film: film, Paper: paper, Grade: "2"}
			tone := newFilmTone(p)
			if tone == nil {
				t.Logf("%s paper %q: generic, no film-tone LUT in either path", film, paper)
				continue
			}
			worst := 0.0
			for k := -40; k <= 200; k++ {
				y := float32(math.Pow(10, float64(k)/100))
				d := math.Abs(float64(tone.at(y) - gpuFilmTone(tone, y)))
				if d > worst {
					worst = d
				}
			}
			if worst > 1e-6 {
				t.Errorf("%s paper %q: GPU film tone differs by %.3g", film, paper, worst)
			}
			t.Logf("%s paper %q: GPU = CPU film tone, worst %.3g", film, paper, worst)
		}
	}
}
