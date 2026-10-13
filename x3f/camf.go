package x3f

// Reader for the CAMF section (camera calibration: white balance gains,
// colour matrices, sensor areas, ...). A Go port of x3fuse-core's
// decode_camf / setup_camf (Copyright 2026 Sang Lee, Apache-2.0,
// crates/x3f-sys/src/load.rs), itself derived from the Kalpanika x3f tools
// (BSD-style); licences and notices in third_party/x3fuse-core/, which must
// stay with this code. Verified against x3f_extract -meta (tools/camfcheck).
// Type 2 (SD9/SD10/SD14) is a keyed XOR
// stream, types 4 (TRUE II/Merrill) and 5 are TRUE-coded like the raw data.

import (
	"errors"
	"fmt"
	"math"
	"os"
)

// CAMF holds a file's decoded calibration entries.
type CAMF struct {
	// Matrices are the numeric entries (CMbM), flattened in storage order.
	Matrices map[string][]float64
	// Dims are each matrix's dimension sizes, outermost first as stored.
	Dims map[string][]int
	// Props are the property lists (CMbP) by entry name, then key;
	// PropKeys keeps each list's keys in file order.
	Props    map[string]map[string]string
	PropKeys map[string][]string
	// Text are the text entries (CMbT).
	Text map[string]string
}

// CAMF reads and decodes the file's CAMF section.
func (f *File) CAMF() (*CAMF, error) {
	var sec Section
	for _, s := range f.sections {
		if s.Type == "CAMF" {
			sec = s
		}
	}
	if sec.Length < 28 {
		return nil, errors.New("x3f: no CAMF section")
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b := make([]byte, sec.Length)
	if _, err := fh.ReadAt(b, int64(sec.Offset)); err != nil {
		return nil, err
	}
	if string(b[0:4]) != "SECc" {
		return nil, errors.New("x3f: bad CAMF magic")
	}
	typ := le.Uint32(b[8:12])
	var val [4]uint32
	for i := range val {
		val[i] = le.Uint32(b[12+4*i:])
	}
	dec, err := decodeCAMF(b[28:], typ, val)
	if err != nil {
		return nil, err
	}
	return parseCAMF(dec)
}

func decodeCAMF(data []byte, typ uint32, val [4]uint32) ([]byte, error) {
	switch typ {
	case 2:
		out := make([]byte, len(data))
		key := val[3]
		for i, old := range data {
			key = (key*1597 + 51749) % 244944
			tmp := uint32(int64(key) * 301593171 >> 24)
			a := key<<8 - tmp
			out[i] = old ^ byte(((a>>1)+tmp)>>17)
		}
		return out, nil
	case 4, 5:
	default:
		return nil, fmt.Errorf("x3f: unsupported CAMF type %d", typ)
	}
	if len(data) < 32 {
		return nil, errors.New("x3f: truncated CAMF")
	}
	var pre prefix
	for i := 0; i < 14; i++ {
		size, code := data[2*i], data[2*i+1]
		if size == 0 {
			break
		}
		if size > 8 {
			return nil, errors.New("x3f: bad CAMF table")
		}
		lo := int(code) &^ (1<<(8-size) - 1)
		for j := lo; j < lo+1<<(8-size); j++ {
			pre[j].n, pre[j].bits = size, uint8(i)
		}
	}
	n := int(le.Uint32(data[28:]))
	if n > len(data)-32 {
		return nil, errors.New("x3f: CAMF bitstream outside its section")
	}
	size := int(val[0])
	if size <= 0 || size > 64<<20 {
		return nil, errors.New("x3f: bad CAMF size")
	}
	r := &trueBits{b: data[32 : 32+n]}
	diff := func() (int32, error) {
		if r.n < 40 {
			r.fill()
		}
		e := pre[r.acc>>56]
		if e.n == 0 {
			return 0, errors.New("x3f: invalid CAMF code")
		}
		r.acc <<= e.n
		r.n -= uint(e.n)
		bits := uint(e.bits)
		if bits == 0 {
			return 0, nil
		}
		if r.n < bits {
			r.fill()
		}
		v := int32(r.acc >> (64 - bits))
		r.acc <<= bits
		r.n -= bits
		if v>>(bits-1) == 0 {
			v -= 1<<bits - 1
		}
		return v, nil
	}
	out := make([]byte, size)
	seed := int32(val[1])
	if typ == 5 {
		acc := seed
		for i := range out {
			d, err := diff()
			if err != nil {
				return nil, err
			}
			acc += d
			out[i] = byte(acc)
		}
		return out, nil
	}
	rows, cols := int(val[3]), int(val[2])
	if rows <= 0 || cols <= 0 {
		return nil, errors.New("x3f: empty CAMF block grid")
	}
	starts := [2][2]int32{{seed, seed}, {seed, seed}}
	at, odd := 0, false
	for row := 0; row < rows; row++ {
		var acc [2]int32
		for col := 0; col < cols; col++ {
			ch := col & 1
			prev := acc[ch]
			if col < 2 {
				prev = starts[row&1][ch]
			}
			d, err := diff()
			if err != nil {
				return nil, err
			}
			v := prev + d
			acc[ch] = v
			if col < 2 {
				starts[row&1][ch] = v
			}
			// Two 12-bit values per three bytes.
			if !odd {
				out[at] = byte(v >> 4)
				if at++; at >= size {
					return out, nil
				}
				out[at] = byte(v<<4) & 0xf0
			} else {
				out[at] |= byte(v>>8) & 0x0f
				if at++; at >= size {
					return out, nil
				}
				out[at] = byte(v)
				if at++; at >= size {
					return out, nil
				}
			}
			odd = !odd
		}
	}
	return out, nil
}

func cstr(b []byte, off int) (string, error) {
	if off < 0 || off >= len(b) {
		return "", errors.New("x3f: CAMF string outside its entry")
	}
	for i := off; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[off:i]), nil
		}
	}
	return "", errors.New("x3f: unterminated CAMF string")
}

