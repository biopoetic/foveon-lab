package calib

// Research: how much of the raw base (a 3×3 matrix and a tone curve per
// channel, see render.RawCorrection) survives as a DNG camera profile, i.e.
// as what Lightroom / Camera Raw apply: a ForwardMatrix and ONE tone curve,
// applied to linear ProPhoto RGB with Adobe's hue-preserving RGB tone
// (DNG SDK RefBaselineRGBTone). Measured against SPP's exports, held out.

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/biopoetic/foveon-lab/render"
)

// Linear sRGB (D65) <-> linear ProPhoto RGB (D50), Bradford adapted.
var (
	srgbToXYZD50 = [9]float64{
		0.4360747, 0.3850649, 0.1430804,
		0.2225045, 0.7168786, 0.0606169,
		0.0139322, 0.0971045, 0.7141733,
	}
	proPhotoToXYZ = [9]float64{
		0.7976749, 0.1351917, 0.0313534,
		0.2880402, 0.7118741, 0.0000857,
		0, 0, 0.8252100,
	}
	srgbToProPhoto, proPhotoToSRGB [9]float64
)

func init() {
	inv, _ := inverse3(to33(proPhotoToXYZ))
	srgbToProPhoto = mul9(from33(inv), srgbToXYZD50)
	back, _ := inverse3(to33(srgbToProPhoto))
	proPhotoToSRGB = from33(back)
}

func to33(m [9]float64) (o [3][3]float64) {
	for i := range 9 {
		o[i/3][i%3] = m[i]
	}
	return
}

func from33(m [3][3]float64) (o [9]float64) {
	for i := range 9 {
		o[i] = m[i/3][i%3]
	}
	return
}

func mul9(a, b [9]float64) (c [9]float64) {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				c[i*3+j] += a[i*3+k] * b[k*3+j]
			}
		}
	}
	return
}

