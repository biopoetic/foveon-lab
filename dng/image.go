package dng

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// maxPixels bounds one decode. It is deliberately below the point where the
// float working images would run into gigabytes: a CFA pixel costs 4 bytes
// for the raw plane and 12 for the developed RGB. 64 MP keeps that under
// about 1 GB even at full size, and no current still camera is refused.
// It is a variable so the fuzz target can lower it.
var maxPixels int64 = 64_000_000

// rawImage is the parsed pixel layout of one DNG IFD: how the samples are
// stored (strips or tiles, packing, compression), where the CFA is, and the
// black/white/crop numbers.
type rawImage struct {
	ifd       *ifd
	width     int
	height    int
	bits      int
	samples   int
	sampleFmt int
	comp      int
	predictor int
	fillOrder int
	planar    int

	rowsPerStrip int
	stripOff     []uint64
	stripCount   []uint64

	tile      bool
	tileW     int
	tileH     int
	tileOff   []uint64
	tileCount []uint64

	cfaPW, cfaPH int
	cfa          []byte // 0 = R, 1 = G, 2 = B
	cfaLayout    int
	mono         bool

	blackRepeatW, blackRepeatH int
	black                      []float64
	blackDeltaH                []float64
	blackDeltaV                []float64
	white                      []float64
	whiteRepeatW, whiteRepeatH int
	linTable                   []uint16
	masked                     []area

	cropX, cropY, cropW, cropH int
	scaleX, scaleY             float64

	orientation int

	opcodes1, opcodes2, opcodes3 []opcode
	opcodeNotice                 string
	gainMaps                     []gainMap
	trim                         []area
	stage3                       []stage3op
}

type area struct{ top, left, bottom, right int }

func (a area) empty() bool { return a.right <= a.left || a.bottom <= a.top }

