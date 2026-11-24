package main

// foveon-lab sharpen-radius: task 92. Is there a sharpening radius, or one
// other kernel shape, that brings our Quattro edge profile closer to SPP's,
// held out, and does it lower the 1200 px error against the exports?
//
// The command reuses the task-90 pair pipeline and held-out base
// (sharpenstudy.go) and the task-90 measures (sharpenmeasure.go), and adds:
//
//   - a sweep of the full-size mask radius through render.SetSharpResearch
//     (0.6, 0.8, 1.0, 1.2, 1.5, plus the deliberately wrong 3.0) at the
//     current amount, with overshoot, registered profile residual and the
//     1200 px error per body;
//   - a radius x Detail grid around the current pair, chosen leave-one-photo
//     and leave-one-body-out;
//   - one alternative kernel shape, a difference of two Gaussians with a
//     ratio of 1.6, fitted to SPP's registered average edge profile on the
//     training bodies and tested on the held-out body;
//   - the view-mask table: what a full-size radius becomes at the scored
//     frame, where sppView fits a 7-tap kernel.
//
//	foveon-lab sharpen-radius [-photos dir] [-dir calib] [-out zz-out/t92]
//	                          [-limit N] [-pics a,b,c] [-ref t90/photos.csv]

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
)

var (
	t92DefaultRadii   = []float64{0.6, 0.8, 1.0, 1.2, 1.5}
	t92DefaultDetails = []float64{-0.25, 0, 0.25}
	t92WrongRadius    = 3.0
	t92DogSigmas      = []float64{0.3, 0.4, 0.5, 0.6, 0.7, 0.8}
	t92DogLambdas     = []float64{0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4}
)

const t92DogRatio = 1.6

// t92Result is one photo's contribution.
type t92Result struct {
	p                 *t90Photo
	heldBase          float64    // calib.MeanError, the held-out base
	unsharp           float64    // meanAbs8 of the render with Detail -1
	grid              []float64  // radii x details errors, row-major
	edge              []edgeStat // ours at detail 0, one per radius
	sppEdge           edgeStat   // SPP's edge stats (same for every cell)
	baseProf, sppProf []float64
	profN             int
	a0                float64 // fitted view amount at the current mask, Detail 0
	k0                [7]float32
	s                 float64
	// The alternative shape, held out: [0] leave-one-photo, [1]
	// leave-one-body-out.
	dogErr   [2]float64
	dogEdge  [2]edgeStat
	dogShape [2]t92DoG
}

type t92DoG struct {
	Sigma1   float64
	Lambda   float64
	TrainMAE float64
	BaseMAE  float64
	Edges    int
	OK       bool
}

func t92Index(v []float64, x float64) int {
	for i, y := range v {
		if math.Abs(y-x) < 1e-9 {
			return i
		}
	}
	return -1
}

func t92ParseFloats(s string) ([]float64, error) {
	var out []float64
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// t92Gauss7 is render's gaussKernel as float64: a normalised 7-tap Gaussian.
func t92Gauss7(sigma float64) [7]float64 {
	var k [7]float64
	var sum float64
	for i := range k {
		x := float64(i - 3)
		k[i] = math.Exp(-x * x / (2 * sigma * sigma))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	return k
}

// t92DoGKernel is the 7-tap kernel whose unsharp response is
// lambda*a*(G(sigma1) - G(1.6*sigma1)): v - k*v = (G1-G2)*v, so k sums to 1.
func t92DoGKernel(sigma1 float64) [7]float32 {
	g1, g2 := t92Gauss7(sigma1), t92Gauss7(sigma1*t92DogRatio)
	var k [7]float32
	for i := range k {
		k[i] = float32(g2[i] - g1[i])
	}
	k[3] += 1
	return k
}

func t92Conv1(v []float64, k [7]float64) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		var s float64
		for j := -3; j <= 3; j++ {
			ii := min(len(v)-1, max(0, i+j))
			s += k[j+3] * v[ii]
		}
		out[i] = s
	}
	return out
}

func t92MAE(a, b []float64, lo, hi int) float64 {
	lo, hi = max(0, lo), min(len(a), hi)
	if hi <= lo {
		return math.Inf(1)
	}
	var e float64
	for i := lo; i < hi; i++ {
		e += math.Abs(a[i] - b[i])
	}
	return e / float64(hi-lo)
}

// t92IdealStep is the profile of a straight ideal step through a 7-tap mask:
// out = in + amount*(in - conv(in, k)), measured with the same 10-90% width,
// overshoot and halo as the photo edges.
func t92IdealStep(k [7]float64, amount float64) (width, overshoot, halo float64) {
	np := t92IdealProfile(k, amount)
	if np == nil {
		return
	}
	width, overshoot, halo, _ = profileNumbers(np)
	return
}

// t92IdealProfile is the normalised profile of an ideal step through the mask
// out = in + amount*(in - conv(in, k)).
func t92IdealProfile(k [7]float64, amount float64) []float64 {
	n := edgeSamples()
	in := make([]float64, n)
	for i := range in {
		t := edgeT0 + float64(i)*edgeStep
		in[i] = 20 + 200*0.5*(1+math.Erf(t/(math.Sqrt2*0.05)))
	}
	c := t92Conv1(in, k)
	out := make([]float64, n)
	for i := range out {
		out[i] = in[i] + amount*(in[i]-c[i])
	}
	low := (out[0] + out[1] + out[2]) / 3
	high := (out[n-1] + out[n-2] + out[n-3]) / 3
	step := high - low
	if math.Abs(step) < 1e-9 {
		return nil
	}
	np := make([]float64, n)
	for i := range out {
		np[i] = (out[i] - low) / step
	}
	return np
}

// t92Pool combines the training photos' mean registered profiles, weighted by
// their edge counts, and their fitted view amount.
func t92Pool(res []*t92Result, idx []int) (p, q []float64, a0 float64, edges int) {
	n := edgeSamples()
	p, q = make([]float64, n), make([]float64, n)
	var wsum float64
	for _, i := range idx {
		r := res[i]
		if r.profN == 0 || len(r.baseProf) != n {
			continue
		}
		w := float64(r.profN)
		for j := 0; j < n; j++ {
			p[j] += r.baseProf[j] * w
			q[j] += r.sppProf[j] * w
		}
		a0 += r.a0 * w
		wsum += w
		edges += r.profN
	}
	if wsum == 0 {
		return nil, nil, 0, 0
	}
	for j := 0; j < n; j++ {
		p[j] /= wsum
		q[j] /= wsum
	}
	return p, q, a0 / wsum, edges
}

