package render

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// readTag reads one tag's rational/srational values from a DNG we wrote.
func readTag(t *testing.T, b []byte, tag uint16) []float64 {
	ifd := binary.LittleEndian.Uint32(b[4:])
	n := int(binary.LittleEndian.Uint16(b[ifd:]))
	for i := 0; i < n; i++ {
		e := b[int(ifd)+2+12*i:]
		if binary.LittleEndian.Uint16(e) != tag {
			continue
		}
		typ, cnt := binary.LittleEndian.Uint16(e[2:]), int(binary.LittleEndian.Uint32(e[4:]))
		off := binary.LittleEndian.Uint32(e[8:])
		var out []float64
		if typ == tFloat {
			for k := 0; k < cnt; k++ {
				out = append(out, float64(math.Float32frombits(binary.LittleEndian.Uint32(b[int(off)+4*k:]))))
			}
			return out
		}
		for k := 0; k < cnt; k++ {
			p := b[int(off)+8*k:]
			if typ == tSRational {
				out = append(out, float64(int32(binary.LittleEndian.Uint32(p)))/float64(int32(binary.LittleEndian.Uint32(p[4:]))))
			} else {
				out = append(out, float64(binary.LittleEndian.Uint32(p))/float64(binary.LittleEndian.Uint32(p[4:])))
			}
		}
		return out
	}
	t.Fatalf("tag %d missing", tag)
	return nil
}

// TestCameraDNGColour develops a camera colour the way the DNG spec tells a
// reader to (white-balance by AsShotNeutral, ForwardMatrix to XYZ D50, scale
// by BaselineExposure) and checks it matches cc · diag(gain) · camera.
func TestCameraDNGColour(t *testing.T) {
	gain := [3]float64{2.1, 1.0, 0.64}
	for i := range gain {
		gain[i] *= 64 // an ISO 6400 push
	}
	cc := [9]float64{3.13, -3.55, 1.42, -1.70, 3.87, -1.17, 1.11, -4.86, 4.75}
	var buf bytes.Buffer
	if err := WriteCameraDNG(&buf, []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6}, 2, 1, gain, cc, nil, "SIGMA", "SD1 Merrill", "test"); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	neutral, fm, be := readTag(t, b, 50728), readTag(t, b, 50964), readTag(t, b, 50730)[0]
	// sRGB(D50 adapted) from XYZ D50: inverse of sRGBToXYZD50.
	var f9 [9]float64
	copy(f9[:], sRGBToXYZD50)
	inv, _ := invert3x3(f9)
	cam := [3]float64{0.012, 0.031, 0.047}
	var wb, xyz, srgb, want [3]float64
	for i := range wb {
		wb[i] = cam[i] / neutral[i]
	}
	for i := 0; i < 3; i++ {
		for k := 0; k < 3; k++ {
			xyz[i] += fm[i*3+k] * wb[k]
		}
	}
	for i := 0; i < 3; i++ {
		for k := 0; k < 3; k++ {
			srgb[i] += inv[i*3+k] * xyz[k]
		}
		srgb[i] *= math.Exp2(be)
		for k := 0; k < 3; k++ {
			want[i] += cc[i*3+k] * gain[k] * cam[k]
		}
		if math.Abs(srgb[i]-want[i]) > 1e-3*math.Max(1, math.Abs(want[i])) {
			t.Errorf("channel %d: reader %g, app %g", i, srgb[i], want[i])
		}
	}
	t.Logf("reader %.4f app %.4f; BaselineExposure %+.2f", srgb, want, be)
}

// TestCameraDNGProfile: with a profile whose matrix tints grey (as the
// fitted raw bases do), ForwardMatrix1 still takes 1,1,1 to D50 white (a
// reader would normalise it otherwise), the tint moves into AsShotNeutral,
// and a reader's colour before the tone curve is matrix · cc · diag(gain) ·
// camera. The tone curve goes in as ProfileToneCurve from (0,0) to (1,1).
func TestCameraDNGProfile(t *testing.T) {
	gain := [3]float64{2.1 * 4, 1.0 * 4, 0.64 * 4}
	cc := [9]float64{3.13, -3.55, 1.42, -1.70, 3.87, -1.17, 1.11, -4.86, 4.75}
	prof := &DNGProfile{Name: "test", Matrix: [9]float64{2.2, -0.3, 0.1, -0.2, 2.9, -0.1, 0.05, -0.4, 3.6},
		Tone: []float64{0, 0, 0.25, 0.4, 1, 1}}
	var buf bytes.Buffer
	if err := WriteCameraDNG(&buf, []float32{0.1, 0.2, 0.3}, 1, 1, gain, cc, prof, "SIGMA", "SD1 Merrill", "test"); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	neutral, fm, be := readTag(t, b, 50728), readTag(t, b, 50964), readTag(t, b, 50730)[0]
	if tone := readTag(t, b, 50940); len(tone) != 6 || tone[3] < 0.399 || tone[3] > 0.401 {
		t.Errorf("ProfileToneCurve %v", tone)
	}
	white := [3]float64{0.9642, 1, 0.8249}
	for i := 0; i < 3; i++ {
		if s := fm[i*3] + fm[i*3+1] + fm[i*3+2]; math.Abs(s-white[i]) > 2e-3 {
			t.Errorf("ForwardMatrix row %d sums to %.4f, want %.4f", i, s, white[i])
		}
	}
	var f9 [9]float64
	copy(f9[:], sRGBToXYZD50)
	inv, _ := invert3x3(f9)
	e := mul33(prof.Matrix, cc)
	cam := [3]float64{0.012, 0.031, 0.047}
	var xyz, srgb, want [3]float64
	for i := 0; i < 3; i++ {
		for k := 0; k < 3; k++ {
			xyz[i] += fm[i*3+k] * cam[k] / neutral[k]
			want[i] += e[i*3+k] * gain[k] * cam[k]
		}
	}
	for i := 0; i < 3; i++ {
		for k := 0; k < 3; k++ {
			srgb[i] += inv[i*3+k] * xyz[k]
		}
		srgb[i] *= math.Exp2(be)
		if math.Abs(srgb[i]-want[i]) > 2e-3*math.Max(1, math.Abs(want[i])) {
			t.Errorf("channel %d: reader %g, app %g", i, srgb[i], want[i])
		}
	}
}
