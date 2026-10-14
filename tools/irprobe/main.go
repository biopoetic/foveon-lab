// irprobe prints, over a grid of cells, an X3F's sensor layers balanced by
// the camera's Sunlight gains (bottom, middle, top; the input the infrared
// looks take), for calibrating the silicon model on full-spectrum shots.
//
//	go run ./tools/irprobe [-grid 12x8] file.X3F
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	gx := flag.Int("gx", 12, "cells across")
	gy := flag.Int("gy", 8, "cells down")
	flag.Parse()
	for _, p := range flag.Args() {
		f, err := x3f.Open(p)
		if err != nil {
			log.Fatal(err)
		}
		c, err := f.CAMF()
		if err != nil {
			log.Fatal(err)
		}
		raw, err := f.DecodeRaw()
		if err != nil {
			log.Fatal(err)
		}
		x3f.FixBadPixels(raw, c)
		img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, "Sunlight"), "Sunlight")
		if err != nil {
			log.Fatal(err)
		}
		gain, _, err := c.WBTransform("Sunlight")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("== %s  %dx%d  Sunlight gains %.3f\n", p, img.W, img.H, gain)
		fmt.Println("cell (col,row): bottom middle top | bottom/top middle/top   (clipped share)")
		for cy := 0; cy < *gy; cy++ {
			for cx := 0; cx < *gx; cx++ {
				x0, x1 := cx*img.W / *gx, (cx+1)*img.W / *gx
				y0, y1 := cy*img.H / *gy, (cy+1)*img.H / *gy
				var s [3]float64
				var n, clip int
				for y := y0; y < y1; y += 2 {
					for x := x0; x < x1; x += 2 {
						px := img.Pix[(y*img.W+x)*3:]
						if px[0] > 0.98 || px[1] > 0.98 || px[2] > 0.98 {
							clip++
							continue
						}
						for k := 0; k < 3; k++ {
							s[k] += float64(px[k]) * gain[k]
						}
						n++
					}
				}
				if n == 0 {
					continue
				}
				for k := range s {
					s[k] /= float64(n)
				}
				fmt.Printf("(%2d,%d): %.4f %.4f %.4f | %.2f %.2f   (%.0f%%)\n", cx, cy, s[0], s[1], s[2], s[0]/s[2], s[1]/s[2], 100*float64(clip)/float64(n+clip))
			}
		}
	}
}