// t92FitDoG fits one difference-of-Gaussians shape to the average training
// profile, and reports the profile MAE of the current Gaussian at the same
// fitted amount for comparison.
func t92FitDoG(p, q []float64, a0 float64, k0 [7]float32) t92DoG {
	d := t92DoG{}
	if len(p) == 0 {
		return d
	}
	var k0d [7]float64
	for i := range k0 {
		k0d[i] = float64(k0[i])
	}
	cur := make([]float64, len(p))
	c0 := t92Conv1(p, k0d)
	for j := range p {
		cur[j] = p[j] + a0*(p[j]-c0[j])
	}
	d.BaseMAE = t92MAE(cur, q, 2, len(p)-2)
	best := math.Inf(1)
	for _, s1 := range t92DogSigmas {
		g1, g2 := t92Gauss7(s1), t92Gauss7(s1*t92DogRatio)
		c1, c2 := t92Conv1(p, g1), t92Conv1(p, g2)
		resp := make([]float64, len(p))
		for j := range resp {
			resp[j] = c1[j] - c2[j]
		}
		for _, lam := range t92DogLambdas {
			out := make([]float64, len(p))
			for j := range out {
				out[j] = p[j] + lam*a0*resp[j]
			}
			e := t92MAE(out, q, 2, len(p)-2)
			if e < best {
				best = e
				d = t92DoG{Sigma1: s1, Lambda: lam, TrainMAE: e, BaseMAE: d.BaseMAE, OK: true}
			}
		}
	}
	return d
}

// t92Pick chooses the candidate c in cand that minimises the median of
// errs[c][i] over idx, with the mean as the tie-break.
func t92Pick(errs [][]float64, cand, idx []int) int {
	bestC, bestMed, bestMean := -1, math.Inf(1), math.Inf(1)
	for _, c := range cand {
		v := make([]float64, 0, len(idx))
		for _, i := range idx {
			v = append(v, errs[c][i])
		}
		med, mn := medianF(v), meanF(v)
		if med < bestMed-1e-9 || (math.Abs(med-bestMed) <= 1e-9 && mn < bestMean) {
			bestC, bestMed, bestMean = c, med, mn
		}
	}
	return bestC
}

func t92BodyPhotos(res []*t92Result) map[string][]int {
	out := map[string][]int{}
	for i, r := range res {
		out[r.p.body] = append(out[r.p.body], i)
	}
	return out
}

