package main

// The raw/JPEG development an export and Publish share: pick the source the
// way the preview does, then the per-photo compensation. The caller owns the
// export semaphore and the pixel work that follows.

import (
	"context"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// developSource returns the full-size development for ph, the preset as it
// should be rendered (Max Detail's fallback is already applied) and the
// compensation. md says Max Detail was used.
func (s *server) developSource(ctx context.Context, ph *Photo, p preset.Params, comp bool, denoise *int) (*render.Linear, preset.Params, render.Compensation, bool, error) {
	waiting := func() error { return ctx.Err() }
	var src *render.Linear
	wbDeveloped := false
	md := p.MaxDetail && maxDetailAvailable(ph)
	if !md && p.MaxDetail && p.SPPFallback != nil {
		// No raw development for this source: export the mode's SPP
		// fallback look, as the preview does.
		fb := *p.SPPFallback
		fb.MaxDetail = false
		p = fb
	}
	p = sourceParams(ph, p)
	if ph.Kind == "dng" {
		l, err := dngDevelop(ph, 0)
		if err != nil {
			return nil, p, render.Compensation{}, md, err
		}
		return l, p, s.compensation(ph, comp), md, nil
	}
	lf, ir := layersSource(ph, p)
	if md {
		l, err := rawMaxDetailExplicit(ph, 0, goStrength(rawExportColor(denoise)), explicitStrength(rawExportColor(denoise)))
		if err != nil {
			return nil, p, render.Compensation{}, md, err
		}
		src = l
		p = maxDetailRenderParams(p)
	} else if ir {
		l, err := rawLinearFor(ph, 0, rawExportColor(denoise), lf)
		if err != nil {
			return nil, p, render.Compensation{}, md, err
		}
		src = l
	} else if ph.RawDecoded && !ph.rawFailed.Load() {
		// SD9/SD10: the same full-size raw development their preview has
		// always used (see rawStillLinear), not the binned accurate path.
		l, err := rawStillLinear(ph, 0)
		if err != nil {
			return nil, p, render.Compensation{}, md, err
		}
		src = l
	} else if s.wantRaw(ph) || ph.jpegBroken.Load() {
		// Accurate mode: export from the raw data even if the preview
		// hasn't switched yet (this waits for the decode).
		color := rawExportColor(denoise)
		wf, _ := wbSource(ph, p)
		wbDeveloped = wf != ""
		l, err := rawLinearFor(ph, 0, color, wf)
		if err != nil {
			return nil, p, render.Compensation{}, md, err
		}
		if color == "sRGB" { // the preview's full decode is ready now too
			s.rawMu.Lock()
			s.rawState[ph.ID] = rawReady
			s.rawMu.Unlock()
		}
		src = l
	} else {
		img, err := decodePreview(ph)
		if err != nil {
			return nil, p, render.Compensation{}, md, err
		}
		src = balanceJPEG(ph, render.FromImage(img, 0, ph.Rotation))
	}
	if err := waiting(); err != nil {
		return nil, p, render.Compensation{}, md, err
	}
	var c render.Compensation
	if md {
		c = rawCompensation(ph)
	} else {
		c = s.compensation(ph, comp)
	}
	if ir {
		c = layerCompensation(c)
		// A colour film's @wb suffix developed the layers with the preset's
		// balance already.
		wbDeveloped = modeDevelopedWB(lf)
	}
	c.WBDeveloped = wbDeveloped
	return src, p, c, md, nil
}
