package render

// A tint with a colour film on must stay a tint. It used to go in front of
// the film, whose contrast turned [NEG] SD1 Gold 200 Punch's 0.78 blue into a
// green picture on every colour film.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func TestFilmTintStaysATint(t *testing.T) {
	cc := [9]float64{2.2, -1.0, -0.2, -0.5, 1.9, -0.4, 0.1, -0.9, 1.8}
	l := &Linear{W: 3, H: 1, Pix: []float32{
		0.18, 0.18, 0.18,
		0.10, 0.20, 0.45,
		0.30, 0.12, 0.08,
	}}
	c := Compensation{SatFactor: 1, CC: &cc, Sensor: "Merrill"}
	for _, film := range []string{"Fujifilm Fujichrome Velvia 100", "Kodak Ektar 100"} {
		plain := preset.Neutral()
		plain.Film = film
		tinted := plain
		tinted.R, tinted.B = 0.94, 0.78
		a := Render(NewBase(l), plain, c)
		b := Render(NewBase(l), tinted, c)
		for i := 0; i < 3; i++ {
			for ch := 0; ch < 3; ch++ {
				if d := math.Abs(float64(a.Pix[i*4+ch]) - float64(b.Pix[i*4+ch])); d > 45 {
					t.Errorf("%s pixel %d channel %d: %d -> %d", film, i, ch, a.Pix[i*4+ch], b.Pix[i*4+ch])
				}
			}
		}
	}
}
