package dng

// Reading the stored samples: strips or tiles, uncompressed or compressed,
// 8/10/12/14/16-bit integers, 16/32-bit floats, little- or big-endian, with
// or without horizontal differencing (TIFF Predictor 2). The compressed block
// is expanded to its stored samples first, so the predictor and the packed-bit
// reader see the same numbers whatever produced them.

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"math"
)

// readRaw reads every sample and normalises it to 0..1 with the file's black
// and white levels. The returned slice is row-major: one value per pixel for
// a CFA or monochrome image, three interleaved values per pixel for LinearRaw.
func (f *File) readRaw(im *rawImage) ([]float32, error) {
	total := im.width * im.height * im.samples
	if total <= 0 {
		return nil, errors.New("dng: empty image")
	}
	raw := make([]float32, total)
	if im.tile {
		if err := f.readTiles(im, raw); err != nil {
			return nil, err
		}
	} else {
		if err := f.readStrips(im, raw); err != nil {
			return nil, err
		}
	}
	// A file with masked (optically black) areas and no BlackLevel tag can
	// have its black level measured from those pixels. Do it on stored values,
	// before the gain map and before normalisation.
	if len(im.black) == 0 && len(im.masked) > 0 {
		im.estimateBlackFromMasked(raw)
	}
	im.applyGainMaps(raw)
	// Black subtraction, white scaling and the linearization table. A CFA's
	// black level can repeat over the pattern, so this is per pixel.
	for y := 0; y < im.height; y++ {
		for x := 0; x < im.width; x++ {
			if im.samples == 1 {
				c := im.cfaAt(x, y)
				i := y*im.width + x
				raw[i] = f.normSample(im, raw[i], c, x, y)
			} else {
				i := (y*im.width + x) * 3
				for c := 0; c < 3; c++ {
					raw[i+c] = f.normSample(im, raw[i+c], c, x, y)
				}
			}
		}
	}
	return raw, nil
}

// estimateBlackFromMasked averages the stored samples of the MaskedAreas,
// per plane (or per CFA colour when the sensor is a colour filter array).
// The tag is the file's own declaration of optically shielded pixels, so
// this is the DNG-sanctioned way to find black when BlackLevel is absent.
func (im *rawImage) estimateBlackFromMasked(raw []float32) {
	sum := [3]float64{}
	n := [3]int{}
	for _, a := range im.masked {
		for y := a.top; y < a.bottom; y++ {
			for x := a.left; x < a.right; x++ {
				if im.samples == 1 {
					c := im.cfaAt(x, y)
					sum[c] += float64(raw[y*im.width+x])
					n[c]++
				} else {
					i := (y*im.width + x) * 3
					for c := 0; c < 3; c++ {
						sum[c] += float64(raw[i+c])
						n[c]++
					}
				}
			}
		}
	}
	if im.samples == 1 {
		var v []float64
		for c := 0; c < 3; c++ {
			if n[c] > 0 {
				v = append(v, sum[c]/float64(n[c]))
			}
		}
		if len(v) == 1 {
			im.black = []float64{v[0]}
		} else if len(v) == 3 {
			im.black = v
			im.blackRepeatW, im.blackRepeatH = 1, 1
			// v is per plane; levelAt sees len==samples only when samples==1,
			// so keep it as a 2x2 BlackLevel-style repeat when possible.
			im.black = []float64{v[0], v[1], v[1], v[2]}
			im.blackRepeatW, im.blackRepeatH = 2, 2
		}
		return
	}
	var v []float64
	for c := 0; c < 3; c++ {
		if n[c] == 0 {
			return
		}
		v = append(v, sum[c]/float64(n[c]))
	}
	im.black = v
	im.blackRepeatW, im.blackRepeatH = 1, 1
}

func (f *File) normSample(im *rawImage, v float32, c, x, y int) float32 {
	v64 := float64(v)
	if len(im.linTable) > 0 && im.sampleFmt != 3 {
		k := int(v64)
		if k < 0 {
			k = 0
		}
		if k >= len(im.linTable) {
			k = len(im.linTable) - 1
		}
		v64 = float64(im.linTable[k])
	}
	black := im.blackAt(c, x, y)
	white := im.whiteAt(c, x, y)
	d := white - black
	if d <= 0 {
		return 0
	}
	return float32((v64 - black) / d)
}

