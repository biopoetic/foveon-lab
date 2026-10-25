package x3f

import (
	"math"
	"math/rand"
	"testing"
)

// The row-by-row vertical pass gives the column-by-column one's result.
func TestBoxBlurSame(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, sz := range [][3]int{{257, 129, 1}, {600, 401, 8}, {1000, 300, 32}, {5, 700, 4}} {
		w, h, r := sz[0], sz[1], sz[2]
		p := make([]float32, w*h)
		for i := range p {
			p[i] = rng.Float32()
		}
		a, b := boxBlur(p, w, h, r), boxBlurRef(p, w, h, r)
		for i := range a {
			if math.Abs(float64(a[i]-b[i])) > 2e-5 {
				t.Fatalf("%dx%d r%d: at %d %g vs %g", w, h, r, i, a[i], b[i])
			}
		}
	}
}

// selectK picks what a full sort would put at k.
func TestSelectK(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for n := 1; n < 300; n += 7 {
		v := make([]float64, n)
		for i := range v {
			v[i] = math.Floor(rng.Float64() * 20) // with ties
		}
		s := append([]float64(nil), v...)
		sortFloats(s)
		for _, k := range []int{0, n / 2, n - 1} {
			c := append([]float64(nil), v...)
			if got := selectK(c, k); got != s[k] {
				t.Fatalf("n %d k %d: %g want %g", n, k, got, s[k])
			}
		}
	}
}

func sortFloats(s []float64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
