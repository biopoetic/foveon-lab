// dngcheck writes an X3F's camera DNG (with its SPP-neutral profile, as the
// app's raw DNG export does), then develops that DNG the way the DNG spec
// tells a reader to (Lightroom / Camera Raw): white balance by
// AsShotNeutral, ForwardMatrix1 to XYZ D50, BaselineExposure, linear
// ProPhoto RGB clipped to 0..1, ProfileToneCurve with the DNG SDK's
// hue-preserving RGB tone, then sRGB. It prints the dE2000 of that
// development against the photo's SPP neutral export, next to the app's
// own raw-base preview.
//
//	go run ./tools/dngcheck <file.X3F> <SPP export .tif> [more pairs…]
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	if len(os.Args) < 3 || len(os.Args)%2 != 1 {
		log.Fatal("usage: dngcheck <file.X3F> <SPP export> [<file.X3F> <SPP export>…]")
	}
	for i := 1; i+1 < len(os.Args); i += 2 {
		if err := check(os.Args[i], os.Args[i+1]); err != nil {
			fmt.Println(os.Args[i], err)
		}
	}
}

func check(path, export string) error {
	f, err := x3f.Open(path)
	if err != nil {
		return err
	}
	c, err := f.CAMF()
	if err != nil {
		return err
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		return err
	}
	x3f.FixBadPixels(raw, c)
	wb := c.CameraWB(strings.TrimSpace(f.Props["WB_DESC"]))
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return err
	}
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return err
	}
	group := x3f.CalibGroup(f.Props["CAMMODEL"])
	base := render.RawBaseFitted(group)
	if base == nil {
		return fmt.Errorf("no raw base for %q", group)
	}
	var buf bytes.Buffer
	if err := render.WriteCameraDNG(&buf, img.Pix, img.W, img.H, gain, cc, base.DNGProfile("check"), "SIGMA", "x", "dngcheck"); err != nil {
		return err
	}
	spp, err := calib.LoadLinear(export)
	if err != nil {
		return err
	}
	dng, err := develop(buf.Bytes(), spp.W)
	if err != nil {
		return err
	}
	// The app's own preview from the same data.
	app := render.FromCamLinear(img.Pix, img.W, img.H, spp.W, 0, render.CamTransform{Gain: gain, CC: cc})
	base.Apply(app)
	dng, app = centreCrop(dng, spp.W, spp.H), centreCrop(app, spp.W, spp.H)
	a, _ := calib.DeltaEStats(dng, spp)
	b, _ := calib.DeltaEStats(app, spp)
	d, _ := calib.DeltaEStats(dng, app)
	fmt.Printf("%-28s %-8s dE2000 vs SPP: DNG as a reader develops it %.2f | app raw base %.2f | DNG vs app %.2f\n",
		path[strings.LastIndexAny(path, `/\`)+1:], group, a, b, d)
	return nil
}

// develop renders our LinearRaw DNG as the spec describes, box-averaged to
// width w first (linear, so averaging commutes with the matrices).
func develop(b []byte, w int) (*render.Linear, error) {
	le := binary.LittleEndian
	ifd := int(le.Uint32(b[4:]))
	tags := map[uint16][]float64{}
	var width, height, stripOff, rowsPer int
	var offsets []int
	for i := 0; i < int(le.Uint16(b[ifd:])); i++ {
		e := b[ifd+2+12*i:]
		tag, typ, cnt := le.Uint16(e), le.Uint16(e[2:]), int(le.Uint32(e[4:]))
		val := le.Uint32(e[8:])
		switch tag {
		case 256:
			width = int(val)
		case 257:
			height = int(val)
		case 278:
			rowsPer = int(val)
		case 273:
			if cnt == 1 {
				offsets = []int{int(val)}
			} else {
				for k := 0; k < cnt; k++ {
					offsets = append(offsets, int(le.Uint32(b[int(val)+4*k:])))
				}
			}
		}
		var vs []float64
		for k := 0; k < cnt; k++ {
			switch typ {
			case 5, 10: // (s)rational
				p := b[int(val)+8*k:]
				if typ == 10 {
					vs = append(vs, float64(int32(le.Uint32(p)))/float64(int32(le.Uint32(p[4:]))))
				} else {
					vs = append(vs, float64(le.Uint32(p))/float64(le.Uint32(p[4:])))
				}
			case 11: // float
				o := int(val) + 4*k
				if cnt == 1 {
					o = ifd + 2 + 12*i + 8
				}
				vs = append(vs, float64(math.Float32frombits(le.Uint32(b[o:]))))
			}
		}
		tags[tag] = vs
	}
	_ = stripOff
	neutral, fm, be, tone := tags[50728], tags[50964], tags[50730], tags[50940]
	if len(neutral) != 3 || len(fm) != 9 || len(be) != 1 || len(tone) < 4 {
		return nil, fmt.Errorf("DNG lacks neutral/forward matrix/baseline exposure/tone")
	}
	// Box-average the camera data to w wide.
	s := float64(w) / float64(width)
	ow, oh := w, max(1, int(math.Round(float64(height)*s)))
	sum := make([]float64, ow*oh*3)
	cnt := make([]float64, ow*oh)
	for y := 0; y < height; y++ {
		strip, r := y/rowsPer, y%rowsPer
		row := b[offsets[strip]+r*width*6:]
		oy := min(oh-1, y*oh/height)
		for x := 0; x < width; x++ {
			ox := min(ow-1, x*ow/width)
			j := oy*ow + ox
			for k := 0; k < 3; k++ {
				sum[j*3+k] += float64(le.Uint16(row[(x*3+k)*2:])) / 65535
			}
			cnt[j]++
		}
	}
	// Reader: camera / neutral -> FM -> XYZ D50 × 2^BE -> ProPhoto -> clip
	// -> tone -> sRGB.
	proToXYZ := [9]float64{0.7976749, 0.1351917, 0.0313534, 0.2880402, 0.7118741, 0.0000857, 0, 0, 0.8252100}
	xyzToPro := inv3(proToXYZ)
	srgbToXYZD50 := [9]float64{0.4360747, 0.3850649, 0.1430804, 0.2225045, 0.7168786, 0.0606169, 0.0139322, 0.0971045, 0.7141733}
	proToSRGB := mul(inv3(srgbToXYZD50), proToXYZ)
	var fm9 [9]float64
	copy(fm9[:], fm)
	camToPro := mul(xyzToPro, fm9)
	ex := math.Exp2(be[0])
	out := &render.Linear{W: ow, H: oh, Pix: make([]float32, ow*oh*3)}
	for j := 0; j < ow*oh; j++ {
		if cnt[j] == 0 {
			continue
		}
		var cam [3]float64
		for k := 0; k < 3; k++ {
			cam[k] = sum[j*3+k] / cnt[j] / neutral[k]
		}
		p := apply(camToPro, cam)
		for k := range p {
			p[k] = math.Max(0, math.Min(1, p[k]*ex))
		}
		o := apply(proToSRGB, rgbTone(p, tone))
		for k := 0; k < 3; k++ {
			out.Pix[j*3+k] = float32(math.Max(0, math.Min(1, o[k])))
		}
	}
	return out, nil
}

// curveAt interpolates the ProfileToneCurve pairs.
func curveAt(t []float64, x float64) float64 {
	n := len(t) / 2
	i := sort.Search(n, func(i int) bool { return t[2*i] >= x })
	if i == 0 {
		return t[1]
	}
	if i >= n {
		return t[2*n-1]
	}
	x0, y0, x1, y1 := t[2*i-2], t[2*i-1], t[2*i], t[2*i+1]
	return y0 + (y1-y0)*(x-x0)/math.Max(1e-12, x1-x0)
}

// rgbTone is the DNG SDK's RefBaselineRGBTone.
func rgbTone(p [3]float64, t []float64) [3]float64 {
	idx := [3]int{0, 1, 2}
	sort.Slice(idx[:], func(a, b int) bool { return p[idx[a]] > p[idx[b]] })
	hi, mid, lo := p[idx[0]], p[idx[1]], p[idx[2]]
	th, tl := curveAt(t, hi), curveAt(t, lo)
	tm := tl
	if hi > lo {
		tm = tl + (th-tl)*(mid-lo)/(hi-lo)
	}
	var o [3]float64
	o[idx[0]], o[idx[1]], o[idx[2]] = th, tm, tl
	return o
}

func centreCrop(l *render.Linear, w, h int) *render.Linear {
	if l.W == w && l.H == h {
		return l
	}
	ch := int(math.Round(float64(l.W) * float64(h) / float64(w)))
	if ch > l.H {
		ch = l.H
	}
	y0 := (l.H - ch) / 2
	o := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		sy := y0 + y*ch/h
		for x := 0; x < w; x++ {
			sx := x * l.W / w
			copy(o.Pix[(y*w+x)*3:(y*w+x)*3+3], l.Pix[(sy*l.W+sx)*3:])
		}
	}
	return o
}

func mul(a, b [9]float64) (c [9]float64) {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				c[i*3+j] += a[i*3+k] * b[k*3+j]
			}
		}
	}
	return c
}

func apply(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{m[0]*v[0] + m[1]*v[1] + m[2]*v[2], m[3]*v[0] + m[4]*v[1] + m[5]*v[2], m[6]*v[0] + m[7]*v[1] + m[8]*v[2]}
}

func inv3(m [9]float64) [9]float64 {
	a, b, c, d, e, f, g, h, i := m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	return [9]float64{(e*i - f*h) / det, (c*h - b*i) / det, (b*f - c*e) / det,
		(f*g - d*i) / det, (a*i - c*g) / det, (c*d - a*f) / det,
		(d*h - e*g) / det, (b*g - a*h) / det, (a*e - b*d) / det}
}