func (f *File) readStrips(im *rawImage, raw []float32) error {
	nstrips := (im.height + im.rowsPerStrip - 1) / im.rowsPerStrip
	if nstrips > 1<<20 {
		return fmt.Errorf("dng: %d strips is too many", nstrips)
	}
	if len(im.stripOff) < nstrips {
		return fmt.Errorf("dng: %d strips for %d rows", len(im.stripOff), im.height)
	}
	for s := 0; s < nstrips; s++ {
		y0 := s * im.rowsPerStrip
		rows := min(im.rowsPerStrip, im.height-y0)
		vals, err := f.blockSamples(im, im.stripOff[s], im.stripCount, s, rows, im.width)
		if err != nil {
			return fmt.Errorf("dng: strip %d: %w", s, err)
		}
		copy(raw[y0*im.width*im.samples:], vals)
	}
	return nil
}

func (f *File) readTiles(im *rawImage, raw []float32) error {
	tx := (im.width + im.tileW - 1) / im.tileW
	ty := (im.height + im.tileH - 1) / im.tileH
	if tx <= 0 || ty <= 0 || tx > 1<<16 || ty > 1<<16 {
		return fmt.Errorf("dng: bad tile grid %dx%d", tx, ty)
	}
	if len(im.tileOff) < tx*ty {
		return fmt.Errorf("dng: %d tiles for a %dx%d grid", len(im.tileOff), tx, ty)
	}
	for j := 0; j < ty; j++ {
		for i := 0; i < tx; i++ {
			t := j*tx + i
			cols := min(im.tileW, im.width-i*im.tileW)
			rows := min(im.tileH, im.height-j*im.tileH)
			if cols <= 0 || rows <= 0 {
				return fmt.Errorf("dng: zero-size tile %d", t)
			}
			// TIFF tiles are padded to the full tile size; some DNG writers
			// trim the right and bottom edge tiles instead. Try the padded
			// form first and fall back to the trimmed one.
			vals, err := f.blockSamples(im, im.tileOff[t], im.tileCount, t, im.tileH, im.tileW)
			stride := im.tileW
			if err != nil {
				vals, err = f.blockSamples(im, im.tileOff[t], im.tileCount, t, rows, cols)
				stride = cols
			}
			if err != nil {
				return fmt.Errorf("dng: tile %d: %w", t, err)
			}
			for r := 0; r < rows; r++ {
				dst := raw[((j*im.tileH+r)*im.width+i*im.tileW)*im.samples:]
				src := vals[r*stride*im.samples : (r*stride+cols)*im.samples]
				copy(dst[:cols*im.samples], src)
			}
		}
	}
	return nil
}

// blockSamples reads and expands one strip or tile of rows x cols pixels.
func (f *File) blockSamples(im *rawImage, off uint64, counts []uint64, idx, rows, cols int) ([]float32, error) {
	src, err := f.blockBytes(im, off, counts, idx)
	if err != nil {
		return nil, err
	}
	n := rows * cols * im.samples
	if n <= 0 {
		return nil, errors.New("dng: zero samples in block")
	}
	if im.comp == compressionJPEG {
		vals, err := decodeLosslessJPEG(src, im.samples)
		if err != nil {
			return nil, err
		}
		if len(vals) != n {
			return nil, fmt.Errorf("lossless JPEG has %d samples, block needs %d", len(vals), n)
		}
		return bytesToFloat32(vals), nil
	}
	if im.comp == compressionLossyJPEG {
		return decodeLossyBlock(src, im, rows, cols)
	}
	if im.sampleFmt == 3 {
		want := int64(rows) * im.rowBytes(cols)
		if int64(len(src)) < want {
			return nil, fmt.Errorf("block has %d bytes, %d rows need %d", len(src), rows, want)
		}
		v, err := unpackFloats(src[:want], n, im.bits, f.bo, rows, cols*im.samples)
		if err != nil {
			return nil, err
		}
		out := make([]float32, n)
		for i := range v {
			out[i] = float32(v[i])
		}
		return out, nil
	}
	want := int64(rows) * im.rowBytes(cols)
	if int64(len(src)) < want {
		return nil, fmt.Errorf("block has %d bytes, %d rows need %d", len(src), rows, want)
	}
	vals, err := unpackSamples(src[:want], n, im.bits, f.bo, im.fillOrder, rows, cols*im.samples)
	if err != nil {
		return nil, err
	}
	applyPredictor(vals, rows, cols*im.samples, im.samples, im.bits, im.predictor)
	return bytesToFloat32(vals), nil
}

