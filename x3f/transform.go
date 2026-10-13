package x3f

// The white balance + colour transform from camera RGB to linear sRGB, as
// x3fuse-core's x3f_get_gain / x3f_get_bmt_to_xyz / get_conv build it
// (Go port; Copyright 2026 Sang Lee, Apache-2.0, from the Kalpanika x3f
// tools, BSD-style; see third_party/x3fuse-core/).

import (
	"errors"
	"fmt"
	"strings"
)

var (
	d65XYZ    = [3]float64{0.95047, 1.00000, 1.08883}
	xyzToSRGB = [9]float64{3.2406, -1.5372, -0.4986, -0.9689, 1.8758, 0.0415, 0.0557, -0.2040, 1.0570}
)

func mul33(a, b [9]float64) (c [9]float64) {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				c[i*3+j] += a[i*3+k] * b[k*3+j]
			}
		}
	}
	return
}

func inv33(m [9]float64) ([9]float64, bool) {
	det := m[0]*(m[4]*m[8]-m[5]*m[7]) - m[1]*(m[3]*m[8]-m[5]*m[6]) + m[2]*(m[3]*m[7]-m[4]*m[6])
	if det == 0 {
		return m, false
	}
	var r [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			i1, i2, j1, j2 := (j+1)%3, (j+2)%3, (i+1)%3, (i+2)%3
			r[i*3+j] = (m[i1*3+j1]*m[i2*3+j2] - m[i1*3+j2]*m[i2*3+j1]) / det
		}
	}
	return r, true
}

// trueEngine reports whether the file carries TRUE-engine colour data (WB
// gains and colour corrections; x3fuse-core's is_true_engine), which also
// decides its white level: RawSaturationLevel, else SaturationLevel.
func (c *CAMF) trueEngine() bool {
	has := func(n string) bool { return c.Props[n] != nil }
	return (has("WhiteBalanceColorCorrections") || has("DP1_WhiteBalanceColorCorrections")) &&
		(has("WhiteBalanceGains") || has("DP1_WhiteBalanceGains"))
}

// matrixForWB looks up the matrix a CAMF property list names for white
// balance wb (Daylight falls back to Sunlight, as some firmware names it).
func (c *CAMF) matrixForWB(list, wb string, n int) []float64 {
	name := c.Props[list][wb]
	if name == "" && wb == "Daylight" {
		name = c.Props[list]["Sunlight"]
	}
	if m := c.Matrices[name]; len(m) == n {
		return m
	}
	return nil
}

// WBTransform returns, for white balance wb (CAMF's names: Auto, Sunlight,
// Shade, Overcast, Incandescent, Fluorescent, Flash, Custom), the per-layer
// gains and the colour matrix from gained camera RGB to linear sRGB: linear
// sRGB = CC · diag(Gain) · camera RGB (0 = black, 1 = white per layer). The
// gains include the sensor, temperature and aperture factors,
// CaptureISO/SensorISO and the per-layer DigitalISOGain (Quattro: e.g.
// 4,4,2 at ISO 1600; not on Merrill bodies, as x3fuse).
func (c *CAMF) WBTransform(wb string) (gain [3]float64, cc [9]float64, err error) {
	g := c.matrixForWB("WhiteBalanceGains", wb, 3)
	if g == nil {
		g = c.matrixForWB("DP1_WhiteBalanceGains", wb, 3)
	}
	if g != nil {
		// TRUE II / Merrill: gains and a colour correction to sRGB.
		copy(gain[:], g)
		m := c.matrixForWB("WhiteBalanceColorCorrections", wb, 9)
		if m == nil {
			m = c.matrixForWB("DP1_WhiteBalanceColorCorrections", wb, 9)
		}
		if m == nil {
			return gain, cc, fmt.Errorf("x3f: no colour correction for %s white balance", wb)
		}
		copy(cc[:], m)
	} else {
		// Older bodies: a camera-to-XYZ matrix per illuminant and a WB
		// correction; the gains make D65 neutral.
		cam := c.matrixForWB("WhiteBalanceIlluminants", wb, 9)
		corr := c.matrixForWB("WhiteBalanceCorrections", wb, 9)
		if cam == nil || corr == nil {
			return gain, cc, fmt.Errorf("x3f: no %s white balance in CAMF", wb)
		}
		var a, b [9]float64
		copy(a[:], corr)
		copy(b[:], cam)
		rawToXYZ := mul33(a, b)
		inv, ok := inv33(rawToXYZ)
		if !ok {
			return gain, cc, errors.New("x3f: singular camera matrix")
		}
		var neutral [3]float64
		for i := 0; i < 3; i++ {
			for k := 0; k < 3; k++ {
				neutral[i] += inv[i*3+k] * d65XYZ[k]
			}
		}
		var diag [9]float64
		for i := 0; i < 3; i++ {
			if neutral[i] <= 0 {
				return gain, cc, errors.New("x3f: bad raw neutral")
			}
			diag[i*4] = neutral[i]
			gain[i] = 1 / neutral[i]
		}
		cc = mul33(xyzToSRGB, mul33(rawToXYZ, diag))
	}
	for _, k := range []string{"SensorAdjustmentGainFact", "TempGainFact", "FNumberGainFact"} {
		if v := c.Matrices[k]; len(v) == 3 {
			for i := range gain {
				gain[i] *= v[i]
			}
		}
	}
	if s, ci := c.Matrices["SensorISO"], c.Matrices["CaptureISO"]; len(s) == 1 && len(ci) == 1 && s[0] > 0 {
		for i := range gain {
			gain[i] *= ci[0] / s[0]
		}
	}
	if d := c.Matrices["DigitalISOGain"]; len(d) == 3 && !strings.Contains(strings.ToUpper(c.Model), "MERRILL") {
		for i := range gain {
			gain[i] *= d[i]
		}
	}
	return gain, cc, nil
}
