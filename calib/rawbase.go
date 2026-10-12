package calib

import (
	"math"

	"github.com/biopoetic/foveon-lab/render"
)

// RawPair is one photo as SPP renders it neutrally (its A00 export) and as
// x3f_extract decodes its raw data, both at the analysis size.
type RawPair struct {
	Name     string
	Raw, SPP *render.Linear
	// JPEGErr: how far the camera-JPEG preview is from SPP (8-bit), the
	// baseline the raw base has to beat.
	JPEGErr float64
}

// MeanError is the mean abs difference (8-bit sRGB) between two images.
func MeanError(a, b *render.Linear) float64 {
	var e float64
	for i := range a.Pix {
		e += math.Abs(float64(render.ToSRGB(a.Pix[i]) - render.ToSRGB(b.Pix[i])))
	}
	return e / float64(len(a.Pix)) * 255
}

func fitRaw(pairs []RawPair) render.RawCorrection {
	var c render.RawCorrection
	var ata, atb [3][3]float64
	for _, p := range pairs {
		for i := 0; i < len(p.SPP.Pix); i += 6 { // every other pixel
			if max(p.SPP.Pix[i], p.SPP.Pix[i+1], p.SPP.Pix[i+2], p.Raw.Pix[i], p.Raw.Pix[i+1], p.Raw.Pix[i+2]) > 0.95 {
				continue
			}
			for r := 0; r < 3; r++ {
				for q := 0; q < 3; q++ {
					ata[r][q] += float64(p.Raw.Pix[i+r]) * float64(p.Raw.Pix[i+q])
					atb[r][q] += float64(p.SPP.Pix[i+r]) * float64(p.Raw.Pix[i+q])
				}
			}
		}
	}
	inv, ok := inverse3(ata)
	if !ok {
		c.Matrix = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	} else {
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				for k := 0; k < 3; k++ {
					c.Matrix[r*3+q] += atb[r][k] * inv[k][q]
				}
			}
		}
	}
	// Tone curve per channel after the matrix: mean SPP output per input bin.
	var sum, cnt [3][64]float64
	m := c.Matrix
	for _, p := range pairs {
		for i := 0; i < len(p.SPP.Pix); i += 3 {
			r, g, b := float64(p.Raw.Pix[i]), float64(p.Raw.Pix[i+1]), float64(p.Raw.Pix[i+2])
			v := [3]float64{m[0]*r + m[1]*g + m[2]*b, m[3]*r + m[4]*g + m[5]*b, m[6]*r + m[7]*g + m[8]*b}
			for k := 0; k < 3; k++ {
				e := float64(render.ToSRGB(float32(math.Max(0, math.Min(1, v[k])))))
				bin := min(63, int(e*64))
				sum[k][bin] += float64(render.ToSRGB(p.SPP.Pix[i+k]))
				cnt[k][bin]++
			}
		}
	}
	for k := 0; k < 3; k++ {
		for b := 0; b < 64; b++ {
			c.Curves[k][b] = -1
			if cnt[k][b] > 50 {
				c.Curves[k][b] = sum[k][b] / cnt[k][b]
			}
		}
	}
	return c
}

func applied(c *render.RawCorrection, l *render.Linear) *render.Linear {
	o := &render.Linear{W: l.W, H: l.H, Pix: append([]float32(nil), l.Pix...)}
	c.Apply(o)
	return o
}

// FitRawBase fits the raw → SPP-neutral mapping for one camera family on
// all its pairs, and judges it honestly: each photo is predicted by a fit
// on the OTHER photos. The raw base is only switched on when that held-out
// error clearly beats the camera JPEG (and there are enough photos).
func FitRawBase(family string, pairs []RawPair) render.RawCorrection {
	c := fitRaw(pairs)
	c.Family, c.Photos = family, len(pairs)
	if len(pairs) < 3 {
		return c
	}
	var raw, jpg float64
	for h := range pairs {
		var train []RawPair
		for i := range pairs {
			if i != h {
				train = append(train, pairs[i])
			}
		}
		fc := fitRaw(train)
		raw += MeanError(applied(&fc, pairs[h].Raw), pairs[h].SPP)
		jpg += pairs[h].JPEGErr
	}
	c.HeldOutRaw, c.HeldOutJPEG = raw/float64(len(pairs)), jpg/float64(len(pairs))
	c.UseRaw = c.HeldOutRaw < 0.8*c.HeldOutJPEG
	return c
}

// LoadLinear reads an export (TIFF/JPEG) at the analysis size.
func LoadLinear(path string) (*render.Linear, error) { return load(path) }
