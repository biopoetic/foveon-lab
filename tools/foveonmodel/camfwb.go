package main

// CAMFWB=1 foveonmodel photo.X3F: every white balance the camera's own
// calibration (CAMF) carries, with its gains and colour matrix.

import (
	"fmt"
	"sort"

	"github.com/biopoetic/foveon-lab/x3f"
)

func camfWBList(c *x3f.CAMF) []string {
	var names []string
	for _, list := range []string{"WhiteBalanceGains", "DP1_WhiteBalanceGains", "WhiteBalanceIlluminants"} {
		for k := range c.Props[list] {
			names = append(names, k)
		}
		if len(names) > 0 {
			break
		}
	}
	sort.Strings(names)
	return names
}

func camfWBMain(c *x3f.CAMF) {
	for _, wb := range camfWBList(c) {
		g, cc, err := c.WBTransform(wb)
		if err != nil {
			fmt.Printf("%-14s %v\n", wb, err)
			continue
		}
		fmt.Printf("%-14s gains %.4f %.4f %.4f  matrix %.3f\n", wb, g[0], g[1], g[2], cc)
	}
}
