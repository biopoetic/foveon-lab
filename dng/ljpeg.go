package dng

// Lossless JPEG (TIFF Compression 7) is the form camera makers and Adobe DNG
// Converter normally use for a Bayer DNG. It is a Huffman-coded first
// difference of each sample from a predictor built from the samples to its
// left and above (ISO/IEC 10918-1, Annex H). It is not the familiar DCT
// JPEG: no quantisation, no colour transform, and the sample values are
// recovered exactly.

import (
	"errors"
	"fmt"
	"io"
)

type huffTable struct {
	counts  [17]int
	first   [17]int
	offset  [17]int
	symbols []byte
}

func (h *huffTable) build(bits []byte, vals []byte) error {
	if len(bits) < 16 {
		return errors.New("ljpeg: short Huffman table")
	}
	n := 0
	for i := 1; i <= 16; i++ {
		h.counts[i] = int(bits[i-1])
		n += h.counts[i]
	}
	if n != len(vals) || n == 0 {
		return fmt.Errorf("ljpeg: Huffman table says %d symbols, has %d", n, len(vals))
	}
	h.symbols = vals
	code, k := 0, 0
	for l := 1; l <= 16; l++ {
		h.first[l] = code
		h.offset[l] = k
		k += h.counts[l]
		code = (code + h.counts[l]) << 1
	}
	return nil
}

type bitReader struct {
	b    []byte
	pos  int  // next byte in b
	cur  byte // bits not yet consumed
	left int  // bits left in cur
}

var errEntropyMarker = errors.New("ljpeg: marker in entropy data")

// rawByte reads one entropy byte, undoing JPEG's FF 00 stuffing. A real
// marker is not consumed: bit() reports errEntropyMarker and marker() reads
// it from the byte position.
func (r *bitReader) rawByte() (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.ErrUnexpectedEOF
	}
	c := r.b[r.pos]
	r.pos++
	if c == 0xFF {
		if r.pos < len(r.b) && r.b[r.pos] == 0 {
			r.pos++
			return 0xFF, nil
		}
		r.pos-- // leave the marker for marker()
		return 0, errEntropyMarker
	}
	return int(c), nil
}

func (r *bitReader) bit() (int, error) {
	if r.left == 0 {
		c, err := r.rawByte()
		if err != nil {
			return 0, err
		}
		r.cur, r.left = byte(c), 8
	}
	v := int((r.cur >> 7) & 1)
	r.cur <<= 1
	r.left--
	return v, nil
}

func (r *bitReader) bits(n int) (int, error) {
	v := 0
	for i := 0; i < n; i++ {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		v = v<<1 | b
	}
	return v, nil
}

func (r *bitReader) align() { r.left = 0 }

// marker reads the next real marker after the entropy-coded segment: it
// skips pad bytes and stuffed FF 00 pairs.
func (r *bitReader) marker() (int, error) {
	r.align()
	for r.pos < len(r.b) {
		if r.b[r.pos] != 0xFF {
			r.pos++
			continue
		}
		r.pos++
		if r.pos >= len(r.b) {
			break
		}
		n := r.b[r.pos]
		r.pos++
		if n == 0 {
			continue
		}
		return int(n), nil
	}
	return 0, io.ErrUnexpectedEOF
}

// decodeLosslessJPEG decodes one lossless JPEG stream with components
// samples per pixel. The samples are returned interleaved in the DNG's
// row-major order.
func decodeLosslessJPEG(src []byte, components int) ([]uint32, error) {
	if len(src) < 4 || src[0] != 0xFF || src[1] != 0xD8 {
		return nil, errors.New("ljpeg: no SOI")
	}
	var precision, width, height, ncomp int
	var huff [4]*huffTable
	var restart, predictor, pointTransform int
	var compID, compTd []int
	seenSOS := false

	i := 2
	for i+4 <= len(src) {
		if src[i] != 0xFF {
			i++
			continue
		}
		marker := src[i+1]
		i += 2
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		if i+2 > len(src) {
			break
		}
		segLen := int(src[i])<<8 | int(src[i+1])
		if segLen < 2 || i+segLen > len(src) {
			return nil, errors.New("ljpeg: bad segment length")
		}
		seg := src[i+2 : i+segLen]
		switch marker {
		case 0xC3: // SOF3: lossless
			if len(seg) < 6 {
				return nil, errors.New("ljpeg: short SOF3")
			}
			precision = int(seg[0])
			height = int(seg[1])<<8 | int(seg[2])
			width = int(seg[3])<<8 | int(seg[4])
			ncomp = int(seg[5])
			if precision < 2 || precision > 16 {
				return nil, fmt.Errorf("ljpeg: %d-bit precision", precision)
			}
			if ncomp < 1 || ncomp > 4 {
				return nil, fmt.Errorf("ljpeg: %d components", ncomp)
			}
			if components > 0 && ncomp != components {
				return nil, fmt.Errorf("ljpeg: %d components, DNG says %d", ncomp, components)
			}
			compID = compID[:0]
			for c := 0; c < ncomp; c++ {
				if 6+3*c+3 > len(seg) {
					return nil, errors.New("ljpeg: short SOF3 components")
				}
				compID = append(compID, int(seg[6+3*c]))
				h := int(seg[7+3*c]) >> 4
				v := int(seg[7+3*c]) & 15
				if h != 1 || v != 1 {
					return nil, unsupported("lossless JPEG sampling %dx%d", h, v)
				}
			}
		case 0xC4: // DHT
			for p := 0; p < len(seg); {
				if p+17 > len(seg) {
					return nil, errors.New("ljpeg: short DHT")
				}
				tc := int(seg[p]) >> 4
				th := int(seg[p]) & 15
				if tc != 0 || th > 3 {
					return nil, unsupported("lossless JPEG Huffman class %d table %d", tc, th)
				}
				bits := seg[p+1 : p+17]
				n := 0
				for _, b := range bits {
					n += int(b)
				}
				if p+17+n > len(seg) {
					return nil, errors.New("ljpeg: short DHT values")
				}
				h := &huffTable{}
				if err := h.build(bits, seg[p+17:p+17+n]); err != nil {
					return nil, err
				}
				huff[th] = h
				p += 17 + n
			}
		case 0xDD: // DRI
			if len(seg) < 2 {
				return nil, errors.New("ljpeg: short DRI")
			}
			restart = int(seg[0])<<8 | int(seg[1])
		case 0xDA: // SOS
			if len(seg) < 1 {
				return nil, errors.New("ljpeg: short SOS")
			}
			ns := int(seg[0])
			if ns != ncomp {
				return nil, unsupported("lossless JPEG scan with %d of %d components", ns, ncomp)
			}
			compTd = compTd[:0]
			for c := 0; c < ns; c++ {
				if 1+2*c+2 > len(seg) {
					return nil, errors.New("ljpeg: short SOS components")
				}
				compTd = append(compTd, int(seg[2+2*c]))
			}
			if len(seg) < 1+2*ns+3 {
				return nil, errors.New("ljpeg: short SOS parameters")
			}
			predictor = int(seg[1+2*ns])
			pt := int(seg[3+2*ns])
			if pt < 0 || pt > 15 {
				return nil, fmt.Errorf("ljpeg: point transform %d", pt)
			}
			pointTransform = pt
			seenSOS = true
			i += segLen
			return decodeScan(src[i:], width, height, ncomp, compTd, huff, predictor, pointTransform, restart, precision)
		}
		i += segLen
	}
	if !seenSOS {
		return nil, errors.New("ljpeg: no SOS")
	}
	return nil, errors.New("ljpeg: no entropy-coded data")
}

