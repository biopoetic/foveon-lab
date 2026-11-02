package dng

// Demosaic: the Malvar-He-Cutler (2004) linear 5×5 interpolation. It is a
// small, published, separable-kernel method: bilinear interpolation corrected
// by the Laplacian of the already-known channel, which removes most of
// bilinear's colour fringing on edges while staying a pure linear filter
// (no thresholds, no iteration, no tuning to one sensor). It is the same
// family of method dcraw's "AHD" builds on, and it is what this project can
// document and test honestly without a camera to tune against.
//
// The kernels below are the paper's Tables 1–4 (divisor 8, 5×5 support).

import (
	"runtime"
	"sync"
)

// MHC kernels, row-major 5×5.
var (
	kGreenAtRB = [25]float64{
		0, 0, -1, 0, 0,
		0, 0, 2, 0, 0,
		-1, 2, 4, 2, -1,
		0, 0, 2, 0, 0,
		0, 0, -1, 0, 0,
	}
	// kRAtGRow: the missing red at a green in a row with red on its left and
	// right; kRAtGCol is its transpose (red above and below).
	kRAtGRow = [25]float64{
		0, 0, 0.5, 0, 0,
		0, -1, 0, -1, 0,
		-1, 4, 5, 4, -1,
		0, -1, 0, -1, 0,
		0, 0, 0.5, 0, 0,
	}
	kRAtGCol = [25]float64{
		0, 0, -1, 0, 0,
		0, -1, 4, -1, 0,
		0.5, 0, 5, 0, 0.5,
		0, -1, 4, -1, 0,
		0, 0, -1, 0, 0,
	}
	kRAtB = [25]float64{
		0, 0, -1.5, 0, 0,
		0, 2, 0, 2, 0,
		-1.5, 0, 6, 0, -1.5,
		0, 2, 0, 2, 0,
		0, 0, -1.5, 0, 0,
	}
)

func sym(i, n int) int {
	if n <= 1 {
		return 0
	}
	for i < 0 || i >= n {
		if i < 0 {
			i = -i - 1
		} else {
			i = 2*n - i - 1
		}
	}
	return i
}

// conv5 applies a kernel at (x, y) with reflecting borders.
func conv5(raw []float32, w, h int, x, y int, k *[25]float64) float32 {
	var sum float64
	for j := -2; j <= 2; j++ {
		yy := sym(y+j, h)
		for i := -2; i <= 2; i++ {
			xx := sym(x+i, w)
			sum += k[(j+2)*5+i+2] * float64(raw[yy*w+xx])
		}
	}
	return float32(sum / 8)
}

// Demosaic turns a one-channel CFA image into interleaved linear RGB.
func Demosaic(raw []float32, w, h int, pattern []byte, pw, ph int) []float32 {
	out := make([]float32, w*h*3)
	rows := runtime.GOMAXPROCS(0)
	if rows > h {
		rows = h
	}
	var wg sync.WaitGroup
	step := (h + rows - 1) / rows
	for y0 := 0; y0 < h; y0 += step {
		y1 := min(y0+step, h)
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					c := int(pattern[(y%ph)*pw+(x%pw)])
					i := (y*w + x) * 3
					v := raw[y*w+x]
					switch c {
					case 0: // red
						out[i], out[i+1], out[i+2] = v, conv5(raw, w, h, x, y, &kGreenAtRB), conv5(raw, w, h, x, y, &kRAtB)
					case 2: // blue
						out[i], out[i+1], out[i+2] = conv5(raw, w, h, x, y, &kRAtB), conv5(raw, w, h, x, y, &kGreenAtRB), v
					default: // green: look left to see whether R or B is horizontal
						left := int(pattern[(y%ph)*pw+((x-1+pw*1024)%pw)])
						if left == 0 {
							out[i], out[i+1], out[i+2] = conv5(raw, w, h, x, y, &kRAtGRow), v, conv5(raw, w, h, x, y, &kRAtGCol)
						} else {
							out[i], out[i+1], out[i+2] = conv5(raw, w, h, x, y, &kRAtGCol), v, conv5(raw, w, h, x, y, &kRAtGRow)
						}
					}
				}
			}
		}(y0, y1)
	}
	wg.Wait()
	return out
}
