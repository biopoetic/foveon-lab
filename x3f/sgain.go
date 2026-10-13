package x3f

// Spatial gain: the per-pixel colour-shading (lens fall-off) correction
// stored in CAMF. A Go port of x3fuse-core's spatial_gain.rs (Copyright 2026
// Sang Lee, Apache-2.0), itself from the Kalpanika x3f tools' spatial gain
// (BSD-style); licences and notices in third_party/x3fuse-core/, which must
// stay with this code. Quattro's 6-channel "HP" tables are not ported.
//
// Merrill-type files carry many tables, one per (aperture, focus) block;
// the four nearest to the shot in (1/aperture, lens position) space, one per
// quadrant, are blended bilinearly. Older files carry one table (per WB or
// plain "SpatialGain").

import (
	"math"
	"strconv"
	"strings"
)

// SpatialGain is one or more gain tables (per channel for Merrill files,
// one multi-channel table for older ones).
type SpatialGain struct {
	tabs []sgTable
}

type sgTable struct {
	rows, cols, channels int
	chan0                int       // first channel the table covers
	gain                 []float64 // rows*cols*channels
}

// sgBlock is one Merrill-type table block and its position.
type sgBlock struct {
	name string
	x, y float64
}

func lensPosition(focal, distance float64) float64 { return 1 / (1/focal - 1/distance) }

// NewSpatialGain builds the shot's spatial gain from CAMF, or nil when the
// file has none. props are the file's PROP entries (FLENGTH), wb its
// white-balance name for the classic tables.
func NewSpatialGain(c *CAMF, props map[string]string, wb string) *SpatialGain {
	if g := merrillSpatialGain(c, props); g != nil {
		return g
	}
	name := c.Props["SpatialGainTables"][wb]
	if name == "" {
		name = "SpatialGain"
	}
	t, d := c.Matrices[name], c.Dims[name]
	if len(d) != 3 || len(t) != d[0]*d[1]*d[2] || len(t) == 0 {
		return nil
	}
	return &SpatialGain{tabs: []sgTable{{rows: d[0], cols: d[1], channels: d[2], gain: t}}}
}

