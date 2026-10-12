package render

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func readCube(t *testing.T, data []byte) (int, [][3]float64) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	n := 0
	var rows [][3]float64
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "LUT_3D_SIZE") {
			fmt.Sscan(strings.TrimPrefix(l, "LUT_3D_SIZE"), &n)
			continue
		}
		var a, b, c float64
		if k, _ := fmt.Sscan(l, &a, &b, &c); k == 3 {
			rows = append(rows, [3]float64{a, b, c})
		}
	}
	if len(rows) != n*n*n {
		t.Fatalf("%d rows for size %d", len(rows), n)
	}
	return n, rows
}

func TestCubeNeutralIsIdentity(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCube(&buf, "Neutral", preset.Neutral(), 17); err != nil {
		t.Fatal(err)
	}
	n, rows := readCube(t, buf.Bytes())
	for bi := 0; bi < n; bi++ {
		for gi := 0; gi < n; gi++ {
			for ri := 0; ri < n; ri++ {
				got := rows[(bi*n+gi)*n+ri]
				want := [3]float64{float64(ri) / float64(n-1), float64(gi) / float64(n-1), float64(bi) / float64(n-1)}
				for k := range got {
					if math.Abs(got[k]-want[k]) > 0.003 {
						t.Fatalf("(%d,%d,%d): %v want %v", ri, gi, bi, got, want)
					}
				}
			}
		}
	}
}

// On a flat-colour image (no neighbours to differ from) the LUT must give
// what the preview renders.
func TestCubeMatchesRender(t *testing.T) {
	p := preset.Params{Exposure: .2, Contrast: .35, Blackness: .15, Highlight: -.5, Saturation: .3, FillLight: .4, R: .95, G: 1.02, B: .9, ColorMode: preset.ModeLandscape}
	var buf bytes.Buffer
	const n = 65
	WriteCube(&buf, "t", p, n)
	_, rows := readCube(t, buf.Bytes())
	for _, c := range [][3]uint8{{128, 64, 32}, {200, 180, 160}, {20, 40, 90}, {64, 128, 64}} {
		// A colour on the LUT grid, so no interpolation error.
		idx := func(v uint8) int { return int(math.Round(float64(v) / 255 * (n - 1))) }
		ri, gi, bi := idx(c[0]), idx(c[1]), idx(c[2])
		snap := func(i int) uint8 { return uint8(math.Round(float64(i) / (n - 1) * 255)) }
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = snap(ri), snap(gi), snap(bi), 255
		}
		out := Render(NewBase(FromImage(img, 0, 0)), p, Compensation{SatFactor: 1})
		row := rows[(bi*n+gi)*n+ri]
		for k := 0; k < 3; k++ {
			if d := math.Abs(float64(out.Pix[32*out.Stride+32*4+k]) - row[k]*255); d > 2.5 {
				t.Errorf("colour %v channel %d: render %d, LUT %.1f", c, k, out.Pix[32*out.Stride+32*4+k], row[k]*255)
			}
		}
	}
}