func t92Bodies(res []*t92Result) []string {
	m := t92BodyPhotos(res)
	var out []string
	for b := range m {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// t92Select applies a held-out choice of one candidate. It returns the
// per-photo held-out errors, the chosen candidate per fold (body for LOBO,
// photo index for LOO).
func t92Select(errs [][]float64, cand []int, res []*t92Result, lobo bool) (held []float64, choice map[string]int, looChoice []int) {
	held = make([]float64, len(res))
	choice = map[string]int{}
	looChoice = make([]int, len(res))
	if lobo {
		for body, idx := range t92BodyPhotos(res) {
			var tr []int
			for i := range res {
				if res[i].p.body != body {
					tr = append(tr, i)
				}
			}
			if len(tr) == 0 {
				continue
			}
			c := t92Pick(errs, cand, tr)
			choice[body] = c
			for _, i := range idx {
				held[i] = errs[c][i]
			}
		}
		return
	}
	for i := range res {
		var tr []int
		for j := range res {
			if j != i {
				tr = append(tr, j)
			}
		}
		c := t92Pick(errs, cand, tr)
		looChoice[i] = c
		held[i] = errs[c][i]
	}
	return
}

// t92Bootstrap is the paired bootstrap of held-out minus current.
func t92Bootstrap(diff []float64) (med, medLo, medHi, mean, meanLo, meanHi float64) {
	const n = 10000
	rng := rand.New(rand.NewSource(42))
	meds, means := make([]float64, n), make([]float64, n)
	sample := make([]float64, len(diff))
	for r := 0; r < n; r++ {
		for i := range sample {
			sample[i] = diff[rng.Intn(len(diff))]
		}
		meds[r], means[r] = medianF(sample), meanF(sample)
	}
	sort.Float64s(meds)
	sort.Float64s(means)
	pct := func(v []float64, p float64) float64 { return v[int(p*float64(len(v)-1))] }
	return medianF(diff), pct(meds, 0.025), pct(meds, 0.975),
		meanF(diff), pct(means, 0.025), pct(means, 0.975)
}

func t92BetterWorse(v, cur []float64) (better, worse, same int) {
	for i := range v {
		switch {
		case v[i] < cur[i]-0.01:
			better++
		case v[i] > cur[i]+0.01:
			worse++
		default:
			same++
		}
	}
	return
}

// t92WeightedEdge aggregates one edge stat per body, edge-count weighted.
func t92WeightedEdge(res []*t92Result, body string, get func(*t92Result) edgeStat) (st edgeStat) {
	var n int
	var w, over, halo, aligned, unaligned, shift float64
	var widthSum, widthN float64
	for _, r := range res {
		if r.p.body != body {
			continue
		}
		e := get(r)
		if e.N == 0 {
			continue
		}
		n += e.N
		w += float64(e.N)
		over += e.Overshoot * float64(e.N)
		halo += e.Halo * float64(e.N)
		aligned += e.ProfileAligned * float64(e.N)
		unaligned += e.ProfileUnaligned * float64(e.N)
		shift += e.Shift * float64(e.N)
		if !math.IsNaN(e.Width) {
			widthSum += e.Width * float64(e.N)
			widthN += float64(e.N)
		}
	}
	if n == 0 {
		return
	}
	st.N = n
	st.Overshoot = over / w
	st.Halo = halo / w
	st.ProfileAligned = aligned / w
	st.ProfileUnaligned = unaligned / w
	st.Shift = shift / w
	if widthN > 0 {
		st.Width = widthSum / widthN
	} else {
		st.Width = math.NaN()
	}
	return
}

func sharpenRadiusCommand(desk string) {
	fs := flag.NewFlagSet("sharpen-radius", flag.ExitOnError)
	photosRoot := fs.String("photos", filepath.Join(desk, "foveon-samples"), "folders with the X3F files")
	calibDir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports under */)")
	out := fs.String("out", filepath.Join("zz-out", "t92"), "output folder")
	limit := fs.Int("limit", 0, "only the first N photos (development)")
	foldCache := fs.String("folds", "", "held-out base cache (default <out>/folds.json)")
	pics := fs.String("pics", "", "comma-separated photo names for the crops")
	radiiS := fs.String("radii", "0.6,0.8,1.0,1.2,1.5", "full-size mask radii to sweep")
	detailsS := fs.String("details", "-0.25,0,0.25", "Detail values for the radius x amount grid")
	refS := fs.String("ref", filepath.Join("zz-out", "t90", "photos.csv"), "task-90 photos.csv for the cross-check (optional)")
	cropsOnly := fs.Bool("crops-only", false, "only write the crops from an existing photos.csv and folds.json")
	fs.Parse(os.Args[2:])

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "sharpen-radius:", err)
		os.Exit(1)
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

	if *cropsOnly {
		if err := t92CropsOnly(*out, *photosRoot, *calibDir, *foldCache, *pics, logf); err != nil {
			fmt.Fprintln(os.Stderr, "sharpen-radius:", err)
			os.Exit(1)
		}
		return
	}

	radii, err := t92ParseFloats(*radiiS)
	if err != nil || len(radii) == 0 {
		fmt.Fprintln(os.Stderr, "sharpen-radius: bad -radii:", err)
		os.Exit(1)
	}
	details, err := t92ParseFloats(*detailsS)
	if err != nil || len(details) == 0 {
		fmt.Fprintln(os.Stderr, "sharpen-radius: bad -details:", err)
		os.Exit(1)
	}
	if t92Index(radii, 1.0) < 0 {
		radii = append(radii, 1.0)
	}
	if t92Index(radii, t92WrongRadius) < 0 {
		radii = append(radii, t92WrongRadius)
	}
	if t92Index(details, 0) < 0 {
		details = append(details, 0)
	}
	sort.SliceStable(radii, func(i, j int) bool { return radii[i] < radii[j] })
	kCur, kDi0 := t92Index(radii, 1.0), t92Index(details, 0)

	// No disk cache: the decoded frames are kept in memory and the cache
	// would be about 0.9 GB. The fold cache is small and kept.
	photos, err := t90Load(*photosRoot, *calibDir, "", *limit, logf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharpen-radius:", err)
		os.Exit(1)
	}
	fc := *foldCache
	if fc == "" {
		fc = filepath.Join(*out, "folds.json")
	}
	folds := t90Folds(fc, photos, logf)

	res := make([]*t92Result, len(photos))
	nCell := len(radii) * len(details)
	for i, p := range photos {
		pred := t90Apply(&folds[i], p.raw)
		b := render.NewBase(pred)
		sppSRGB := t90SRGB(p.spp.Pix)
		w, h := p.raw.W, p.raw.H
		r := &t92Result{p: p, heldBase: calib.MeanError(pred, p.spp), s: p.raw.Scale}
		if r.s <= 0 {
			r.s = 1
		}
		r.grid = make([]float64, nCell)
		render.SetSharpResearch(render.SharpResearch{})
		r.a0, r.k0 = render.SPPViewMask(r.s, p.iso, "Quattro")
		// The unsharpened render: the profile the alternative shape is
		// fitted to, and the no-sharpening reference.
		un := t90Render(b, p.ph, -1)
		r.unsharp = meanAbs8(un, sppSRGB)
		r.baseProf, r.sppProf, r.profN = edgeMeanProfiles(un, sppSRGB, w, h)
		for ri, rad := range radii {
			render.SetSharpResearch(render.SharpResearch{Sigma: rad})
			for di, d := range details {
				img := t90Render(b, p.ph, d)
				r.grid[ri*len(details)+di] = meanAbs8(img, sppSRGB)
				if di == kDi0 {
					o, s := edgeProfileStats(img, sppSRGB, w, h)
					r.edge = append(r.edge, o)
					// SPP's profile is filtered by our accepted edges, so
					// keep the reference from the current pair, not the last
					// radius in the loop.
					if ri == kCur {
						r.sppEdge = s
					}
				}
			}
		}
		render.SetSharpResearch(render.SharpResearch{})
		res[i] = r
		logf("  %d/%d %s %s base %.2f unsharp %.2f cur %.2f", i+1, len(res), p.body, p.name, r.heldBase, r.unsharp, r.grid[kCur*len(details)+kDi0])
	}

	// The current comparison errors.
	cur := make([]float64, len(res))
	for i, r := range res {
		cur[i] = r.grid[kCur*len(details)+kDi0]
	}

	// Radius-only matrices and held-out choices.
	radiusErrs := make([][]float64, len(radii))
	for ri := range radii {
		radiusErrs[ri] = make([]float64, len(res))
		for i := range res {
			radiusErrs[ri][i] = res[i].grid[ri*len(details)+kDi0]
		}
	}
	radCand := make([]int, len(radii))
	for i := range radCand {
		radCand[i] = i
	}
	radLOO, _, _ := t92Select(radiusErrs, radCand, res, false)
	radLOBO, radChoice, _ := t92Select(radiusErrs, radCand, res, true)

	// The radius x amount grid.
	pairErrs := make([][]float64, nCell)
	for c := range pairErrs {
		pairErrs[c] = make([]float64, len(res))
		for i := range res {
			pairErrs[c][i] = res[i].grid[c]
		}
	}
	pairCand := make([]int, nCell)
	for i := range pairCand {
		pairCand[i] = i
	}
	pairLOO, _, _ := t92Select(pairErrs, pairCand, res, false)
	pairLOBO, pairChoice, _ := t92Select(pairErrs, pairCand, res, true)

	// The alternative shape: fit on the training profiles and apply to the
	// held-out photo (LOO) or body (LOBO). The fits are cheap; the renders
	// are the second pass.
	looDog := make([]t92DoG, len(res))
	for i := range res {
		var tr []int
		for j := range res {
			if j != i {
				tr = append(tr, j)
			}
		}
		p, q, a0, edges := t92Pool(res, tr)
		looDog[i] = t92FitDoG(p, q, a0, res[i].k0)
		looDog[i].Edges = edges
	}
	bodyDog := map[string]t92DoG{}
	for body := range t92BodyPhotos(res) {
		var tr []int
		for i := range res {
			if res[i].p.body != body {
				tr = append(tr, i)
			}
		}
		p, q, a0, edges := t92Pool(res, tr)
		d := t92FitDoG(p, q, a0, res[0].k0)
		d.Edges = edges
		bodyDog[body] = d
	}

	// Second pass: the DoG renders and the crops.
	wantPics := map[string]bool{}
	for _, n := range strings.Split(*pics, ",") {
		if n = strings.TrimSpace(n); n != "" {
			wantPics[strings.ToUpper(n)] = true
		}
	}
	picNames := t92PickPictures(res, cur, wantPics)
	picSet := map[string]bool{}
	for _, n := range picNames {
		picSet[strings.ToUpper(n)] = true
	}
	bodyBestRadius := map[string]int{}
	for body, idx := range t92BodyPhotos(res) {
		bodyBestRadius[body] = t92Pick(radiusErrs, radCand, idx)
	}
	for i, r := range res {
		pred := t90Apply(&folds[i], r.p.raw)
		b := render.NewBase(pred)
		sppSRGB := t90SRGB(r.p.spp.Pix)
		w, h := r.p.raw.W, r.p.raw.H
		// Leave-one-photo-out alternative shape. If the fold had no usable
		// profiles the current mask is the honest fallback.
		r.dogShape[0] = looDog[i]
		var img []float32
		if looDog[i].OK && looDog[i].Sigma1 > 0 {
			render.SetSharpResearch(render.SharpResearch{KernelSet: true, Kernel: t92DoGKernel(looDog[i].Sigma1), AmountScale: looDog[i].Lambda})
			img = t90Render(b, r.p.ph, 0)
		} else {
			render.SetSharpResearch(render.SharpResearch{})
			img = t90Render(b, r.p.ph, 0)
		}
		r.dogErr[0] = meanAbs8(img, sppSRGB)
		// Leave-one-body-out alternative shape.
		d := bodyDog[r.p.body]
		r.dogShape[1] = d
		if d.OK && d.Sigma1 > 0 {
			render.SetSharpResearch(render.SharpResearch{KernelSet: true, Kernel: t92DoGKernel(d.Sigma1), AmountScale: d.Lambda})
			img = t90Render(b, r.p.ph, 0)
		} else {
			render.SetSharpResearch(render.SharpResearch{})
			img = t90Render(b, r.p.ph, 0)
		}
		r.dogErr[1] = meanAbs8(img, sppSRGB)
		_, r.dogEdge[1] = edgeProfileStats(img, sppSRGB, w, h)
		render.SetSharpResearch(render.SharpResearch{})
		if (i+1)%10 == 0 {
			logf("  second pass %d/%d", i+1, len(res))
		}
		if picSet[strings.ToUpper(r.p.name)] {
			// Current, SPP, and the body's in-sample best radius.
			curIMG := t90Render(b, r.p.ph, 0)
			bestR := radii[bodyBestRadius[r.p.body]]
			render.SetSharpResearch(render.SharpResearch{Sigma: bestR})
			bestIMG := t90Render(b, r.p.ph, 0)
			render.SetSharpResearch(render.SharpResearch{})
			x0, y0 := bestEdgeWindow(sppSRGB, w, h, 256, 8)
			path := filepath.Join(*out, fmt.Sprintf("crop_%02d_%s_%s.png", i+1, r.p.body, r.p.name))
			if err := writeCropPNG(path, [][]float32{curIMG, sppSRGB, bestIMG}, w, h, x0, y0, 256); err != nil {
				logf("  crop %s: %v", path, err)
			} else {
				logf("  crop %s best radius %.2f", path, bestR)
			}
		}
	}

	ref := t92RefErrors(*refS)
	writeT92Tables(*out, res, radii, details, kCur, kDi0, cur, radiusErrs, radLOO, radLOBO, radChoice,
		pairErrs, pairLOO, pairLOBO, pairChoice, looDog, bodyDog, picNames, ref)
	writeT92CSV(*out, res, radii, details)
	fmt.Printf("wrote %s and photos.csv\n", filepath.Join(*out, "tables.md"))
}

