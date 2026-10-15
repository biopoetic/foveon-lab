package render

// The GPU preview: the editor renders the photo on the GPU (web/gpu.js)
// from the Base, sent once as half floats (GPUBase), and a recipe of the
// current settings (NewGPURecipe). The recipe is the state of the same
// pixOp the CPU render builds — its matrices, and its per-brightness
// functions sampled into tables — so the GPU executes this file's model,
// not a second copy of it. What the GPU can't do yet (halation) is
// reported in CPU, and the editor renders those settings here instead.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
)

// GPULutN: the tables have GPULutN+1 samples over 0..1.
const GPULutN = toneLUTN

// GPUCorr is a RawCorrection (raw base or measured colour mode) for the
// GPU: Matrix then a curve per channel on the encoded value (Curves, 3 ×
// GPULutN+1, linear output), or the taste form (Tone over enc(Y), linear
// output, and Sat).
type GPUCorr struct {
	Matrix [9]float32 `json:"matrix"`
	Curves []float32  `json:"curves,omitempty"`
	Tone   []float32  `json:"tone,omitempty"`
	Sat    float32    `json:"sat"`
}

func gpuCorr(c *RawCorrection) *GPUCorr {
	if c == nil {
		return nil
	}
	g := &GPUCorr{Sat: float32(c.Sat)}
	if len(c.Tone) > 0 {
		g.Tone = make([]float32, GPULutN+1)
		for i := range g.Tone {
			g.Tone[i] = float32(c.tone(float64(dec(float32(i) / GPULutN))))
		}
		return g
	}
	for i, v := range c.Matrix {
		g.Matrix[i] = float32(v)
	}
	g.Curves = make([]float32, 3*(GPULutN+1))
	for k := 0; k < 3; k++ {
		for i := 0; i <= GPULutN; i++ {
			g.Curves[k*(GPULutN+1)+i] = dec(float32(c.curve(k, float64(i)/GPULutN)))
		}
	}
	return g
}

// GPURecipe is a render's settings for the GPU (see web/gpu.js for the
// order of the steps, which follows renderInto / pixOp.apply).
type GPURecipe struct {
	CPU string `json:"cpu,omitempty"` // non-empty: render on the CPU (why)

	Undo      *[9]float32 `json:"undo,omitempty"`
	UndoGamma bool        `json:"undoGamma,omitempty"`
	Layer     *[9]float32 `json:"layer,omitempty"`
	LayerMono *[3]float32 `json:"layerMono,omitempty"`
	Mono      bool        `json:"mono"`

	Measured bool `json:"measured"`
	// Formula path.
	Gain          [3]float32 `json:"gain"`
	MaskScale     float32    `json:"maskScale"`
	Fill          float32    `json:"fill"`
	HL            float32    `json:"hl"`
	Blk           float32    `json:"blk"`
	CT            float32    `json:"ct"`
	SatF          float32    `json:"satF"`
	Mode          int        `json:"mode"`
	FilmTone      []float32  `json:"filmTone,omitempty"` // filmToneN+1 over Y^(1/4)
	FilmToneScale float32    `json:"filmToneScale,omitempty"`
	// Measured path.
	WB         *GPUCorr   `json:"wb,omitempty"`
	ModeMap    *GPUCorr   `json:"modeMap,omitempty"`
	Tint       [3]float32 `json:"tint"`
	Pre        []float32  `json:"pre,omitempty"` // x then chroma, 2 × GPULutN+1
	Post       []float32  `json:"post,omitempty"`
	FillABCK   []float32  `json:"fillABCK,omitempty"`  // two-scale Fill Light: A, B, C, chroma
	FillLocal  []float32  `json:"fillLocal,omitempty"` // one-scale: 8×8 over (m, x), then chroma GPULutN+1
	SatMeasure float32    `json:"satMeasure"`

	// Film grain (output-linear) and sharpening (on the encoded image).
	GrainSigma float32 `json:"grainSigma"`
	GrainMono  bool    `json:"grainMono"`
	SppSharpen float32 `json:"sppSharpen"`
	Sharpen    float32 `json:"sharpen"`

	// A colour film (colorfilm.go): its matrices here; its curves and 3-D
	// table from /api/gpu/colorfilm?key= (GPUColorFilmData), fetched once.
	ColorFilm *GPUColorFilm `json:"colorFilm,omitempty"`
}

