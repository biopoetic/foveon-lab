package main

// The driver of pixmap-study: see pixmap.go for the model and the measures.

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type pmRow struct {
	Group      string  `json:"group"`
	N          int     `json:"n"`
	Params     int     `json:"params"`
	Alpha      float64 `json:"alpha"`
	Beta       float64 `json:"beta"`
	Kind       string  `json:"kind"`
	Score      float64 `json:"score"`
	Photos     int     `json:"photos"`
	Base       float64 `json:"base"`
	BaseSD     float64 `json:"baseSD"`
	Iterations int     `json:"iterations"`
}

type pmPhotoRow struct {
	Group     string  `json:"group"`
	Name      string  `json:"name"`
	Series    string  `json:"series"`
	N         int     `json:"n"`
	Alpha     float64 `json:"alpha"`
	Beta      float64 `json:"beta"`
	Base      float64 `json:"base"`
	HeldOut   float64 `json:"heldout"`
	SeriesOut float64 `json:"seriesout"`
	Oracle    float64 `json:"oracle"`
	Train     float64 `json:"train"`
}

func pmBaseScore(s *pmSample) float64 {
	var e float64
	for i := 0; i < s.n(); i++ {
		for c := 0; c < 3; c++ {
			e += math.Abs(float64(s.Base[i*3+c])/255 - float64(s.Targ[i*3+c])/65535)
		}
	}
	return e / float64(3*s.n()) * 255
}

// pmSeriesLabels groups frames of one shoot: valid capture clocks within gap
// seconds of each other. Photos with an unset clock (around 2001-01-01) are
// singletons, as are isolated frames.
func pmSeriesLabels(ss []*pmSample, gap int64) []string {
	const epoch2001 = 978307200
	valid := func(t int64) bool { return t > epoch2001+90*86400 }
	order := make([]int, len(ss))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if ss[order[a]].Time != ss[order[b]].Time {
			return ss[order[a]].Time < ss[order[b]].Time
		}
		return ss[order[a]].Name < ss[order[b]].Name
	})
	labels := make([]string, len(ss))
	i := 0
	for i < len(order) {
		if !valid(ss[order[i]].Time) {
			labels[order[i]] = ss[order[i]].Name
			i++
			continue
		}
		j := i + 1
		for j < len(order) && valid(ss[order[j]].Time) &&
			ss[order[j]].Time-ss[order[j-1]].Time <= gap {
			j++
		}
		if j-i == 1 {
			labels[order[i]] = ss[order[i]].Name
		} else {
			lab := "S:" + ss[order[i]].Name
			for k := i; k < j; k++ {
				labels[order[k]] = lab
			}
		}
		i = j
	}
	return labels
}

func pmParseSizes(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err == nil && v >= 2 && v <= 14 {
			out = append(out, v)
		}
	}
	return out
}

func pmParseFloats(s string) []float64 {
	var out []float64
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err == nil {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = []float64{0}
	}
	return out
}

