// camfdump prints summary statistics of named CAMF matrices of an X3F
// (count, mean, min, max per row), for research on calibration tables.
//
//	go run ./tools/camfdump <file.X3F> <matrix name>...
package main

import (
	"fmt"
	"log"
	"math"
	"os"

	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	f, err := x3f.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range os.Args[2:] {
		m, ok := c.Matrices[name]
		if !ok {
			fmt.Printf("%s: missing\n", name)
			continue
		}
		dims := c.Dims[name]
		fmt.Printf("%s dims %v, %d values\n", name, dims, len(m))
		rows := 1
		if len(dims) >= 2 {
			rows = dims[0]
		}
		per := len(m) / rows
		for r := 0; r < rows; r++ {
			v := m[r*per : (r+1)*per]
			mean, lo, hi := 0.0, math.Inf(1), math.Inf(-1)
			for _, x := range v {
				mean += x
				lo, hi = math.Min(lo, x), math.Max(hi, x)
			}
			fmt.Printf("  row %d: n=%d mean %.3f min %.3f max %.3f  first %v\n", r, len(v), mean/float64(len(v)), lo, hi, v[:min(6, len(v))])
		}
	}
}
