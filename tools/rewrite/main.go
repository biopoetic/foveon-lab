// rewrite re-encodes an X3F's raw data with x3f's TRUE encoder and checks
// the copy decodes identically (and its preview/CAMF/PROP still read).
//
//	go run ./tools/rewrite <in.X3F> <out.X3F>
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	f, err := x3f.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		log.Fatal(err)
	}
	if err := f.WriteWithRaw(os.Args[2], raw); err != nil {
		log.Fatal(err)
	}
	g, err := x3f.Open(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	back, err := g.DecodeRaw()
	if err != nil {
		log.Fatal(err)
	}
	diff := 0
	for i := range raw.Pix {
		if raw.Pix[i] != back.Pix[i] {
			diff++
		}
	}
	_, _, perr := g.PreviewJPEG()
	_, cerr := g.CAMF()
	a, _ := os.Stat(os.Args[1])
	b, _ := os.Stat(os.Args[2])
	fmt.Printf("differing values %d; preview err %v; CAMF err %v; props %d vs %d; size %d -> %d\n", diff, perr, cerr, len(g.Props), len(f.Props), a.Size(), b.Size())
}