func apply9(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

// toneLUT is a tone curve on linear 0..1, sampled at 1024 points.
type toneLUT [1025]float64

func (t *toneLUT) at(x float64) float64 {
	x = math.Max(0, math.Min(1, x)) * 1024
	i := min(1023, int(x))
	f := x - float64(i)
	return t[i]*(1-f) + t[i+1]*f
}

// inverse: the x with at(x) = y (the curve is made monotone first).
func (t *toneLUT) inverse(y float64) float64 {
	i := sort.Search(1025, func(i int) bool { return t[i] >= y })
	if i == 0 {
		return 0
	}
	if i > 1024 {
		return 1
	}
	f := (y - t[i-1]) / math.Max(1e-12, t[i]-t[i-1])
	return (float64(i-1) + f) / 1024
}

// adobeTone is the DNG SDK's RefBaselineRGBTone: the largest and smallest
// channels go through the curve, the middle one keeps its relative
// position between them (so hue is preserved).
func adobeTone(p [3]float64, t *toneLUT) [3]float64 {
	idx := [3]int{0, 1, 2}
	sort.Slice(idx[:], func(a, b int) bool { return p[idx[a]] > p[idx[b]] })
	hi, mid, lo := p[idx[0]], p[idx[1]], p[idx[2]]
	th, tl := t.at(hi), t.at(lo)
	tm := tl
	if hi > lo {
		tm = tl + (th-tl)*(mid-lo)/(hi-lo)
	}
	var o [3]float64
	o[idx[0]], o[idx[1]], o[idx[2]] = th, tm, tl
	return o
}

// neutralTone is the raw base's tone curve for grey input (the mean of its
// three channel curves), as a linear-in, linear-out LUT.
func neutralTone(c *render.RawCorrection) *toneLUT {
	id := *c
	id.Matrix = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	var t toneLUT
	prev := 0.0
	for i := range t {
		x := float32(float64(i) / 1024)
		r, g, b := id.Pixel(x, x, x)
		v := math.Max(prev, (float64(r)+float64(g)+float64(b))/3) // monotone
		t[i], prev = v, v
	}
	return &t
}

// profileRender renders linear sRGB l as a DNG profile would: matrix m
// (linear sRGB in and out), then tone t on linear ProPhoto.
func profileRender(l *render.Linear, m [9]float64, t *toneLUT) *render.Linear {
	o := &render.Linear{W: l.W, H: l.H, Pix: make([]float32, len(l.Pix))}
	pm := mul9(srgbToProPhoto, m)
	for i := 0; i < len(l.Pix); i += 3 {
		v := apply9(pm, [3]float64{float64(l.Pix[i]), float64(l.Pix[i+1]), float64(l.Pix[i+2])})
		for k := range v {
			v[k] = math.Max(0, math.Min(1, v[k]))
		}
		s := apply9(proPhotoToSRGB, adobeTone(v, t))
		for k := range s {
			o.Pix[i+k] = float32(math.Max(0, math.Min(1, s[k])))
		}
	}
	return o
}

// fitProfileMatrix refits the matrix for a given tone t: SPP's output,
// taken back through t (per channel, in ProPhoto) to linear sRGB, regressed
// on the raw decode.
func fitProfileMatrix(pairs []RawPair, t *toneLUT) [9]float64 {
	var ata, atb [3][3]float64
	for _, p := range pairs {
		for i := 0; i < len(p.SPP.Pix); i += 6 {
			if max(p.SPP.Pix[i], p.SPP.Pix[i+1], p.SPP.Pix[i+2], p.Raw.Pix[i], p.Raw.Pix[i+1], p.Raw.Pix[i+2]) > 0.95 {
				continue
			}
			pp := apply9(srgbToProPhoto, [3]float64{float64(p.SPP.Pix[i]), float64(p.SPP.Pix[i+1]), float64(p.SPP.Pix[i+2])})
			for k := range pp {
				pp[k] = t.inverse(math.Max(0, pp[k]))
			}
			y := apply9(proPhotoToSRGB, pp)
			x := [3]float64{float64(p.Raw.Pix[i]), float64(p.Raw.Pix[i+1]), float64(p.Raw.Pix[i+2])}
			for r := 0; r < 3; r++ {
				for q := 0; q < 3; q++ {
					ata[r][q] += x[r] * x[q]
					atb[r][q] += y[r] * x[q]
				}
			}
		}
	}
	inv, ok := inverse3(ata)
	if !ok {
		return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	}
	var m [9]float64
	for r := 0; r < 3; r++ {
		for q := 0; q < 3; q++ {
			for k := 0; k < 3; k++ {
				m[r*3+q] += atb[r][k] * inv[k][q]
			}
		}
	}
	return m
}

// DNGProfileReport prints, held out per photo, the dE2000 vs SPP of the
// full raw base and of its DNG-profile forms.
func DNGProfileReport(family string, pairs []RawPair, w io.Writer) {
	var full, keep, refit float64
	n := 0
	for h := range pairs {
		if pairs[h].TrainOnly {
			continue
		}
		var train []RawPair
		for i := range pairs {
			if i != h {
				train = append(train, pairs[i])
			}
		}
		fc := fitRaw(train)
		t := neutralTone(&fc)
		a, _ := DeltaEStats(applied(&fc, pairs[h].Raw), pairs[h].SPP)
		b, _ := DeltaEStats(profileRender(pairs[h].Raw, fc.Matrix, t), pairs[h].SPP)
		c, _ := DeltaEStats(profileRender(pairs[h].Raw, fitProfileMatrix(train, t), t), pairs[h].SPP)
		fmt.Fprintf(w, "    %-36s full %5.2f   profile (same matrix) %5.2f   profile (refit matrix) %5.2f\n", pairs[h].Name, a, b, c)
		full, keep, refit, n = full+a, keep+b, refit+c, n+1
	}
	if n > 0 {
		fmt.Fprintf(w, "%s held out, dE2000 mean vs SPP: raw base %.2f | DNG profile, same matrix %.2f | DNG profile, refit matrix %.2f (%d photos)\n",
			family, full/float64(n), keep/float64(n), refit/float64(n), n)
	}
}
