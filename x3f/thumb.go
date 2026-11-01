package x3f

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
)

// Image-section formats (low 16 bits of x3f_tools' type IDs).
const (
	FormatPlainRGB   = 3  // uncompressed 8-bit RGB rows (SD9 previews)
	FormatHuffmanRGB = 11 // Huffman-coded 8-bit RGB deltas (SD10 previews)
)

// sectionBytes reads an image section's payload (after its 28-byte header).
func (f *File) sectionBytes(im Image) ([]byte, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	buf := make([]byte, im.Length-28)
	_, err = fh.ReadAt(buf, int64(im.Offset)+28)
	return buf, err
}

// bestPreview returns the largest preview this package can decode: a JPEG,
// or an 8-bit plain/Huffman thumbnail (SD9/SD10 have no JPEG preview).
func (f *File) bestPreview() (Image, bool) {
	var best Image
	found := false
	for _, im := range f.Images {
		if im.ImgType != 2 {
			continue
		}
		switch im.Format {
		case FormatJPEG, FormatPlainRGB, FormatHuffmanRGB:
		default:
			continue
		}
		if !found || im.Width*im.Height > best.Width*best.Height {
			best, found = im, true
		}
	}
	return best, found
}

// Preview decodes the largest embedded preview. full reports whether it is
// big enough to work from at normal size (SD9/SD10 previews are 567 px).
func (f *File) Preview() (img image.Image, full bool, err error) {
	im, ok := f.bestPreview()
	if !ok {
		return nil, false, errors.New("x3f: no decodable preview")
	}
	full = im.Width >= 1000
	switch im.Format {
	case FormatJPEG:
		data, err := f.readJPEG(im)
		if err != nil {
			return nil, full, err
		}
		img, err = jpeg.Decode(bytes.NewReader(data))
		return img, full, err
	case FormatPlainRGB:
		data, err := f.sectionBytes(im)
		if err != nil {
			return nil, full, err
		}
		img, err = decodePlainRGB(data, int(im.Width), int(im.Height), int(im.RowStride))
		return img, full, err
	default:
		data, err := f.sectionBytes(im)
		if err != nil {
			return nil, full, err
		}
		img, err = decodeHuffmanRGB(data, int(im.Width), int(im.Height))
		return img, full, err
	}
}

func decodePlainRGB(data []byte, w, h, stride int) (*image.RGBA, error) {
	if stride < w*3 || len(data) < stride*(h-1)+w*3 {
		return nil, fmt.Errorf("x3f: plain preview too short (%d bytes for %dx%d stride %d)", len(data), w, h, stride)
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		src := data[y*stride:]
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < w; x++ {
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = src[x*3], src[x*3+1], src[x*3+2], 255
		}
	}
	return out, nil
}

// huffTree is a binary decoding tree; leaf >= 0 marks a leaf.
type huffTree struct {
	child [][2]int32
	leaf  []int32
}

func newHuffTree() *huffTree { return &huffTree{child: [][2]int32{{-1, -1}}, leaf: []int32{-1}} }

// add inserts a code of the given bit length (MSB first) for value.
func (t *huffTree) add(code uint32, length int, value int32) {
	n := int32(0)
	for b := length - 1; b >= 0; b-- {
		bit := (code >> uint(b)) & 1
		if t.child[n][bit] < 0 {
			t.child = append(t.child, [2]int32{-1, -1})
			t.leaf = append(t.leaf, -1)
			t.child[n][bit] = int32(len(t.child) - 1)
		}
		n = t.child[n][bit]
	}
	t.leaf[n] = value
}

type bitReader struct {
	data []byte
	pos  int // bit position
}

