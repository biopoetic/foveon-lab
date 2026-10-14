package render

import (
	"math"
	"strconv"
	"testing"
)

// The pad at a Wratten's position reproduces that Wratten's matrix (same
// model, same edges): the live computation is the offline one.
func TestPadMatchesWratten(t *testing.T) {
	for _, c := range []struct {
		name    string
		a, b, s float64
	}{{"yellow", 380, 720, 0}, {"yellow", 495, 720, 1}, {"red", 595, 720, 1}, {"green", 490, 585, 1}, {"blue", 380, 505, 1}} {
		for _, gen := range []string{"Merrill", "TRUEII", "Quattro"} {
			pad := padFilter(gen, fmtPad(c.a, c.b, c.s))
			if pad == nil {
				t.Fatalf("%s: no pad matrix", gen)
			}
			if c.s == 0 {
				for i, v := range pad {
					if want := float64(map[bool]int{true: 1}[i%4 == 0]); math.Abs(v-want) > 1e-6 {
						t.Errorf("%s clear glass: %v", gen, *pad)
						break
					}
				}
				continue
			}
			w := lensFilter(gen, c.name)
			var e float64
			for i := range pad {
				e = math.Max(e, math.Abs(pad[i]-w[i]))
			}
			t.Logf("%s %s: max |pad - Wratten| %.4f", gen, c.name, e)
			if e > 0.05 {
				t.Errorf("%s %s: pad differs from the Wratten matrix by %.3f", gen, c.name, e)
			}
		}
	}
}

func fmtPad(a, b, s float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return "pad:" + f(a) + "," + f(b) + "," + f(s)
}