func (f *File) image() (*rawImage, error) {
	d := f.raw
	im := &rawImage{
		ifd: d, width: int(firstUint(f, d, tagWidth)), height: int(firstUint(f, d, tagHeight)),
		samples: int(firstUint(f, d, tagSamplesPerPixel)), comp: int(firstUint(f, d, tagCompression)),
		predictor: int(firstUint(f, d, tagPredictor)), fillOrder: int(firstUint(f, d, tagFillOrder)),
		planar: int(firstUint(f, d, tagPlanarConfig)), cfaLayout: int(firstUint(f, d, tagCFALayout)),
		scaleX: 1, scaleY: 1, orientation: 1,
		sampleFmt: int(firstUint(f, d, tagSampleFormat)),
	}
	if im.samples == 0 {
		im.samples = 1
	}
	if im.sampleFmt == 0 {
		im.sampleFmt = 1
	}
	if im.comp == 0 {
		im.comp = compressionNone
	}
	if im.fillOrder == 0 {
		im.fillOrder = 1
	}
	if im.planar == 0 {
		im.planar = 1
	}
	if t, ok := d.tags[tagBitsPerSample]; ok {
		v := f.uintValues(t)
		if len(v) == 0 {
			return nil, errors.New("dng: empty BitsPerSample")
		}
		im.bits = int(v[0])
		for _, b := range v {
			if int(b) != im.bits {
				return nil, unsupported("mixed bit depths %v", v)
			}
		}
	}
	if im.bits == 0 {
		if im.comp == compressionLossyJPEG {
			im.bits = 8
		} else if im.sampleFmt == 3 {
			im.bits = 16
		} else {
			im.bits = 16
		}
	}
	switch im.sampleFmt {
	case 1:
		if im.bits != 8 && im.bits != 10 && im.bits != 12 && im.bits != 14 && im.bits != 16 {
			return nil, unsupported("%d-bit integer samples (8-16 bits are supported)", im.bits)
		}
	case 2:
		return nil, unsupported("signed integer samples (SampleFormat 2)")
	case 3:
		if im.bits != 16 && im.bits != 32 {
			return nil, unsupported("%d-bit float samples (16 and 32 bits are supported)", im.bits)
		}
	default:
		return nil, unsupported("SampleFormat %d", im.sampleFmt)
	}
	if im.sampleFmt == 3 && im.predictor > 1 {
		return nil, unsupported("TIFF Predictor %d on float samples", im.predictor)
	}
	if im.planar != 1 {
		return nil, unsupported("planar configuration %d (only chunky is supported)", im.planar)
	}
	if im.width <= 0 || im.height <= 0 {
		return nil, fmt.Errorf("dng: bad image size %dx%d", im.width, im.height)
	}
	pixels := int64(im.width) * int64(im.height)
	if pixels > maxPixels {
		return nil, fmt.Errorf("dng: image %dx%d has %d pixels, more than this reader decodes at once (%d)", im.width, im.height, pixels, maxPixels)
	}

	// Strips and tiles.
	im.rowsPerStrip = int(firstUint(f, d, tagRowsPerStrip))
	if im.rowsPerStrip <= 0 || im.rowsPerStrip > im.height {
		im.rowsPerStrip = im.height
	}
	if t, ok := d.tags[tagStripOffsets]; ok {
		im.stripOff = f.uintValues(t)
		im.stripCount = f.uintValues(d.tags[tagStripByteCounts])
		if len(im.stripCount) != len(im.stripOff) {
			return nil, fmt.Errorf("dng: %d strip offsets but %d byte counts", len(im.stripOff), len(im.stripCount))
		}
	}
	if t, ok := d.tags[tagTileOffsets]; ok {
		im.tile = true
		im.tileOff = f.uintValues(t)
		im.tileCount = f.uintValues(d.tags[tagTileByteCounts])
		if len(im.tileCount) != len(im.tileOff) {
			return nil, fmt.Errorf("dng: %d tile offsets but %d byte counts", len(im.tileOff), len(im.tileCount))
		}
		im.tileW = int(firstUint(f, d, tagTileWidth))
		im.tileH = int(firstUint(f, d, tagTileLength))
		if im.tileW <= 0 || im.tileH <= 0 {
			return nil, errors.New("dng: tiles without TileWidth/TileLength")
		}
		if im.tileW > 1<<20 || im.tileH > 1<<20 {
			return nil, fmt.Errorf("dng: tile size %dx%d is too large", im.tileW, im.tileH)
		}
	}
	if !im.tile && len(im.stripOff) == 0 {
		return nil, errors.New("dng: no strips or tiles")
	}

	// CFA pattern, monochrome plane, or LinearRaw.
	ph := firstUint(f, d, tagPhotometric)
	if im.samples == 1 {
		if ph == photometricLinearRaw {
			// One plane of LinearRaw is a monochrome sensor.
			im.mono = true
		} else {
			pw, phh := 2, 2
			if t, ok := d.tags[tagCFARepeatPattern]; ok {
				if v := f.uintValues(t); len(v) >= 2 && v[0] > 0 && v[1] > 0 {
					pw, phh = int(v[0]), int(v[1])
				}
			}
			var pat []uint64
			if t, ok := d.tags[tagCFAPattern]; ok {
				pat = f.uintValues(t)
			}
			if len(pat) < pw*phh {
				return nil, errors.New("dng: CFA image without a complete CFAPattern")
			}
			if pw*phh > 64 {
				return nil, unsupported("CFA pattern %dx%d", pw, phh)
			}
			plane := []uint64{0, 1, 2}
			if t, ok := d.tags[tagCFAPlaneColor]; ok {
				if v := f.uintValues(t); len(v) >= 3 {
					plane = v[:3]
				}
			}
			allSame := true
			for _, v := range pat[1 : pw*phh] {
				if v != pat[0] {
					allSame = false
					break
				}
			}
			if pw*phh == 1 || allSame {
				// A 1x1 pattern, or a pattern that names one colour
				// everywhere, is a monochrome sensor with a CFA-style IFD.
				im.mono = true
				im.cfaPW, im.cfaPH = 1, 1
				im.cfa = []byte{0}
			} else if pw != 2 || phh != 2 {
				return nil, unsupported("CFA pattern %dx%d (only 2x2 Bayer, or a monochrome 1x1 pattern, is demosaiced; X-Trans and other layouts are not)", pw, phh)
			} else {
				im.cfaPW, im.cfaPH = pw, phh
				im.cfa = make([]byte, pw*phh)
				for i := range im.cfa {
					v := int(pat[i])
					if v < 0 || v >= len(plane) || plane[v] > 2 {
						return nil, unsupported("CFA plane colour %d (only RGB planes are supported)", v)
					}
					im.cfa[i] = byte(plane[v])
				}
			}
		}
	} else if im.samples != 3 {
		return nil, unsupported("%d samples per pixel (only 1 (CFA/monochrome) and 3 (LinearRaw) are supported)", im.samples)
	}
	if im.samples == 3 {
		if ph != 0 && ph != photometricLinearRaw {
			return nil, unsupported("photometric interpretation %d with 3 samples", ph)
		}
	}
	if im.cfaLayout != 0 && im.cfaLayout != 1 {
		return nil, unsupported("CFA layout %d", im.cfaLayout)
	}

	// Black level: a scalar, one per sample, or a repeat pattern.
	im.blackRepeatW, im.blackRepeatH = 1, 1
	if t, ok := d.tags[tagBlackLevelRepeat]; ok {
		if v := f.uintValues(t); len(v) >= 2 && v[0] > 0 && v[1] > 0 {
			im.blackRepeatW, im.blackRepeatH = int(v[0]), int(v[1])
		}
	}
	if t, ok := d.tags[tagBlackLevel]; ok {
		im.black = f.floatValues(t)
	}
	if len(im.black) > 1 && im.blackRepeatW*im.blackRepeatH == 1 {
		switch {
		case len(im.black) == 4 && im.samples == 1:
			im.blackRepeatW, im.blackRepeatH = 2, 2
		case len(im.black) == im.samples:
			// per sample, handled by levelAt
		case im.samples == 1:
			im.blackRepeatW, im.blackRepeatH = len(im.black), 1
		}
	}
	if t, ok := d.tags[tagBlackLevelDeltaH]; ok {
		im.blackDeltaH = f.floatValues(t)
	}
	if t, ok := d.tags[tagBlackLevelDeltaV]; ok {
		im.blackDeltaV = f.floatValues(t)
	}
	if t, ok := d.tags[tagWhiteLevel]; ok {
		im.white = f.floatValues(t)
	}
	im.whiteRepeatW, im.whiteRepeatH = 1, 1
	if len(im.white) > 1 && im.samples == 1 && len(im.white) == 4 {
		// Some files repeat the white level over the 2x2 CFA; accept it.
		im.whiteRepeatW, im.whiteRepeatH = 2, 2
	}
	if t, ok := d.tags[tagLinearization]; ok {
		for _, v := range f.uintValues(t) {
			im.linTable = append(im.linTable, uint16(v))
		}
	}
	if t, ok := d.tags[tagMaskedAreas]; ok {
		if v := f.uintValues(t); len(v) >= 4 {
			if len(v)%4 != 0 {
				return nil, fmt.Errorf("dng: MaskedAreas has %d values, not a multiple of 4", len(v))
			}
			for i := 0; i+3 < len(v); i += 4 {
				a := area{top: int(v[i]), left: int(v[i+1]), bottom: int(v[i+2]), right: int(v[i+3])}
				if a.empty() || a.top < 0 || a.left < 0 || a.bottom > im.height || a.right > im.width {
					return nil, fmt.Errorf("dng: MaskedAreas rectangle %d is outside the %dx%d image", i/4, im.width, im.height)
				}
				im.masked = append(im.masked, a)
			}
		}
	}

	// Opcode lists. They are applied later, but TrimBounds affects the crop
	// and an unknown required opcode must be reported.
	if err := f.parseOpcodes(im); err != nil {
		return nil, err
	}

	// Default crop, then DefaultScale. ActiveArea sets the usable rectangle;
	// DefaultCropOrigin is relative to its top-left corner (DNG 1.6,
	// DefaultCropOrigin), which is why the two are added rather than one
	// replacing the other.
	im.cropX, im.cropY, im.cropW, im.cropH = 0, 0, im.width, im.height
	active := area{top: 0, left: 0, bottom: im.height, right: im.width}
	if t, ok := d.tags[tagActiveArea]; ok {
		if v := f.uintValues(t); len(v) >= 4 && v[2] > v[0] && v[3] > v[1] {
			a := area{top: int(v[0]), left: int(v[1]), bottom: int(v[2]), right: int(v[3])}
			if a.top >= 0 && a.left >= 0 && a.bottom <= im.height && a.right <= im.width {
				active = a
			}
		}
	}
	im.cropX, im.cropY, im.cropW, im.cropH = active.left, active.top, active.right-active.left, active.bottom-active.top
	if t, ok := d.tags[tagDefaultCropOrig]; ok {
		if v := f.floatValues(t); len(v) >= 2 {
			im.cropX += int(math.Round(v[0]))
			im.cropY += int(math.Round(v[1]))
		}
	}
	if t, ok := d.tags[tagDefaultCropSize]; ok {
		if v := f.floatValues(t); len(v) >= 2 && v[0] > 0 && v[1] > 0 {
			im.cropW, im.cropH = int(math.Round(v[0])), int(math.Round(v[1]))
		}
	}
	// Keep the default crop inside the active area.
	im.cropX = max(active.left, min(im.cropX, active.right-1))
	im.cropY = max(active.top, min(im.cropY, active.bottom-1))
	im.cropW = max(1, min(im.cropW, active.right-im.cropX))
	im.cropH = max(1, min(im.cropH, active.bottom-im.cropY))
	// TrimBounds opcodes trim the image before the default crop; intersect.
	for _, t := range im.trim {
		x0, y0 := max(im.cropX, t.left), max(im.cropY, t.top)
		x1, y1 := min(im.cropX+im.cropW, t.right), min(im.cropY+im.cropH, t.bottom)
		if x1 <= x0 || y1 <= y0 {
			return nil, fmt.Errorf("dng: TrimBounds leaves no image in the default crop")
		}
		im.cropX, im.cropY, im.cropW, im.cropH = x0, y0, x1-x0, y1-y0
	}
	if t, ok := d.tags[tagDefaultScale]; ok {
		if v := f.floatValues(t); len(v) >= 2 && v[0] > 0 && v[1] > 0 {
			im.scaleX, im.scaleY = v[0], v[1]
		}
	}
	if t, ok := d.tags[tagOrientation]; ok {
		if v := f.uintValues(t); len(v) > 0 && v[0] >= 1 && v[0] <= 8 {
			im.orientation = int(v[0])
		}
	}
	return im, nil
}

