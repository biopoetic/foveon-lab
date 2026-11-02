package render

// The GPU preview must draw the same grain as the CPU export. This mirrors
// web/gpu.js's lattice and Gaussian hashes on the recipe's parameters and
// compares the result with film.go's per-pixel field, including the
// spatially resolved clumps and the common colour component.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func t47GPUHash(x uint32) uint32 {
	x ^= x >> 16
	x *= 0x7feb352d
	x ^= x >> 15
	x *= 0x846ca68b
	x ^= x >> 16
	return x
}

func t47GPULat(x, y int, seed uint32) float32 {
	h := t47GPUHash(uint32(x)*0x9e3779b1 ^ uint32(y)*0x85ebca77 ^ seed)
	u := float32(h>>8) * (1.0 / 16777216.0)
	return (2*u - 1) * 1.7320508
}

func t47GPUSample(px, py int, cell float64, seed uint32) float32 {
	fx := (float64(px) + 0.5) / cell
	fy := (float64(py) + 0.5) / cell
	ix, iy := int(math.Floor(fx)), int(math.Floor(fy))
	tx, ty := float32(fx-float64(ix)), float32(fy-float64(iy))
	a := t47GPULat(ix, iy, seed) + (t47GPULat(ix+1, iy, seed)-t47GPULat(ix, iy, seed))*tx
	b := t47GPULat(ix, iy+1, seed) + (t47GPULat(ix+1, iy+1, seed)-t47GPULat(ix, iy+1, seed))*tx
	return (a + (b-a)*ty) * 1.5
}

func t47GPUGauss(i int, seed uint32) float32 {
	a := t47GPUHash(uint32(i)*0x9e3779b1 + seed)
	b := t47GPUHash(a ^ 0x85ebca6b)
	s := float32(a&0xffff) + float32(a>>16) + float32(b&0xffff) + float32(b>>16)
	return (s/65535 - 2) * 1.7320508
}

// t47GPUField is what web/gpu.js's PIX shader computes for grain at (x,y).
func t47GPUField(r *GPURecipe, i, x, y int) [3]float32 {
	sqi := float32(math.Sqrt(math.Max(0, float64(1-r.GrainRho))))
	sqr := float32(math.Sqrt(math.Max(0, float64(r.GrainRho))))
	var n [3]float32
	if r.GrainMono {
		for j, o := range r.GrainOct {
			n[0] += o.Amp * t47GPUSample(x, y, float64(o.Cell), r.GrainSeed^0x1234567^uint32(j)*0x9e3779b9)
		}
		n[0] += r.GrainFine * t47GPUGauss(i, r.GrainSeed^0x1234567)
		return [3]float32{n[0], n[0], n[0]}
	}
	for j, o := range r.GrainOct {
		var com float32
		if r.GrainRho > 0 {
			com = t47GPUSample(x, y, float64(o.Cell), r.GrainSeed^0x1234567^uint32(j)*0x9e3779b9)
		}
		for c := 0; c < 3; c++ {
			ind := t47GPUSample(x, y, float64(o.Cell), r.GrainSeed^0x5bf03635^uint32(j)*0x9e3779b9^uint32(c)*0x85ebca6b)
			n[c] += o.Amp * (sqi*ind + sqr*com)
		}
	}
	var com float32
	if r.GrainRho > 0 {
		com = t47GPUGauss(i, r.GrainSeed^0x1234567)
	}
	for c := 0; c < 3; c++ {
		ind := t47GPUGauss(i, r.GrainSeed^0x5bf03635^uint32(c)*0x85ebca6b)
		n[c] += r.GrainFine * (sqi*ind + sqr*com)
	}
	return n
}

// t48GPUgrain1 mirrors web/gpu.js's grain1, the tone application of one
// density sample. web/gpu.js and film.go must keep the same curve.
func t48GPUgrain1(v, n float32) float32 {
	if v <= 0 {
		return v
	}
	u := min(1, enc(v))
	amp := 4 * u * (1 - u)
	return v * float32(math.Exp(-2.302585*float64(amp*n)))
}

func TestGrainToneGPUAgrees(t *testing.T) {
	for _, v := range []float32{0.001, 0.01, 0.05, 0.2, 0.5, 0.8, 0.95, 0.999} {
		for _, n := range []float32{-3, -1, -0.25, 0.25, 1, 3} {
			want := grainDensity(v, n)
			got := t48GPUgrain1(v, n)
			if d := math.Abs(float64(got - want)); d > 1e-6 {
				t.Errorf("v %.3f n %.2f: GPU %.8f, CPU %.8f", v, n, got, want)
			}
		}
	}
}

func TestGrainGPUAgrees(t *testing.T) {
	for _, tc := range []struct {
		name string
		mono bool
	}{
		{"Kodak Tri-X Pan 400 (TX)", true},
		{"Kodak Portra 800", false},
		{"Ilford Delta 3200", true},
	} {
		f, _ := FilmByKey(tc.name)
		l := flat(200, 100, 0.5)
		l.Frame, l.Scale = 4704, 1 // 7.65 µm pixels: some octaves are spatial
		b := NewBase(l)
		p := preset.Params{Film: tc.name, Grain: f.Grain, FilmWidth: 36}
		cpu := b.newFilm(p, tc.mono)
		r := NewGPURecipe(b, p, Compensation{})
		if r.GrainMono != tc.mono {
			t.Errorf("%s: mono %v", tc.name, r.GrainMono)
		}
		if len(r.GrainOct) != len(cpu.oct) {
			t.Errorf("%s: %d recipe octaves, %d CPU", tc.name, len(r.GrainOct), len(cpu.oct))
		}
		for _, i := range []int{0, 1, 37, 133, 199, 1234, 5000, 19999} {
			x, y := i%200, i/200
			got := t47GPUField(r, i, x, y)
			for c := 0; c < 3; c++ {
				var want float32
				if tc.mono {
					want = cpu.noise[i]
				} else {
					want = cpu.noise[i*3+c]
				}
				if d := math.Abs(float64(got[c] - want)); d > 2e-5 {
					t.Errorf("%s pixel %d channel %d: GPU %.7f, CPU %.7f", tc.name, i, c, got[c], want)
					break
				}
			}
		}
	}
}
