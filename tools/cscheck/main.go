// cscheck compares exports of the same photo in different colour spaces
// (SPP: sRGB, Apple RGB, Adobe RGB, ColorMatch RGB, ProPhoto RGB), each
// through its own embedded ICC profile (primaries rXYZ/gXYZ/bXYZ and the
// rTRC/gTRC/bTRC tone curves), in XYZ (D50, the ICC connection space).
// It answers whether a wide-gamut export carries colours sRGB cannot hold,
// or is sRGB clipped and re-encoded.
//
//	go run ./tools/cscheck <reference sRGB.tif> <other.tif>...
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/biopoetic/foveon-lab/render"
	_ "golang.org/x/image/tiff"
)

var be = binary.BigEndian

// profile is an RGB matrix/TRC ICC profile: linear RGB -> XYZ (D50).
type profile struct {
	desc string
	m    [9]float64 // columns: r, g, b colourants
	trc  [3]func(float64) float64
}

func s15(b []byte) float64 { return float64(int32(be.Uint32(b))) / 65536 }

func parseICC(p []byte) (*profile, error) {
	if len(p) < 132 {
		return nil, errors.New("short profile")
	}
	n := int(be.Uint32(p[128:]))
	tags := map[string][]byte{}
	for i := 0; i < n; i++ {
		e := 132 + 12*i
		if e+12 > len(p) {
			break
		}
		off, size := int(be.Uint32(p[e+4:])), int(be.Uint32(p[e+8:]))
		if off+size <= len(p) {
			tags[string(p[e:e+4])] = p[off : off+size]
		}
	}
	pr := &profile{}
	if d, ok := tags["desc"]; ok && len(d) > 12 && string(d[:4]) == "desc" {
		l := int(be.Uint32(d[8:]))
		if 12+l <= len(d) {
			pr.desc = string(bytes.TrimRight(d[12:12+l], "\x00"))
		}
	}
	for k, t := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		x, ok := tags[t]
		if !ok || len(x) < 20 {
			return nil, errors.New("no " + t)
		}
		pr.m[k], pr.m[3+k], pr.m[6+k] = s15(x[8:]), s15(x[12:]), s15(x[16:])
	}
	for k, t := range []string{"rTRC", "gTRC", "bTRC"} {
		c, ok := tags[t]
		if !ok {
			return nil, errors.New("no " + t)
		}
		f, err := curve(c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		pr.trc[k] = f
	}
	return pr, nil
}

// curve decodes a curv or para tag into encoded -> linear.
func curve(c []byte) (func(float64) float64, error) {
	switch string(c[:4]) {
	case "curv":
		n := int(be.Uint32(c[8:]))
		switch {
		case n == 0:
			return func(x float64) float64 { return x }, nil
		case n == 1:
			g := float64(be.Uint16(c[12:])) / 256
			return func(x float64) float64 { return math.Pow(x, g) }, nil
		}
		t := make([]float64, n)
		for i := range t {
			t[i] = float64(be.Uint16(c[12+2*i:])) / 65535
		}
		return func(x float64) float64 {
			f := math.Max(0, math.Min(1, x)) * float64(n-1)
			i := min(int(f), n-2)
			return t[i] + (f-float64(i))*(t[i+1]-t[i])
		}, nil
	case "para":
		typ := be.Uint16(c[8:])
		var g [7]float64
		for i := range g {
			if 12+4*i+4 <= len(c) {
				g[i] = s15(c[12+4*i:])
			}
		}
		switch typ {
		case 0:
			return func(x float64) float64 { return math.Pow(x, g[0]) }, nil
		case 3: // Y = (aX+b)^g for X >= d, else cX
			return func(x float64) float64 {
				if x >= g[4] {
					return math.Pow(g[1]*x+g[2], g[0])
				}
				return g[3] * x
			}, nil
		}
		return nil, fmt.Errorf("para type %d", typ)
	}
	return nil, errors.New("unknown curve " + string(c[:4]))
}