// decodeLossyBlock decodes the JPEG stream of a lossy DNG (Compression
// 34892). Go's standard image/jpeg is a pure-Go baseline/progressive decoder,
// so there is a simple path: the block must be exactly rows x cols pixels and
// carry one (grey) or three components. The DNG tags around it (black/white,
// linearization, opcodes) are applied as for any other block. JPEG XL has no
// pure-Go decoder in this tree and is refused by Readable.
func decodeLossyBlock(src []byte, im *rawImage, rows, cols int) ([]float32, error) {
	img, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("lossy DNG JPEG: %w", err)
	}
	b := img.Bounds()
	if b.Dx() != cols || b.Dy() != rows {
		return nil, fmt.Errorf("lossy DNG JPEG is %dx%d, block is %dx%d", b.Dx(), b.Dy(), cols, rows)
	}
	n := rows * cols * im.samples
	out := make([]float32, n)
	if im.samples == 3 {
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				i := (y*cols + x) * 3
				out[i] = float32(r >> 8)
				out[i+1] = float32(g >> 8)
				out[i+2] = float32(bl >> 8)
			}
		}
		return out, nil
	}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			c := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			out[y*cols+x] = float32(c.Y)
		}
	}
	return out, nil
}

func (f *File) blockBytes(im *rawImage, off uint64, counts []uint64, idx int) ([]byte, error) {
	var count uint64
	if idx < len(counts) {
		count = counts[idx]
	}
	if count == 0 {
		return nil, errors.New("zero byte count")
	}
	if count > 512<<20 {
		return nil, fmt.Errorf("block of %d bytes is too large", count)
	}
	if off > uint64(f.size) || count > uint64(f.size)-off {
		return nil, fmt.Errorf("block %d at offset %d with %d bytes runs past the end of the file", idx, off, count)
	}
	src := make([]byte, count)
	if err := f.readAt(src, int64(off)); err != nil {
		return nil, err
	}
	switch im.comp {
	case compressionNone:
		return src, nil
	case compressionDeflate, compressionDeflate2:
		return inflate(src)
	case compressionLZW:
		return lzwDecode(src)
	case compressionPackBits:
		return packBits(src)
	case compressionJPEG, compressionLossyJPEG:
		return src, nil
	case compressionJPEGXL:
		return nil, unsupported("JPEG XL compression (52546): no pure-Go decoder is available")
	}
	return nil, unsupported("compression %d", im.comp)
}

func inflate(src []byte) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(src)); err == nil {
		out, rerr := io.ReadAll(io.LimitReader(zr, 512<<20))
		zr.Close()
		if rerr == nil {
			return out, nil
		}
	}
	fr := flate.NewReader(bytes.NewReader(src))
	defer fr.Close()
	out, err := io.ReadAll(io.LimitReader(fr, 512<<20))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// unpackSamples turns packed bytes into one integer per sample. bits 8 and