func merrillSpatialGain(c *CAMF, props map[string]string) *SpatialGain {
	ap := c.Matrices["CaptureAperture"]
	blocks := c.PropKeys["IncludeBlocks"]
	if len(ap) != 1 || ap[0] <= 0 || len(blocks) == 0 {
		return nil
	}
	focal := 30.0
	if v, err := strconv.ParseFloat(strings.TrimSpace(props["FLENGTH"]), 64); err == nil && v > 0 {
		focal = v
	}
	distance := math.Inf(1)
	if d := c.Matrices["ObjectDistance"]; len(d) == 1 {
		distance = d[0] * 10 // cm -> mm
	}
	mod := 280.0
	if li := c.Matrices["LensInformation"]; len(li) == 1 {
		switch li[0] {
		case 1003:
			mod = 200 // DP1 Merrill
		case 1004:
			mod = 280 // DP2 Merrill
		case 1005:
			mod = 226 // DP3 Merrill
		}
	}
	fstops := c.Matrices["SpatialGain_Fstop"]
	var gains []sgBlock
	for _, b := range blocks {
		rest, ok := strings.CutPrefix(b, "SpatialGainsProps_")
		if !ok || c.Props[b] == nil {
			continue
		}
		a, f, ok := strings.Cut(rest, "_")
		if !ok {
			continue
		}
		if fstops != nil {
			i, err := strconv.Atoi(a)
			if err != nil || i < 0 || i >= len(fstops) || len(f) != 3 {
				continue
			}
			var lp float64
			switch f {
			case "INF":
				lp = lensPosition(focal, math.Inf(1))
			case "MOD":
				lp = lensPosition(focal, mod)
			default:
				continue
			}
			gains = append(gains, sgBlock{b, 1 / fstops[i], lp})
		} else {
			av, err1 := strconv.ParseFloat(a, 64)
			lv, err2 := strconv.ParseFloat(f, 64)
			if f == "INF" {
				lv, err2 = math.Inf(1), nil
			}
			if err1 != nil || err2 != nil {
				continue
			}
			gains = append(gains, sgBlock{b, 1 / av, lv})
		}
	}
	if len(gains) == 0 {
		return nil
	}
	x, y := 1/ap[0], lensPosition(focal, distance)
	// Closest block per quadrant; x3fuse walks its list newest-first.
	var q [4]*sgBlock
	qdx := [4]float64{math.Inf(1), math.Inf(-1), math.Inf(-1), math.Inf(1)}
	qdy := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	qd2 := [4]float64{math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(1)}
	for i := len(gains) - 1; i >= 0; i-- {
		g := &gains[i]
		dx, dy := g.x-x, g.y-y
		d2 := dx*dx + dy*dy
		k := 2
		switch {
		case dx > 0 && dy > 0:
			k = 0
		case dx > 0:
			k = 3
		case dy > 0:
			k = 1
		}
		if d2 < qd2[k] {
			q[k], qdx[k], qdy[k], qd2[k] = g, dx, dy, d2
		}
	}
	wx := [4]float64{qdx[1] / (qdx[1] - qdx[0]), qdx[0] / (qdx[0] - qdx[1]), qdx[3] / (qdx[3] - qdx[2]), qdx[2] / (qdx[2] - qdx[3])}
	wy := [4]float64{qdy[3] / (qdy[3] - qdy[0]), qdy[2] / (qdy[2] - qdy[1]), qdy[1] / (qdy[1] - qdy[2]), qdy[0] / (qdy[0] - qdy[3])}
	var w [4]float64
	for i := range w {
		if math.IsNaN(wx[i]) {
			wx[i] = 1
		}
		if math.IsNaN(wy[i]) {
			wy[i] = 1
		}
		w[i] = wx[i] * wy[i]
	}
	sg := &SpatialGain{}
	for ch, cname := range []string{"R", "G", "B"} {
		var sum []float64
		tab := sgTable{rows: -1, cols: -1, channels: 1, chan0: ch}
		for i, blk := range q {
			if blk == nil {
				continue
			}
			p := c.Props[blk.name]
			tname := p["GainsTable"+cname]
			t, d := c.Matrices[tname], c.Dims[tname]
			mn, err1 := strconv.ParseFloat(p["MinGains"+cname], 64)
			delta, err2 := strconv.ParseFloat(p["Delta"+cname], 64)
			if len(d) != 2 || len(t) != d[0]*d[1] || err1 != nil || err2 != nil {
				return nil
			}
			if tab.rows == -1 {
				tab.rows, tab.cols = d[0], d[1]
			} else if tab.rows != d[0] || tab.cols != d[1] {
				return nil
			}
			if sum == nil {
				sum = make([]float64, len(t))
			}
			for j, v := range t {
				sum[j] += w[i] * (mn + delta*v)
			}
		}
		if sum == nil {
			return nil
		}
		tab.gain = sum
		sg.tabs = append(sg.tabs, tab)
	}
	return sg
}

// At is the gain for channel ch at (row, col) of a rows×cols image.
func (s *SpatialGain) At(row, col, ch, rows, cols int) float64 {
	g := 1.0
	rrel, crel := float64(row)/float64(rows), float64(col)/float64(cols)
	for i := range s.tabs {
		s := &s.tabs[i]
		t := s.gain
		c := ch - s.chan0
		if c < 0 || c >= s.channels {
			continue
		}
		rc := rrel * float64(s.rows-1)
		ri := int(math.Floor(rc))
		rf := rc - float64(ri)
		cc := crel * float64(s.cols-1)
		ci := int(math.Floor(cc))
		cf := cc - float64(ci)
		r1, r2 := ri, ri+1
		if ri < 0 {
			r1, r2 = 0, 0
		} else if ri >= s.rows-1 {
			r1, r2 = s.rows-1, s.rows-1
		}
		c1, c2 := ci, ci+1
		if ci >= s.cols-1 {
			c1, c2 = s.cols-1, s.cols-1
		}
		at := func(r, cl int) float64 { return t[(r*s.cols+cl)*s.channels+c] }
		g1 := at(r1, c1) + cf*(at(r1, c2)-at(r1, c1))
		g2 := at(r2, c1) + cf*(at(r2, c2)-at(r2, c1))
		g *= g1 + rf*(g2-g1)
	}
	return g
}
