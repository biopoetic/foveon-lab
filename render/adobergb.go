package render

// SPP's AdobeRGB branch. A Merrill-generation X3F (SD1, DP Merrill) shot
// with the camera set to AdobeRGB carries that mark in its preview's EXIF
// (ColorSpace 0xFFFF, interop index R03), and SPP renders it differently
// even when exporting sRGB: red is pulled against green (greens, cyans and
// blues lose red; reds gain a little). The X3F's own COLORSPACE property
// has no effect. Measured with the same raw marked both ways
// (tools/setprop, tools/exifcs): this linear-sRGB matrix, fitted on SD1
// SDIM0031, takes the unmarked render to the marked one with 4.46 -> 0.28
// mean error (0-255), and 2.78 -> 0.24 on SDIM0064, which it never saw.
// Each row sums to 1, so neutrals are unchanged.
var sppAdobeRGB = [9]float32{
	1.3980, -0.3977, -0.0001,
	0, 1, 0,
	0.0001, -0.0427, 1.0426,
}

// ApplySPPAdobeRGB applies SPP's AdobeRGB branch to a linear sRGB image.
func ApplySPPAdobeRGB(l *Linear) {
	m := sppAdobeRGB
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			j := i * 3
			r, g, b := l.Pix[j], l.Pix[j+1], l.Pix[j+2]
			l.Pix[j] = m[0]*r + m[1]*g + m[2]*b
			l.Pix[j+1] = m[3]*r + m[4]*g + m[5]*b
			l.Pix[j+2] = m[6]*r + m[7]*g + m[8]*b
		}
	})
}
