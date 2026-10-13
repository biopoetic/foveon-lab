package render

import (
	"image"
	"math"
)

// CamTransform is what Sigma Photo Pro applies to a photo's raw camera
// RGB for its white balance, read from that file's CAMF block: the
// per-channel gains (<WB>WBGain) and the colour matrix to linear output
// RGB (<WB>CCMatrix, rows sum to 1). For Auto WB both are the file's own
// (AutoWBGain/AutoCCMatrix), which is why they are read per photo.
type CamTransform struct {
	Gain [3]float64
	CC   [9]float64
}

// Matrix is CC · diag(Gain).
func (t CamTransform) Matrix() [9]float64 {
	var m [9]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			m[r*3+c] = t.CC[r*3+c] * t.Gain[c]
		}
	}
	return m
}

// FromCameraRaw reads x3f_extract's unconverted 16-bit decode
// (-color none: linear camera RGB) through t, box-downscaled to at most
// maxW wide (after rotation rot), keeping full precision — unlike
// FromImage, which assumes 8-bit sRGB.
func FromCameraRaw(img image.Image, maxW, rot int, t CamTransform) *Linear {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	outW := sw
	if rot == 90 || rot == 270 {
		outW = sh
	}
	s := 1.0
	if maxW > 0 && outW > maxW {
		s = float64(maxW) / float64(outW)
	}
	dw, dh := max(1, int(math.Round(float64(sw)*s))), max(1, int(math.Round(float64(sh)*s)))
	colMap := make([]int, sw)
	for x := range colMap {
		colMap[x] = x * dw / sw
	}
	get := pixelGetter16(img)
	m := t.Matrix()
	var mf [9]float32
	for i, v := range m {
		mf[i] = float32(v / 65535)
	}
	l := &Linear{W: dw, H: dh, Pix: make([]float32, dw*dh*3), Scale: float64(dw) / float64(sw)}
	parallelRows(dh, func(y0, y1 int) {
		sum := make([]float32, dw*3)
		cnt := make([]float32, dw)
		for dy := y0; dy < y1; dy++ {
			clear(sum)
			clear(cnt)
			for sy := dy * sh / dh; sy < (dy+1)*sh/dh; sy++ {
				for sx := 0; sx < sw; sx++ {
					r, g, bb := get(b.Min.X+sx, b.Min.Y+sy)
					d := colMap[sx]
					sum[d*3] += mf[0]*r + mf[1]*g + mf[2]*bb
					sum[d*3+1] += mf[3]*r + mf[4]*g + mf[5]*bb
					sum[d*3+2] += mf[6]*r + mf[7]*g + mf[8]*bb
					cnt[d]++
				}
			}
			row := l.Pix[dy*dw*3:]
			for x := 0; x < dw; x++ {
				if c := cnt[x]; c > 0 {
					row[x*3], row[x*3+1], row[x*3+2] = sum[x*3]/c, sum[x*3+1]/c, sum[x*3+2]/c
				}
			}
		}
	})
	return l.Rotate(rot)
}

func pixelGetter16(img image.Image) func(x, y int) (float32, float32, float32) {
	switch m := img.(type) {
	case *image.RGBA64:
		return func(x, y int) (float32, float32, float32) {
			i := m.PixOffset(x, y)
			p := m.Pix[i : i+6 : i+6]
			return float32(uint16(p[0])<<8 | uint16(p[1])), float32(uint16(p[2])<<8 | uint16(p[3])), float32(uint16(p[4])<<8 | uint16(p[5]))
		}
	case *image.NRGBA64:
		return func(x, y int) (float32, float32, float32) {
			i := m.PixOffset(x, y)
			p := m.Pix[i : i+6 : i+6]
			return float32(uint16(p[0])<<8 | uint16(p[1])), float32(uint16(p[2])<<8 | uint16(p[3])), float32(uint16(p[4])<<8 | uint16(p[5]))
		}
	}
	return func(x, y int) (float32, float32, float32) {
		r, g, b, _ := img.At(x, y).RGBA()
		return float32(r), float32(g), float32(b)
	}
}
