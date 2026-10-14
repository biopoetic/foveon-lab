package calib

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// TransferReport: can a body with few or no SPP pairs (Quattro, SD9/SD10)
// borrow another body's base? For each photo of the target group, dE2000 vs
// SPP of: the camera JPEG (today's start), the target's own full base (held
// out), and the source group's taste and full base (fitted on all of the
// source, which never saw the target).
func TransferReport(groups map[string][]RawPair, from, to string, w io.Writer) {
	src, dst := groups[from], groups[to]
	if len(src) < 3 || len(dst) == 0 {
		fmt.Fprintf(w, "transfer %s -> %s: not enough photos (%d, %d)\n", from, to, len(src), len(dst))
		return
	}
	st := fitTaste(src)
	sf := fitRaw(src)
	js, _ := json.Marshal(TasteJSON(src))
	fmt.Fprintf(w, "TASTE %s\n", js)
	fmt.Fprintf(w, "transfer %s -> %s, dE2000 mean vs SPP (taste sat %.2f):\n", from, to, st.Sat)
	fmt.Fprintf(w, "  %-28s %8s %8s %8s %8s %8s\n", "photo", "JPEG", "camera", "own", from+" t", from+" f")
	var s [5]float64
	var nj, n int
	for h, p := range dst {
		if p.TrainOnly {
			continue
		}
		var train []RawPair
		for i := range dst {
			if i != h {
				train = append(train, dst[i])
			}
		}
		own := -1.0
		if len(train) >= 2 {
			oc := fitRaw(train)
			own, _ = DeltaEStats(applied(&oc, p.Raw), p.SPP)
		}
		jp := -1.0
		if p.JPEG != nil {
			jp, _ = DeltaEStats(p.JPEG, p.SPP)
			s[0] += jp
			nj++
		}
		cam, _ := DeltaEStats(p.Raw, p.SPP)
		t, _ := DeltaEStats(st.applied(p.Raw), p.SPP)
		f, _ := DeltaEStats(applied(&sf, p.Raw), p.SPP)
		s[1], s[2], s[3], s[4] = s[1]+cam, s[2]+own, s[3]+t, s[4]+f
		n++
		fmt.Fprintf(w, "  %-28s %8.2f %8.2f %8.2f %8.2f %8.2f\n", p.Name, jp, cam, own, t, f)
	}
	fmt.Fprintf(w, "  %-28s %8.2f %8.2f %8.2f %8.2f %8.2f   (%d photos, %d with JPEG)\n", "MEAN", s[0]/float64(max(1, nj)), s[1]/float64(n), s[2]/float64(n), s[3]/float64(n), s[4]/float64(n), n, nj)
}

// TasteJSON is the taste learnt on all of a group's photos, as the
// render.RawCorrection fields (tone, sat) for render/rawbase.json.
func TasteJSON(pairs []RawPair) map[string]any {
	t := fitTaste(pairs)
	tone := make([]float64, len(t.Tone))
	for i, v := range t.Tone {
		tone[i] = math.Round(v*1e4) / 1e4
	}
	return map[string]any{"tone": tone, "sat": math.Round(t.Sat*1e4) / 1e4}
}
