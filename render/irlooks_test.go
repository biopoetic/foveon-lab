package render

import "testing"

// Every infrared look maps the white-balanced white (1,1,1) to white: the
// model normalises each band to a flat spectrum's response.
func TestIRLooksWhite(t *testing.T) {
	for _, gen := range []string{"Merrill", "TRUEII", "SD9", "Quattro"} {
		for _, look := range []string{"aerochrome", "ir590", "ir720"} {
			m, _ := irLook(gen, "ir-"+look)
			if m == nil {
				t.Fatalf("%s %s missing", gen, look)
			}
			for r := 0; r < 3; r++ {
				if s := m[r*3] + m[r*3+1] + m[r*3+2]; s < 0.9 || s > 1.1 {
					t.Errorf("%s %s row %d sums to %.3f", gen, look, r, s)
				}
			}
		}
	}
	if m, _ := irLook("Merrill", "red"); m != nil {
		t.Error("a visible lens filter taken as an infrared look")
	}
}