// t92PickPictures chooses, per default, the median current-error photo of
// dp0, sd and sdH, as task 90 did.
func t92PickPictures(res []*t92Result, cur []float64, want map[string]bool) []string {
	if len(want) > 0 {
		var out []string
		for _, r := range res {
			if want[strings.ToUpper(r.p.name)] {
				out = append(out, r.p.name)
			}
		}
		return out
	}
	var out []string
	for _, body := range []string{"dp0", "sd", "sdH"} {
		var idx []int
		for i, r := range res {
			if r.p.body == body {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			continue
		}
		sort.Slice(idx, func(a, b int) bool { return cur[idx[a]] < cur[idx[b]] })
		out = append(out, res[idx[len(idx)/2]].p.name)
	}
	return out
}

func t92PairName(radii, details []float64, c int) string {
	nd := len(details)
	return fmt.Sprintf("r%.2f d%+.2f", radii[c/nd], details[c%nd])
}

func writeT92Tables(dir string, res []*t92Result, radii, details []float64, kCur, kDi0 int,
	cur []float64, radiusErrs [][]float64, radLOO, radLOBO []float64, radChoice map[string]int,
	pairErrs [][]float64, pairLOO, pairLOBO []float64, pairChoice map[string]int,
	looDog []t92DoG, bodyDog map[string]t92DoG, picNames []string, ref map[string]float64) {
	f, err := os.Create(filepath.Join(dir, "tables.md"))
	if err != nil {
		return
	}
	defer f.Close()
	w := f
	bodies := t92Bodies(res)
	fmt.Fprint(w, "# TASK-92 tables\n\n")
	fmt.Fprint(w, "Errors are the 8-bit sRGB mean absolute difference at the 1200 px frame, the\n")
	fmt.Fprint(w, "same measure and pairs as task 90. `held out` = the matrix and curves fitted on\n")
	fmt.Fprint(w, "the other photos. Radii are the full-size mask sigma; the render at the 1200 px\n")
	fmt.Fprint(w, "frame is at about 0.2x native and goes through sppView's reduced-view fit.\n\n")

	// Table 0 — cross-check and the current row.
	curMean, curMed := meanF(cur), medianF(cur)
	var hb []float64
	for _, r := range res {
		hb = append(hb, r.heldBase)
	}
	fmt.Fprint(w, "## Table 0 — cross-check\n\n")
	fmt.Fprintln(w, "| quantity | mean | median | n |")
	fmt.Fprintln(w, "|---|---:|---:|---:|")
	fmt.Fprintf(w, "| held-out base, calib.MeanError | %.2f | %.2f | %d |\n", meanF(hb), medianF(hb), len(hb))
	fmt.Fprintf(w, "| current pair (r 1.00, Detail 0) | %.2f | %.2f | %d |\n", curMean, curMed, len(cur))
	if len(ref) > 0 {
		var diffs []float64
		for i, r := range res {
			if v, ok := ref[r.p.name]; ok {
				diffs = append(diffs, cur[i]-v)
			}
		}
		if len(diffs) > 0 {
			maxAbs, sum := 0.0, 0.0
			for _, d := range diffs {
				maxAbs = math.Max(maxAbs, math.Abs(d))
				sum += d
			}
			fmt.Fprintf(w, "| current vs task-90 csv | mean diff %+.4f | max |diff| %.4f | %d |\n", sum/float64(len(diffs)), maxAbs, len(diffs))
		}
	}

	// Table 1 — the ideal step.
	fmt.Fprint(w, "\n## Table 1 — the ideal step through the mask\n\n")
	fmt.Fprint(w, "### 1a. Full-size mask, amount 0.5, 7-tap Gaussian (the code's kernel)\n\n")
	fmt.Fprint(w, "The input is an ideal step, so the width is sampling-limited (0.5 px samples);\n")
	fmt.Fprint(w, "the overshoot and halo are the mask's own ring, which is what a radius changes.\n\n")
	fmt.Fprintln(w, "| radius px | width 10-90% px | overshoot % | halo px |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|")
	for _, rad := range radii {
		wid, over, halo := t92IdealStep(t92Gauss7(rad), 0.5)
		fmt.Fprintf(w, "| %.2f | %.2f | %.2f | %.1f |\n", rad, wid, 100*over, halo)
	}
	fmt.Fprint(w, "\n### 1b. At the scored frame: what sppView fits at s=0.22, ISO 100 Quattro\n\n")
	fmt.Fprint(w, "The fitted kernel is the floor sigma 0.5 for every radius; the radius only\n")
	fmt.Fprint(w, "changes the fitted amount (it grows as radius squared).\n\n")
	fmt.Fprintln(w, "| radius px | view amount | kernel centre | width px | overshoot % | halo px |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|---:|---:|")
	for _, rad := range radii {
		render.SetSharpResearch(render.SharpResearch{Sigma: rad})
		a, k := render.SPPViewMask(0.22, 100, "Quattro")
		render.SetSharpResearch(render.SharpResearch{})
		var kd [7]float64
		for i := range k {
			kd[i] = float64(k[i])
		}
		wid, over, halo := t92IdealStep(kd, a)
		fmt.Fprintf(w, "| %.2f | %.4f | %.3f | %.2f | %.2f | %.1f |\n", rad, a, k[3], wid, 100*over, halo)
	}

	// Table 2 — the radius sweep at the current amount.
	fmt.Fprint(w, "\n## Table 2 — the radius sweep at Detail 0 (in-sample for the radius)\n\n")
	fmt.Fprint(w, "| radius | mean | median |")
	for _, b := range bodies {
		fmt.Fprintf(w, " %s median |", b)
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, "|---:|---:|---:|")
	for range bodies {
		fmt.Fprint(w, "---:|")
	}
	fmt.Fprintln(w)
	for ri, rad := range radii {
		var v []float64
		for _, r := range res {
			v = append(v, r.grid[ri*len(details)+kDi0])
		}
		mark := ""
		if ri == kCur {
			mark = " **current**"
		}
		fmt.Fprintf(w, "| %.2f%s | %.2f | %.2f |", rad, mark, meanF(v), medianF(v))
		for _, b := range bodies {
			var bv []float64
			for _, r := range res {
				if r.p.body == b {
					bv = append(bv, r.grid[ri*len(details)+kDi0])
				}
			}
			fmt.Fprintf(w, " %.2f |", medianF(bv))
		}
		fmt.Fprintln(w)
	}

	// Table 2b — edges: overshoot and registered residual.
	fmt.Fprint(w, "\n## Table 2b — edges: overshoot / registered profile residual, ours vs SPP\n\n")
	fmt.Fprint(w, "Each row is one edge statistic for ours; SPP's own overshoot and width are the\n")
	fmt.Fprint(w, "reference rows and are the same for every radius. The profile residual is\n")
	fmt.Fprint(w, "ours against SPP after the local registration (shape, not position).\n\n")
	fmt.Fprint(w, "| radius | stat |")
	for _, b := range bodies {
		fmt.Fprintf(w, " %s |", b)
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, "|---:|---|")
	for range bodies {
		fmt.Fprint(w, "---|")
	}
	fmt.Fprintln(w)
	sppOver := map[string]float64{}
	sppWid := map[string]float64{}
	for _, b := range bodies {
		st := t92WeightedEdge(res, b, func(r *t92Result) edgeStat { return r.sppEdge })
		sppOver[b], sppWid[b] = 100*st.Overshoot, st.Width
	}
	fmt.Fprint(w, "| SPP | overshoot % |")
	for _, b := range bodies {
		fmt.Fprintf(w, " %.2f |", sppOver[b])
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, "| SPP | width px |")
	for _, b := range bodies {
		fmt.Fprintf(w, " %.2f |", sppWid[b])
	}
	fmt.Fprintln(w)
	for ri, rad := range radii {
		rowOver, rowRes, rowWid := "", "", ""
		for _, b := range bodies {
			st := t92WeightedEdge(res, b, func(r *t92Result) edgeStat { return r.edge[ri] })
			rowOver += fmt.Sprintf(" %.2f |", 100*st.Overshoot)
			rowRes += fmt.Sprintf(" %.2f |", 100*st.ProfileAligned)
			rowWid += fmt.Sprintf(" %.2f |", st.Width)
		}
		fmt.Fprintf(w, "| %.2f | overshoot %% |%s\n", rad, rowOver)
		fmt.Fprintf(w, "| %.2f | profile err %% |%s\n", rad, rowRes)
		fmt.Fprintf(w, "| %.2f | width px |%s\n", rad, rowWid)
	}

	// Table 3 — held-out radius.
	fmt.Fprint(w, "\n## Table 3 — held-out choice of one radius\n\n")
	fmt.Fprintln(w, "| method | mean | median | delta mean | delta median | better/worse/same |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|")
	fmt.Fprintf(w, "| current 1.00 | %.2f | %.2f | — | — | — |\n", curMean, curMed)
	lb, lw, ls := t92BetterWorse(radLOO, cur)
	ob, ow, osame := t92BetterWorse(radLOBO, cur)
	fmt.Fprintf(w, "| leave-one-photo-out | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(radLOO), medianF(radLOO), meanF(radLOO)-curMean, medianF(radLOO)-curMed, lb, lw, ls)
	fmt.Fprintf(w, "| leave-one-body-out | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(radLOBO), medianF(radLOBO), meanF(radLOBO)-curMean, medianF(radLOBO)-curMed, ob, ow, osame)
	dmed, dmedLo, dmedHi, dmean, dmeanLo, dmeanHi := t92Bootstrap(t92Diff(radLOBO, cur))
	fmt.Fprintf(w, "\nLOBO-current paired bootstrap (10,000, seed 42): median %+.3f [%+.3f, %+.3f], mean %+.3f [%+.3f, %+.3f].\n", dmed, dmedLo, dmedHi, dmean, dmeanLo, dmeanHi)
	fmt.Fprint(w, "\nPer body: the LOBO radius, the held-out median at it, and the body's own in-sample best radius.\n\n")
	fmt.Fprintln(w, "| body | n | LOBO radius | median current | median LOBO | delta | in-sample best | in-sample median |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|")
	for _, b := range bodies {
		idx := t92BodyPhotos(res)[b]
		var c, l []float64
		for _, i := range idx {
			c = append(c, cur[i])
			l = append(l, radLOBO[i])
		}
		inK := t92Pick(radiusErrs, t92Cand(len(radii)), idx)
		var inV []float64
		for _, i := range idx {
			inV = append(inV, radiusErrs[inK][i])
		}
		fmt.Fprintf(w, "| %s | %d | %.2f | %.2f | %.2f | %+.2f | %.2f | %.2f |\n", b, len(idx), radii[radChoice[b]], medianF(c), medianF(l), medianF(l)-medianF(c), radii[inK], medianF(inV))
	}

	// Table 4 — the pair grid.
	fmt.Fprint(w, "\n## Table 4 — the radius x Detail grid (in-sample for the pair)\n\n")
	fmt.Fprintln(w, "| radius | Detail | amount x current | mean | median |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|---:|")
	curScale := 1.0
	for ri, rad := range radii {
		for di, d := range details {
			var v []float64
			for _, r := range res {
				v = append(v, r.grid[ri*len(details)+di])
			}
			scale := (rad * rad / (1.0 * 1.0)) * (1 + d)
			mark := ""
			if ri == kCur && di == kDi0 {
				mark = " **current**"
				curScale = scale
			}
			fmt.Fprintf(w, "| %.2f%s | %+.2f | %.2f | %.2f | %.2f |\n", rad, mark, d, scale/curScale, meanF(v), medianF(v))
		}
	}
	fmt.Fprint(w, "\n### Table 4b — held-out choice of one radius x Detail pair\n\n")
	fmt.Fprintln(w, "| method | mean | median | delta mean | delta median | better/worse/same |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|")
	fmt.Fprintf(w, "| current | %.2f | %.2f | — | — | — |\n", curMean, curMed)
	pb, pw, ps := t92BetterWorse(pairLOO, cur)
	qb, qw, qs := t92BetterWorse(pairLOBO, cur)
	fmt.Fprintf(w, "| leave-one-photo-out | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(pairLOO), medianF(pairLOO), meanF(pairLOO)-curMean, medianF(pairLOO)-curMed, pb, pw, ps)
	fmt.Fprintf(w, "| leave-one-body-out | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(pairLOBO), medianF(pairLOBO), meanF(pairLOBO)-curMean, medianF(pairLOBO)-curMed, qb, qw, qs)
	pm, pml, pmh, pn, pnl, pnh := t92Bootstrap(t92Diff(pairLOBO, cur))
	fmt.Fprintf(w, "\nLOBO-current paired bootstrap: median %+.3f [%+.3f, %+.3f], mean %+.3f [%+.3f, %+.3f].\n\n", pm, pml, pmh, pn, pnl, pnh)
	fmt.Fprintln(w, "| body | n | LOBO pair | median current | median LOBO | delta | in-sample best pair | in-sample median |")
	fmt.Fprintln(w, "|---|---:|---|---:|---:|---:|---|---:|")
	for _, b := range bodies {
		idx := t92BodyPhotos(res)[b]
		var c, l []float64
		for _, i := range idx {
			c = append(c, cur[i])
			l = append(l, pairLOBO[i])
		}
		inK := t92Pick(pairErrs, t92Cand(len(pairErrs)), idx)
		var inV []float64
		for _, i := range idx {
			inV = append(inV, pairErrs[inK][i])
		}
		fmt.Fprintf(w, "| %s | %d | %s | %.2f | %.2f | %+.2f | %s | %.2f |\n", b, len(idx), t92PairName(radii, details, pairChoice[b]), medianF(c), medianF(l), medianF(l)-medianF(c), t92PairName(radii, details, inK), medianF(inV))
	}

	// Table 5 — the alternative shape.
	fmt.Fprint(w, "\n## Table 5 — one alternative kernel shape (difference of Gaussians, ratio 1.6)\n\n")
	fmt.Fprint(w, "Fitted to the training bodies' edge-count weighted average registered SPP edge\n")
	fmt.Fprint(w, "profile. `profile MAE` is on that average profile (fraction of the step, x1000);\n")
	fmt.Fprint(w, "`gauss` is the current sigma-0.5 view kernel at the same fitted amount.\n\n")
	fmt.Fprintln(w, "| fold (held-out body) | sigma1 | lambda | edges | DoG profile MAE | gauss profile MAE | held-out mean | held-out median | delta median |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, b := range bodies {
		d := bodyDog[b]
		var v, c []float64
		for i, r := range res {
			if r.p.body != b {
				continue
			}
			v = append(v, r.dogErr[1])
			c = append(c, cur[i])
		}
		fmt.Fprintf(w, "| %s | %.2f | %.2f | %d | %.1f | %.1f | %.2f | %.2f | %+.2f |\n", b, d.Sigma1, d.Lambda, d.Edges, 1000*d.TrainMAE, 1000*d.BaseMAE, meanF(v), medianF(v), medianF(v)-medianF(c))
	}
	var dogLOBO, dogLOO []float64
	var ls1, llam, lmae, lbase []float64
	for _, r := range res {
		dogLOBO = append(dogLOBO, r.dogErr[1])
		dogLOO = append(dogLOO, r.dogErr[0])
	}
	for _, d := range looDog {
		if !d.OK {
			continue
		}
		ls1 = append(ls1, d.Sigma1)
		llam = append(llam, d.Lambda)
		lmae = append(lmae, 1000*d.TrainMAE)
		lbase = append(lbase, 1000*d.BaseMAE)
	}
	if len(ls1) > 0 {
		sort.Float64s(ls1)
		sort.Float64s(llam)
		fmt.Fprintf(w, "\nLOO fitted shapes: sigma1 median %.2f [%.2f, %.2f], lambda median %.2f [%.2f, %.2f]; profile MAE per fold mean DoG %.1f, gauss %.1f (x1000 of the step).\n",
			medianF(ls1), ls1[0], ls1[len(ls1)-1], medianF(llam), llam[0], llam[len(llam)-1], meanF(lmae), meanF(lbase))
	}
	db, dw, ds := t92BetterWorse(dogLOO, cur)
	eb, ew, es := t92BetterWorse(dogLOBO, cur)
	fmt.Fprintf(w, "\n| method | mean | median | delta mean | delta median | better/worse/same |\n")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|")
	fmt.Fprintf(w, "| current | %.2f | %.2f | — | — | — |\n", curMean, curMed)
	fmt.Fprintf(w, "| DoG leave-one-photo-out | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(dogLOO), medianF(dogLOO), meanF(dogLOO)-curMean, medianF(dogLOO)-curMed, db, dw, ds)
	fmt.Fprintf(w, "| DoG leave-one-body-out | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(dogLOBO), medianF(dogLOBO), meanF(dogLOBO)-curMean, medianF(dogLOBO)-curMed, eb, ew, es)
	dm, dml, dmh, dn, dnl, dnh := t92Bootstrap(t92Diff(dogLOBO, cur))
	fmt.Fprintf(w, "\nDoG LOBO-current paired bootstrap: median %+.3f [%+.3f, %+.3f], mean %+.3f [%+.3f, %+.3f].\n", dm, dml, dmh, dn, dnl, dnh)
	fmt.Fprint(w, "\nPer body edges at the held-out DoG shape (ours; SPP on the same row as Table 2b):\n\n")
	fmt.Fprintln(w, "| body | overshoot % | profile err % | width px |")
	fmt.Fprintln(w, "|---|---:|---:|---:|")
	for _, b := range bodies {
		st := t92WeightedEdge(res, b, func(r *t92Result) edgeStat { return r.dogEdge[1] })
		fmt.Fprintf(w, "| %s | %.2f | %.2f | %.2f |\n", b, 100*st.Overshoot, 100*st.ProfileAligned, st.Width)
	}

	// Table 6 — the wrong radius.
	fmt.Fprint(w, "\n## Table 6 — the check that must fail: radius 3.0\n\n")
	fmt.Fprintln(w, "| body | mean current | mean r3 | median current | median r3 | overshoot current % | overshoot r3 % | profile err current % | profile err r3 % |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	riWrong := t92Index(radii, t92WrongRadius)
	for _, b := range bodies {
		idx := t92BodyPhotos(res)[b]
		var c, x []float64
		for _, i := range idx {
			c = append(c, cur[i])
			x = append(x, radiusErrs[riWrong][i])
		}
		sc := t92WeightedEdge(res, b, func(r *t92Result) edgeStat { return r.edge[kCur] })
		sx := t92WeightedEdge(res, b, func(r *t92Result) edgeStat { return r.edge[riWrong] })
		fmt.Fprintf(w, "| %s | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f |\n", b, meanF(c), meanF(x), medianF(c), medianF(x), 100*sc.Overshoot, 100*sx.Overshoot, 100*sc.ProfileAligned, 100*sx.ProfileAligned)
	}
	var xAll []float64
	for i := range res {
		xAll = append(xAll, radiusErrs[riWrong][i])
	}
	fmt.Fprintf(w, "| all | %.2f | %.2f | %.2f | %.2f | — | — | — | — |\n", curMean, meanF(xAll), curMed, medianF(xAll))

	// Table 7 — the crops.
	fmt.Fprint(w, "\n## Table 7 — the crops\n\n")
	fmt.Fprint(w, "1:1 256 px crops, left = ours current (r 1.00, Detail 0), middle = SPP,\nright = ours at the photo's body's in-sample best radius.\n\n")
	fmt.Fprintln(w, "| photo | body | file |")
	fmt.Fprintln(w, "|---|---|---|")
	for i, r := range res {
		for _, n := range picNames {
			if n != r.p.name {
				continue
			}
			fmt.Fprintf(w, "| %s | %s | crop_%02d_%s_%s.png |\n", r.p.name, r.p.body, i+1, r.p.body, r.p.name)
		}
	}
}

// t92CropsOnly writes the three 1:1 crops from an existing photos.csv (for
// the body's in-sample best radius) and folds.json, without rerunning the
// sweep.
func t92CropsOnly(out, photosRoot, calibDir, foldPath, pics string, logf func(string, ...any)) error {
	csvPath := filepath.Join(out, "photos.csv")
	f, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil || len(rows) < 2 {
		return fmt.Errorf("read %s: %w", csvPath, err)
	}
	hdr := rows[0]
	photoCol, bodyCol := -1, -1
	type radErr struct {
		rad float64
		col int
	}
	var radCols []radErr
	curCol := -1
	for i, h := range hdr {
		switch h {
		case "photo":
			photoCol = i
		case "body":
			bodyCol = i
		}
		if strings.HasPrefix(h, "r") {
			if k := strings.Index(h, "_d"); k > 0 {
				r, errR := strconv.ParseFloat(h[1:k], 64)
				d, errD := strconv.ParseFloat(h[k+2:], 64)
				if errR == nil && errD == nil {
					if math.Abs(d) < 1e-9 {
						radCols = append(radCols, radErr{r, i})
					}
					if math.Abs(r-1.0) < 1e-9 && math.Abs(d) < 1e-9 {
						curCol = i
					}
				}
			}
		}
	}
	if photoCol < 0 || bodyCol < 0 || curCol < 0 || len(radCols) == 0 {
		return fmt.Errorf("%s: no photo/body/current/radius columns", csvPath)
	}
	sort.Slice(radCols, func(i, j int) bool { return radCols[i].rad < radCols[j].rad })
	type rowInfo struct {
		photo, body string
		cur         float64
		errs        map[float64]float64
	}
	var infos []rowInfo
	for _, row := range rows[1:] {
		if photoCol >= len(row) || bodyCol >= len(row) {
			continue
		}
		ri := rowInfo{photo: row[photoCol], body: row[bodyCol], errs: map[float64]float64{}}
		ri.cur, _ = strconv.ParseFloat(row[curCol], 64)
		for _, rc := range radCols {
			if rc.col < len(row) {
				ri.errs[rc.rad], _ = strconv.ParseFloat(row[rc.col], 64)
			}
		}
		infos = append(infos, ri)
	}
	// The body's held-out (leave-one-body-out) radius, from the same
	// photos.csv errors the sweep wrote: train on the other bodies and apply
	// the choice to this body unchanged.
	bodySet := map[string]bool{}
	for _, in := range infos {
		bodySet[in.body] = true
	}
	errs := make([][]float64, len(radCols))
	for ri, rc := range radCols {
		errs[ri] = make([]float64, len(infos))
		for i, in := range infos {
			errs[ri][i] = in.errs[rc.rad]
		}
	}
	cand := t92Cand(len(radCols))
	best := map[string]float64{}
	for body := range bodySet {
		var train []int
		for i, in := range infos {
			if in.body != body {
				train = append(train, i)
			}
		}
		if len(train) == 0 {
			for i, in := range infos {
				if in.body == body {
					train = append(train, i)
				}
			}
		}
		best[body] = radCols[t92Pick(errs, cand, train)].rad
	}
	// The pictures: -pics, or the median current-error photo of dp0/sd/sdH.
	var names []string
	for _, n := range strings.Split(pics, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		for _, body := range []string{"dp0", "sd", "sdH"} {
			var v []rowInfo
			for _, in := range infos {
				if in.body == body {
					v = append(v, in)
				}
			}
			if len(v) == 0 {
				continue
			}
			sort.Slice(v, func(i, j int) bool { return v[i].cur < v[j].cur })
			names = append(names, v[len(v)/2].photo)
		}
	}
	if foldPath == "" {
		foldPath = filepath.Join(out, "folds.json")
	}
	fb, err := os.ReadFile(foldPath)
	if err != nil {
		return err
	}
	var ff struct {
		Names []string
		Folds []render.RawCorrection
	}
	if err := json.Unmarshal(fb, &ff); err != nil {
		return fmt.Errorf("read %s: %w", foldPath, err)
	}
	foldOf := map[string]int{}
	for i, n := range ff.Names {
		foldOf[n] = i
	}
	exports := calib.ExportsAnywhere(calibDir, "A00")
	all := scanPhotos([]string{photosRoot}, false)
	var report strings.Builder
	fmt.Fprint(&report, "| photo | body | ISO | current error | held-out (LOBO) radius | error at it | file |\n")
	fmt.Fprint(&report, "|---|---|---:|---:|---:|---:|---|\n")
	for _, name := range names {
		var ph *Photo
		for _, p := range all {
			if strings.EqualFold(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)), name) {
				ph = p
				break
			}
		}
		if ph == nil {
			logf("  crop %s: photo not found", name)
			continue
		}
		stem := strings.ToUpper(strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)))
		exp, ok := exports[stem]
		if !ok {
			logf("  crop %s: no A00 export", name)
			continue
		}
		fi, ok := foldOf[strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name))]
		if !ok {
			logf("  crop %s: no fold", name)
			continue
		}
		spp, err := calib.LoadLinear(exp)
		if err != nil {
			return err
		}
		maxW := sourceW
		if ph.Rotation == 90 || ph.Rotation == 270 {
			maxW = max(sourceW, spp.H)
		}
		raw, err := goLinear(ph, maxW, defaultNR)
		if err != nil {
			return err
		}
		if ph.Rotation != 0 {
			raw = raw.Rotate(ph.Rotation)
		}
		raw = fitAspect(raw.Downscale(spp.W), spp.W, spp.H)
		body := ""
		for _, in := range infos {
			if strings.EqualFold(in.photo, name) {
				body = in.body
				break
			}
		}
		br, ok := best[body]
		if !ok {
			br = 1.0
		}
		pred := t90Apply(&ff.Folds[fi], raw)
		b := render.NewBase(pred)
		sppSRGB := t90SRGB(spp.Pix)
		render.SetSharpResearch(render.SharpResearch{})
		cur := t90Render(b, ph, 0)
		render.SetSharpResearch(render.SharpResearch{Sigma: br})
		bestSRGB := t90Render(b, ph, 0)
		render.SetSharpResearch(render.SharpResearch{})
		w, h := raw.W, raw.H
		x0, y0 := bestEdgeWindow(sppSRGB, w, h, 256, 8)
		file := fmt.Sprintf("crop_%s_%s.png", body, strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)))
		path := filepath.Join(out, file)
		if err := writeCropPNG(path, [][]float32{cur, sppSRGB, bestSRGB}, w, h, x0, y0, 256); err != nil {
			return err
		}
		curErr := meanAbs8(cur, sppSRGB)
		bestErr := meanAbs8(bestSRGB, sppSRGB)
		fmt.Fprintf(&report, "| %s | %s | %.0f | %.2f | %.2f | %.2f | %s |\n", name, body, photoISO(ph), curErr, br, bestErr, file)
		logf("  crop %s best radius %.2f error %.2f -> %.2f", path, br, curErr, bestErr)
	}
	return os.WriteFile(filepath.Join(out, "crops.md"), []byte(report.String()), 0o644)
}

