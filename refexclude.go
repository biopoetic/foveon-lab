package main

import (
	"fmt"

	"github.com/biopoetic/foveon-lab/calib"
)

// printReferenceExclusions lists the reference exclusion list so every tool
// run says which references it was told to ignore, and why.
func printReferenceExclusions() {
	ex := calib.ReferenceExclusions()
	if len(ex) == 0 {
		return
	}
	fmt.Printf("excluded references (%d in calib/refexclude.txt):\n", len(ex))
	for _, e := range ex {
		group := e.Group
		if group == "" {
			group = "all"
		}
		fmt.Printf("  %-12s %-8s %s\n", e.Name, group, e.Reason)
	}
}
