// camfcheck compares x3f.CAMF's matrices with x3f_extract -meta's dump.
//
//	go run ./tools/camfcheck <x3f_extract> <file.X3F>...
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	tool := os.Args[1]
	tmp, _ := os.MkdirTemp("", "camfcheck")
	defer os.RemoveAll(tmp)
	for _, p := range os.Args[2:] {
		f, err := x3f.Open(p)
		if err != nil {
			fmt.Println(p, err)
			continue
		}
		c, err := f.CAMF()
		if err != nil {
			fmt.Println(filepath.Base(p), "go:", err)
			continue
		}
		exec.Command(tool, "-q", "-meta", "-o", tmp, p).Run()
		mp := filepath.Join(tmp, filepath.Base(p)+".meta")
		data, err := os.ReadFile(mp)
		os.Remove(mp)
		if err != nil {
			fmt.Println(filepath.Base(p), "meta:", err)
			continue
		}
		want := parse(data)
		bad, worst := 0, ""
		for name, w := range want {
			g := c.Matrices[name]
			if len(w) == 100 && len(g) > 100 {
				g = g[:100] // -meta prints at most 100 values
			}
			if len(g) != len(w) {
				bad++
				worst = fmt.Sprintf("%s: len go %d extract %d", name, len(g), len(w))
				continue
			}
			for i := range w {
				if math.Abs(g[i]-w[i]) > 1e-4*math.Max(1, math.Abs(w[i])) {
					bad++
					worst = fmt.Sprintf("%s[%d]: go %g extract %g", name, i, g[i], w[i])
					break
				}
			}
		}
		fmt.Printf("%s: %d matrices (extract %d), %d props, %d text; %d differ %s\n", filepath.Base(p), len(c.Matrices), len(want), len(c.Props), len(c.Text), bad, worst)
	}
}

func parse(data []byte) map[string][]float64 {
	out := map[string][]float64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	name := ""
	var vals []float64
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "BEGIN: CAMF matrix meta data ("):
			name = strings.TrimSuffix(strings.TrimPrefix(line, "BEGIN: CAMF matrix meta data ("), ")")
			vals = nil
		case strings.HasPrefix(line, "END: CAMF matrix"):
			if name != "" {
				out[name] = vals
			}
			name = ""
		case name != "":
			if strings.HasPrefix(line, "x:") || strings.HasPrefix(line, "y:") || strings.HasPrefix(line, "z:") || strings.Contains(line, "[") {
				continue
			}
			for _, f := range strings.Fields(line) {
				if v, err := strconv.ParseFloat(f, 64); err == nil {
					vals = append(vals, v)
				}
			}
		}
	}
	return out
}
