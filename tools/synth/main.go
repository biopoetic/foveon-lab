// synth writes synthetic X3F test charts for measuring Sigma Photo Pro as a
// black box: a real file's container and calibration, its sensor margins
// (dark shields, so the black level stays real), and an active area filled
// with flat patches of known raw values. Export the chart from SPP and
// compare patch centres with what the raw values should give.
//
//	go run ./tools/synth -in SDIM0260.X3F -out chart.X3F [-grid 12x8] [-seed 1]
//
// It also writes chart.X3F.json: each patch's rectangle (in the active
// area's pixels) and its raw values above black (bottom, middle, top).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"

	"github.com/biopoetic/foveon-lab/x3f"
)

type patch struct {
	X0, Y0, X1, Y1 int        // in the active area, inclusive
	Raw            [3]float64 // above black, in raw units
}

func main() {
	in := flag.String("in", "", "source X3F (TRUE-coded: SD15/SD1/DP Merrill)")
	out := flag.String("out", "chart.X3F", "output X3F")
	cols := flag.Int("cols", 12, "patch columns")
	rows := flag.Int("rows", 8, "patch rows")
	seed := flag.Int64("seed", 1, "random seed for the colour patches")
	mod := flag.Bool("mod", false, "modulate the photo instead of replacing it: each patch multiplies the real raw data (above black) by per-layer gains, keeping its texture and noise; the JSON then holds the gains")
	neutralFlag := flag.String("neutral", "", "raw neutral b,m,t (top 1) to centre the patches on; default 1/WB gain")
	spread := flag.Float64("spread", 0.45, "per-layer spread around neutral, in stops (sigma)")
	stops := flag.Float64("stops", 6, "darkest patch, in stops below the peak (SPP renders the whole image black when large areas sit at the black level)")
	peak := flag.Float64("peak", 1, "brightest patch as a fraction of the raw range")
	frac := flag.Float64("frac", 1, "fraction of the active area (centred) the chart covers; the rest keeps the photo")
	flag.Parse()
	f, err := x3f.Open(*in)
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
	ax0, ay0, ax1, ay1 := 0, 0, raw.W-1, raw.H-1
	if a := c.Matrices["ActiveImageArea"]; len(a) == 4 {
		ax0, ay0, ax1, ay1 = int(a[0]), int(a[1]), int(a[2]), int(a[3])
	}
	// Black: the dark shields' mean, as x3f.CameraRGB measures it.
	var black [3]float64
	n := 0
	for _, name := range []string{"DarkShieldTop", "DarkShieldBottom"} {
		r := c.Matrices[name]
		if len(r) != 4 {
			continue
		}
		for y := int(r[1]); y <= int(r[3]); y++ {
			for x := int(r[0]); x <= int(r[2]); x++ {
				for k := 0; k < 3; k++ {
					black[k] += float64(raw.Pix[(y*raw.W+x)*3+k])
				}
				n++
			}
		}
	}
	for k := range black {
		black[k] /= float64(max(n, 1))
	}
	white := 4095.0
	if d := c.Matrices["ImageDepth"]; len(d) == 1 {
		white = math.Exp2(d[0]) - 1
	}
	span := (white - math.Max(black[0], math.Max(black[1], black[2]))) * *peak
	// The camera's raw neutral: what a grey surface gives under the shot's
	// white balance, i.e. 1/gain (normalised to a top of 1).
	neutral := [3]float64{1, 1, 1}
	wb := map[string]string{"Daylight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast", "Incandescent": "Incandescent",
		"Fluorescent": "Fluorescent", "Flash": "Flash", "Auto": "Auto", "Custom": "Custom", "Sunlight": "Sunlight"}[f.Props["WB_DESC"]]
	if *neutralFlag != "" {
		fmt.Sscanf(*neutralFlag, "%f,%f,%f", &neutral[0], &neutral[1], &neutral[2])
	} else if g := c.Matrices[c.Props["WhiteBalanceGains"][wb]]; len(g) == 3 {
		m := 0.0
		for k := range neutral {
			neutral[k] = 1 / g[k]
			m = math.Max(m, neutral[k])
		}
		for k := range neutral {
			neutral[k] /= m
		}
	}
	// Patch values: row 0 = a neutral-ish ramp (equal raw above black on
	// every layer, 12 steps on a log scale), the rest random triplets
	// (log-uniform level per layer, so dark and bright both get covered).
	rng := rand.New(rand.NewSource(*seed))
	aw, ah := ax1-ax0+1, ay1-ay0+1
	if *frac < 1 {
		cw, ch := int(float64(aw)**frac), int(float64(ah)**frac)
		ax0, ay0 = ax0+(aw-cw)/2, ay0+(ah-ch)/2
		aw, ah = cw, ch
	}
	var ps []patch
	for r := 0; r < *rows; r++ {
		for col := 0; col < *cols; col++ {
			var v [3]float64
			if r == 0 {
				lv := span * math.Pow(2, -*stops+*stops*float64(col)/float64(*cols-1))
				for k := range v {
					v[k] = lv * neutral[k]
				}
			} else {
				// A level 0-8 stops down, each layer off neutral by a
				// normal amount (sigma 0.45 stops): real-scene colours.
				lv := span * math.Pow(2, -*stops*rng.Float64())
				for k := range v {
					v[k] = math.Min(span, lv*neutral[k]*math.Pow(2, *spread*rng.NormFloat64()))
				}
			}
			if *mod {
				for k := range v {
					v[k] = v[k] / span * 2 // gain: up to 2x, down to 2^-stops
				}
			}
			p := patch{X0: col * aw / *cols, Y0: r * ah / *rows, X1: (col+1)*aw / *cols - 1, Y1: (r+1)*ah / *rows - 1, Raw: v}
			ps = append(ps, p)
			for y := p.Y0; y <= p.Y1; y++ {
				for x := p.X0; x <= p.X1; x++ {
					i := ((y+ay0)*raw.W + x + ax0) * 3
					for k := 0; k < 3; k++ {
						if *mod {
							// v is a gain here: 2^(-stops..+1) per layer.
							o := (float64(raw.Pix[i+k]) - black[k]) * v[k]
							raw.Pix[i+k] = uint16(math.Max(0, math.Min(white, math.Round(black[k]+o))))
							continue
						}
						// Realistic noise (shot + read): SPP renders the whole
						// image black when large areas have zero variance.
						nv := v[k] + math.Sqrt(v[k]*0.13+4)*rng.NormFloat64()
						raw.Pix[i+k] = uint16(math.Max(0, math.Min(white, math.Round(black[k]+nv))))
					}
				}
			}
		}
	}
	if err := f.WriteWithRaw(*out, raw); err != nil {
		log.Fatal(err)
	}
	js, _ := json.MarshalIndent(map[string]any{"source": *in, "black": black, "white": white, "active": []int{ax0, ay0, ax1, ay1}, "patches": ps}, "", " ")
	if err := os.WriteFile(*out+".json", js, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s: %d patches, black %.1f %.1f %.1f, white %.0f\n", *out, len(ps), black[0], black[1], black[2], white)
}