// GPUColorFilm is a colour film's per-render part.
type GPUColorFilm struct {
	Key    string     `json:"key"`
	RP     [18]float32 `json:"rp"`  // sensor layers -> film exposures (colorFilm.rp)
	Inv    [9]float32 `json:"inv"` // linear sRGB -> sensor layers
	HWhite float32    `json:"hWhite"`
}

// GPU colour-film tables: each layer's curve sampled over log H, and the
// 3-D table over the densities.
const (
	gpuCFCurveN = 2048
	gpuCFLgMin  = -7.0
	gpuCFLgMax  = 3.0
)

var (
	gpuCFMu   sync.Mutex
	gpuCFKeys = map[string]*colorFilm{}
)

// GPUColorFilmData is a colour film's tables for the GPU: a little-endian
// uint32 header length, a JSON header, then float32s: the 3-D table
// (cfLutN³ × 3, -log10 linear sRGB over D_R, D_G, D_B; R fastest is the
// LAST axis, as colorFilm.lut), then the three curves (gpuCFCurveN each,
// density over log H from gpuCFLgMin to gpuCFLgMax).
func GPUColorFilmData(key string) ([]byte, bool) {
	gpuCFMu.Lock()
	f := gpuCFKeys[key]
	gpuCFMu.Unlock()
	if f == nil {
		return nil, false
	}
	hdr, _ := json.Marshal(map[string]any{"n": cfLutN, "dmax": cfLutDMax, "curveN": gpuCFCurveN, "lgMin": gpuCFLgMin, "lgMax": gpuCFLgMax})
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(len(hdr)))
	buf.Write(hdr)
	for buf.Len()%4 != 0 {
		buf.WriteByte(' ')
	}
	binary.Write(&buf, binary.LittleEndian, f.lut)
	cv := make([]float32, 3*gpuCFCurveN)
	for k := 0; k < 3; k++ {
		for i := 0; i < gpuCFCurveN; i++ {
			lg := gpuCFLgMin + (gpuCFLgMax-gpuCFLgMin)*float64(i)/(gpuCFCurveN-1)
			cv[k*gpuCFCurveN+i] = float32(f.density(k, lg))
		}
	}
	binary.Write(&buf, binary.LittleEndian, cv)
	return buf.Bytes(), true
}

