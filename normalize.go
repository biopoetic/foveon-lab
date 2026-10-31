package main

import "image"

// normalizeBrightness scales an 8-bit image so its 90th-percentile luminance
// is target. It is a display normalisation for the film comparison: the tiles
// are about colour and tone, and each film (and the un-filmed render) has its
// own exposure, so without some match the darkest tile reads as the "wrong"
// film.
//
// The 90th percentile, not the median: a film's curve changes the shadows
// most, so on a dark frame the median sits far below mid-grey. Scaling that
// median to 128 pushed the highlights of every tile, even the un-filmed one,
// into flat white (12% of a white building under a blue sky), which is a fault
// of the comparison, not of any film. The upper midtones are closer to the
// brightness the eye judges, and they leave the highlights room.
//
// A second cap, from the brightest channel, stops the match from creating
// new clipped highlights: it may only brighten until the brightest pixel
// reaches 253, just below the 254 that the clipping display marks. Existing
// near-white detail in the render stays the film's own.
func normalizeBrightness(img *image.RGBA, target float64) {
	n := len(img.Pix) / 4
	if n == 0 {
		return
	}
	var hist [256]int
	hi := 0
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, b := int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
		y := (54*r + 183*g + 19*b) >> 8
		hist[y]++
		if m := max(r, g, b); m > hi {
			hi = m
		}
	}
	percentile := func(q float64) int {
		want, acc := int(q*float64(n)), 0
		for y := 0; y < 256; y++ {
			acc += hist[y]
			if acc >= want {
				return y
			}
		}
		return 255
	}
	mid := percentile(0.90)
	if mid < 4 {
		return
	}
	k := target / float64(mid)
	if hi > 0 {
		if cap := 253 / float64(hi); k > cap {
			k = cap
		}
	}
	if k <= 0 {
		return
	}
	for i := 0; i < len(img.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			v := float64(img.Pix[i+c]) * k
			if v > 255 {
				v = 255
			}
			img.Pix[i+c] = uint8(v + 0.5)
		}
	}
}
