package x3f

// Decoder for the raw data of the SD15/DP1/DP2 and the Merrill bodies (SD1,
// DP1/2/3 Merrill), TRUE-compressed, and of the SD9/SD10/SD14 (10-bit
// Huffman or packed, formats 5/6).
//
// This is a Go port of x3fuse-core's decoder (Copyright 2026 Sang Lee,
// Apache-2.0: crates/x3f-sys/src/load.rs and entropy.rs), itself a port of
// the Kalpanika x3f tools (Copyright 2015 Roland Karlsson, Erik Karlsson,
// Mark Roden; BSD-style). The format knowledge is theirs; the licences and
// notices are in third_party/x3fuse-core/ and must stay with this code.
// It is verified value-for-value against x3fuse-core's x3f_extract
// (tools/rawcheck). The TRUE bitstream: per colour plane, a
// prefix code gives the bit length of a difference, the difference is added
// to the previous sample of the same column parity, and each row's first two
// samples continue from the first two of the row two above.

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// Raw image types and formats of the data section (type<<16 | format in
// x3fuse's naming).
const (
	rawTypeMerrill = 1  // SD1 / DP Merrill
	rawTypeTRUE    = 3  // SD9 / SD10 / SD14 / SD15 / DP1 / DP2
	FormatTRUE     = 30 // TRUE entropy-coded planes
	formatX530     = 5  // 10-bit Huffman (x3fuse's HUFFMAN_X530)
	format10bit    = 6  // 10-bit Huffman or packed (SD9, SD10, SD14)
)

// RawImage is the undecoded sensor data: three layers per pixel, interleaved
// (bottom/middle/top, in the file's plane order), before any black level,
// gain or colour processing.
type RawImage struct {
	W, H int
	Pix  []uint16 // len W*H*3
	// Quattro (layout with a full-resolution top layer): the bottom and
	// middle layers are Pix's channels 0 and 1 at W×H, and the top layer is
	// Top at TopW×TopH (2× each way); Pix's channel 2 stays 0.
	Top        []uint16
	TopW, TopH int
}

// RawSection returns the image section holding the TRUE raw data, if any.
func (f *File) RawSection() (Image, bool) {
	for _, im := range f.Images {
		if (im.ImgType == rawTypeMerrill || im.ImgType == rawTypeTRUE) && im.Format == FormatTRUE ||
			im.ImgType == rawTypeTRUE && (im.Format == formatX530 || im.Format == format10bit) ||
			im.ImgType == rawTypeMerrill && isQuattro(im.Format) {
			return im, true
		}
	}
	return Image{}, false
}

