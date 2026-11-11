package main

// Photo loading for the Quattro diagnosis tool.
//
// The pairs are made exactly as `foveon-lab fit-raw -go -diagnose` makes
// them: x3f.DecodeRaw through the native development (camera white balance,
// colour matrix, highlight clip, the app's default denoise, the low-frequency
// shadow-colour and Quattro mottle passes, the per-model exposure), reduced
// to the export's analysis frame. This file mirrors the relevant part of
// camfread.go/rawdecode.go at the commit this tool was written on; the
// tool's test checks the mapping arithmetic, not the decode.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

const (
	analysisW = calib.AnalysisW
	sourceW   = 1600
	defaultNR = 0.3
	baseNR    = 0.15
)

// Photo is one Quattro sample with its SPP neutral export.
type Photo struct {
	Name   string
	Path   string
	Export string
	Folder string
	Body   string // dp0, dp1, dp2, dp3, sd, sdH
	Model  string
	ISO    float64

	// WB is the camera's recorded white-balance setting: EXIF WhiteBalance is
	// 0 (auto) or 1 (manual), and LightSource names a manual preset. Quattro
	// X3F files carry no PROP section, so WB_DESC is absent on all of them.
	WB          string
	WBManual    bool
	LightSource int
	HasEXIFWB   bool

	// File numbers the camera recorded (CAMF matrices; EXIF where readable).
	ExpComp, ExpNet float64
	HasExpNet       bool
	ColorTemp       float64 // CAMF ColorTempValue: the camera's own estimate
	AutoGain        [3]float64
	SunGain         [3]float64
	ManualGain      [3]float64 // CAMF gains for the recorded manual setting, when known

	RawW, RawH int // bottom/middle layer (Quattro top is 2x this)
	Res        string

	Raw, SPP *render.Linear
}

// bodyOf maps the sample folder to the short body name used in the tables.
func bodyOf(folder string) string {
	switch strings.ToLower(folder) {
	case "dp0-quattro":
		return "dp0"
	case "dp1-quattro":
		return "dp1"
	case "dp2-quattro":
		return "dp2"
	case "dp3-quattro":
		return "dp3"
	case "sd-quattro":
		return "sd"
	case "sd-quattro-h":
		return "sdH"
	}
	return folder
}

