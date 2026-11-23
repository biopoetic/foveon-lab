package main

// mfilm-compare (research): develops photos with Foveon Lab's own film model
// and, beside it, through M-Film's film tables (HNGSeries/mfilm-canon-film-
// simulation, the LOOKnn.LUT files of its release's tables zip), and writes
// the pairs and their differences. Nothing here is used by the app.
//
// M-Film's darkroom is GPL-2 and is not used: this follows its written
// description (ml-dev/DEVELOPING.md, steps 5 and 8) on our own camera-linear
// sRGB. Its steps 6 (vignette), 7 (halation) and 9 (grain) are left out on
// both sides, so the comparison is colour and tone only.

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
)

// mfilmTable is one LOOKnn.LUT: 33 points a side, red fastest, 8-bit sRGB.
type mfilmTable struct {
	n   int
	rgb []uint8
}

func loadMFilmTable(path string) (*mfilmTable, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	const n = 33
	if len(b) != 8+n*n*n*3 {
		return nil, fmt.Errorf("%s: %d bytes, want %d", path, len(b), 8+n*n*n*3)
	}
	return &mfilmTable{n: n, rgb: b[8:]}, nil
}

// at reads the table at a Cineon-coded place (0..1 each) through the
// tetrahedron the place falls in.
func (t *mfilmTable) at(r, g, b float64) (float64, float64, float64) {
	n := t.n - 1
	fr, fg, fb := r*float64(n), g*float64(n), b*float64(n)
	ir, ig, ib := min(n-1, int(fr)), min(n-1, int(fg)), min(n-1, int(fb))
	dr, dg, db := fr-float64(ir), fg-float64(ig), fb-float64(ib)
	px := func(a, bb, c int) [3]float64 {
		i := ((c*t.n+bb)*t.n + a) * 3
		return [3]float64{float64(t.rgb[i]), float64(t.rgb[i+1]), float64(t.rgb[i+2])}
	}
	c000 := px(ir, ig, ib)
	c111 := px(ir+1, ig+1, ib+1)
	var out [3]float64
	lerp := func(w0 float64, a [3]float64, w1 float64, bb [3]float64, w2 float64, c [3]float64, w3 float64, d [3]float64) {
		for k := 0; k < 3; k++ {
			out[k] = w0*a[k] + w1*bb[k] + w2*c[k] + w3*d[k]
		}
	}
	switch {
	case dr >= dg && dg >= db:
		lerp(1-dr, c000, dr-dg, px(ir+1, ig, ib), dg-db, px(ir+1, ig+1, ib), db, c111)
	case dr >= db && db >= dg:
		lerp(1-dr, c000, dr-db, px(ir+1, ig, ib), db-dg, px(ir+1, ig, ib+1), dg, c111)
	case db >= dr && dr >= dg:
		lerp(1-db, c000, db-dr, px(ir, ig, ib+1), dr-dg, px(ir+1, ig, ib+1), dg, c111)
	case dg >= dr && dr >= db:
		lerp(1-dg, c000, dg-dr, px(ir, ig+1, ib), dr-db, px(ir+1, ig+1, ib), db, c111)
	case dg >= db && db >= dr:
		lerp(1-dg, c000, dg-db, px(ir, ig+1, ib), db-dr, px(ir, ig+1, ib+1), dr, c111)
	default: // db >= dg >= dr
		lerp(1-db, c000, db-dg, px(ir, ig, ib+1), dg-dr, px(ir, ig+1, ib+1), dr, c111)
	}
	return out[0], out[1], out[2]
}

func cineonCode(x float64) float64 {
	v := (685 + 300*math.Log10(math.Max(1e-9, 0.9892*x+0.0108))) / 1023
	return math.Max(0, math.Min(1, v))
}

// mfilmDevelop is M-Film's steps 5 and 8 on camera-linear sRGB: the
// scanner's exposure (a negative's log-average key to 0.11; a slide +1 EV)
// and the table.
func mfilmDevelop(l *render.Linear, t *mfilmTable, slide bool) *image.RGBA {
	ev := 1.0
	if !slide {
		var s float64
		var n int
		step := max(1, l.W/180) // about every fourth pixel of a 720 px print
		for y := 0; y < l.H; y += step {
			for x := 0; x < l.W; x += step {
				i := (y*l.W + x) * 3
				lum := math.Min(1, 0.2126*float64(l.Pix[i])+0.7152*float64(l.Pix[i+1])+0.0722*float64(l.Pix[i+2]))
				s += math.Log(math.Max(0, lum) + 0.0001)
				n++
			}
		}
		key := math.Exp(s / float64(n))
		ev = math.Max(-2, math.Min(3.5, math.Log2(0.11/key)))
	}
	k := math.Exp2(ev)
	out := image.NewRGBA(image.Rect(0, 0, l.W, l.H))
	for j := 0; j < l.W*l.H; j++ {
		c := [3]float64{}
		for q := 0; q < 3; q++ {
			c[q] = cineonCode(math.Max(0, math.Min(16, float64(l.Pix[j*3+q])*k)))
		}
		r, g, b := t.at(c[0], c[1], c[2])
		o := out.Pix[j*4:]
		o[0], o[1], o[2], o[3] = uint8(r+0.5), uint8(g+0.5), uint8(b+0.5), 255
	}
	return out
}

