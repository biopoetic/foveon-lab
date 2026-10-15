// Command wbcodes fits SPP's preset WhiteBalance codes from exports of the
// same photo at "as shot" (K01) and at each code (K02..K07): the colour
// mapping K01 -> Kxx, as the calibration fits its W steps. Each photo is
// predicted by a fit on the other photo(s) (held out). Prints the mapping
// as JSON per code (render.RawCorrection) for render/model*.json "wb".
//
//	go run ./tools/wbcodes -dir <exports\wb-codes> -photos SDIM0031,SDIM0363
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
)

func main() {
	dir := flag.String("dir", "", "folder with <photo>_Kxx.tif exports")
	photos := flag.String("photos", "", "photo names, comma separated")
	codes := flag.String("codes", "2,3,4,5,6,7", "WhiteBalance codes")
	flag.Parse()
	names := strings.Split(*photos, ",")
	load := func(n string, code int) *render.Linear {
		l, err := calib.LoadLinear(filepath.Join(*dir, fmt.Sprintf("%s_K%02d.tif", n, code)))
		if err != nil {
			log.Fatal(err)
		}
		return l
	}
	out := map[string]render.RawCorrection{}
	for _, cs := range strings.Split(*codes, ",") {
		var code int
		fmt.Sscan(cs, &code)
		var pairs []calib.RawPair
		for _, n := range names {
			pairs = append(pairs, calib.RawPair{Raw: load(n, 1), SPP: load(n, code)})
		}
		all := calib.FitMapping(pairs)
		var held, before []string
		for i, p := range pairs {
			var rest []calib.RawPair
			for j, q := range pairs {
				if j != i {
					rest = append(rest, q)
				}
			}
			f := calib.FitMapping(rest)
			cp := *p.Raw
			cp.Pix = append([]float32(nil), p.Raw.Pix...)
			pred := &cp
			f.Apply(pred)
			held = append(held, fmt.Sprintf("%.2f", calib.MeanError(pred, p.SPP)))
			before = append(before, fmt.Sprintf("%.2f", calib.MeanError(p.Raw, p.SPP)))
		}
		fmt.Fprintf(os.Stderr, "code %d: as shot vs code %s/255 -> held-out fit %s/255\n", code, strings.Join(before, " "), strings.Join(held, " "))
		out[cs] = all
	}
	b, _ := json.MarshalIndent(out, "", " ")
	fmt.Println(string(b))
}
