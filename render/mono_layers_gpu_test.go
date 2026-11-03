package render

// Task 54: web/gpu.js's per-layer Monochrome arithmetic must be the CPU's.
// The shader mirrors applyAt + applyMeasured: each layer gained and floored,
// the mixer's weights after, then the high-ISO black lift and power. This
// test rebuilds the shader's numbers from the GPU recipe and compares them
// with the CPU path, and checks the extra layer block in GPUBase.

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// t54GPUMono is what web/gpu.js computes for the linear grey of one pixel.
func t54GPUMono(r *GPURecipe, l [3]float32) float32 {
	// vec3 L = mul(uMonoG, layer); y = dot(uMonoW, uMonoA*L + uMonoF)
	g := r.MonoG
	l0 := g[0]*l[0] + g[1]*l[1] + g[2]*l[2]
	l1 := g[3]*l[0] + g[4]*l[1] + g[5]*l[2]
	l2 := g[6]*l[0] + g[7]*l[1] + g[8]*l[2]
	y := r.MonoW[0]*(r.MonoA[0]*l0+r.MonoF[0]) +
		r.MonoW[1]*(r.MonoA[1]*l1+r.MonoF[1]) +
		r.MonoW[2]*(r.MonoA[2]*l2+r.MonoF[2])
	y = max(0, y)
	y = max(0, y+r.MonoB)
	if y > 0 {
		y = float32(math.Pow(float64(y), float64(r.MonoGamma)))
	}
	return y
}

func TestMonoLayersGPUAgrees(t *testing.T) {
	for _, iso := range []float64{100, 400, 800, 1600, 3200, 6400} {
		for _, gains := range []bool{false, true} {
			l := &Linear{W: 4, H: 1, Pix: make([]float32, 12), ISO: iso}
			for i := range l.Pix {
				l.Pix[i] = 0.1
			}
			l.MonoLayers = []float32{
				0.002, 0.004, 0.008,
				0.02, 0.05, 0.03,
				0.12, 0.08, 0.2,
				0.4, 0.5, 0.3,
			}
			b := NewBase(l)
			p := preset.Neutral()
			p.Mono, p.MonoMixSet = true, true
			p.MonoR, p.MonoG, p.MonoB = 0.2, 0.3, 0.5
			if gains {
				p.LayerBottom, p.LayerMiddle, p.LayerTop = 2, -1, 0.5
			}
			c := Compensation{SatFactor: 1, ISO: iso}
			r := NewGPURecipe(b, p, c)
			if !r.MonoLayers {
				t.Fatalf("ISO %g: recipe has no layer block", iso)
			}
			if r.LayerMono != nil {
				t.Errorf("ISO %g: layerMono must not double-apply the mixer", iso)
			}
			op := newPixOp(p, c)
			op.forBase(b)
			if op.monoLayers == nil {
				t.Fatalf("ISO %g: CPU has no layers", iso)
			}
			for i := 0; i < 4; i++ {
				var lay [3]float32
				copy(lay[:], l.MonoLayers[i*3:])
				got := t54GPUMono(r, lay)
				want, _, _ := op.applyAt(i, 0, 0, 0, 0, 0, 0)
				if d := math.Abs(float64(got - want)); d > 2e-5 {
					t.Errorf("ISO %g gains %v pixel %d: GPU %.7f, CPU %.7f", iso, gains, i, got, want)
				}
			}
		}
	}
}

func TestMonoLayersGPUBaseBlock(t *testing.T) {
	l := &Linear{W: 3, H: 2, Pix: make([]float32, 18), ISO: 3200}
	l.MonoLayers = make([]float32, 18)
	for i := range l.MonoLayers {
		l.MonoLayers[i] = float32(i) / 20
	}
	b := NewBase(l)
	raw := b.GPUBase()
	hl := binary.LittleEndian.Uint32(raw)
	var hdr map[string]any
	if err := json.Unmarshal(raw[4:4+hl], &hdr); err != nil {
		t.Fatal(err)
	}
	if ok, _ := hdr["layers"].(bool); !ok {
		t.Fatal("header does not announce the layer block")
	}
	off := 4 + int(hl)
	off += off % 2
	n := 3 * 2
	if have, want := len(raw)-off, n*18; have != want {
		t.Fatalf("payload %d bytes, want %d", have, want)
	}
	for j := 0; j < n; j++ {
		for k := 0; k < 3; k++ {
			v := f16(l.MonoLayers[j*3+k])
			at := off + n*12 + (j*3+k)*2
			if got := binary.LittleEndian.Uint16(raw[at:]); got != v {
				t.Fatalf("layer %d,%d: got %04x want %04x", j, k, got, v)
			}
		}
	}
}