// 16 take the fast paths; the others are read MSB-first (TIFF FillOrder 1)
// or LSB-first (FillOrder 2). rows and rowSamples define the byte stride,
// which is rounded up to a byte at the end of every row.
func unpackSamples(src []byte, n, bits int, bo binary.ByteOrder, fillOrder, rows, rowSamples int) ([]uint32, error) {
	out := make([]uint32, n)
	stride := (rowSamples*bits + 7) / 8
	switch bits {
	case 8:
		for i := 0; i < n; i++ {
			r := i / rowSamples
			c := i % rowSamples
			off := r*stride + c
			if off >= len(src) {
				return nil, io.ErrUnexpectedEOF
			}
			out[i] = uint32(src[off])
		}
		return out, nil
	case 16:
		for i := 0; i < n; i++ {
			r := i / rowSamples
			c := i % rowSamples
			off := r*stride + c*2
			if off+2 > len(src) {
				return nil, io.ErrUnexpectedEOF
			}
			out[i] = uint32(bo.Uint16(src[off:]))
		}
		return out, nil
	case 32:
		for i := 0; i < n; i++ {
			r := i / rowSamples
			c := i % rowSamples
			off := r*stride + c*4
			if off+4 > len(src) {
				return nil, io.ErrUnexpectedEOF
			}
			out[i] = bo.Uint32(src[off:])
		}
		return out, nil
	}
	mask := uint32((uint64(1) << bits) - 1)
	for r := 0; r < rows; r++ {
		row := src[r*stride:]
		if len(row)*8 < rowSamples*bits {
			return nil, io.ErrUnexpectedEOF
		}
		for c := 0; c < rowSamples; c++ {
			bit := c * bits
			var v uint32
			if fillOrder == 2 {
				// Least-significant bit first within each byte.
				for k := 0; k < bits; k++ {
					pos := bit + k
					if row[pos/8]&(1<<uint(pos%8)) != 0 {
						v |= 1 << uint(k)
					}
				}
			} else {
				for k := 0; k < bits; k++ {
					pos := bit + k
					v = v<<1 | uint32((row[pos/8]>>uint(7-pos%8))&1)
				}
			}
			out[r*rowSamples+c] = v & mask
		}
	}
	return out, nil
}

// applyPredictor undoes TIFF Predictor 2 (horizontal differencing).
func applyPredictor(v []uint32, rows, rowSamples, samples, bits, predictor int) {
	if predictor != 2 {
		return
	}
	mask := uint32((uint64(1) << bits) - 1)
	if bits >= 32 {
		mask = math.MaxUint32
	}
	for r := 0; r < rows; r++ {
		base := r * rowSamples
		for i := samples; i < rowSamples; i++ {
			v[base+i] = (v[base+i] + v[base+i-samples]) & mask
		}
	}
}

// packBits is TIFF's PackBits run-length code.
func packBits(src []byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(src); {
		n := int(int8(src[i]))
		i++
		switch {
		case n >= 0:
			if i+n+1 > len(src) {
				return nil, io.ErrUnexpectedEOF
			}
			out = append(out, src[i:i+n+1]...)
			i += n + 1
		case n != -128:
			if i >= len(src) {
				return nil, io.ErrUnexpectedEOF
			}
			for k := 0; k < 1-n; k++ {
				out = append(out, src[i])
			}
			i++
		}
		if len(out) > 512<<20 {
			return nil, errors.New("packbits: output too large")
		}
	}
	return out, nil
}

// lzwDecode is the TIFF LZW variant: 9- to 12-bit MSB-first codes, a clear
// code 256, an end code 257, and the "early change" that widens the code
// one entry before the table is full.
func lzwDecode(src []byte) ([]byte, error) {
	const (
		clear = 256
		eoi   = 257
	)
	var out []byte
	table := make([][]byte, 4096)
	reset := func() {
		for i := range table {
			table[i] = nil
		}
		for i := 0; i < 256; i++ {
			table[i] = []byte{byte(i)}
		}
	}
	reset()
	width := 9
	next := 258
	var prev []byte
	bitPos := 0
	readCode := func() (int, bool) {
		if bitPos+width > len(src)*8 {
			return 0, false
		}
		var v int
		for k := 0; k < width; k++ {
			p := bitPos + k
			v = v<<1 | int((src[p/8]>>uint(7-p%8))&1)
		}
		bitPos += width
		return v, true
	}
	for {
		code, ok := readCode()
		if !ok {
			break
		}
		switch {
		case code == clear:
			reset()
			width, next, prev = 9, 258, nil
			continue
		case code == eoi:
			return out, nil
		}
		var entry []byte
		switch {
		case code < next && code < len(table) && table[code] != nil:
			entry = table[code]
		case code == next && prev != nil:
			entry = append(append([]byte(nil), prev...), prev[0])
		default:
			return nil, fmt.Errorf("lzw: code %d out of range (next %d)", code, next)
		}
		out = append(out, entry...)
		if prev != nil && next < 4096 {
			table[next] = append(append([]byte(nil), prev...), entry[0])
			next++
			// Early change: widen when the next code would not fit.
			if next == (1<<width)-1 && width < 12 {
				width++
			}
		}
		prev = entry
		if len(out) > 512<<20 {
			return nil, errors.New("lzw: output too large")
		}
	}
	return out, nil
}
