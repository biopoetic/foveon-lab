package main

// field-study (task 23, part 1): measure the spatial colour field of the
// older bodies against SPP and report how much of it repeats across photos.
// It uses the app's own linear preview (raw base applied, before rendering)
// and SPP's neutral A00 export, both at the analysis width, so it measures
// the same image spp-check scores.
//
//	foveon-lab field-study [-photos "..."] [-dir ...] [-groups SD14,SD15,SD9,SD10,DP] [-csv out.csv]

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/x3f"
)

// sampleSpatialGain stores log(CAMF SpatialGain) on the field grid, both in
// display coordinates (for the comparison with G) and on a sensor-fixed grid
// (for comparing the files' tables with each other).
func sampleSpatialGain(ph *Photo, ourW, ourH int, s *calib.FieldSample) {
	f, err := x3f.Open(ph.path)
	if err != nil {
		return
	}
	c, err := f.CAMF()
	if err != nil {
		return
	}
	wb := developWB(c, ph)
	sg := x3f.NewSpatialGain(c, f.Props, wb)
	if sg == nil {
		return
	}
	s.SGPresent = true
	name := c.Props["SpatialGainTables"][wb]
	if name == "" {
		name = "SpatialGain"
	}
	if d := c.Dims[name]; len(d) == 3 {
		s.SGDims = fmt.Sprintf("%dx%dx%d", d[0], d[1], d[2])
	}
	rows, cols := ourH, ourW
	if ph.Rotation == 90 || ph.Rotation == 270 {
		rows, cols = ourW, ourH
	}
	n := calib.FieldGW * calib.FieldGH
	for ch := 0; ch < 3; ch++ {
		s.SG[ch] = make([]float64, n)
		s.SGSensor[ch] = make([]float64, n)
	}
	for j := 0; j < calib.FieldGH; j++ {
		for i := 0; i < calib.FieldGW; i++ {
			idx := j*calib.FieldGW + i
			ud := -1 + 2*(float64(i)+0.5)/calib.FieldGW
			vd := -1 + 2*(float64(j)+0.5)/calib.FieldGH
			us, vs := ud, vd
			switch ph.Rotation {
			case 90:
				us, vs = vd, -ud
			case 180:
				us, vs = -ud, -vd
			case 270:
				us, vs = -vd, ud
			}
			row := int(math.Round((vs + 1) / 2 * float64(rows-1)))
			col := int(math.Round((us + 1) / 2 * float64(cols-1)))
			// the sensor-fixed grid
			su := -1 + 2*(float64(i)+0.5)/calib.FieldGW
			sv := -1 + 2*(float64(j)+0.5)/calib.FieldGH
			srow := int(math.Round((sv + 1) / 2 * float64(rows-1)))
			scol := int(math.Round((su + 1) / 2 * float64(cols-1)))
			for ch := 0; ch < 3; ch++ {
				s.SG[ch][idx] = math.Log(sg.At(row, col, ch, rows, cols))
				s.SGSensor[ch][idx] = math.Log(sg.At(srow, scol, ch, rows, cols))
			}
		}
	}
}

func fieldStudyCommand(desk string) {
	fs := flag.NewFlagSet("field-study", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports in its exports\\ and other subfolders)")
	photos := fs.String("photos", desk, "folders with the X3F files (separated by ;)")
	groupsFlag := fs.String("groups", "SD14,SD15,SD9,SD10,DP", "camera groups to measure")
	csvOut := fs.String("csv", "", "write the per-photo surface coefficients to this CSV")
	fs.Parse(os.Args[2:])

	want := map[string]bool{}
	for _, g := range strings.Split(*groupsFlag, ",") {
		want[strings.TrimSpace(g)] = true
	}
	exports, names := calib.ExportsFor(*dir, "A00")
	byName := map[string]*Photo{}
	for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
		if p.Kind != "x3f" {
			continue
		}
		key := strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)))
		if _, ok := byName[key]; !ok {
			byName[key] = p
		}
	}
	var samples []*calib.FieldSample
	for i, e := range exports {
		name := names[i]
		ph := byName[strings.ToUpper(name)]
		if ph == nil {
			continue
		}
		group := calibGroup(ph)
		if !want[group] {
			continue
		}
		if _, bad := calib.ExcludedReference(name, group); bad {
			continue
		}
		spp, err := calib.LoadLinear(e)
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			continue
		}
		our, err := linearForCheck(ph, spp)
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			continue
		}
		if our.W != spp.W || our.H != spp.H {
			fmt.Printf("  %s: %dx%d against SPP's %dx%d, skipped\n", name, our.W, our.H, spp.W, spp.H)
			continue
		}
		s := calib.MeasureField(our, spp)
		s.Name, s.Group, s.Rotation = name, group, ph.Rotation
		s.Lens = ph.props["LENSMODEL"]
		s.Focal = ph.props["FLENGTH"]
		s.Aperture = ph.props["AP_DESC"]
		s.WB = ph.props["WB_DESC"]
		sampleSpatialGain(ph, our.W, our.H, s)
		samples = append(samples, s)
		fmt.Printf("  measured %-26s %-6s rot %d lens %-4s f%-4s ap %-4s\n", name, group, ph.Rotation, s.Lens, s.Focal, s.Aperture)
	}
	sort.Slice(samples, func(a, b int) bool {
		if samples[a].Group != samples[b].Group {
			return samples[a].Group < samples[b].Group
		}
		return samples[a].Name < samples[b].Name
	})
	calib.FieldReport(samples, os.Stdout)

	if *csvOut != "" {
		f, err := os.Create(*csvOut)
		if err != nil {
			fmt.Println(err)
			return
		}
		w := csv.NewWriter(f)
		head := []string{"group", "photo", "rotation", "lens", "focal", "aperture", "wb"}
		for _, ch := range []string{"r", "g", "b"} {
			head = append(head, "mean_"+ch, "std_"+ch, "ownR2_"+ch)
			for _, t := range []string{"c0", "u", "v", "r2"} {
				head = append(head, "coef_"+ch+"_"+t)
			}
			for _, t := range []string{"c0", "u", "v", "r2"} {
				head = append(head, "sensor_"+ch+"_"+t)
			}
		}
		w.Write(head)
		for _, s := range samples {
			row := []string{s.Group, s.Name, strconv.Itoa(s.Rotation), s.Lens, s.Focal, s.Aperture, s.WB}
			for c := 0; c < 3; c++ {
				row = append(row, fmt.Sprintf("%.6f", s.Mean[c]), fmt.Sprintf("%.6f", s.SpatialStd[c]), fmt.Sprintf("%.4f", s.OwnR2[c]))
				for t := 0; t < 4; t++ {
					row = append(row, fmt.Sprintf("%.6f", s.Coef[c][t]))
				}
				for t := 0; t < 4; t++ {
					row = append(row, fmt.Sprintf("%.6f", s.CoefSensor[c][t]))
				}
			}
			w.Write(row)
		}
		w.Flush()
		fmt.Println("saved", *csvOut)
	}
}