// DecodeRaw decodes the file's raw data.
func (f *File) DecodeRaw() (*RawImage, error) {
	im, ok := f.RawSection()
	if !ok {
		return nil, errors.New("x3f: no supported raw data")
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	sec := make([]byte, im.Length)
	if _, err := fh.ReadAt(sec, int64(im.Offset)); err != nil {
		return nil, err
	}
	if im.Format == formatX530 || im.Format == format10bit {
		return decodeHuffman10(sec[28:], int(im.Width), int(im.Height), int(im.RowStride))
	}
	return decodeTRUE(sec[28:], int(im.Width), int(im.Height), isQuattro(im.Format))
}

// Quattro formats: sd/dp Quattro (35), sd Quattro (37), sd Quattro H (39).
func isQuattro(format uint32) bool { return format == 35 || format == 37 || format == 39 }

// decodeHuffman10 decodes the 10-bit formats (x3fuse's load_huffman with
// bits 10, mapped): 1024 uint16 deltas, then either a Huffman table (1024
// uint32: length<<27 | code), the bit stream and one uint32 row offset per
// row (row stride 0), or rows of packed uint32 pixels holding three 10-bit
// delta indices. Each channel accumulates its deltas along the row as an
// int16; negative values are stored as 0. A Huffman image whose minimum
// goes negative is decoded again with that minimum as the row start.
func decodeHuffman10(b []byte, w, h, stride int) (*RawImage, error) {
	const n = 1 << 10
	if w <= 0 || h <= 0 || w > 20000 || h > 20000 {
		return nil, fmt.Errorf("x3f: bad raw size %dx%d", w, h)
	}
	if len(b) < n*2 {
		return nil, errors.New("x3f: truncated Huffman mapping")
	}
	var mapping [n]uint16
	for i := range mapping {
		mapping[i] = le.Uint16(b[2*i:])
	}
	b = b[n*2:]
	out := &RawImage{W: w, H: h, Pix: make([]uint16, w*h*3)}
	if stride != 0 {
		if w*4 > stride || stride*h > len(b) {
			return nil, errors.New("x3f: packed raw exceeds its section")
		}
		for y := 0; y < h; y++ {
			var acc [3]uint16
			for x := 0; x < w; x++ {
				packed := le.Uint32(b[y*stride+x*4:])
				for c := 0; c < 3; c++ {
					acc[c] += mapping[packed>>(10*c)&(n-1)]
					v := acc[c]
					if int16(v) <= 0 {
						v = 0
					}
					out.Pix[(y*w+x)*3+c] = v
				}
			}
		}
		return out, nil
	}
	if len(b) < n*4+h*4 {
		return nil, errors.New("x3f: truncated Huffman table")
	}
	tree := newHuffTree()
	codes := 0
	for i := 0; i < n; i++ {
		e := le.Uint32(b[i*4:])
		if length := int(e >> 27); length > 0 {
			tree.add(e&0x07ffffff, length, int32(mapping[i]))
			codes++
		}
	}
	if codes == 0 {
		return nil, errors.New("x3f: empty Huffman table")
	}
	stream := b[n*4 : len(b)-h*4]
	offsets := b[len(b)-h*4:]
	decode := func(start int16) (int, error) {
		minimum := 0
		for y := 0; y < h; y++ {
			off := int(le.Uint32(offsets[y*4:]))
			if off >= len(stream) {
				return 0, fmt.Errorf("x3f: row %d offset beyond the data", y)
			}
			r := &bitReader{data: stream, pos: off * 8}
			acc := [3]int16{start, start, start}
			for x := 0; x < w; x++ {
				for c := 0; c < 3; c++ {
					d, err := tree.next(r)
					if err != nil {
						return 0, fmt.Errorf("x3f: raw row %d col %d: %w", y, x, err)
					}
					acc[c] += int16(uint16(d))
					v := int(acc[c])
					minimum = min(minimum, v)
					out.Pix[(y*w+x)*3+c] = uint16(max(v, 0))
				}
			}
		}
		return minimum, nil
	}
	minimum, err := decode(0)
	if err == nil && minimum < 0 {
		_, err = decode(int16(-minimum))
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// prefix maps the next 8 bits of the stream to a code: its length in bits
// and the difference bit length it stands for (all TRUE codes are <= 8 bits).
type prefix [256]struct{ n, bits uint8 }

// code12 is a whole pixel decided by the top 12 bits of the window: how many
// bits it consumes and the difference it produces. bits == 0 means the code
// does not fit in 12 bits and the slow path has to run.
type code12 struct {
	bits uint8
	diff int16
}

// buildCodes12 resolves every 12-bit prefix once per file. 86% of the pixels
// of a Quattro H frame and 99% of a Merrill one are decided by one lookup:
// 4096 four-byte entries are 16 KB, which stays in L1.
func buildCodes12(pre *prefix) []code12 {
	t := make([]code12, 1<<12)
	for i := range t {
		e := pre[uint8(i>>4)]
		n := int(e.n) + int(e.bits)
		if e.n == 0 || n > 12 {
			continue // no code, or the code runs past the window
		}
		u := int32(i>>(12-n)) & (1<<e.bits - 1)
		if e.bits > 0 && u>>(e.bits-1) == 0 { // leading 0: negative
			u -= 1<<e.bits - 1
		}
		t[i] = code12{bits: uint8(n), diff: int16(u)}
	}
	return t
}

// DecodeTrueForBench is decodeTRUE for benchmarks, so the decoder can be
// measured on data already in memory.
func DecodeTrueForBench(b []byte, w, h int, quattro bool) (*RawImage, error) {
	return decodeTRUE(b, w, h, quattro)
}

func decodeTRUE(b []byte, w, h int, quattro bool) (*RawImage, error) {
	if w <= 0 || h <= 0 || w > 20000 || h > 20000 {
		return nil, fmt.Errorf("x3f: bad raw size %dx%d", w, h)
	}
	// Quattro: each plane's own size first (columns, rows as uint16).
	var pw, ph [3]int
	for i := range pw {
		pw[i], ph[i] = w, h
	}
	layout := false
	if quattro {
		if len(b) < 12 {
			return nil, errors.New("x3f: truncated Quattro header")
		}
		for i := range pw {
			pw[i], ph[i] = int(le.Uint16(b[4*i:])), int(le.Uint16(b[4*i+2:]))
		}
		b = b[12:]
		switch {
		case ph[0] == h/2:
			layout = true // quarter-size bottom/middle, full-size top
			if pw[0] != pw[1] || ph[0] != ph[1] || pw[2] < 2*pw[0] || ph[2] < 2*ph[0] {
				return nil, errors.New("x3f: inconsistent Quattro planes")
			}
		case ph[0] == h:
			for i := range pw {
				if ph[i] != h || pw[i] < w {
					return nil, errors.New("x3f: inconsistent binned Quattro planes")
				}
			}
		default:
			return nil, errors.New("x3f: unknown Quattro layer size")
		}
	}
	if len(b) < 8 {
		return nil, errors.New("x3f: truncated TRUE header")
	}
	var seed [3]int32
	for i := range seed {
		seed[i] = int32(le.Uint16(b[2*i:]))
	}
	p := 8 // 3 seeds + one unknown u16
	var pre prefix
	for i := 0; ; i++ {
		if p+2 > len(b) {
			return nil, errors.New("x3f: truncated TRUE table")
		}
		size, code := b[p], b[p+1]
		p += 2
		if size == 0 {
			break
		}
		if i >= 31 || size > 8 {
			return nil, errors.New("x3f: bad TRUE table")
		}
		// Every 8-bit window starting with this code decodes to it.
		lo := int(code) &^ (1<<(8-size) - 1)
		for j := lo; j < lo+1<<(8-size); j++ {
			if pre[j].n != 0 {
				return nil, errors.New("x3f: overlapping TRUE codes")
			}
			pre[j].n, pre[j].bits = size, uint8(i)
		}
	}
	if quattro {
		p += 4 // an unknown uint32
	}
	if p+12 > len(b) {
		return nil, errors.New("x3f: truncated TRUE plane sizes")
	}
	var planes [3][]byte
	off := p + 12
	for i := range planes {
		n := int(le.Uint32(b[p+4*i:]))
		if n <= 0 || off+n > len(b) {
			return nil, errors.New("x3f: TRUE plane outside the image data")
		}
		planes[i] = b[off : off+n]
		off += (n + 15) &^ 15
	}
	ow, oh := w, h
	if layout {
		ow, oh = pw[0], ph[0]
	}
	out := &RawImage{W: ow, H: oh, Pix: make([]uint16, ow*oh*3)}
	if layout {
		out.TopW, out.TopH = pw[2], ph[2]
		out.Top = make([]uint16, pw[2]*ph[2])
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	codes := buildCodes12(&pre)
	for c := range planes {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			if layout && c == 2 {
				errs[c] = decodePlane(planes[c], &pre, codes, seed[c], ph[c], pw[c], out.Top, out.TopW, 1, 0)
			} else {
				errs[c] = decodePlane(planes[c], &pre, codes, seed[c], ph[c], pw[c], out.Pix, ow, 3, c)
			}
		}(c)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// trueBits reads MSB-first bits through a 64-bit window.
type trueBits struct {
	b   []byte
	pos int    // next byte to load
	acc uint64 // valid bits are the top n
	n   uint
}

func (r *trueBits) fill() {
	for r.n <= 56 {
		// Three bytes at a time while the window can take them: a fill from
		// the usual n < 40 then needs one step instead of three.
		if r.n <= 40 && r.pos+3 <= len(r.b) {
			x := uint64(r.b[r.pos])<<16 | uint64(r.b[r.pos+1])<<8 | uint64(r.b[r.pos+2])
			r.acc |= x << (40 - r.n)
			r.pos += 3
			r.n += 24
			continue
		}
		var v byte
		if r.pos < len(r.b) {
			v = r.b[r.pos]
		}
		r.pos++
		r.acc |= uint64(v) << (56 - r.n)
		r.n += 8
	}
}

// overrun reports reading past the end (missing bytes read as zeros).
func (r *trueBits) overrun() bool { return r.pos-int(r.n/8) > len(r.b) }

// decodePlane decodes one rows×cols plane into dst (outW columns kept,
// nch channels per pixel, this plane in channel c).
func decodePlane(data []byte, pre *prefix, codes []code12, seed int32, rows, cols int, dst []uint16, outW, nch, c int) error {
	r := &trueBits{b: data}
	starts := [2][2]int32{{seed, seed}, {seed, seed}}
	for row := 0; row < rows; row++ {
		par := row & 1
		// Start the accumulators at this row parity's seeds: the first two
		// columns then need no special case inside the loop.
		acc := [2]int32{starts[par][0], starts[par][1]}
		base := row * outW * nch
		for col := 0; col < cols; col++ {
			if r.n < 40 {
				r.fill()
			}
			// The common case: the whole pixel - prefix code and payload -
			// fits in the top 12 bits, so one lookup has the difference.
			if e := codes[r.acc>>52]; e.bits != 0 {
				r.acc <<= e.bits
				r.n -= uint(e.bits)
				odd := col & 1
				v := acc[odd] + int32(e.diff)
				acc[odd] = v
				if col < 2 {
					starts[par][odd] = v
				}
				if col < outW {
					dst[base+col*nch+c] = uint16(v)
				}
				continue
			}
			e := pre[r.acc>>56]
			if e.n == 0 {
				return fmt.Errorf("x3f: invalid TRUE code (plane %d, row %d)", c, row)
			}
			r.acc <<= e.n
			r.n -= uint(e.n)
			var diff int32
			if bits := uint(e.bits); bits > 0 {
				if r.n < bits {
					r.fill()
				}
				v := int32(r.acc >> (64 - bits))
				r.acc <<= bits
				r.n -= bits
				if v>>(bits-1) == 0 { // leading 0: negative
					v -= 1<<bits - 1
				}
				diff = v
			}
			odd := col & 1
			v := acc[odd] + diff
			acc[odd] = v
			if col < 2 {
				starts[par][odd] = v
			}
			if col < outW {
				dst[base+col*nch+c] = uint16(v)
			}
		}
		if r.overrun() {
			return fmt.Errorf("x3f: truncated TRUE plane %d at row %d", c, row)
		}
	}
	return nil
}