// blackAt returns the black level for plane c (0,1,2) at pixel (x, y),
// including the per-column and per-row BlackLevelDelta corrections.
func (im *rawImage) blackAt(c, x, y int) float64 {
	v := levelAt(im.black, c, x, y, im.blackRepeatW, im.blackRepeatH, im.samples)
	if len(im.blackDeltaH) > 0 {
		v += im.blackDeltaH[min(x, len(im.blackDeltaH)-1)]
	}
	if len(im.blackDeltaV) > 0 {
		v += im.blackDeltaV[min(y, len(im.blackDeltaV)-1)]
	}
	return v
}

func (im *rawImage) whiteAt(c, x, y int) float64 {
	if len(im.white) == 0 {
		if im.sampleFmt == 3 {
			return 1
		}
		return float64(uint64(1)<<im.bits) - 1
	}
	return levelAt(im.white, c, x, y, im.whiteRepeatW, im.whiteRepeatH, im.samples)
}

// levelAt indexes a per-plane, per-CFA-cell or per-pixel-repeat tag. The
// order of the checks matters: a 4-value BlackLevel on a 1-sample CFA is a
// 2x2 repeat, while a 3-value one on a 3-sample LinearRaw is per plane.
func levelAt(v []float64, c, x, y, rw, rh, samples int) float64 {
	if len(v) == 0 {
		return 0
	}
	if len(v) == 1 {
		return v[0]
	}
	if rw > 0 && rh > 0 && rw*rh == len(v) && (rw > 1 || rh > 1) {
		return v[(y%rh)*rw+(x%rw)]
	}
	if samples > 1 && len(v) == samples*(rw*rh) && (rw > 1 || rh > 1) {
		return v[(((y%rh)*rw+(x%rw))*samples)+c]
	}
	if len(v) == samples && c < len(v) {
		return v[c]
	}
	return v[0]
}