// LoadPhotos finds the Quattro X3F files that have an A00 export.
func LoadPhotos(photosRoot, exportsDir string) ([]*Photo, error) {
	want := map[string]string{
		"dp0-quattro": "dp0", "dp1-quattro": "dp1", "dp2-quattro": "dp2",
		"dp3-quattro": "dp3", "sd-quattro": "sd", "sd-quattro-h": "sdH",
	}
	exports := map[string]string{}
	ents, err := os.ReadDir(exportsDir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		lower := strings.ToLower(n)
		var base string
		switch {
		case strings.HasSuffix(lower, "_a00.tif"):
			base = n[:len(n)-len("_A00.tif")]
		case strings.HasSuffix(lower, "_a00.png"):
			base = n[:len(n)-len("_A00.png")]
		default:
			continue
		}
		if _, ok := exports[strings.ToUpper(base)]; !ok {
			exports[strings.ToUpper(base)] = filepath.Join(exportsDir, n)
		}
	}
	dirs, err := os.ReadDir(photosRoot)
	if err != nil {
		return nil, err
	}
	var out []*Photo
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		body, ok := want[strings.ToLower(d.Name())]
		if !ok {
			continue
		}
		files, err := os.ReadDir(filepath.Join(photosRoot, d.Name()))
		if err != nil {
			return nil, err
		}
		for _, fe := range files {
			if fe.IsDir() || !strings.EqualFold(filepath.Ext(fe.Name()), ".x3f") {
				continue
			}
			stem := strings.ToUpper(strings.TrimSuffix(fe.Name(), filepath.Ext(fe.Name())))
			exp, ok := exports[stem]
			if !ok {
				continue
			}
			out = append(out, &Photo{
				Name:   strings.TrimSuffix(fe.Name(), filepath.Ext(fe.Name())),
				Path:   filepath.Join(photosRoot, d.Name(), fe.Name()),
				Export: exp,
				Folder: d.Name(), Body: body,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Folder != out[j].Folder {
			return out[i].Folder < out[j].Folder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// exportFor returns photo p's A00 export path.
func exportFor(p *Photo, exportsDir string) string {
	for _, ext := range []string{".tif", ".png", ".TIF", ".PNG"} {
		path := filepath.Join(exportsDir, p.Name+"_A00"+ext)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// fillMeta reads the numbers this tool tests as gain predictors. It does not
// touch the pixels.
func fillMeta(p *Photo, f *x3f.File, c *x3f.CAMF) {
	p.ISO = photoISO(f.Props)
	p.WB, p.WBManual, p.LightSource, p.HasEXIFWB = exifWB(f)
	p.ExpComp, p.ExpNet, p.HasExpNet = camfNumbers(c)
	p.ColorTemp = first(c.Matrices["ColorTempValue"])
	p.AutoGain = wbGain(c, "Auto")
	p.SunGain = wbGain(c, "Sunlight")
	p.ManualGain = manualGains(c, p)
}

// wbGain is the CAMF white-balance gain matrix for an illuminant.
func wbGain(c *x3f.CAMF, wb string) [3]float64 {
	name := c.Props["WhiteBalanceGains"][wb]
	if name == "" {
		return [3]float64{}
	}
	return gainMatrix(c, name)
}

// LoadMetadata fills every photo's metadata without decoding raw data.
func LoadMetadata(photos []*Photo) error {
	for _, p := range photos {
		f, err := x3f.Open(p.Path)
		if err != nil {
			return err
		}
		c, err := f.CAMF()
		if err != nil {
			return err
		}
		fillMeta(p, f, c)
	}
	return nil
}

// LoadPairs decodes every photo and loads its SPP export.
func LoadPairs(photos []*Photo, exportsDir, cacheDir string, logf func(string, ...any)) error {
	for i, p := range photos {
		if err := loadOne(p, exportsDir, cacheDir, logf); err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
		logf("  [%d/%d] %s %s ISO %.0f %s raw %dx%d", i+1, len(photos), p.Body, p.Name, p.ISO, p.Res, p.RawW, p.RawH)
	}
	return nil
}

func loadOne(p *Photo, exportsDir, cacheDir string, logf func(string, ...any)) error {
	e := p.Export
	if e == "" {
		e = exportFor(p, exportsDir)
	}
	if e == "" {
		return errors.New("no A00 export")
	}
	spp, err := calib.LoadLinear(e)
	if err != nil {
		return err
	}
	p.SPP = spp
	f, err := x3f.Open(p.Path)
	if err != nil {
		return err
	}
	c, err := f.CAMF()
	if err != nil {
		return err
	}
	fillMeta(p, f, c)
	// The model decides the per-model exposure and the mottle/despeckle path.
	p.Model = f.Model()
	if p.Model == "" {
		p.Model = f.Props["CAMMODEL"]
	}
	if p.Model == "" {
		p.Model = p.Folder
	}
	if cacheDir != "" {
		if l, w, h, ok := loadCache(cacheDir, p); ok {
			p.Raw, p.RawW, p.RawH = l, w, h
			p.Res = resName(p)
			return nil
		}
	}
	// Frame as fit-raw does: the working width, turned, reduced to the
	// export's width, then cropped/stretched to the export's frame.
	maxW := sourceW
	if f.Rotation == 90 || f.Rotation == 270 {
		maxW = max(sourceW, spp.H)
	}
	l, rawW, rawH, err := developLinear(f, c, p.ISO, "Quattro", maxW)
	if err != nil {
		return err
	}
	p.RawW, p.RawH = rawW, rawH
	p.Res = resName(p)
	if f.Rotation != 0 {
		l = l.Rotate(f.Rotation)
	}
	l = fitAspect(l.Downscale(spp.W), spp.W, spp.H)
	p.Raw = l
	if cacheDir != "" {
		saveCache(cacheDir, p)
	}
	return nil
}

// resName is the camera's readout mode, from the Quattro top layer's size:
// the sd Quattro H's larger sensor is the only body whose files come in more
// than one size in this set.
func resName(p *Photo) string {
	// RawW is the bottom/middle layer width; the top layer is two per axis.
	top := p.RawW * 2
	switch {
	case p.Body != "sdH":
		return "full"
	case top >= 6600:
		return "HI"
	case top >= 4000:
		return "MED"
	}
	return "LOW"
}

// mirror of camfread.go's developWB/photoISO and rawdecode.go's photo load.
func photoISO(props map[string]string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(props["ISO"]), 64)
	return v
}

func developWB(c *x3f.CAMF, props map[string]string) string {
	if os.Getenv("FOVEON_WB") != "camera" && c.Props["WhiteBalanceGains"]["Auto"] != "" {
		return "Auto"
	}
	return c.CameraWB(strings.TrimSpace(props["WB_DESC"]))
}

func denoiseStrengthFor(iso float64, group string) float64 {
	if iso > 0 && iso <= 400 && (group == "DPM" || group == "Quattro") {
		return baseNR
	}
	return defaultNR
}

// developLinear mirrors goLinearForGatesExplicit for the ordinary preview:
// no IR layers, no highlight rebuild, the default denoise.
func developLinear(f *x3f.File, c *x3f.CAMF, iso float64, group string, maxW int) (*render.Linear, int, int, error) {
	raw, err := f.DecodeRaw()
	if err != nil {
		return nil, 0, 0, err
	}
	x3f.FixBadPixels(raw, c)
	rawW, rawH := raw.W, raw.H
	wb := developWB(c, f.Props)
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return nil, 0, 0, err
	}
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return nil, 0, 0, err
	}
	t := render.CamTransform{Gain: gain, CC: cc}
	x3f.ClipHighlights(img, t.Gain)
	strength := denoiseStrengthFor(iso, group)
	fullW := img.W
	nativeLong := max(img.W, img.H)
	previewW, previewH := previewSize(f)
	dw, dh, bx, by := reducedView(previewW, previewH, img.W, img.H, maxW)
	origW := img.W
	if strength > 0 {
		if bx > 1 || by > 1 {
			img = binCam2(img, bx, by)
		}
		if os.Getenv("FOVEON_DN_NOSPECK") == "" && iso >= 800 && (group == "DPM" || group == "Quattro") {
			lin := &render.Linear{W: img.W, H: img.H, Pix: img.Pix}
			render.Despeckle(lin, 0.5, 0.01, 0.05)
			img.Pix = lin.Pix
		}
		x3f.Denoise(img, x3f.DenoiseOptions{
			Strength: strength, Scale: 1 / float64(bx), ISO: iso,
			Grey:       [3]float64{1 / t.Gain[0], 1 / t.Gain[1], 1 / t.Gain[2]},
			NoiseAware: group == "DPM" || group == "Quattro", Quattro: group == "Quattro",
		})
	}
	if dw == 0 && bx > 1 {
		if binnedW := origW / bx; maxW > binnedW {
			maxW = binnedW
		}
	}
	var l *render.Linear
	if dw > 0 {
		l = render.FromCamLinearSize(img.Pix, img.W, img.H, dw, dh, 0, t)
	} else {
		l = render.FromCamLinear(img.Pix, img.W, img.H, maxW, 0, t)
	}
	l.ISO = iso
	l.Sensor = render.SharpKey(group)
	if nativeLong > max(l.W, l.H) {
		l.NativeFrame = nativeLong
	}
	if strength > 0 && os.Getenv("FOVEON_DN_NOSC") == "" {
		render.ShadowChromaFix(l, fullW, t.Colour)
	}
	if strength > 0 && os.Getenv("FOVEON_DN_MOTTLE") != "0" && group == "Quattro" && iso >= 6400 {
		render.MottleChroma(l, render.MottleSigma(fullW, l.W), 0.02, 0.10)
	}
	if g := render.ModelExposure(f.Model()); g != 1 {
		for i := range l.Pix {
			l.Pix[i] *= g
		}
	}
	return l, rawW, rawH, nil
}

// previewSize is the largest embedded JPEG's size, as loadMeta records it.
func previewSize(f *x3f.File) (w, h int) {
	for _, im := range f.Images {
		if im.ImgType == 2 && im.Format == 18 && int(im.Width) > w {
			w, h = int(im.Width), int(im.Height)
		}
	}
	return
}

// reducedView mirrors camfread.go.
func reducedView(previewW, previewH, w, h, maxW int) (dw, dh, bx, by int) {
	if maxW > 0 && maxW < w && previewW > 0 && previewH > 0 {
		if r := float64(w*previewH) / float64(h*previewW); r >= 1.5 || r <= 0.67 {
			dw, dh = maxW, int(math.Round(float64(maxW*previewH)/float64(previewW)))
			if dh > h {
				dw, dh = 0, 0
			}
		}
	}
	bx, by = 1, 1
	if dw > 0 {
		bx, by = max(1, (w+dw-1)/dw), max(1, (h+dh-1)/dh)
	} else if maxW > 0 {
		bx = max(1, w/maxW)
		by = bx
	}
	return
}

// binCam2 averages bx×by blocks of camera RGB.
func binCam2(c *x3f.CamImage, bx, by int) *x3f.CamImage {
	w, h := c.W/bx, c.H/by
	out := &x3f.CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	inv := 1 / float32(bx*by)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s [3]float32
			for yy := y * by; yy < y*by+by; yy++ {
				row := c.Pix[(yy*c.W+x*bx)*3:]
				for xx := 0; xx < bx; xx++ {
					s[0] += row[xx*3]
					s[1] += row[xx*3+1]
					s[2] += row[xx*3+2]
				}
			}
			o := (y*w + x) * 3
			out.Pix[o], out.Pix[o+1], out.Pix[o+2] = s[0]*inv, s[1]*inv, s[2]*inv
		}
	}
	return out
}

// fitAspect/stretchTo/cropLinear are copied from fitraw.go.
func fitAspect(l *render.Linear, w, h int) *render.Linear {
	r := float64(l.W) * float64(h) / (float64(l.H) * float64(w))
	if r > 1.02 && r < 1.5 {
		cw := int(math.Round(float64(l.H) * float64(w) / float64(h)))
		l = cropLinear(l, (l.W-cw)/2, 0, cw, l.H)
	} else if r < 0.98 && r > 0.67 {
		ch := int(math.Round(float64(l.W) * float64(h) / float64(w)))
		l = cropLinear(l, 0, (l.H-ch)/2, l.W, ch)
	}
	return stretchTo(l, w, h)
}

func cropLinear(l *render.Linear, x0, y0, w, h int) *render.Linear {
	o := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: l.Scale, Frame: l.Frame, Raw: l.Raw, ISO: l.ISO, Sensor: l.Sensor}
	for y := 0; y < h; y++ {
		copy(o.Pix[y*w*3:(y+1)*w*3], l.Pix[((y+y0)*l.W+x0)*3:])
	}
	return o
}

func stretchTo(l *render.Linear, w, h int) *render.Linear {
	if l.W == w && l.H == h {
		return l
	}
	o := *l
	o.W, o.H, o.Pix = w, h, make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := ((y*l.H/h)*l.W + x*l.W/w) * 3
			copy(o.Pix[(y*w+x)*3:], l.Pix[s:s+3])
		}
	}
	return &o
}

