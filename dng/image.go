package dng

import (
	"errors"
	"fmt"
	"math"
)

// rawImage is the parsed pixel layout of one DNG IFD: how the samples are
// stored (strips or tiles, packing, compression), where the CFA is, and the
// black/white/crop numbers.
type rawImage struct {
	ifd       *ifd
	width     int
	height    int
	bits      int
	samples   int
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

	blackRepeatW, blackRepeatH int
	black                      []float64
	white                      []float64
	linTable                   []uint16

	cropX, cropY, cropW, cropH int
	scaleX, scaleY             float64

	orientation int
}

func (f *File) image() (*rawImage, error) {
	d := f.raw
	im := &rawImage{
		ifd: d, width: int(firstUint(f, d, tagWidth)), height: int(firstUint(f, d, tagHeight)),
		samples: int(firstUint(f, d, tagSamplesPerPixel)), comp: int(firstUint(f, d, tagCompression)),
		predictor: int(firstUint(f, d, tagPredictor)), fillOrder: int(firstUint(f, d, tagFillOrder)),
		planar: int(firstUint(f, d, tagPlanarConfig)), cfaLayout: int(firstUint(f, d, tagCFALayout)),
		scaleX: 1, scaleY: 1, orientation: 1,
	}
	if im.samples == 0 {
		im.samples = 1
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
		im.bits = 16
	}
	if im.bits != 8 && im.bits != 10 && im.bits != 12 && im.bits != 14 && im.bits != 16 && im.bits != 32 {
		return nil, unsupported("%d-bit samples", im.bits)
	}
	if im.planar != 1 {
		return nil, unsupported("planar configuration %d (only chunky is supported)", im.planar)
	}
	if im.width <= 0 || im.height <= 0 || int64(im.width)*int64(im.height) > 400e6 {
		return nil, fmt.Errorf("dng: bad image size %dx%d", im.width, im.height)
	}

	// Strips and tiles.
	im.rowsPerStrip = int(firstUint(f, d, tagRowsPerStrip))
	if im.rowsPerStrip <= 0 || im.rowsPerStrip > im.height {
		im.rowsPerStrip = im.height
	}
	if t, ok := d.tags[tagStripOffsets]; ok {
		im.stripOff = f.uintValues(t)
		im.stripCount = f.uintValues(d.tags[tagStripByteCounts])
	}
	if t, ok := d.tags[tagTileOffsets]; ok {
		im.tile = true
		im.tileOff = f.uintValues(t)
		im.tileCount = f.uintValues(d.tags[tagTileByteCounts])
		im.tileW = int(firstUint(f, d, tagTileWidth))
		im.tileH = int(firstUint(f, d, tagTileLength))
		if im.tileW <= 0 || im.tileH <= 0 {
			return nil, errors.New("dng: tiles without TileWidth/TileLength")
		}
	}
	if !im.tile && len(im.stripOff) == 0 {
		return nil, errors.New("dng: no strips or tiles")
	}

	// CFA pattern.
	if im.samples == 1 {
		pw, ph := 2, 2
		if t, ok := d.tags[tagCFARepeatPattern]; ok {
			if v := f.uintValues(t); len(v) >= 2 && v[0] > 0 && v[1] > 0 {
				pw, ph = int(v[0]), int(v[1])
			}
		}
		var pat []uint64
		if t, ok := d.tags[tagCFAPattern]; ok {
			pat = f.uintValues(t)
		}
		if len(pat) < pw*ph {
			return nil, errors.New("dng: CFA image without a complete CFAPattern")
		}
		if pw*ph > 64 {
			return nil, unsupported("CFA pattern %dx%d", pw, ph)
		}
		plane := []uint64{0, 1, 2}
		if t, ok := d.tags[tagCFAPlaneColor]; ok {
			if v := f.uintValues(t); len(v) >= 3 {
				plane = v[:3]
			}
		}
		im.cfaPW, im.cfaPH = pw, ph
		im.cfa = make([]byte, pw*ph)
		for i := range im.cfa {
			v := int(pat[i])
			if v < 0 || v >= len(plane) || plane[v] > 2 {
				return nil, unsupported("CFA plane colour %d (only RGB planes are supported)", v)
			}
			im.cfa[i] = byte(plane[v])
		}
	} else if im.samples != 3 {
		return nil, unsupported("%d samples per pixel (only 1 (CFA) and 3 (LinearRaw) are supported)", im.samples)
	}
	if im.samples == 3 {
		// DNG LinearRaw is three-channel RGB; anything else is a colour space
		// this reader does not know.
		ph := firstUint(f, d, tagPhotometric)
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
			// per sample, handled by blackAt
		case im.samples == 1:
			im.blackRepeatW, im.blackRepeatH = len(im.black), 1
		}
	}
	if t, ok := d.tags[tagWhiteLevel]; ok {
		im.white = f.floatValues(t)
	}
	if len(im.white) > 1 && len(im.white) != im.samples {
		// Some files give a CFA repeat of white levels; use the first.
		im.white = im.white[:1]
	}
	if t, ok := d.tags[tagLinearization]; ok {
		for _, v := range f.uintValues(t) {
			im.linTable = append(im.linTable, uint16(v))
		}
	}

	// Default crop, then DefaultScale. Our own writer omits both.
	im.cropX, im.cropY, im.cropW, im.cropH = 0, 0, im.width, im.height
	if t, ok := d.tags[tagActiveArea]; ok {
		if v := f.uintValues(t); len(v) >= 4 && v[2] > v[0] && v[3] > v[1] {
			im.cropY, im.cropX = int(v[0]), int(v[1])
			im.cropH, im.cropW = int(v[2])-im.cropY, int(v[3])-im.cropX
		}
	}
	if t, ok := d.tags[tagDefaultCropOrig]; ok {
		if v := f.floatValues(t); len(v) >= 2 {
			im.cropX, im.cropY = int(math.Round(v[0])), int(math.Round(v[1]))
		}
	}
	if t, ok := d.tags[tagDefaultCropSize]; ok {
		if v := f.floatValues(t); len(v) >= 2 && v[0] > 0 && v[1] > 0 {
			im.cropW, im.cropH = int(math.Round(v[0])), int(math.Round(v[1]))
		}
	}
	im.cropX = max(0, min(im.cropX, im.width-1))
	im.cropY = max(0, min(im.cropY, im.height-1))
	im.cropW = max(1, min(im.cropW, im.width-im.cropX))
	im.cropH = max(1, min(im.cropH, im.height-im.cropY))
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

// blackAt returns the black level for plane c (0,1,2) at pixel (x, y).
func (im *rawImage) blackAt(c, x, y int) float64 {
	return levelAt(im.black, c, x, y, im.blackRepeatW, im.blackRepeatH, im.samples)
}

func (im *rawImage) whiteAt(c, x, y int) float64 {
	if len(im.white) == 0 {
		return float64(uint64(1)<<im.bits) - 1
	}
	return levelAt(im.white, c, x, y, 1, 1, im.samples)
}

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
	if len(v) == samples && c < len(v) {
		return v[c]
	}
	return v[0]
}

// cfaAt is the colour plane at pixel (x, y).
func (im *rawImage) cfaAt(x, y int) int {
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