// cfaAt is the colour plane at pixel (x, y).
func (im *rawImage) cfaAt(x, y int) int {
	if im.mono || len(im.cfa) == 0 {
		return 0
	}
	return int(im.cfa[(y%im.cfaPH)*im.cfaPW+(x%im.cfaPW)])
}

// rowBytes is the packed byte length of one row of cols pixels.
func (im *rawImage) rowBytes(cols int) int64 {
	return (int64(cols)*int64(im.samples)*int64(im.bits) + 7) / 8
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// bytesToFloat32 is the exact conversion of a stored integer sample.
func bytesToFloat32(v []uint32) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}

// halfToFloat converts an IEEE 754 binary16 value to float64.
func halfToFloat(h uint16) float64 {
	sign := uint32(h>>15) & 1
	exp := uint32(h>>10) & 0x1f
	mant := uint32(h) & 0x3ff
	var f uint32
	switch {
	case exp == 0:
		if mant == 0 {
			f = sign << 31
		} else {
			// Subnormal half: normalize it.
			e := uint32(127 - 15 + 1)
			for mant&0x400 == 0 {
				mant <<= 1
				e--
			}
			mant &= 0x3ff
			f = sign<<31 | e<<23 | mant<<13
		}
	case exp == 0x1f:
		f = sign<<31 | 0xff<<23 | mant<<13
	default:
		f = sign<<31 | (exp+127-15)<<23 | mant<<13
	}
	return float64(math.Float32frombits(f))
}

// unpackFloats reads 16-bit half or 32-bit float samples.
func unpackFloats(src []byte, n, bits int, bo binary.ByteOrder, rows, rowSamples int) ([]float64, error) {
	out := make([]float64, n)
	stride := (rowSamples*bits + 7) / 8
	step := bits / 8
	for i := 0; i < n; i++ {
		r := i / rowSamples
		c := i % rowSamples
		off := r*stride + c*step
		if off+step > len(src) {
			return nil, errors.New("dng: float block is shorter than its rows")
		}
		if bits == 16 {
			out[i] = halfToFloat(bo.Uint16(src[off:]))
		} else {
			out[i] = float64(math.Float32frombits(bo.Uint32(src[off:])))
		}
	}
	return out, nil
}
