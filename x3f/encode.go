package x3f

// TRUE encoder: the inverse of decodeTRUE, to write X3F files whose raw data
// we choose (synthetic test signals for measuring Sigma Photo Pro as a black
// box: feed it known input, read what it outputs). The container is copied
// from a real file; only the raw section is replaced.

import (
	"bytes"
	"errors"
	"fmt"
	"math/bits"
	"os"
)

// trueCode is one category's prefix code (MSB-aligned in a byte, as stored).
type trueCode struct{ size, code uint8 }

// encodeTRUE codes raw (W×H×3) as a TRUE section payload (everything after
// the 28-byte image header): the header of the original file (seeds, the
// unknown field and its code table, reused so readers see the camera's own
// table), plane sizes and the three planes, each padded to 16 bytes.
func encodeTRUE(raw *RawImage, orig []byte) ([]byte, error) {
	var seed [3]uint16
	for i := range seed {
		seed[i] = le.Uint16(orig[2*i:])
	}
	p := 8
	var table []trueCode
	for p+2 <= len(orig) && orig[p] != 0 {
		table = append(table, trueCode{orig[p], orig[p+1]})
		p += 2
	}
	p += 2 // terminator
	hdr := bytes.NewBuffer(append([]byte{}, orig[:p]...))
	var planes [3][]byte
	for c := range planes {
		p, err := encodePlane(raw, c, int32(seed[c]), table)
		if err != nil {
			return nil, err
		}
		planes[c] = p
	}
	out := hdr.Bytes()
	for _, p := range planes {
		out = le.AppendUint32(out, uint32(len(p)))
	}
	for _, p := range planes {
		out = append(out, p...)
		out = append(out, make([]byte, (len(p)+15)&^15-len(p))...)
	}
	return out, nil
}

type bitWriter struct {
	b   []byte
	acc uint64
	n   uint
}

func (w *bitWriter) put(v uint64, n uint) {
	for n > 0 {
		k := min(n, 32)
		n -= k
		w.acc = w.acc<<k | (v>>n)&(1<<k-1)
		w.n += k
		for w.n >= 8 {
			w.n -= 8
			w.b = append(w.b, byte(w.acc>>w.n))
		}
	}
}

func (w *bitWriter) flush() []byte {
	if w.n > 0 {
		// The camera pads the last byte with ones.
		w.b = append(w.b, byte(w.acc<<(8-w.n))|byte(1<<(8-w.n)-1))
		w.n = 0
	}
	return w.b
}

func encodePlane(raw *RawImage, c int, seed int32, table []trueCode) ([]byte, error) {
	w := &bitWriter{b: make([]byte, 0, raw.W*raw.H)}
	starts := [2][2]int32{{seed, seed}, {seed, seed}}
	for row := 0; row < raw.H; row++ {
		par := row & 1
		var acc [2]int32
		for col := 0; col < raw.W; col++ {
			odd := col & 1
			prev := acc[odd]
			if col < 2 {
				prev = starts[par][odd]
			}
			v := int32(raw.Pix[(row*raw.W+col)*3+c])
			d := v - prev
			n := 0
			if d != 0 {
				n = bits.Len32(uint32(max(d, -d)))
			}
			if n >= len(table) || table[n].size == 0 {
				return nil, fmt.Errorf("x3f: difference %d has no code in the table", d)
			}
			t := table[n]
			w.put(uint64(t.code>>(8-t.size)), uint(t.size))
			if n > 0 {
				u := d
				if d < 0 {
					u = d + (1<<n - 1)
				}
				w.put(uint64(u), uint(n))
			}
			acc[odd] = v
			if col < 2 {
				starts[par][odd] = v
			}
		}
	}
	return w.flush(), nil
}

// WriteWithRaw writes a copy of f to path with its TRUE raw data replaced
// by raw (same size). Every other section is copied unchanged.
func (f *File) WriteWithRaw(path string, raw *RawImage) error {
	im, ok := f.RawSection()
	if !ok || im.Format != FormatTRUE {
		return errors.New("x3f: only TRUE-coded files can be rewritten")
	}
	if raw.W != int(im.Width) || raw.H != int(im.Height) {
		return fmt.Errorf("x3f: raw is %dx%d, the file's %dx%d", raw.W, raw.H, im.Width, im.Height)
	}
	src, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	old := src[im.Offset : im.Offset+im.Length]
	payload, err := encodeTRUE(raw, old[28:])
	if err != nil {
		return err
	}
	newSec := append(append([]byte{}, old[:28]...), payload...)
	// Keep the file layout: pad a smaller section to the original length,
	// so every later section stays where it was (Sigma Photo Pro renders a
	// file black when the raw section shrinks and the rest moves up).
	if len(newSec) < len(old) {
		newSec = append(newSec, make([]byte, len(old)-len(newSec))...)
	}
	// Sections in file order; everything before the first is the header.
	secs := append([]Section(nil), f.sections...)
	for i := 1; i < len(secs); i++ {
		for j := i; j > 0 && secs[j].Offset < secs[j-1].Offset; j-- {
			secs[j], secs[j-1] = secs[j-1], secs[j]
		}
	}
	if len(secs) == 0 {
		return errors.New("x3f: no sections")
	}
	out := append([]byte{}, src[:secs[0].Offset]...)
	type ent struct {
		off, n uint32
		typ    string
	}
	var dir []ent
	for _, s := range secs {
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		body := src[s.Offset : s.Offset+s.Length]
		if s.Offset == im.Offset {
			body = newSec
		}
		dir = append(dir, ent{uint32(len(out)), uint32(len(body)), s.Type})
		out = append(out, body...)
	}
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	dirOff := uint32(len(out))
	oldDir := int(le.Uint32(src[len(src)-4:]))
	out = append(out, src[oldDir:oldDir+8]...) // "SECd" + version
	out = le.AppendUint32(out, uint32(len(dir)))
	for _, e := range dir {
		out = le.AppendUint32(out, e.off)
		out = le.AppendUint32(out, e.n)
		out = append(out, e.typ...)
	}
	out = le.AppendUint32(out, dirOff)
	return os.WriteFile(path, out, 0o644)
}
