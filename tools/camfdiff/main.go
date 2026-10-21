// camfdiff prints the CAMF matrices, CAMF properties and X3F properties that
// differ between two X3F files - e.g. two frames of an ISO series, to find
// what the camera records per ISO (research).
//
//	go run ./tools/camfdiff a.X3F b.X3F
package main

import (
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/biopoetic/foveon-lab/x3f"
)

func load(p string) (*x3f.File, *x3f.CAMF) {
	f, err := x3f.Open(p)
	if err != nil {
		log.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		log.Fatal(err)
	}
	return f, c
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: camfdiff a.X3F b.X3F")
	}
	fa, a := load(os.Args[1])
	fb, b := load(os.Args[2])
	keys := map[string]bool{}
	for k := range fa.Props {
		keys[k] = true
	}
	for k := range fb.Props {
		keys[k] = true
	}
	var ks []string
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Println("== properties")
	for _, k := range ks {
		if fa.Props[k] != fb.Props[k] {
			fmt.Printf("%s: %q | %q\n", k, fa.Props[k], fb.Props[k])
		}
	}
	fmt.Println("== CAMF properties")
	for n, pa := range a.Props {
		for k, v := range pa {
			if b.Props[n][k] != v {
				fmt.Printf("%s.%s: %q | %q\n", n, k, v, b.Props[n][k])
			}
		}
	}
	fmt.Println("== CAMF matrices")
	var ms []string
	for n := range a.Matrices {
		ms = append(ms, n)
	}
	sort.Strings(ms)
	for _, n := range ms {
		va, vb := a.Matrices[n], b.Matrices[n]
		if len(va) != len(vb) {
			fmt.Printf("%s: %d | %d values\n", n, len(va), len(vb))
			continue
		}
		d := 0
		for i := range va {
			if va[i] != vb[i] {
				d++
			}
		}
		if d > 0 {
			fmt.Printf("%s: %d/%d differ  %.4g | %.4g\n", n, d, len(va), va[:min(6, len(va))], vb[:min(6, len(vb))])
		}
	}
	for n := range b.Matrices {
		if _, ok := a.Matrices[n]; !ok {
			fmt.Printf("%s: only in the second file\n", n)
		}
	}
}
