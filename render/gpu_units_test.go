package render

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestGPUTextureUnitsDistinct reads web/gpu.js and checks that the PIX
// program's samplers each have their own texture unit. Task 54 bound the
// layer texture (a 2D sampler) to unit 4, which holds the colour-film 3D LUT:
// WebGL then refused every draw ("two textures of different types use the
// same sampler location") and the preview was black, in colour too.
func TestGPUTextureUnitsDistinct(t *testing.T) {
	src, err := os.ReadFile("../web/gpu.js")
	if err != nil {
		t.Fatal(err)
	}
	units := map[int]string{}
	add := func(name string, u int) {
		if prev, ok := units[u]; ok && prev != name {
			t.Errorf("texture unit %d is used by both %s and %s", u, prev, name)
		}
		units[u] = name
	}
	for _, m := range regexp.MustCompile(`bind\(P, "(\w+)", (\d+),`).FindAllSubmatch(src, -1) {
		u, _ := strconv.Atoi(string(m[2]))
		add(string(m[1]), u)
	}
	for _, m := range regexp.MustCompile(`gl\.uniform1i\(u\.(\w+), (\d+)\)`).FindAllSubmatch(src, -1) {
		u, _ := strconv.Atoi(string(m[2]))
		add(string(m[1]), u)
	}
	if len(units) < 6 {
		t.Fatalf("found only %d sampler bindings in web/gpu.js; the pattern no longer matches", len(units))
	}
}