func invert(m [9]float64) [9]float64 {
	d := m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
	return [9]float64{
		(m[4]*m[8] - m[5]*m[7]) / d, (m[2]*m[7] - m[1]*m[8]) / d, (m[1]*m[5] - m[2]*m[4]) / d,
		(m[5]*m[6] - m[3]*m[8]) / d, (m[0]*m[8] - m[2]*m[6]) / d, (m[2]*m[3] - m[0]*m[5]) / d,
		(m[3]*m[7] - m[4]*m[6]) / d, (m[1]*m[6] - m[0]*m[7]) / d, (m[0]*m[4] - m[1]*m[3]) / d,
	}
}

func mul(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{m[0]*v[0] + m[1]*v[1] + m[2]*v[2], m[3]*v[0] + m[4]*v[1] + m[5]*v[2], m[6]*v[0] + m[7]*v[1] + m[8]*v[2]}
}

// iccOf finds the ICC profile (TIFF tag 34675) in a TIFF file.
func iccOf(b []byte) []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	if string(b[:2]) == "MM" {
		bo = binary.BigEndian
	}
	ifd := int(bo.Uint32(b[4:]))
	n := int(bo.Uint16(b[ifd:]))
	for i := 0; i < n; i++ {
		e := ifd + 2 + 12*i
		if bo.Uint16(b[e:]) == 34675 {
			cnt, off := int(bo.Uint32(b[e+4:])), int(bo.Uint32(b[e+8:]))
			return b[off : off+cnt]
		}
	}
	return nil
}

type photo struct {
	name string
	p    *profile
	img  image.Image
}

func load(path string) photo {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	icc := iccOf(b)
	if icc == nil {
		log.Fatalf("%s: no ICC profile", path)
	}
	p, err := parseICC(icc)
	if err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	img, _, err := render.DecodeImage(b)
	if err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	return photo{filepath.Base(path), p, img}
}

// xyz is the pixel's XYZ (D50) through its file's profile.
func (ph photo) xyz(x, y int) [3]float64 {
	r, g, b, _ := ph.img.At(x, y).RGBA()
	v := [3]float64{ph.p.trc[0](float64(r) / 65535), ph.p.trc[1](float64(g) / 65535), ph.p.trc[2](float64(b) / 65535)}
	return mul(ph.p.m, v)
}

func main() {
	if len(os.Args) < 3 {
		log.Fatal("usage: cscheck <reference sRGB.tif> <other.tif>...")
	}
	ref := load(os.Args[1])
	toSRGB := invert(ref.p.m)
	bd := ref.img.Bounds()
	const step = 4
	fmt.Printf("reference %s (%s)\n", ref.name, ref.p.desc)
	for _, path := range os.Args[2:] {
		o := load(path)
		var diff, n, outside, outsideFar float64
		for y := bd.Min.Y; y < bd.Max.Y; y += step {
			for x := bd.Min.X; x < bd.Max.X; x += step {
				a, c := ref.xyz(x, y), o.xyz(x, y)
				// The other file's colour as linear sRGB: outside [0,1]
				// means a colour the sRGB export cannot hold.
				s := mul(toSRGB, c)
				lo, hi := math.Min(s[0], math.Min(s[1], s[2])), math.Max(s[0], math.Max(s[1], s[2]))
				if lo < -0.01 || hi > 1.01 {
					outside++
				}
				if lo < -0.05 || hi > 1.05 {
					outsideFar++
				}
				// Compare where the reference is not clipped.
				r := mul(toSRGB, a)
				if math.Min(r[0], math.Min(r[1], r[2])) > 0.002 && math.Max(r[0], math.Max(r[1], r[2])) < 0.995 {
					for k := 0; k < 3; k++ {
						diff += math.Abs(a[k] - c[k])
					}
					n += 3
				}
			}
		}
		total := float64(((bd.Dy() + step - 1) / step) * ((bd.Dx() + step - 1) / step))
		fmt.Printf("%-34s %-42q outside sRGB: %5.2f%% (>5%%: %5.2f%%)  |XYZ diff| where sRGB unclipped: %.4f\n",
			o.name, o.p.desc, 100*outside/total, 100*outsideFar/total, diff/math.Max(n, 1))
	}
}