func parseCAMF(d []byte) (*CAMF, error) {
	c := &CAMF{Matrices: map[string][]float64{}, Dims: map[string][]int{}, Props: map[string]map[string]string{}, PropKeys: map[string][]string{}, Text: map[string]string{}}
	for off := 0; off+20 <= len(d); {
		id := string(d[off : off+4])
		if le.Uint32(d[off:]) == 0 {
			break // zero padding
		}
		size := int(le.Uint32(d[off+8:]))
		if size < 20 || off+size > len(d) {
			return nil, errors.New("x3f: bad CAMF entry length")
		}
		e := d[off : off+size]
		off += size
		nameOff, valOff := int(le.Uint32(e[12:])), int(le.Uint32(e[16:]))
		if nameOff < 20 || valOff < nameOff || valOff > len(e) {
			return nil, errors.New("x3f: bad CAMF entry offsets")
		}
		name, err := cstr(e[:valOff], nameOff)
		if err != nil {
			return nil, err
		}
		v := e[valOff:]
		switch id {
		case "CMbT":
			if len(v) < 4 {
				return nil, errors.New("x3f: truncated CAMF text")
			}
			n := int(le.Uint32(v))
			if 4+n > len(v) {
				return nil, errors.New("x3f: CAMF text outside its entry")
			}
			t, err := cstr(v, 4)
			if err != nil {
				return nil, err
			}
			c.Text[name] = t
		case "CMbP":
			if len(v) < 8 {
				return nil, errors.New("x3f: truncated CAMF properties")
			}
			n, strOff := int(le.Uint32(v)), int(le.Uint32(v[4:]))
			if 8+8*n > len(v) {
				return nil, errors.New("x3f: CAMF properties outside their entry")
			}
			props := map[string]string{}
			keys := make([]string, 0, n)
			for i := 0; i < n; i++ {
				k, err1 := cstr(e, strOff+int(le.Uint32(v[8+8*i:])))
				val, err2 := cstr(e, strOff+int(le.Uint32(v[12+8*i:])))
				if err1 != nil || err2 != nil {
					return nil, errors.New("x3f: bad CAMF property")
				}
				props[k] = val
				keys = append(keys, k)
			}
			c.Props[name] = props
			c.PropKeys[name] = keys
		case "CMbM":
			if len(v) < 12 {
				return nil, errors.New("x3f: truncated CAMF matrix")
			}
			typ, ndim, dataOff := le.Uint32(v), int(le.Uint32(v[4:])), int(le.Uint32(v[8:]))
			if ndim > 8 || 12+12*ndim > len(v) {
				return nil, errors.New("x3f: bad CAMF matrix dimensions")
			}
			count := 1
			dims := make([]int, ndim)
			for i := range dims {
				dims[i] = int(le.Uint32(v[12+12*i:]))
				count *= dims[i]
				if count > 1<<24 {
					return nil, errors.New("x3f: CAMF matrix too large")
				}
			}
			var esize int
			switch typ {
			case 0, 6:
				esize = 2
			case 1, 2, 3:
				esize = 4
			case 5:
				esize = 1
			default:
				return nil, fmt.Errorf("x3f: unknown CAMF matrix type %d", typ)
			}
			if dataOff < 0 || dataOff+count*esize > len(e) {
				return nil, errors.New("x3f: CAMF matrix outside its entry")
			}
			raw := e[dataOff:]
			vals := make([]float64, count)
			for i := range vals {
				switch typ {
				case 0:
					vals[i] = float64(int16(le.Uint16(raw[2*i:])))
				case 1, 2:
					vals[i] = float64(le.Uint32(raw[4*i:]))
				case 3:
					vals[i] = float64(math.Float32frombits(le.Uint32(raw[4*i:])))
				case 5:
					vals[i] = float64(raw[i])
				case 6:
					vals[i] = float64(le.Uint16(raw[2*i:]))
				}
			}
			c.Matrices[name] = vals
			c.Dims[name] = dims
		default:
			return nil, fmt.Errorf("x3f: unknown CAMF entry %q", id)
		}
	}
	return c, nil
}
