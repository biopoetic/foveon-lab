package calib

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"foveon-lab/render"
	"foveon-lab/x3f"
)

// Candidate is an image scored for calibration usefulness.
type Candidate struct {
	Path   string
	Camera string
	Score  float64
	Why    string
}

// Suggest scores X3F files by how well they cover the tonal range and
// colours (wide brightness spread, real shadows and highlights, saturated
// colour, little clipping), using only the embedded thumbnails.
func Suggest(paths []string) []Candidate {
	var out []Candidate
	for _, p := range paths {
		f, err := x3f.Open(p)
		if err != nil {
			continue
		}
		data, err := f.ThumbJPEG()
		if err != nil {
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		l := render.FromImage(img, 0, 0)
		n := l.W * l.H
		vs := make([]float64, n)
		var dark, bright, clip, chroma float64
		for i := 0; i < n; i++ {
			j := i * 3
			y := luma(l.Pix, j)
			v := float64(render.ToSRGB(y))
			vs[i] = v
			if v < 0.12 {
				dark++
			}
			if v > 0.8 {
				bright++
			}
			for c := 0; c < 3; c++ {
				if l.Pix[j+c] > 0.99 {
					clip++
					break
				}
			}
			var c2 float64
			for c := 0; c < 3; c++ {
				c2 += math.Pow(float64(render.ToSRGB(l.Pix[j+c]))-v, 2)
			}
			chroma += math.Sqrt(c2)
		}
		sort.Float64s(vs)
		spread := vs[n*98/100] - vs[n*2/100]
		fd, fb, fc := dark/float64(n), bright/float64(n), clip/float64(n)
		mc := chroma / float64(n)
		score := spread + math.Min(fd, 0.15) + math.Min(fb, 0.15) + 2*mc - 3*fc
		out = append(out, Candidate{
			Path: p, Camera: f.Camera(), Score: score,
			Why: fmt.Sprintf("raspon %.0f%%, sjene %.0f%%, svjetla %.0f%%, boja %.2f, pregorjelo %.1f%%", spread*100, fd*100, fb*100, mc, fc*100),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// WritePack creates dir with the SPP preset XML, instructions and an empty
// export folder. It never overwrites the export folder's contents.
func WritePack(dir string, candidates []Candidate) (string, error) {
	if err := os.MkdirAll(filepath.Join(dir, "izvoz"), 0o755); err != nil {
		return "", err
	}
	steps := Steps()
	xmlPath := filepath.Join(dir, "FoveonLab_KALIBRACIJA.xml")
	if err := os.WriteFile(xmlPath, []byte(PackXML(steps)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "UPUTE.txt"), []byte(Instructions(steps, candidates)), 0o644); err != nil {
		return "", err
	}
	return xmlPath, nil
}

// Instructions is the Croatian how-to shipped with the pack.
func Instructions(steps []Step, cands []Candidate) string {
	var b strings.Builder
	b.WriteString(`FOVEON LAB — KALIBRACIJA SPP-a
================================

Cilj: izmjeriti što Sigma Photo Pro točno radi s pojedinim klizačem, da
Foveon Lab emulira SPP krivuljama izmjerenim na tvojim slikama, umjesto
procjenom. Svaki [CAL] preset mijenja SAMO JEDNU postavku; sve ostalo je
jednako neutralnom presetu A00.

KORACI
1. U SPP-u uvezi "FoveonLab_KALIBRACIJA.xml" (isto kao ostale XML
   presete). Uvezi ga SAMO JEDNOM — ponovni uvoz duplicira presete.
2. Otvori jednu sliku (prijedlozi dolje). Uvijek ISTU sliku.
3. Za svaki kod redom: primijeni preset "[CAL] <kod> ...", pa spremi kao
   TIFF u mapu "izvoz" pored ove datoteke, s imenom:

       <ime slike>_<kod>.tif      npr.  SDIM0031_A00.tif, SDIM0031_A03.tif

   Postavke spremanja — ISTE za sve izvoze:
     - TIFF (8 ili 16 bit, svejedno), prostor boja sRGB
     - puna veličina, bez rezanja i promjene veličine
   (JPEG najviše kvalitete također radi, TIFF je bolji.)
4. Kad završiš, pokreni:   foveon-lab.exe analiza
   ili mi samo javi da je gotovo — pokrenut ću ja.

NAJVAŽNIJE: A00 (neutralno) mora postojati — sve se mjeri prema njemu.
Nemoj ručno dirati klizače između izvoza; samo klikni preset i spremi.

PRIORITETI
  A00–A22  obavezno (23 izvoza, jedna slika) — pokriva sve glavne klizače
  B01–B22  poželjno — krajnje vrijednosti, Sharpness
  C00–C12  istraživanje: što znače nepoznati ColorMode kodovi. Kad
           primijeniš C preset, zapiši što SPP pokazuje u izborniku
           Color Mode (npr. "C05 = Vivid") u datoteku izvoz\colormode.txt
  Ako imaš vremena: ponovi A00–A22 i na drugoj slici (ili SD15 slici) —
  SPP može drukčije obrađivati SD1 i SD15.

`)
	if len(cands) > 0 {
		b.WriteString("PREDLOŽENE SLIKE (širok raspon tonova i boja, malo pregorjelog):\n")
		per := map[string]int{}
		for _, c := range cands {
			if per[c.Camera] >= 3 {
				continue
			}
			per[c.Camera]++
			fmt.Fprintf(&b, "  %-5s %s\n        %s\n", c.Camera, c.Path, c.Why)
		}
		b.WriteString("\n")
	}
	b.WriteString("POPIS PRESETA\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "  %s  %s\n", s.Code, s.Label)
	}
	return b.String()
}