func pixmapStudyCommand(desk string) {
	fs := flag.NewFlagSet("pixmap-study", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports in its exports\\ and other subfolders)")
	photos := fs.String("photos", desk, "folders with the X3F files (separated by ;)")
	groupsFlag := fs.String("groups", "SD14,SD9,SD10,SD15", "camera groups to study")
	sizesFlag := fs.String("sizes", "2,3,4,5,6,7,8", "grid nodes per axis, coarse to fine")
	alphaFlag := fs.String("alpha", "0.3", "ridge strengths (times the mean data diagonal)")
	betaFlag := fs.String("beta", "1", "smoothness strengths (times the mean data diagonal)")
	stride := fs.Int("stride", pmSampleStride, "pixel stride of the study samples")
	cache := fs.String("cache", filepath.Join("zz-out", "task25", "samples"), "sample cache folder (empty: none)")
	csvOut := fs.String("csv", filepath.Join("zz-out", "task25", "pixmap.csv"), "summary CSV")
	perOut := fs.String("perphoto", filepath.Join("zz-out", "task25", "pixmap-photos.csv"), "per-photo CSV")
	jsonOut := fs.String("json", filepath.Join("zz-out", "task25", "pixmap.json"), "summary JSON")
	gapSec := fs.Int64("seriesgap", 1800, "frames of one shoot: capture-clock gap in seconds")
	fs.Parse(os.Args[2:])

	only := map[string]bool{}
	for _, g := range strings.Split(*groupsFlag, ",") {
		only[strings.TrimSpace(g)] = true
	}
	samples := pmLoad(*dir, *photos, only, *stride, *cache)
	var groups []string
	for g := range samples {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	sizes := pmParseSizes(*sizesFlag)
	alphas := pmParseFloats(*alphaFlag)
	betas := pmParseFloats(*betaFlag)

	var rows []pmRow
	var photorows []pmPhotoRow
	for _, g := range groups {
		ss := samples[g]
		if len(ss) < 3 {
			fmt.Printf("%s: %d photos, not enough\n", g, len(ss))
			continue
		}
		L0, L1, A0, A1, B0, B1 := pmBox(ss)
		base := make([]float64, len(ss))
		var sum, sum2 float64
		for i := range ss {
			base[i] = pmBaseScore(ss[i])
			sum += base[i]
			sum2 += base[i] * base[i]
		}
		baseMean := sum / float64(len(ss))
		baseSD := math.Sqrt(math.Max(0, sum2/float64(len(ss))-baseMean*baseMean))
		series := pmSeriesLabels(ss, *gapSec)
		seriesN := map[string]int{}
		for i := range series {
			seriesN[series[i]]++
		}
		nmulti, nsingle := 0, 0
		for _, n := range seriesN {
			if n > 1 {
				nmulti++
			} else {
				nsingle++
			}
		}
		fmt.Printf("\n%s: %d photos, %d in %d multi-frame series, base %.3f (sd %.3f)\n",
			g, len(ss), len(ss)-nsingle, nmulti, baseMean, baseSD)
		out := &strings.Builder{}
		fmt.Fprintf(out, "%-5s %-6s %-7s %-10s %7s %7s %7s\n", "N", "params", "alpha", "beta", "train", "heldout", "oracle")
		for _, N := range sizes {
			grid := pmNewGrid(N, L0, L1, A0, A1, B0, B1)
			// One IRLS reweighting step for the whole grid: fit once with
			// equal weights, then let each sample count as 1/|residual| so
			// the least-squares fit follows the mean absolute error the
			// score is made of instead of chasing squared outliers.  The
			// weights do not depend on alpha/beta, so every strength below
			// uses the same robust weighting.
			raw := make([]*pmCells, len(ss))
			for i := range ss {
				c := pmNewCells(N)
				grid.pmAccumulate(c, ss[i], nil)
				raw[i] = c
			}
			m0 := pmAssemble(raw, N)
			r0 := pmRHS(raw, N)
			refL1 := alphas[0] * m0.diagMean
			refL2 := betas[0] * m0.diagMean * math.Pow(float64(N-1), 4)
			th0, _ := pmSolve(m0, nil, 0, N, refL1, refL2, r0, nil)
			weights := make([][]float32, len(ss))
			for i := range ss {
				weights[i] = pmWeights(th0, grid, ss[i])
			}
			cells := make([]*pmCells, len(ss))
			for i := range ss {
				c := pmNewCells(N)
				grid.pmAccumulate(c, ss[i], weights[i])
				cells[i] = c
			}
			mat := pmAssemble(cells, N)
			rhs := pmRHS(cells, N)
			cellRHS := make([][]float64, len(ss))
			for i := range ss {
				cellRHS[i] = pmRHS([]*pmCells{cells[i]}, N)
			}
			// Series folds: hold the whole shoot out at once.
			seriesIdx := map[string][]int{}
			for i := range ss {
				seriesIdx[series[i]] = append(seriesIdx[series[i]], i)
			}
			for _, alpha := range alphas {
				for _, beta := range betas {
					l1 := alpha * mat.diagMean
					l2 := beta * mat.diagMean * math.Pow(float64(N-1), 4)
					theta, it := pmSolve(mat, nil, 0, N, l1, l2, rhs, nil)
					train := 0.0
					for i := range ss {
						train += pmScore(theta, grid, ss[i])
					}
					train /= float64(len(ss))
					held := make([]float64, len(ss))
					oracle := make([]float64, len(ss))
					for i := range ss {
						r := make([]float64, 3*N*N*N)
						for c := 0; c < 3; c++ {
							copy(r[c*N*N*N:(c+1)*N*N*N], rhs[c*N*N*N:(c+1)*N*N*N])
						}
						for c := 0; c < 3; c++ {
							for k := 0; k < N*N*N; k++ {
								r[c*N*N*N+k] -= cellRHS[i][c*N*N*N+k]
							}
						}
						th, _ := pmSolve(mat, cells[i], -1, N, l1, l2, r, theta)
						held[i] = pmScore(th, grid, ss[i])
						tho, _ := pmSolve(nil, cells[i], 1, N, l1, l2, cellRHS[i], theta)
						oracle[i] = pmScore(tho, grid, ss[i])
					}
					// Held-out series: one fit without the whole shoot.
					seriesHeld := make([]float64, len(ss))
					seriesOK := make([]bool, len(ss))
					for i := range ss {
						if seriesN[series[i]] == 1 {
							seriesHeld[i], seriesOK[i] = held[i], true
						}
					}
					for _, idxs := range seriesIdx {
						if len(idxs) < 2 {
							continue
						}
						if len(ss)-len(idxs) < 3 {
							continue
						}
						comb := pmNewCells(N)
						for _, i := range idxs {
							comb.add(cells[i])
						}
						sr := make([]float64, 3*N*N*N)
						for c := 0; c < 3; c++ {
							copy(sr[c*N*N*N:(c+1)*N*N*N], rhs[c*N*N*N:(c+1)*N*N*N])
						}
						for c := 0; c < 3; c++ {
							for k := 0; k < N*N*N; k++ {
								sr[c*N*N*N+k] -= cellRHS[idxs[0]][c*N*N*N+k]
							}
						}
						// subtract every other frame's cells too
						for _, i := range idxs[1:] {
							for c := 0; c < 3; c++ {
								for k := 0; k < N*N*N; k++ {
									sr[c*N*N*N+k] -= cellRHS[i][c*N*N*N+k]
								}
							}
						}
						ths, _ := pmSolve(mat, comb, -1, N, l1, l2, sr, theta)
						for _, i := range idxs {
							seriesHeld[i] = pmScore(ths, grid, ss[i])
							seriesOK[i] = true
						}
					}
					var heldMean, oracleMean, seriesMean float64
					var nSeries int
					for i := range ss {
						heldMean += held[i]
						oracleMean += oracle[i]
						if seriesOK[i] {
							seriesMean += seriesHeld[i]
							nSeries++
						}
					}
					n := float64(len(ss))
					heldMean /= n
					oracleMean /= n
					if nSeries > 0 {
						seriesMean /= float64(nSeries)
					} else {
						seriesMean = math.NaN()
					}
					fmt.Fprintf(out, "%-5d %-6d %-7.2g %-10.2g %7.3f %7.3f %7.3f  it %d\n",
						N, 3*N*N*N, alpha, beta, train, heldMean, oracleMean, it)
					rows = append(rows,
						pmRow{Group: g, N: N, Params: 3 * N * N * N, Alpha: alpha, Beta: beta, Kind: "train", Score: train, Photos: len(ss), Base: baseMean, BaseSD: baseSD, Iterations: it},
						pmRow{Group: g, N: N, Params: 3 * N * N * N, Alpha: alpha, Beta: beta, Kind: "heldout_photo", Score: heldMean, Photos: len(ss), Base: baseMean, BaseSD: baseSD, Iterations: it},
						pmRow{Group: g, N: N, Params: 3 * N * N * N, Alpha: alpha, Beta: beta, Kind: "heldout_series", Score: seriesMean, Photos: len(ss), Base: baseMean, BaseSD: baseSD, Iterations: it},
						pmRow{Group: g, N: N, Params: 3 * N * N * N, Alpha: alpha, Beta: beta, Kind: "oracle", Score: oracleMean, Photos: len(ss), Base: baseMean, BaseSD: baseSD, Iterations: it},
					)
					for i := range ss {
						so := held[i]
						if seriesOK[i] {
							so = seriesHeld[i]
						}
						photorows = append(photorows, pmPhotoRow{Group: g, Name: ss[i].Name, Series: series[i], N: N,
							Alpha: alpha, Beta: beta, Base: base[i], HeldOut: held[i], SeriesOut: so,
							Oracle: oracle[i], Train: pmScore(theta, grid, ss[i])})
					}
				}
			}
		}
		fmt.Print(out.String())
		fmt.Printf("  base: mean %.3f sd %.3f min %.2f max %.2f\n", baseMean, baseSD, minF(base), maxF(base))
	}

	if *csvOut != "" {
		os.MkdirAll(filepath.Dir(*csvOut), 0o755)
		if f, err := os.Create(*csvOut); err == nil {
			w := csv.NewWriter(f)
			w.Write([]string{"group", "n", "params", "alpha", "beta", "kind", "score", "photos", "base", "base_sd", "iters"})
			for _, r := range rows {
				w.Write([]string{r.Group, strconv.Itoa(r.N), strconv.Itoa(r.Params),
					fmt.Sprintf("%.4g", r.Alpha), fmt.Sprintf("%.4g", r.Beta), r.Kind,
					fmt.Sprintf("%.4f", r.Score), strconv.Itoa(r.Photos),
					fmt.Sprintf("%.4f", r.Base), fmt.Sprintf("%.4f", r.BaseSD), strconv.Itoa(r.Iterations)})
			}
			w.Flush()
			f.Close()
			fmt.Println("saved", *csvOut)
		}
	}
	if *perOut != "" {
		os.MkdirAll(filepath.Dir(*perOut), 0o755)
		if f, err := os.Create(*perOut); err == nil {
			w := csv.NewWriter(f)
			w.Write([]string{"group", "photo", "series", "n", "alpha", "beta", "base", "heldout", "seriesout", "oracle", "train"})
			for _, r := range photorows {
				w.Write([]string{r.Group, r.Name, r.Series, strconv.Itoa(r.N),
					fmt.Sprintf("%.4g", r.Alpha), fmt.Sprintf("%.4g", r.Beta),
					fmt.Sprintf("%.4f", r.Base), fmt.Sprintf("%.4f", r.HeldOut),
					fmt.Sprintf("%.4f", r.SeriesOut), fmt.Sprintf("%.4f", r.Oracle), fmt.Sprintf("%.4f", r.Train)})
			}
			w.Flush()
			f.Close()
			fmt.Println("saved", *perOut)
		}
	}
	if *jsonOut != "" {
		os.MkdirAll(filepath.Dir(*jsonOut), 0o755)
		b, _ := json.MarshalIndent(struct {
			Rows   []pmRow      `json:"rows"`
			Photos []pmPhotoRow `json:"photos"`
		}{rows, photorows}, "", " ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err == nil {
			fmt.Println("saved", *jsonOut)
		}
	}
}

func minF(v []float64) float64 {
	m := math.Inf(1)
	for _, x := range v {
		m = math.Min(m, x)
	}
	return m
}

func maxF(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		m = math.Max(m, x)
	}
	return m
}
