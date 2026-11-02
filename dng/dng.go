// Package dng reads DNG files: the TIFF structure, the raw sensor data
// (Bayer CFA or LinearRaw), a demosaic, and the colour development the DNG
// tags describe.
//
// It reads the tags this app already writes (render/dng.go) and the ones a
// camera or Adobe DNG Converter writes: ColorMatrix1/2, ForwardMatrix1/2,
// AsShotNeutral/AsShotWhiteXY, CalibrationIlluminant1/2, BaselineExposure,
// BlackLevel/WhiteLevel, DefaultCrop and DefaultScale. The result is linear
// sRGB, the space the film stage takes from a JPEG or TIFF source. The
// camera profile's own tone curve and hue/saturation map are deliberately
// not applied: they are the camera maker's look, and the film stage supplies
// the look here.
//
// No third-party code and no cgo: the TIFF/DNG structure, the Deflate, LZW,
// PackBits and lossless-JPEG decoders, the demosaic and the colour matrices
// are all in this package.
package dng

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// TIFF and DNG tags this package reads.
const (
	tagNewSubFileType   = 254
	tagWidth            = 256
	tagHeight           = 257
	tagBitsPerSample    = 258
	tagCompression      = 259
	tagPhotometric      = 262
	tagMake             = 271
	tagModel            = 272
	tagStripOffsets     = 273
	tagOrientation      = 274
	tagSamplesPerPixel  = 277
	tagRowsPerStrip     = 278
	tagStripByteCounts  = 279
	tagPlanarConfig     = 284
	tagSoftware         = 305
	tagPredictor        = 317
	tagTileWidth        = 322
	tagTileLength       = 323
	tagTileOffsets      = 324
	tagTileByteCounts   = 325
	tagSubIFDs          = 330
	tagFillOrder        = 266
	tagExifIFD          = 34665
	tagCFARepeatPattern = 33421
	tagCFAPattern       = 33422
	tagCFAPlaneColor    = 50710
	tagCFALayout        = 50711
	tagLinearization    = 50712
	tagBlackLevelRepeat = 50713
	tagBlackLevel       = 50714
	tagWhiteLevel       = 50717
	tagDefaultScale     = 50718
	tagDefaultCropOrig  = 50719
	tagDefaultCropSize  = 50720
	tagColorMatrix1     = 50721
	tagColorMatrix2     = 50722
	tagCameraCalib1     = 50723
	tagCameraCalib2     = 50724
	tagAnalogBalance    = 50727
	tagAsShotNeutral    = 50728
	tagAsShotWhiteXY    = 50729
	tagBaselineExposure = 50730
	tagDNGVersion       = 50706
	tagCalibIllum1      = 50778
	tagCalibIllum2      = 50779
	tagForwardMatrix1   = 50964
	tagForwardMatrix2   = 50965
	tagProfileTone      = 50940
	tagActiveArea       = 50829
	tagISO              = 34855
	tagDateTimeOriginal = 36867
)

// PhotometricInterpretation values.
const (
	photometricCFA       = 32803
	photometricLinearRaw = 34892
)

// Compression values.
const (
	compressionNone     = 1
	compressionLZW      = 5
	compressionJPEG     = 7
	compressionDeflate  = 8
	compressionPackBits = 32773
	compressionDeflate2 = 32946
)

// ErrUnsupported is wrapped by errors for valid DNGs whose features this
// reader does not implement.
var ErrUnsupported = errors.New("dng: unsupported")

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, args...))
}

// Meta is what the photo list needs from a DNG without decoding its pixels.
type Meta struct {
	Width, Height int
	Make, Model   string
	UniqueModel   string
	Software      string
	DateTime      string
	ISO           float64
	// Rotation is the clockwise rotation the file asks for in its TIFF
	// Orientation, 0/90/180/270. Mirrored orientations are returned as their
	// rotation (the mirroring is not applied).
	Rotation int
	// Bayer: the image is a colour-filter array, not an already-demosaiced
	// LinearRaw image.
	Bayer       bool
	Bits        int
	Samples     int
	Compression int
	// TwoColour: the file has no usable colour matrix; its data is taken as
	// linear sRGB. The UI should say the colour is approximate.
	ApproxColour bool
}

type tag struct {
	typ   uint16
	count uint32
	data  []byte
}

func (t tag) empty() bool { return t.count == 0 || len(t.data) == 0 }

