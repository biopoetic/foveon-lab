package calib

import (
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// exportExts are the formats an SPP export may be kept in: SPP writes TIFF,
// and tools/compress-exports turns them into lossless PNG (half the size).
var exportExts = []string{".tif", ".png"}

// ExportFile returns the path of photo name's export for step code, in
// whichever format it is kept ("" if there is none).
func ExportFile(dir, name, code string) string {
	for _, ext := range exportExts {
		p := filepath.Join(dir, ExportsDir, name+"_"+code+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ExportsAnywhere finds step code's exports in every subfolder of the
// calibration folder dir, in either format, by upper-case photo name. A
// name found in several folders keeps the one in ExportsDir. The tools that
// score against SPP (spp-check and its relatives) used to look for "*.tif"
// only, which a compressed export folder would have emptied.
func ExportsAnywhere(dir, code string) map[string]string {
	out := map[string]string{}
	for _, ext := range exportExts {
		suffix := "_" + code + ext
		m, _ := filepath.Glob(filepath.Join(dir, "*", "*"+suffix))
		for _, p := range m {
			name := strings.ToUpper(strings.TrimSuffix(filepath.Base(p), suffix))
			if _, ok := out[name]; !ok || filepath.Base(filepath.Dir(p)) == ExportsDir {
				out[name] = p
			}
		}
	}
	return out
}

// ExportsFor lists every exported step code's files, as path → photo name.
func ExportsFor(dir, code string) (paths, names []string) {
	seen := map[string]bool{}
	for _, ext := range exportExts {
		m, _ := filepath.Glob(filepath.Join(dir, ExportsDir, "*_"+code+ext))
		for _, p := range m {
			n := strings.TrimSuffix(filepath.Base(p), "_"+code+ext)
			if !seen[strings.ToUpper(n)] {
				seen[strings.ToUpper(n)] = true
				paths, names = append(paths, p), append(names, n)
			}
		}
	}
	idx := make([]int, len(paths))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return names[idx[a]] < names[idx[b]] })
	ps, ns := make([]string, len(idx)), make([]string, len(idx))
	for i, j := range idx {
		ps[i], ns[i] = paths[j], names[j]
	}
	return ps, ns
}
