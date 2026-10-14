package main

// Black-and-white film emulation: a film's spectral sensitivity (from its
// data sheet, log sensitivity per 10 nm, 400-700 nm) as a weighted sum of
// the Foveon layers. The weights are fitted, for each sensor generation,
// so that the white-balanced layers reproduce the film's exposure of 400
// reflectance spectra in daylight (each normalised so white = 1): the grey
// then sees colours as that film does (a film blind to red renders a red
// apple dark, a blue-sensitive one renders the sky light).
//
//	FILMBW=spectral.json OUT=filmbw.json go run ./tools/foveonmodel

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
)

func filmBW(in, out string) {
	data, err := os.ReadFile(in)
	if err != nil {
		log.Fatal(err)
	}
	var films map[string]struct {
		Src  string     `json:"src"`
		LogS []*float64 `json:"logS"`
	}
	if err := json.Unmarshal(data, &films); err != nil {
		log.Fatal(err)
	}
	names := make([]string, 0, len(films))
	for n := range films {
		names = append(names, n)
	}
	sort.Strings(names)
	sc := newScene(5500, 400, 1)
	res := map[string]map[string][3]float64{}
	for _, name := range names {
		f := films[name]
		// The film's sensitivity on the model's wavelength grid (zero
		// where the sheet has no value, i.e. beyond its red cut-off).
		sf := make([]float64, len(lams))
		for i, l := range lams {
			x := (l - 400) / 10
			k := int(math.Floor(x))
			if k < 0 || k+1 >= len(f.LogS) || f.LogS[k] == nil || f.LogS[k+1] == nil {
				if k >= 0 && k < len(f.LogS) && f.LogS[k] != nil && x == float64(k) {
					sf[i] = math.Pow(10, *f.LogS[k])
				}
				continue
			}
			t := x - float64(k)
			sf[i] = math.Pow(10, *f.LogS[k]*(1-t)+*f.LogS[k+1]*t)
		}
		var wht float64
		for i := range lams {
			wht += sf[i] * sc.illum[i]
		}
		res[name] = map[string][3]float64{}
		for _, gen := range []string{"Merrill", "TRUEII", "SD9", "Quattro"} {
			m := loadModel(gen)
			L := sc.layers(m)
			var ata [3][3]float64
			var atb [3]float64
			H := make([]float64, len(sc.refl))
			for j, r := range sc.refl {
				for i := range lams {
					H[j] += sf[i] * sc.illum[i] * r[i]
				}
				H[j] /= wht
				for p := 0; p < 3; p++ {
					atb[p] += L[j][p] * H[j]
					for q := 0; q < 3; q++ {
						ata[p][q] += L[j][p] * L[j][q]
					}
				}
			}
			inv := inv3(ata)
			var w [3]float64
			for p := 0; p < 3; p++ {
				for q := 0; q < 3; q++ {
					w[p] += inv[p][q] * atb[q]
				}
			}
			var e float64
			for j := range sc.refl {
				d := w[0]*L[j][0] + w[1]*L[j][1] + w[2]*L[j][2] - H[j]
				e += d * d
			}
			fmt.Printf("%-20s %-8s weights bottom/middle/top %.3f %.3f %.3f  (sum %.3f) fit RMS %.4f\n", name, gen, w[0], w[1], w[2], w[0]+w[1]+w[2], math.Sqrt(e/float64(len(sc.refl))))
			res[name][gen] = w
		}
	}
	js, _ := json.MarshalIndent(res, "", " ")
	if err := os.WriteFile(out, js, 0o644); err != nil {
		log.Fatal(err)
	}
}
