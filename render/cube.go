package render

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/biopoetic/foveon-lab/preset"
)

// WriteCube writes the preset as a 3D LUT in the .cube format (Adobe/Resolve)
// that darktable, Photoshop, Camera Raw profiles, Resolve and Affinity read:
// sRGB in, sRGB out, n points per axis.
//
// It is the same per-pixel maths as Render, so the look matches the
// preview, with two necessary simplifications: Fill Light lifts shadows by
// each pixel's own brightness instead of its blurred surroundings (a LUT
// can't see neighbours), and Sharpness is left out.
func WriteCube(w io.Writer, title string, p preset.Params, n int) error {
	op := newPixOp(p, Compensation{SatFactor: 1})
	bw := bufio.NewWriter(w)
	title = strings.NewReplacer(`"`, "'", "\n", " ", "\r", " ").Replace(title)
	fmt.Fprintf(bw, "TITLE \"%s\"\n# Made by Foveon Lab from an SPP preset: apply it to a neutral sRGB rendering.\n", title)
	fmt.Fprintf(bw, "LUT_3D_SIZE %d\nDOMAIN_MIN 0.0 0.0 0.0\nDOMAIN_MAX 1.0 1.0 1.0\n", n)
	step := 1 / float32(n-1)
	// .cube order: red changes fastest, then green, then blue.
	for bi := 0; bi < n; bi++ {
		for gi := 0; gi < n; gi++ {
			for ri := 0; ri < n; ri++ {
				r, g, b := dec(float32(ri)*step), dec(float32(gi)*step), dec(float32(bi)*step)
				Y := 0.2126*r + 0.7152*g + 0.0722*b
				ro, gO, bo := op.apply(r, g, b, enc(Y), 0, 0) // a LUT has no surroundings
				fmt.Fprintf(bw, "%.6f %.6f %.6f\n", clamp01(enc(ro)), clamp01(enc(gO)), clamp01(enc(bo)))
			}
		}
	}
	return bw.Flush()
}

func clamp01(v float32) float32 { return max(0, min(1, v)) }
