package render

// Task 72: the integrated Status A read must not move a single bit of any
// colour path that is not a modelled slide. This probe hashes the rendered
// output of the non-slide paths on a fixed synthetic frame; the expected
// hashes are the v0.4.108 values, so task 73 also pins the fallback.

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// t72NonSlideHash is the v0.4.108 hash of the rendered frame, first 12 bytes.
var t72NonSlideHash = map[string]string{
	"no film":        "3ccccfdb37310b97ab75b29b",
	"negative print": "bd4f5b40972bccbaaf41f565",
	"negative scan":  "62661061d05c267614721ccb",
	"negative fuji":  "6802240afe626a494666f74d",
	"paper edge":     "4ec86d20963416f857153af0",
	"bw tri-x":       "95554348795aa89a678e6434",
	"bw t-max":       "51871fd79fbe81403b95f656",
	"mono":           "64c28d59ce38c0589c8a4af1",
	"generic agfa":   "3ccccfdb37310b97ab75b29b",
	"generic fuji":   "3ccccfdb37310b97ab75b29b",
}

func TestT72NonSlideBytes(t *testing.T) {
	const w, h = 32, 24
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			l.Pix[i] = float32(0.002 + 0.35*u + 0.10*v)
			l.Pix[i+1] = float32(0.002 + 0.30*u + 0.12*(1-v))
			l.Pix[i+2] = float32(0.002 + 0.25*v + 0.10*u)
		}
	}
	b := NewBase(l)
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
	cases := []struct {
		name, film, paper string
		mono              bool
	}{
		{"no film", "", "", false},
		{"negative print", "Kodak Portra 400", "", false},
		{"negative scan", "Kodak Portra 400", "scan", false},
		{"negative fuji", "Fujifilm Fujicolor Pro 400H", "", false},
		{"paper edge", "Kodak Portra 400", "Kodak Ektacolor Edge", false},
		{"bw tri-x", "Kodak Tri-X Pan 400 (TX)", "", false},
		{"bw t-max", "Kodak T-Max 100", "", false},
		{"mono", "", "", true},
		{"generic agfa", "Agfa Agfacolor Optima 100", "", false},
		{"generic fuji", "Fujifilm Fujicolor Superia 100", "", false},
	}
	pinned := pinnedBytesHere(t)
	for _, c := range cases {
		p := preset.Neutral()
		p.Film, p.Paper, p.Mono = c.film, c.paper, c.mono
		p.NoGrain = true
		out := Render(b, p, comp)
		sum := sha256.Sum256(out.Pix)
		got := fmt.Sprintf("%x", sum[:12])
		t.Logf("%-16s %s %d", c.name, got, len(out.Pix))
		if want := t72NonSlideHash[c.name]; pinned && want != "" && got != want {
			t.Errorf("%s: hash %s, want %s (the v0.4.108 bytes)", c.name, got, want)
		}
	}
}