func (r *bitReader) bit() (uint32, bool) {
	i := r.pos >> 3
	if i >= len(r.data) {
		return 0, false
	}
	b := uint32(r.data[i]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return b, true
}

// next walks the tree to a leaf.
func (t *huffTree) next(r *bitReader) (int32, error) {
	n := int32(0)
	for t.leaf[n] < 0 {
		b, ok := r.bit()
		if !ok {
			return 0, errors.New("x3f: Huffman data ended early")
		}
		n = t.child[n][b]
		if n < 0 {
			return 0, errors.New("x3f: invalid Huffman code")
		}
	}
	return t.leaf[n], nil
}

// decodeHuffmanRGB decodes an 8-bit Huffman preview (x3f_tools'
// THUMB_HUFFMAN, row stride 0): 256 code-table entries (bit length in the
// top 5 bits, code in the low 27), then the bit stream, then one uint32 row
// offset per row. Each pixel adds a decoded delta to a per-row running value
// per channel; the stored byte is that value modulo 256.
func decodeHuffmanRGB(data []byte, w, h int) (*image.RGBA, error) {
	const tableSize = 256
	if len(data) < tableSize*4+h*4 {
		return nil, errors.New("x3f: Huffman preview too short")
	}
	tree := newHuffTree()
	for i := 0; i < tableSize; i++ {
		e := le.Uint32(data[i*4:])
		if length := int(e >> 27); length > 0 {
			tree.add(e&0x07ffffff, length, int32(i))
		}
	}
	stream := data[tableSize*4 : len(data)-h*4]
	offsets := data[len(data)-h*4:]
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		off := int(le.Uint32(offsets[y*4:]))
		if off > len(stream) {
			return nil, fmt.Errorf("x3f: row %d offset %d beyond data", y, off)
		}
		r := &bitReader{data: stream, pos: off * 8}
		var c [3]int32
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < w; x++ {
			for ch := 0; ch < 3; ch++ {
				d, err := tree.next(r)
				if err != nil {
					return nil, fmt.Errorf("row %d col %d: %w", y, x, err)
				}
				c[ch] += d
				dst[x*4+ch] = uint8(c[ch])
			}
			dst[x*4+3] = 255
		}
	}
	return out, nil
}

// readJPEGPrefix reads at most n bytes of an image section's payload. The
// EXIF thumbnails sit in the first APP1 segment, so a thumbnail never needs
// the 12-18 MB of pixel data that follows it.
func (f *File) readJPEGPrefix(im Image, n int) ([]byte, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	length := int(im.Length) - 28
	if length <= 0 {
		return nil, errors.New("x3f: empty image section")
	}
	buf := make([]byte, min(length, n))
	if _, err := fh.ReadAt(buf, int64(im.Offset)+28); err != nil {
		return nil, err
	}
	i := bytes.Index(buf[:min(len(buf), 256)], []byte{0xFF, 0xD8})
	if i < 0 {
		return nil, errors.New("x3f: embedded image is not a JPEG")
	}
	return buf[i:], nil
}

// EXIFThumbJPEG returns the largest JPEG thumbnail stored in the EXIF block
// of the file's largest embedded preview, at most maxW pixels wide. Every
// body that stores only one big preview (the Quattros, the SD14) also stores
// a 640x480 and a 160x120 EXIF thumbnail, so a list tile can use the camera's
// own thumbnail instead of decoding the 12-18 MB preview.
func (f *File) EXIFThumbJPEG(maxW int) ([]byte, error) {
	js := f.jpegImages()
	if len(js) == 0 {
		return nil, errors.New("x3f: no embedded JPEG preview")
	}
	data, err := f.readJPEGPrefix(js[0], 256<<10)
	if err != nil {
		return nil, err
	}
	if b := exifThumbJPEG(data, maxW); b != nil {
		return b, nil
	}
	return nil, errors.New("x3f: no EXIF thumbnail")
}

// exifThumbJPEG finds the JPEG thumbnails in a preview's EXIF APP1 and
// returns the largest one no wider than maxW. The APP1's TIFF block is not
// needed: every thumbnail is a complete JPEG between its own SOI and EOI.
func exifThumbJPEG(data []byte, maxW int) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil
	}
	var app1 []byte
	for p := 2; p+4 <= len(data); {
		if data[p] != 0xFF {
			p++
			continue
		}
		m := data[p+1]
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			p += 2
			continue
		}
		if m == 0xDA || m == 0xD9 {
			break
		}
		n := int(data[p+2])<<8 | int(data[p+3])
		if n < 2 || p+2+n > len(data) {
			break
		}
		if m == 0xE1 {
			seg := data[p+4 : p+2+n]
			if bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
				app1 = seg
				break
			}
		}
		p += 2 + n
	}
	if app1 == nil {
		return nil
	}
	var best []byte
	bestW := 0
	for i := 0; i+1 < len(app1); i++ {
		if app1[i] != 0xFF || app1[i+1] != 0xD8 {
			continue
		}
		j := bytes.Index(app1[i+2:], []byte{0xFF, 0xD9})
		if j < 0 {
			continue
		}
		end := i + 2 + j + 2
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(app1[i:end]))
		if err == nil && cfg.Width >= 64 && cfg.Width <= maxW && cfg.Width > bestW {
			best = app1[i:end]
			bestW = cfg.Width
		}
		i = end - 1
	}
	return best
}