// t92RefErrors reads the task-90 current-error column from a photos.csv, for
// the cross-check that this study measures the same pairs.
func t92RefErrors(path string) map[string]float64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) < 2 {
		return nil
	}
	pi, ci := -1, -1
	for i, h := range rows[0] {
		switch h {
		case "photo":
			pi = i
		case "d+0.00":
			ci = i
		}
	}
	if pi < 0 || ci < 0 {
		return nil
	}
	out := map[string]float64{}
	for _, row := range rows[1:] {
		if pi >= len(row) || ci >= len(row) {
			continue
		}
		if v, err := strconv.ParseFloat(row[ci], 64); err == nil {
			out[row[pi]] = v
		}
	}
	return out
}

func t92Cand(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func t92Diff(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out
}

func writeT92CSV(dir string, res []*t92Result, radii, details []float64) {
	f, err := os.Create(filepath.Join(dir, "photos.csv"))
	if err != nil {
		return
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	defer cw.Flush()
	hdr := []string{"photo", "body", "iso", "held_base", "unsharp"}
	for _, rad := range radii {
		for _, d := range details {
			hdr = append(hdr, fmt.Sprintf("r%.2f_d%+.2f", rad, d))
		}
	}
	hdr = append(hdr, "edge_radius", "edge_ours_width", "edge_ours_over", "edge_ours_profile", "edge_spp_width", "edge_spp_over", "edge_spp_profile", "edge_n", "a0", "scale", "dog_loo", "dog_lobo", "dog_loo_sigma", "dog_loo_lambda", "dog_lobo_sigma", "dog_lobo_lambda")
	cw.Write(hdr)
	for _, r := range res {
		row := []string{r.p.name, r.p.body, fmt.Sprintf("%.0f", r.p.iso), fmt.Sprintf("%.3f", r.heldBase), fmt.Sprintf("%.3f", r.unsharp)}
		for _, e := range r.grid {
			row = append(row, fmt.Sprintf("%.3f", e))
		}
		row = append(row, fmt.Sprintf("%.2f", radii[t92Index(radii, 1.0)]))
		// The edge columns are the current 1.00 row; the per-radius rows are
		// in tables.md.
		var st edgeStat
		if len(r.edge) > 0 {
			st = r.edge[t92Index(radii, 1.0)]
		}
		row = append(row, fmt.Sprintf("%.2f", st.Width), fmt.Sprintf("%.2f", 100*st.Overshoot), fmt.Sprintf("%.4f", st.ProfileAligned),
			fmt.Sprintf("%.2f", r.sppEdge.Width), fmt.Sprintf("%.2f", 100*r.sppEdge.Overshoot), fmt.Sprintf("%.4f", r.sppEdge.ProfileAligned), fmt.Sprintf("%d", st.N),
			fmt.Sprintf("%.4f", r.a0), fmt.Sprintf("%.3f", r.s), fmt.Sprintf("%.3f", r.dogErr[0]), fmt.Sprintf("%.3f", r.dogErr[1]),
			fmt.Sprintf("%.2f", r.dogShape[0].Sigma1), fmt.Sprintf("%.2f", r.dogShape[0].Lambda),
			fmt.Sprintf("%.2f", r.dogShape[1].Sigma1), fmt.Sprintf("%.2f", r.dogShape[1].Lambda))
		cw.Write(row)
	}
}