// ---------- metadata ----------

func first(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

func gainMatrix(c *x3f.CAMF, name string) [3]float64 {
	var g [3]float64
	v := c.Matrices[name]
	if len(v) != 3 {
		return g
	}
	copy(g[:], v)
	return g
}

func camfNumbers(c *x3f.CAMF) (expComp, expNet float64, ok bool) {
	expComp = first(c.Matrices["CaptureExpComp"])
	if v := c.Matrices["ExpNet"]; len(v) > 0 {
		expNet, ok = v[0], true
	}
	return
}

// manualGains is the CAMF gains for the camera's recorded manual WB preset,
// when the EXIF LightSource names one.
func manualGains(c *x3f.CAMF, p *Photo) [3]float64 {
	if !p.WBManual {
		return [3]float64{}
	}
	name := map[int]string{1: "Sunlight", 2: "Fluorescent", 3: "Incandescent", 4: "Flash",
		9: "Sunlight", 10: "Overcast", 11: "Shade"}[p.LightSource]
	if name == "" {
		// A manual setting with no named light source: the CAMF stores the
		// camera's Custom entry for it.
		name = "Custom"
	}
	return wbGain(c, name)
}

// exifWB reads the embedded preview JPEG's EXIF WhiteBalance (0 auto, 1
// manual) and LightSource. Quattro X3F files have no PROP section, so this is
// the only record of the camera's white-balance setting.
func exifWB(f *x3f.File) (name string, manual bool, light int, ok bool) {
	var best x3f.Image
	for _, im := range f.Images {
		if im.ImgType == 2 && im.Format == 18 && int(im.Width) > int(best.Width) {
			best = im
		}
	}
	if best.Length == 0 {
		return "unrecorded", false, 0, false
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return "unrecorded", false, 0, false
	}
	defer fh.Close()
	n := int64(best.Length)
	if n > 256<<10 {
		n = 256 << 10
	}
	buf := make([]byte, n)
	if _, err := fh.ReadAt(buf, int64(best.Offset)); err != nil {
		return "unrecorded", false, 0, false
	}
	i := 0
	for i+2 < len(buf) && !(buf[i] == 0xff && buf[i+1] == 0xd8) {
		i++
	}
	vals := map[uint16]uint32{}
	parseJPEGEXIF(buf[i:], vals)
	wb, wok := vals[0xa403]
	ls := int(vals[0x9208])
	if !wok {
		if ls > 0 {
			return lightName(ls), true, ls, true
		}
		return "unrecorded", false, ls, false
	}
	if wb == 0 {
		return "Auto", false, ls, true
	}
	if ls > 0 {
		return lightName(ls), true, ls, true
	}
	return "Manual", true, ls, true
}

func lightName(v int) string {
	switch v {
	case 1, 9:
		return "Sunlight"
	case 2:
		return "Fluorescent"
	case 3:
		return "Incandescent"
	case 4:
		return "Flash"
	case 10:
		return "Overcast"
	case 11:
		return "Shade"
	}
	return fmt.Sprintf("LightSource%d", v)
}

// parseJPEGEXIF walks the JPEG's APP1 Exif segment and records the integer
// tags of IFD0 and the Exif sub-IFD. Only the tags this tool needs are kept.
func parseJPEGEXIF(b []byte, out map[uint16]uint32) {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xff {
			i++
			continue
		}
		m := b[i+1]
		if m == 0xd8 || m == 0x01 || (m >= 0xd0 && m <= 0xd7) {
			i += 2
			continue
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if i+2+n > len(b) {
			return
		}
		seg := b[i+4 : i+2+n]
		if m == 0xe1 && len(seg) > 6 && string(seg[:4]) == "Exif" {
			tiff := seg[6:]
			if len(tiff) < 8 {
				return
			}
			var bo binary.ByteOrder = binary.LittleEndian
			if string(tiff[:2]) == "MM" {
				bo = binary.BigEndian
			}
			var walk func(off int)
			walk = func(off int) {
				if off+2 > len(tiff) {
					return
				}
				cnt := int(bo.Uint16(tiff[off:]))
				for k := 0; k < cnt; k++ {
					p := off + 2 + 12*k
					if p+12 > len(tiff) {
						return
					}
					tag, typ := bo.Uint16(tiff[p:]), bo.Uint16(tiff[p+2:])
					num := int(bo.Uint32(tiff[p+4:]))
					pval := int(bo.Uint32(tiff[p+8:]))
					if tag == 0x8769 {
						walk(pval)
						continue
					}
					if typ < 3 || (typ > 4 && typ != 10) {
						continue
					}
					switch typ {
					case 3:
						if num >= 1 {
							if num == 1 {
								out[tag] = uint32(bo.Uint16(tiff[p+8:]))
							} else if pval+2 <= len(tiff) {
								out[tag] = uint32(bo.Uint16(tiff[pval:]))
							}
						}
					case 4:
						if num >= 1 {
							if num == 1 {
								out[tag] = bo.Uint32(tiff[p+8:])
							} else if pval+4 <= len(tiff) {
								out[tag] = bo.Uint32(tiff[pval:])
							}
						}
					case 10:
						if num >= 1 && pval+8 <= len(tiff) {
							nv := int32(bo.Uint32(tiff[pval:]))
							dv := int32(bo.Uint32(tiff[pval+4:]))
							if dv != 0 {
								v := nv * 1000 / dv
								if v >= 0 {
									out[tag] = uint32(v)
								}
							}
						}
					}
				}
			}
			walk(int(bo.Uint32(tiff[4:])))
			return
		}
		i += 2 + n
	}
}

// ---------- cache ----------

func cachePath(dir string, p *Photo) string {
	st, err := os.Stat(p.Path)
	key := p.Name
	if err == nil {
		key = fmt.Sprintf("%d_%d_%x", st.Size(), st.ModTime().Unix(), fnv(p.Path))
	}
	return filepath.Join(dir, p.Body+"_"+key+".f32")
}

func fnv(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func loadCache(dir string, p *Photo) (*render.Linear, int, int, bool) {
	b, err := os.ReadFile(cachePath(dir, p))
	if err != nil || len(b) < 32 {
		return nil, 0, 0, false
	}
	if string(b[:4]) != "T86L" {
		return nil, 0, 0, false
	}
	bo := binary.LittleEndian
	w, h := int(bo.Uint32(b[4:])), int(bo.Uint32(b[8:]))
	rawW, rawH := int(bo.Uint32(b[12:])), int(bo.Uint32(b[16:]))
	n := w * h * 3
	if n <= 0 || 24+n*4 != len(b) {
		return nil, 0, 0, false
	}
	pix := make([]float32, n)
	for i := range pix {
		pix[i] = math.Float32frombits(bo.Uint32(b[24+i*4:]))
	}
	l := &render.Linear{W: w, H: h, Pix: pix}
	return l, rawW, rawH, true
}

func saveCache(dir string, p *Photo) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	l := p.Raw
	b := make([]byte, 24+len(l.Pix)*4)
	copy(b, "T86L")
	bo := binary.LittleEndian
	bo.PutUint32(b[4:], uint32(l.W))
	bo.PutUint32(b[8:], uint32(l.H))
	bo.PutUint32(b[12:], uint32(p.RawW))
	bo.PutUint32(b[16:], uint32(p.RawH))
	for i, v := range l.Pix {
		bo.PutUint32(b[24+i*4:], math.Float32bits(v))
	}
	tmp := cachePath(dir, p) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, cachePath(dir, p))
	}
}