// NewGPURecipe is the recipe for rendering b with p and c (only b's
// geometry and metadata are used, not its pixels).
func NewGPURecipe(b *Base, p preset.Params, c Compensation) *GPURecipe {
	op := newPixOp(p, c)
	r := &GPURecipe{
		Undo: op.undo, UndoGamma: op.undoGamma, Layer: op.layer, LayerMono: op.layerMono,
		Mono: op.mono, Measured: op.measured,
		Gain: [3]float32{op.cr, op.cg, op.cb}, MaskScale: op.maskScale, Fill: op.fill,
		HL: op.hl, Blk: op.blk, CT: op.ct, SatF: float32(op.satF), Mode: op.mode,
		Tint: op.tint, SatMeasure: float32(op.satMeasure),
	}
	if p.Halation > 0 {
		r.CPU = "halation"
	}
	if f := op.cfilm; f != nil {
		key := fmt.Sprintf("%s|%s|%s", p.Film, p.Paper, c.Sensor)
		gpuCFMu.Lock()
		gpuCFKeys[key] = f
		gpuCFMu.Unlock()
		r.ColorFilm = &GPUColorFilm{Key: key, RP: f.rp, Inv: op.cfInv, HWhite: float32(f.hWhite)}
	}
	if op.tone != nil && !op.measured {
		r.FilmTone, r.FilmToneScale = op.tone.lut[:], op.tone.scale
	}
	if op.measured {
		r.WB, r.ModeMap = gpuCorr(op.wb), gpuCorr(op.modeMap)
		pack := func(t *toneLUT) []float32 { return append(append([]float32{}, t.x[:]...), t.c[:]...) }
		r.Pre, r.Post = pack(op.preL), pack(op.postL)
		if op.fillL != nil {
			t := op.fillL
			r.FillABCK = append(append(append(append([]float32{}, t.a[:]...), t.b[:]...), t.c[:]...), t.k[:]...)
		} else if f := op.fillM; f != nil {
			var tab [64]float32
			k := make([]float32, GPULutN+1)
			for i := range k {
				k[i] = 1
			}
			for j, s := range f.steps {
				w := f.weights[j]
				for a := 0; a < 8; a++ {
					for q := 0; q < 8; q++ {
						tab[a*8+q] += float32(s.Local[a][q] * w)
					}
				}
				for i := range k {
					k[i] += float32((knot(&s.Chroma, float64(i)/GPULutN) - 1) * math.Abs(w))
				}
			}
			r.FillLocal = append(tab[:], k...)
		}
	}
	if film := b.newFilm(p, op.mono || op.layerMono != nil); film != nil {
		r.GrainSigma, r.GrainMono = film.sigma, film.mono
	}
	if b.Raw {
		r.SppSharpen = sppAmount(b.scale(), b.ISO, b.Sensor)
	}
	if p.Sharpness != 0 {
		r.Sharpen = float32(math.Max(-1, math.Min(2, p.Sharpness*1.5*b.scale())))
	}
	return r
}

// f16 is the IEEE half float nearest to v (no NaN; out of range clamps).
func f16(v float32) uint16 {
	b := math.Float32bits(v)
	sign := uint16(b>>16) & 0x8000
	exp := int(b>>23&0xff) - 127 + 15
	man := b & 0x7fffff
	switch {
	case exp >= 31:
		return sign | 0x7bff
	case exp <= 0:
		if exp < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint(14 - exp)
		h := uint16(man >> shift)
		if man>>(shift-1)&1 != 0 {
			h++
		}
		return sign | h
	}
	h := sign | uint16(exp)<<10 | uint16(man>>13)
	if man&0x1000 != 0 {
		h++ // round to nearest (may carry into the exponent: still correct)
	}
	return h
}

// GPUBase is b for the GPU: a little-endian uint32 header length, a JSON
// header, then half floats: RGB (W·H·3), then the Fill Light mask and the
// two surroundings' differences (W·H each).
func (b *Base) GPUBase() []byte {
	b.fields()
	hdr, _ := json.Marshal(map[string]any{
		"w": b.W, "h": b.H, "scale": b.scale(), "frame": b.frame(), "raw": b.Raw, "iso": b.ISO, "sensor": b.Sensor,
	})
	n := b.W * b.H
	var buf bytes.Buffer
	buf.Grow(8 + len(hdr) + n*12)
	binary.Write(&buf, binary.LittleEndian, uint32(len(hdr)))
	buf.Write(hdr)
	for buf.Len()%2 != 0 {
		buf.WriteByte(' ')
	}
	out := make([]byte, n*12)
	parallelRows(b.H, func(y0, y1 int) {
		for j := y0 * b.W; j < y1*b.W; j++ {
			for k := 0; k < 3; k++ {
				binary.LittleEndian.PutUint16(out[(j*3+k)*2:], f16(b.Pix[j*3+k]))
			}
			binary.LittleEndian.PutUint16(out[(n*3+j)*2:], f16(b.mPix[j]))
			binary.LittleEndian.PutUint16(out[(n*4+j)*2:], f16(b.da[j]))
			binary.LittleEndian.PutUint16(out[(n*5+j)*2:], f16(b.db[j]))
		}
	})
	buf.Write(out)
	return buf.Bytes()
}
