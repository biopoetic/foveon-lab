package main

// "Distinct looks": many presets in a pack are near-copies of each other
// (V2/V3/V4 of one idea, the same look for SD1 and SD15). This renders every
// preset small on the current photo, measures how different the results
// LOOK, and groups the ones a viewer couldn't tell apart, so the grid can
// show one tile per look.

import (
	"encoding/json"
	"errors"
	"image"
	"math"
	"net/http"
	"sort"

	"github.com/biopoetic/foveon-lab/render"
)

const (
	lookW    = 96 // render width for comparing looks
	lookGrid = 8  // the render is summarised as lookGrid × lookGrid cells
)

// lookFeature summarises a render as per-cell lightness and two colour
// opponents (red–green, yellow–blue), roughly perceptual, 0–100 scale.
func lookFeature(img *image.RGBA) []float64 {
	b := img.Bounds()
	f := make([]float64, lookGrid*lookGrid*3)
	cnt := make([]float64, lookGrid*lookGrid)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		cy := (y - b.Min.Y) * lookGrid / b.Dy()
		for x := b.Min.X; x < b.Max.X; x++ {
			cx := (x - b.Min.X) * lookGrid / b.Dx()
			i := img.PixOffset(x, y)
			r, g, bl := float64(img.Pix[i])/2.55, float64(img.Pix[i+1])/2.55, float64(img.Pix[i+2])/2.55
			c := cy*lookGrid + cx
			f[c*3] += 0.299*r + 0.587*g + 0.114*bl
			f[c*3+1] += r - g
			f[c*3+2] += (r+g)/2 - bl
			cnt[c]++
		}
	}
	for c, n := range cnt {
		if n > 0 {
			f[c*3] /= n
			f[c*3+1] /= n
			f[c*3+2] /= n
		}
	}
	return f
}

// lookDistance is the RMS difference between two features (0–100 scale).
func lookDistance(a, b []float64) float64 {
	var s float64
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return math.Sqrt(s / float64(len(a)))
}

// groupLooks clusters items (complete linkage: every two members of a group
// are within maxDist of each other, so similar-to-similar chains can't
// merge a warm and a cool look), and returns groups of indexes, each led by its medoid (the
// member most like the rest), in the order the groups first appear.
func groupLooks(feat [][]float64, maxDist float64) [][]int {
	n := len(feat)
	d := make([][]float64, n)
	for i := range d {
		d[i] = make([]float64, n)
		for j := 0; j < i; j++ {
			d[i][j] = lookDistance(feat[i], feat[j])
			d[j][i] = d[i][j]
		}
	}
	groups := make([][]int, n)
	for i := range groups {
		groups[i] = []int{i}
	}
	link := func(a, b []int) float64 {
		var m float64
		for _, i := range a {
			for _, j := range b {
				m = max(m, d[i][j])
			}
		}
		return m
	}
	for len(groups) > 1 {
		bi, bj, best := -1, -1, math.Inf(1)
		for i := range groups {
			for j := i + 1; j < len(groups); j++ {
				if v := link(groups[i], groups[j]); v < best {
					bi, bj, best = i, j, v
				}
			}
		}
		if best > maxDist {
			break
		}
		groups[bi] = append(groups[bi], groups[bj]...)
		groups = append(groups[:bj], groups[bj+1:]...)
	}
	for gi, g := range groups {
		sort.Ints(g)
		med, medSum := g[0], math.Inf(1)
		for _, i := range g {
			var s float64
			for _, j := range g {
				s += d[i][j]
			}
			if s < medSum {
				med, medSum = i, s
			}
		}
		out := []int{med}
		for _, i := range g {
			if i != med {
				out = append(out, i)
			}
		}
		groups[gi] = out
	}
	sort.SliceStable(groups, func(a, b int) bool { return minOf(groups[a]) < minOf(groups[b]) })
	return groups
}

func minOf(xs []int) int {
	m := xs[0]
	for _, x := range xs {
		m = min(m, x)
	}
	return m
}

// lookThresholds: how alike a group's members must be (0–100 RMS over the
// 8×8 grid). Measured on the built-in pack: a warm vs a cool daylight
// variant is ~1.7 apart, warm vs cool base ~2.2, a film look's V1 vs V2 ~1.9,
// so "normal" keeps those apart and merges only near-identical renders.
var lookThresholds = map[string]float64{"fine": 0.6, "normal": 1.2, "coarse": 2.2}

// handleLooks: POST {id, comp, presets: [ids], detail} → {groups: [[ids]]}.
func (s *server) handleLooks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string   `json:"id"`
		Comp    bool     `json:"comp"`
		Presets []string `json:"presets"`
		Detail  string   `json:"detail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	if len(req.Presets) > 2000 {
		httpErr(w, 400, errors.New("too many presets"))
		return
	}
	maxDist, ok := lookThresholds[req.Detail]
	if !ok {
		maxDist = lookThresholds["normal"]
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	b, err := s.base(ph, lookW)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	comp := s.compensation(ph, req.Comp)
	s.pmu.RLock()
	var ids []string
	var feats [][]float64
	for _, id := range req.Presets {
		p, ok := s.pByID[id]
		if !ok {
			continue
		}
		ids = append(ids, id)
		feats = append(feats, lookFeature(render.Render(b, p.Params, comp)))
	}
	s.pmu.RUnlock()
	var out [][]string
	for _, g := range groupLooks(feats, maxDist) {
		var ids2 []string
		for _, i := range g {
			ids2 = append(ids2, ids[i])
		}
		out = append(out, ids2)
	}
	writeJSON(w, map[string]any{"groups": out})
}