// srgbLab converts an 8-bit sRGB pixel to CIELAB (D65).
func srgbLab(r, g, b uint8) (float64, float64, float64) {
	lin := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	R, G, B := lin(r), lin(g), lin(b)
	X := (0.4124*R + 0.3576*G + 0.1805*B) / 0.95047
	Y := 0.2126*R + 0.7152*G + 0.0722*B
	Z := (0.0193*R + 0.1192*G + 0.9505*B) / 1.08883
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(X), f(Y), f(Z)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// labStats is a picture's mean L*, mean chroma C*, and the mean Lab colour.
func labStats(img *image.RGBA) (L, C, a, b float64) {
	n := 0
	for i := 0; i+3 < len(img.Pix); i += 4 * 3 {
		l, aa, bb := srgbLab(img.Pix[i], img.Pix[i+1], img.Pix[i+2])
		L += l
		C += math.Hypot(aa, bb)
		a += aa
		b += bb
		n++
	}
	f := float64(n)
	return L / f, C / f, a / f, b / f
}

// meanDE76 is the mean CIE76 difference of two same-size pictures, on
// 8x8 block means (colour and tone, not grain or edges).
func meanDE76(p, q *image.RGBA) float64 {
	w, h := min(p.Rect.Dx(), q.Rect.Dx()), min(p.Rect.Dy(), q.Rect.Dy())
	var s float64
	n := 0
	for by := 0; by+8 <= h; by += 8 {
		for bx := 0; bx+8 <= w; bx += 8 {
			var c [2][3]float64
			for y := by; y < by+8; y++ {
				for x := bx; x < bx+8; x++ {
					for k, im := range []*image.RGBA{p, q} {
						i := y*im.Stride + x*4
						l, a, b := srgbLab(im.Pix[i], im.Pix[i+1], im.Pix[i+2])
						c[k][0] += l
						c[k][1] += a
						c[k][2] += b
					}
				}
			}
			s += math.Sqrt(math.Pow(c[0][0]-c[1][0], 2)+math.Pow(c[0][1]-c[1][1], 2)+math.Pow(c[0][2]-c[1][2], 2)) / 64
			n++
		}
	}
	return s / float64(n)
}

func mfilmCompareCommand(desk string) {
	fs := flag.NewFlagSet("mfilm-compare", flag.ExitOnError)
	photos := fs.String("photos", filepath.Join(desk, "foveon-samples"), "folder with the X3F files")
	names := fs.String("names", "", "comma-separated photo file names")
	tables := fs.String("tables", "", "folder with M-Film's LOOKnn.LUT and TABLES.txt")
	server := fs.String("server", "http://127.0.0.1:8797", "a running Foveon Lab that renders our side")
	pairs := fs.String("pairs", "", "ourFilm=mfilmKey[:slide];… (our film's full name as the Film menu has it)")
	width := fs.Int("w", 800, "picture width")
	out := fs.String("out", "", "output folder")
	fs.Parse(os.Args[2:])
	if *tables == "" || *out == "" || *pairs == "" || *names == "" {
		log.Fatal("usage: foveon-lab mfilm-compare -tables DIR -out DIR -names a.x3f,b.x3f -pairs 'Kodak Portra 400=portra400;…'")
	}
	os.MkdirAll(*out, 0o755)
	// TABLES.txt: LOOKnn.LUT  key  source
	files := map[string]string{}
	srcOf := map[string]string{}
	tb, err := os.ReadFile(filepath.Join(*tables, "TABLES.txt"))
	if err != nil {
		log.Fatal(err)
	}
	for _, line := range strings.Split(string(tb), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.HasPrefix(f[0], "LOOK") {
			files[f[1]], srcOf[f[1]] = filepath.Join(*tables, f[0]), f[2]
		}
	}
	type pair struct {
		ours, key string
		slide     bool
	}
	var ps []pair
	for _, s := range strings.Split(*pairs, ";") {
		ours, key, ok := strings.Cut(strings.TrimSpace(s), "=")
		if !ok {
			continue
		}
		key, sl := strings.CutSuffix(key, ":slide")
		ps = append(ps, pair{strings.TrimSpace(ours), strings.TrimSpace(key), sl})
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*names, ",") {
		want[strings.ToLower(strings.TrimSpace(n))] = true
	}
	type row struct {
		Photo, Ours, Key, Source                                       string
		DE, OursL, OursC, OursA, OursB, TheirL, TheirC, TheirA, TheirB float64
		OursFile, TheirFile                                            string
	}
	var rows []row
	for _, ph := range scanPhotos([]string{*photos}, false) {
		if !want[strings.ToLower(ph.Name)] {
			continue
		}
		l, err := goLinear(ph, *width*2, defaultNR)
		if err != nil {
			log.Printf("%s: %v", ph.Name, err)
			continue
		}
		l = uprightRaw(ph, l)
		stem := strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name))
		for _, p := range ps {
			// Our side, from the running app: exactly what the app shows.
			params := map[string]any{"film": p.ours, "noGrain": true}
			pj, _ := json.Marshal(params)
			u := fmt.Sprintf("%s/api/render?id=%s&w=%d&comp=1&p=%s", *server, ph.ID, *width, url.QueryEscape(string(pj)))
			resp, err := http.Get(u)
			if err != nil || resp.StatusCode != 200 {
				msg := ""
				if resp != nil {
					b, _ := io.ReadAll(resp.Body)
					msg = string(b)
					resp.Body.Close()
				}
				log.Printf("%s %s: render failed: %v %s", ph.Name, p.ours, err, msg)
				continue
			}
			im, _, err := image.Decode(resp.Body)
			resp.Body.Close()
			if err != nil {
				log.Printf("%s %s: %v", ph.Name, p.ours, err)
				continue
			}
			ours := image.NewRGBA(im.Bounds())
			for y := 0; y < im.Bounds().Dy(); y++ {
				for x := 0; x < im.Bounds().Dx(); x++ {
					ours.Set(x, y, im.At(im.Bounds().Min.X+x, im.Bounds().Min.Y+y))
				}
			}
			// Their side, on our camera-linear picture brought to the same size.
			t, err := loadMFilmTable(files[p.key])
			if err != nil {
				log.Printf("%s: %v", p.key, err)
				continue
			}
			src := fitAspect(l.Downscale(ours.Rect.Dx()), ours.Rect.Dx(), ours.Rect.Dy())
			theirs := mfilmDevelop(src, t, p.slide)
			of := fmt.Sprintf("%s__%s__ours.jpg", stem, p.key)
			tf := fmt.Sprintf("%s__%s__mfilm.jpg", stem, p.key)
			for f, img := range map[string]*image.RGBA{of: ours, tf: theirs} {
				w, _ := os.Create(filepath.Join(*out, f))
				jpeg.Encode(w, img, &jpeg.Options{Quality: 92})
				w.Close()
			}
			r := row{Photo: ph.Name, Ours: p.ours, Key: p.key, Source: srcOf[p.key], DE: meanDE76(ours, theirs), OursFile: of, TheirFile: tf}
			r.OursL, r.OursC, r.OursA, r.OursB = labStats(ours)
			r.TheirL, r.TheirC, r.TheirA, r.TheirB = labStats(theirs)
			rows = append(rows, r)
			fmt.Printf("%-22s %-34s %-14s %-11s dE %5.1f | L* %4.1f/%4.1f  C* %4.1f/%4.1f  a* %+5.1f/%+5.1f  b* %+5.1f/%+5.1f\n",
				ph.Name, p.ours, p.key, srcOf[p.key], r.DE, r.OursL, r.TheirL, r.OursC, r.TheirC, r.OursA, r.TheirA, r.OursB, r.TheirB)
		}
	}
	js, _ := json.MarshalIndent(rows, "", " ")
	os.WriteFile(filepath.Join(*out, "compare.json"), js, 0o644)
	// A page to look at the pairs side by side.
	var sb strings.Builder
	sb.WriteString(`<!doctype html><meta charset="utf-8"><title>Foveon Lab vs M-Film</title><style>body{background:#111;color:#ddd;font:13px sans-serif;margin:12px}h2{font-size:15px;margin:22px 0 6px}.p{display:flex;gap:8px;flex-wrap:wrap}.p div{flex:1 1 380px;max-width:49%}img{width:100%;display:block}small{color:#999}</style><h1 style="font-size:17px">Foveon Lab's film model (left) and M-Film's tables (right), on the same camera data</h1><p>Colour and tone only: no grain, halation or vignette on either side. The M-Film side follows its documented exposure (negatives: log-average key to 0.11; slides +1 EV) and its 33×33×33 tables. ΔE is CIE76 on 8×8 blocks.</p>`)
	for _, r := range rows {
		fmt.Fprintf(&sb, `<h2>%s · %s <small>vs M-Film %s (%s) · ΔE %.1f · L* %.0f/%.0f · C* %.0f/%.0f</small></h2><div class="p"><div><img src="%s"><small>Foveon Lab</small></div><div><img src="%s"><small>M-Film table</small></div></div>`,
			r.Photo, r.Ours, r.Key, r.Source, r.DE, r.OursL, r.TheirL, r.OursC, r.TheirC, r.OursFile, r.TheirFile)
	}
	os.WriteFile(filepath.Join(*out, "index.html"), []byte(sb.String()), 0o644)
	fmt.Println("wrote", filepath.Join(*out, "index.html"))
}
