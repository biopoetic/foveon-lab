package main

import "image"

// normalizeMean scales an 8-bit image so its median luminance is target. It is
// a display normalisation for the film comparison: the tiles are about colour
// and tone, and each film (and the un-filmed render) has its own exposure, so
// without this the darkest tile reads as the "wrong" film.
//
// The median is used rather than the mean because a bright sky clips at 255:
// scaling up would push many pixels to the ceiling and leave the mean short of
// the target, so two correctly matched tiles would still look different.
func normalizeMean(img *image.RGBA, target float64) {
	n := len(img.Pix) / 4
	if n == 0 {
		return
	}
	var hist [256]int
	for i := 0; i < len(img.Pix); i += 4 {
		y := (54*int(img.Pix[i]) + 183*int(img.Pix[i+1]) + 19*int(img.Pix[i+2])) >> 8
		hist[y]++
	}
	half, acc, median := n/2, 0, 0
	for y := 0; y < 256; y++ {
		acc += hist[y]
		if acc >= half {
			median = y
			break
		}
	}
	if median < 4 {
		return
	}
	k := target / float64(median)
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
