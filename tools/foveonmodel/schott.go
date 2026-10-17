package main

// Real filter glass from SCHOTT's catalogue, read through the render
// package (render/schott.go) so the tool and the app share one loader and
// one file: render/schottglass.json. Used where made-up logistic edges
// stood: the infrared looks' lens filters are the glasses they always
// described (OG590, RG715), and B+W's filter numbers are glass numbers.

import (
	"log"

	"github.com/biopoetic/foveon-lab/render"
)

// schottT is the named glass's transmission T(l) = pd * ti(l).
func schottT(name string) func(l float64) float64 {
	T := render.SchottTransmission(name)
	if T == nil {
		log.Fatalf("schottglass.json: no glass %q", name)
	}
	return T
}