// File is a parsed DNG. Close it when done.
type File struct {
	src      io.ReaderAt
	size     int64
	bo       binary.ByteOrder
	firstIFD int64
	ifds     []*ifd
	raw      *ifd
	Meta     Meta
	closeFn  func() error
}

type ifd struct {
	off  int64
	tags map[uint16]tag
	next int64
}

// Open parses path's DNG directory.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d, err := NewReader(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	d.closeFn = f.Close
	return d, nil
}

// Close releases the file.
func (f *File) Close() error {
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

// Info opens path only far enough to read its metadata.
func Info(path string) (Meta, error) {
	f, err := Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	return f.Meta, nil
}

// NewReader parses a DNG from an io.ReaderAt of the given size.
func NewReader(r io.ReaderAt, size int64) (*File, error) {
	f := &File{src: r, size: size}
	var head [8]byte
	if err := f.readAt(head[:], 0); err != nil {
		return nil, err
	}
	switch string(head[:2]) {
	case "II":
		f.bo = binary.LittleEndian
	case "MM":
		f.bo = binary.BigEndian
	default:
		return nil, errors.New("dng: not a TIFF file (no II or MM byte order)")
	}
	if f.bo.Uint16(head[2:]) != 42 {
		return nil, errors.New("dng: not a classic TIFF file")
	}
	off := int64(f.bo.Uint32(head[4:]))
	if off < 8 || off >= size {
		return nil, errors.New("dng: bad first IFD offset")
	}
	f.firstIFD = off
	if err := f.parseIFDs(); err != nil {
		return nil, err
	}
	if err := f.chooseRaw(); err != nil {
		return nil, err
	}
	if err := f.readMeta(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) readAt(p []byte, off int64) error {
	if off < 0 || off+int64(len(p)) > f.size {
		return io.ErrUnexpectedEOF
	}
	n, err := f.src.ReadAt(p, off)
	if err != nil {
		if errors.Is(err, io.EOF) && n == len(p) {
			return nil
		}
		return err
	}
	if n != len(p) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// typeSize is the byte size of one TIFF field value.
func typeSize(typ uint16) int {
	switch typ {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11, 13: // LONG, SLONG, FLOAT, IFD
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 0
}

func (f *File) parseIFDs() error {
	type job struct {
		off   int64
		depth int
	}
	queue := []job{{f.firstIFD, 0}}
	seen := map[int64]bool{}
	// Walk the main IFD chain (IFD0, IFD1, ...) and every SubIFD.
	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]
		if j.depth > 3 || seen[j.off] {
			continue
		}
		seen[j.off] = true
		d, err := f.readIFD(j.off)
		if err != nil {
			if j.depth == 0 {
				return err
			}
			continue
		}
		f.ifds = append(f.ifds, d)
		if d.next != 0 && d.next != j.off {
			queue = append(queue, job{d.next, j.depth})
		}
		if t, ok := d.tags[tagSubIFDs]; ok {
			for _, o := range f.uintValues(t) {
				if o != 0 {
					queue = append(queue, job{int64(o), j.depth + 1})
				}
			}
		}
		// The Exif IFD is not an image, but it holds the ISO and date.
		if t, ok := d.tags[tagExifIFD]; ok {
			for _, o := range f.uintValues(t) {
				if o == 0 || seen[int64(o)] {
					continue
				}
				seen[int64(o)] = true
				if e, err := f.readIFD(int64(o)); err == nil {
					e.next = 0 // Exif IFD: no image chain
					f.ifds = append(f.ifds, e)
				}
			}
		}
	}
	if len(f.ifds) == 0 {
		return errors.New("dng: no image file directory")
	}
	return nil
}

func (f *File) readIFD(off int64) (*ifd, error) {
	var hdr [2]byte
	if err := f.readAt(hdr[:], off); err != nil {
		return nil, err
	}
	n := int(f.bo.Uint16(hdr[:]))
	if n <= 0 || n > 4096 {
		return nil, fmt.Errorf("dng: IFD at %d has %d entries", off, n)
	}
	buf := make([]byte, 2+12*n+4)
	if err := f.readAt(buf, off); err != nil {
		return nil, err
	}
	d := &ifd{off: off, tags: map[uint16]tag{}}
	for i := 0; i < n; i++ {
		e := buf[2+12*i:]
		tg := tag{typ: f.bo.Uint16(e[2:]), count: f.bo.Uint32(e[4:])}
		sz := typeSize(tg.typ)
		if sz == 0 || tg.count == 0 || int64(tg.count) > 1<<28 {
			continue
		}
		total := int64(sz) * int64(tg.count)
		if total <= 4 {
			tg.data = append([]byte(nil), e[8:8+total]...)
		} else {
			vo := int64(f.bo.Uint32(e[8:]))
			if vo <= 0 || vo+total > f.size || total > 64<<20 {
				continue
			}
			tg.data = make([]byte, total)
			if err := f.readAt(tg.data, vo); err != nil {
				continue
			}
		}
		d.tags[f.bo.Uint16(e)] = tg
	}
	d.next = int64(f.bo.Uint32(buf[2+12*n:]))
	return d, nil
}

func (f *File) uintValues(t tag) []uint64 {
	var out []uint64
	switch t.typ {
	case 1, 6, 7:
		for _, b := range t.data {
			out = append(out, uint64(b))
		}
	case 3, 8:
		for i := 0; i+2 <= len(t.data); i += 2 {
			out = append(out, uint64(f.bo.Uint16(t.data[i:])))
		}
	case 4, 9, 13:
		for i := 0; i+4 <= len(t.data); i += 4 {
			out = append(out, uint64(f.bo.Uint32(t.data[i:])))
		}
	case 5, 10:
		for i := 0; i+8 <= len(t.data); i += 8 {
			num := f.bo.Uint32(t.data[i:])
			den := f.bo.Uint32(t.data[i+4:])
			if den != 0 {
				out = append(out, uint64(num/den))
			}
		}
	}
	return out
}

func (f *File) floatValues(t tag) []float64 {
	var out []float64
	switch t.typ {
	case 1, 6, 7:
		for _, b := range t.data {
			out = append(out, float64(b))
		}
	case 3:
		for i := 0; i+2 <= len(t.data); i += 2 {
			out = append(out, float64(f.bo.Uint16(t.data[i:])))
		}
	case 8:
		for i := 0; i+2 <= len(t.data); i += 2 {
			out = append(out, float64(int16(f.bo.Uint16(t.data[i:]))))
		}
	case 4:
		for i := 0; i+4 <= len(t.data); i += 4 {
			out = append(out, float64(f.bo.Uint32(t.data[i:])))
		}
	case 9:
		for i := 0; i+4 <= len(t.data); i += 4 {
			out = append(out, float64(int32(f.bo.Uint32(t.data[i:]))))
		}
	case 5, 10:
		for i := 0; i+8 <= len(t.data); i += 8 {
			num, den := f.bo.Uint32(t.data[i:]), f.bo.Uint32(t.data[i+4:])
			v := 0.0
			if den != 0 {
				v = float64(num) / float64(den)
				if t.typ == 10 {
					v = float64(int32(num)) / float64(int32(den))
				}
			}
			out = append(out, v)
		}
	case 11:
		for i := 0; i+4 <= len(t.data); i += 4 {
			out = append(out, float64(math.Float32frombits(f.bo.Uint32(t.data[i:]))))
		}
	case 12:
		for i := 0; i+8 <= len(t.data); i += 8 {
			out = append(out, math.Float64frombits(f.bo.Uint64(t.data[i:])))
		}
	}
	return out
}

func (f *File) ascii(t tag) string {
	s := string(t.data)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// imageIFD returns the IFD that looks like a picture (not Exif metadata).
func (f *File) imageIFD(d *ifd) bool {
	_, w := d.tags[tagWidth]
	_, h := d.tags[tagHeight]
	_, p := d.tags[tagPhotometric]
	return w && h && p
}

// chooseRaw picks the largest main image whose photometric is CFA or
// LinearRaw. A DNG usually keeps a JPEG preview in IFD0 and the raw in a
// SubIFD, so this cannot simply take IFD0.
func (f *File) chooseRaw() error {
	var best *ifd
	var bestScore int64 = -1
	for _, d := range f.ifds {
		if !f.imageIFD(d) {
			continue
		}
		ph := f.uintValues(d.tags[tagPhotometric])
		if len(ph) == 0 {
			continue
		}
		if ph[0] != photometricCFA && ph[0] != photometricLinearRaw {
			continue
		}
		w := int64(firstUint(f, d, tagWidth))
		h := int64(firstUint(f, d, tagHeight))
		if w <= 0 || h <= 0 {
			continue
		}
		score := w * h
		// A reduced-resolution image (NewSubFileType bit 0) is a preview.
		if n := firstUint(f, d, tagNewSubFileType); n&1 != 0 {
			score /= 8
		}
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	if best == nil {
		return errors.New("dng: no Bayer or LinearRaw image in the file")
	}
	f.raw = best
	return nil
}

func firstUint(f *File, d *ifd, tag uint16) uint64 {
	if t, ok := d.tags[tag]; ok {
		if v := f.uintValues(t); len(v) > 0 {
			return v[0]
		}
	}
	return 0
}

func (f *File) readMeta() error {
	d := f.raw
	m := &f.Meta
	m.Width = int(firstUint(f, d, tagWidth))
	m.Height = int(firstUint(f, d, tagHeight))
	if t, ok := d.tags[tagPhotometric]; ok {
		if v := f.uintValues(t); len(v) > 0 && v[0] == photometricCFA {
			m.Bayer = true
		}
	}
	if t, ok := d.tags[tagBitsPerSample]; ok {
		if v := f.uintValues(t); len(v) > 0 {
			m.Bits = int(v[0])
		}
	}
	if m.Bits == 0 {
		m.Bits = 16
	}
	m.Compression = int(firstUint(f, d, tagCompression))
	if m.Compression == 0 {
		m.Compression = compressionNone
	}
	m.Samples = int(firstUint(f, d, tagSamplesPerPixel))
	if m.Samples == 0 {
		m.Samples = 1
	}
	m.Make = f.firstASCII(tagMake)
	m.Model = f.firstASCII(tagModel)
	m.UniqueModel = f.firstASCII(50708)
	m.Software = f.firstASCII(tagSoftware)
	m.DateTime = f.firstASCII(tagDateTimeOriginal)
	if m.DateTime == "" {
		m.DateTime = f.firstASCII(306)
	}
	if t, ok := f.findTag(tagISO); ok {
		if v := f.floatValues(t); len(v) > 0 {
			m.ISO = v[0]
		}
	}
	if t, ok := f.findTag(tagOrientation); ok {
		if v := f.uintValues(t); len(v) > 0 {
			switch v[0] {
			case 3, 4:
				m.Rotation = 180
			case 5, 6:
				m.Rotation = 90
			case 7, 8:
				m.Rotation = 270
			}
		}
	}
	// A LinearRaw file with no colour matrix cannot be colour managed; the
	// caller is told so rather than being handed an invented matrix.
	if !f.hasColourMatrix() {
		m.ApproxColour = true
	}
	return nil
}

func (f *File) firstASCII(tg uint16) string {
	if t, ok := f.findTag(tg); ok {
		return f.ascii(t)
	}
	return ""
}

// findTag looks in the raw IFD first, then the main IFD, then any IFD.
func (f *File) findTag(tg uint16) (tag, bool) {
	if f.raw != nil {
		if t, ok := f.raw.tags[tg]; ok {
			return t, true
		}
	}
	for _, d := range f.ifds {
		if t, ok := d.tags[tg]; ok {
			return t, true
		}
	}
	return tag{}, false
}

func (f *File) hasColourMatrix() bool {
	for _, tg := range []uint16{tagColorMatrix1, tagColorMatrix2, tagForwardMatrix1, tagForwardMatrix2} {
		if t, ok := f.findTag(tg); ok && len(f.floatValues(t)) >= 9 {
			return true
		}
	}
	return false
}

// Readable reports whether this reader implements the file's pixel storage.
// The photo list uses it to say so plainly instead of failing on the first
// render. It does not read any pixels.
func (m Meta) Readable() error {
	switch m.Compression {
	case 0, compressionNone, compressionLZW, compressionJPEG, compressionDeflate, compressionDeflate2, compressionPackBits:
	default:
		return fmt.Errorf("compression %d is not supported", m.Compression)
	}
	if m.Samples != 1 && m.Samples != 3 {
		return fmt.Errorf("%d samples per pixel is not supported", m.Samples)
	}
	return nil
}

// Description is a one-line summary for a log or a test.
func (m Meta) Description() string {
	bayer := "linear"
	if m.Bayer {
		bayer = "Bayer"
	}
	return fmt.Sprintf("%dx%d %s, %d-bit, compression %d, %s %s", m.Width, m.Height, bayer, m.Bits, m.Compression, m.Make, m.Model)
}
