// x3fprops prints an X3F's PROP entries and its CAMF entries (property
// lists, matrices, text) whose name or key matches a regular expression.
//
//	go run ./tools/x3fprops [-m regexp] file.X3F...
package main

import (
	"flag"
	"fmt"
	"log"
	"regexp"
	"sort"

	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	pat := flag.String("m", ".", "regular expression (case-insensitive) on names and keys")
	flag.Parse()
	re := regexp.MustCompile("(?i)" + *pat)
	for _, p := range flag.Args() {
		f, err := x3f.Open(p)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("==", p)
		var ks []string
		for k := range f.Props {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			if re.MatchString(k) {
				fmt.Printf("PROP %s = %s\n", k, f.Props[k])
			}
		}
		c, err := f.CAMF()
		if err != nil {
			fmt.Println("CAMF:", err)
			continue
		}
		var names []string
		for n := range c.Props {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			for _, k := range c.PropKeys[n] {
				if re.MatchString(n) || re.MatchString(k) {
					fmt.Printf("CAMF %s[%s] = %s\n", n, k, c.Props[n][k])
				}
			}
		}
		names = names[:0]
		for n := range c.Matrices {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if re.MatchString(n) {
				m := c.Matrices[n]
				if len(m) > 12 {
					m = m[:12]
				}
				fmt.Printf("MATRIX %s %v %v\n", n, c.Dims[n], m)
			}
		}
		for n, t := range c.Text {
			if re.MatchString(n) {
				fmt.Printf("TEXT %s = %.200s\n", n, t)
			}
		}
	}
}
