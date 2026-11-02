package dng

import (
	"bytes"
	"testing"
)

// packCodes packs 9-bit MSB-first LZW codes, as TIFF stores them.
func packCodes(codes []int) []byte {
	var out []byte
	bit := 0
	for _, c := range codes {
		for k := 8; k >= 0; k-- {
			if bit%8 == 0 {
				out = append(out, 0)
			}
			if c&(1<<uint(k)) != 0 {
				out[len(out)-1] |= 1 << uint(7-bit%8)
			}
			bit++
		}
	}
	return out
}

func TestLZWDecode(t *testing.T) {
	cases := []struct {
		codes []int
		want  string
	}{
		{[]int{256, 'H', 'i', 257}, "Hi"},
		{[]int{256, 'a', 258, 257}, "aaa"},
	}
	for _, c := range cases {
		got, err := lzwDecode(packCodes(c.codes))
		if err != nil {
			t.Fatalf("%v: %v", c.codes, err)
		}
		if string(got) != c.want {
			t.Fatalf("%v: got %q want %q", c.codes, got, c.want)
		}
	}
}

func TestPackBits(t *testing.T) {
	// A literal run of 3 ("abc"), then a run of 3 "x".
	src := []byte{2, 'a', 'b', 'c', 0xFE, 'x'}
	got, err := packBits(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("abcxxx")) {
		t.Fatalf("got %q", got)
	}
}