func decodeScan(src []byte, width, height, ncomp int, compTd []int, huff [4]*huffTable, predictor, pt, restart, precision int) ([]uint32, error) {
	if width <= 0 || height <= 0 || width*height > 400e6 {
		return nil, fmt.Errorf("ljpeg: bad size %dx%d", width, height)
	}
	out := make([]uint32, width*height*ncomp)
	// up is the previous line, cur the one being decoded. A component's
	// predictor may need all three of Ra (cur[x-1]), Rb (up[x]) and Rc
	// (up[x-1]).
	up := make([][]int, ncomp)
	cur := make([][]int, ncomp)
	for c := range up {
		up[c] = make([]int, width)
		cur[c] = make([]int, width)
	}
	firstVal := 1 << (precision - pt - 1)
	reset := func() {
		for c := range up {
			for x := range up[c] {
				up[c][x] = firstVal
				cur[c][x] = firstVal
			}
		}
	}
	reset()
	r := &bitReader{b: src}
	mask := (1 << precision) - 1
	mcus := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if restart > 0 && mcus == restart {
				m, err := r.marker()
				if err != nil {
					return nil, err
				}
				if m < 0xD0 || m > 0xD7 {
					return nil, fmt.Errorf("ljpeg: expected RST, got 0x%02X", m)
				}
				reset()
				mcus = 0
			}
			for c := 0; c < ncomp; c++ {
				td := 0
				if c < len(compTd) {
					td = compTd[c]
				}
				ht := huff[td]
				if ht == nil {
					return nil, fmt.Errorf("ljpeg: missing Huffman table %d", td)
				}
				cat, err := decodeHuff(r, ht)
				if err != nil {
					return nil, err
				}
				var diff int
				if cat > 0 {
					v, err := r.bits(cat)
					if err != nil {
						return nil, err
					}
					if v < 1<<(cat-1) {
						v -= (1 << cat) - 1
					}
					diff = v
				}
				ra, rb, rc := firstVal, firstVal, firstVal
				if x > 0 {
					ra = cur[c][x-1]
				}
				if y > 0 {
					rb = up[c][x]
					if x > 0 {
						rc = up[c][x-1]
					}
				}
				var pred int
				switch {
				case y == 0 && x == 0:
					pred = firstVal
				case y == 0:
					pred = ra
				case x == 0:
					pred = rb
				case predictor == 1:
					pred = ra
				case predictor == 2:
					pred = rb
				case predictor == 3:
					pred = rc
				case predictor == 4:
					pred = ra + rb - rc
				case predictor == 5:
					pred = ra + (rb-rc)/2
				case predictor == 6:
					pred = rb + (ra-rc)/2
				case predictor == 7:
					pred = (ra + rb) / 2
				default:
					return nil, unsupported("lossless JPEG predictor %d", predictor)
				}
				v := (pred + diff) & mask
				cur[c][x] = v
				out[(y*width+x)*ncomp+c] = uint32(v << pt)
			}
			mcus++
		}
		for c := range up {
			up[c], cur[c] = cur[c], up[c]
		}
	}
	return out, nil
}

func decodeHuff(r *bitReader, h *huffTable) (int, error) {
	code := 0
	for l := 1; l <= 16; l++ {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		code = code<<1 | b
		if n := h.counts[l]; n > 0 && code-h.first[l] < n {
			return int(h.symbols[h.offset[l]+code-h.first[l]]), nil
		}
	}
	return 0, errors.New("ljpeg: bad Huffman code")
}
