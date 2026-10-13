package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/render"
)

// camfWBName maps the X3F's WB_DESC to the illuminant name CAMF uses.
var camfWBName = map[string]string{
	"Daylight": "Sunlight", "Sunlight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast",
	"Incandescent": "Incandescent", "Fluorescent": "Fluorescent", "Flash": "Flash",
	"Auto": "Auto", "Custom": "Custom",
}

// camfTransform reads the photo's white-balance gains and colour matrix
// from its CAMF block (via x3f_extract -meta), for its in-camera white
// balance — what Sigma Photo Pro applies to the raw data.
func camfTransform(ph *Photo) (render.CamTransform, error) {
	var t render.CamTransform
	wb := camfWBName[strings.TrimSpace(ph.props["WB_DESC"])]
	if wb == "" {
		wb = "Auto"
	}
	m, err := camfMatrices(ph.path)
	if err != nil {
		return t, err
	}
	g, cc := m[wb+"WBGain"], m[wb+"CCMatrix"]
	if len(g) != 3 || len(cc) != 9 {
		return t, fmt.Errorf("%s: no %s white balance in CAMF", ph.Name, wb)
	}
	copy(t.Gain[:], g)
	copy(t.CC[:], cc)
	return t, nil
}

// camfMatrices returns every numeric CAMF matrix of an X3F by name.
func camfMatrices(path string) (map[string][]float64, error) {
	if rawTool == "" {
		return nil, fmt.Errorf("no x3f_extract")
	}
	tmp, err := os.MkdirTemp("", "foveon-meta-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, rawTool, "-q", "-meta", "-o", tmp, path).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("x3f_extract -meta: %v %s", err, trimOutput(out))
	}
	data, err := os.ReadFile(filepath.Join(tmp, filepath.Base(path)+".meta"))
	if err != nil {
		return nil, err
	}
	return parseCamfMeta(data), nil
}

// parseCamfMeta reads the "BEGIN: CAMF matrix meta data (Name)" blocks of
// an x3f_extract -meta dump.
func parseCamfMeta(data []byte) map[string][]float64 {
	out := map[string][]float64{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	name := ""
	var vals []float64
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "BEGIN: CAMF matrix meta data ("):
			name = strings.TrimSuffix(strings.TrimPrefix(line, "BEGIN: CAMF matrix meta data ("), ")")
			vals = nil
		case strings.HasPrefix(line, "END: CAMF matrix"):
			if name != "" {
				out[name] = vals
			}
			name = ""
		case name != "":
			if strings.HasPrefix(line, "x:") || strings.HasPrefix(line, "y:") || strings.HasPrefix(line, "z:") || strings.Contains(line, "[") {
				continue
			}
			for _, f := range strings.Fields(line) {
				if v, err := strconv.ParseFloat(f, 64); err == nil {
					vals = append(vals, v)
				}
			}
		}
	}
	return out
}
