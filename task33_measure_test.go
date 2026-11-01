package main

// Task 33 measurement harness. Manual only: set FOVEON_T33_DIR to a folder
// of at least 100 X3F files and run TestTask33. It times the three first-time
// waits (folder thumbnails, a cold next photo, the preset grid) and breaks
// each into file read, X3F parse, JPEG decode, resize, render and encode.
//
// Redirect the caches with FOVEON_META_CACHE and LOCALAPPDATA so a run can be
// made cold without touching the real configuration.

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

func t33sum(st []t33stage, name string) time.Duration {
	var d time.Duration
	for _, s := range st {
		if s.name == name {
			d += s.d
		}
	}
	return d
}

func t33photos(t *testing.T, dir string) []*Photo {
	t.Helper()
	photos := scanPhotos([]string{dir}, false)
	var out []*Photo
	for _, p := range photos {
		if p.Kind == "x3f" && p.Unsupported == "" {
			out = append(out, p)
		}
	}
	if len(out) < 100 {
		t.Fatalf("%s has %d usable X3F files, want 100+", dir, len(out))
	}
	return out
}

// t33preview reads an embedded preview the way decodePreview does, timing the
// container parse, the JPEG byte read and the decode apart.
func t33preview(t *testing.T, ph *Photo) (image.Image, []t33stage) {
	t.Helper()
	var st []t33stage
	t0 := time.Now()
	f, err := x3f.Open(ph.path)
	if err != nil {
		t.Logf("%s: %v", ph.Name, err)
		return nil, st
	}
	st = append(st, t33stage{"x3f parse", time.Since(t0)})
	t0 = time.Now()
	data, _, err := f.PreviewJPEG()
	if err != nil {
		st = append(st, t33stage{"file read", 0}, t33stage{"jpeg decode", 0})
		t.Logf("%s: %v", ph.Name, err)
		return nil, st
	}
	st = append(st, t33stage{"file read", time.Since(t0)})
	t0 = time.Now()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Logf("%s: %v", ph.Name, err)
		return nil, st
	}
	st = append(st, t33stage{"jpeg decode", time.Since(t0)})
	return img, st
}

func TestTask33(t *testing.T) {
	dir := os.Getenv("FOVEON_T33_DIR")
	if dir == "" {
		t.Skip("FOVEON_T33_DIR not set")
	}
	photos := t33photos(t, dir)
	t.Logf("photos: %d in %s", len(photos), dir)

	t.Run("thumbnails", func(t *testing.T) {
		t33clearThumbCache()
		s := &server{thumbs: newLRU(4000), byID: map[string]*Photo{}}
		for _, p := range photos {
			s.byID[p.ID] = p
		}
		type row struct {
			name string
			d    time.Duration
			kind string
		}
		var total, small, full time.Duration
		var nsmall, nfull int
		var rows []row
		for _, p := range photos {
			f, ferr := x3f.Open(p.path)
			kind := "small thumb"
			if ferr == nil {
				if _, err := f.ThumbJPEG(); err != nil {
					kind = "full preview"
				}
			}
			r := httptest.NewRequest("GET", "/api/thumb?id="+p.ID, nil)
			w := httptest.NewRecorder()
			t0 := time.Now()
			s.handleThumb(w, r)
			d := time.Since(t0)
			if w.Code != 200 {
				t.Fatalf("%s: %d %s", p.Name, w.Code, w.Body.String())
			}
			total += d
			if kind == "full preview" {
				full += d
				nfull++
			} else {
				small += d
				nsmall++
			}
			rows = append(rows, row{p.Name, d, kind})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].d > rows[j].d })
		fmt.Printf("THUMBS serial total %s: small %s (%d), full-preview %s (%d)\n",
			total.Round(time.Millisecond), small.Round(time.Millisecond), nsmall,
			full.Round(time.Millisecond), nfull)
		for i := 0; i < min(8, len(rows)); i++ {
			fmt.Printf("  slow %-40s %-12s %s\n", rows[i].name, rows[i].kind, rows[i].d.Round(time.Millisecond))
		}
	})

	t.Run("cold-next-photo", func(t *testing.T) {
		seen := map[string]bool{}
		for _, p := range photos {
			key := calibGroup(p)
			if seen[key] {
				continue
			}
			seen[key] = true
			// Whole handler with a cold server (the person-visible wait).
			s := &server{sources: newLRU(6), bases: newLRU(40), thumbs: newLRU(8),
				sem: make(chan struct{}, 4), byID: map[string]*Photo{p.ID: p},
				rawState: map[string]string{}, rawBusy: map[string]bool{},
				rawQueue: make(chan *Photo, 8), rawQueueLow: make(chan *Photo, 8)}
			r := httptest.NewRequest("GET", "/api/render?id="+p.ID+"&w=1600&m=jpg&comp=0", nil)
			w := httptest.NewRecorder()
			t0 := time.Now()
			s.handleRender(w, r)
			total := time.Since(t0)
			if w.Code != 200 {
				t.Fatalf("%s: %d %s", p.Name, w.Code, w.Body.String())
			}
			base, rr, enc := t33serverTiming(w)
			// Stage split of the decode itself, on a fresh read.
			img, st := t33preview(t, p)
			if img == nil {
				fmt.Printf("COLD %-22s %-10s total %s | non-JPEG preview, no stage split\n", p.Name, key, total.Round(time.Millisecond))
				continue
			}
			t1 := time.Now()
			l := render.FromImage(img, sourceW, p.Rotation)
			down := time.Since(t1)
			t1 = time.Now()
			b := render.NewBase(l)
			mkbase := time.Since(t1)
			t1 = time.Now()
			render.Render(b, presetNeutral(), render.Compensation{SatFactor: 1})
			rd := time.Since(t1)
			t1 = time.Now()
			var buf bytes.Buffer
			_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16)), &jpeg.Options{Quality: 88})
			_ = time.Since(t1)
			fmt.Printf("COLD %-22s %-10s total %s | parse %s read %s decode %s downscale %s newbase %s render %s | srv base %s render %s encode %s\n",
				p.Name, key, total.Round(time.Millisecond),
				t33sum(st, "x3f parse").Round(time.Millisecond), t33sum(st, "file read").Round(time.Millisecond),
				t33sum(st, "jpeg decode").Round(time.Millisecond), down.Round(time.Millisecond),
				mkbase.Round(time.Millisecond), rd.Round(time.Millisecond),
				base.Round(time.Millisecond), rr.Round(time.Millisecond), enc.Round(time.Millisecond))
		}
	})

	t.Run("thumbdiff", func(t *testing.T) {
		// The groups whose production tile changes: those files have no small
		// embedded JPEG, so the old path decoded the whole preview and the new
		// path uses the camera's EXIF tile. Groups with a small JPEG keep it
		// and are unchanged (difference 0).
		changed := map[string]bool{
			"dp0-Quattro": true, "dp1-Quattro": true, "dp2-Quattro": true,
			"dp3-Quattro": true, "sd-Quattro": true, "sd-Quattro-H": true, "SD14": true,
		}
		seen := map[string]int{}
		var worstMax, over2 int
		var sumMean float64
		var n int
		for _, p := range photos {
			if !changed[p.Folder] || seen[p.Folder] >= 2 {
				continue
			}
			f, err := x3f.Open(p.path)
			if err != nil {
				continue
			}
			if _, err := f.ThumbJPEG(); err == nil {
				continue // production keeps this small JPEG
			}
			b, err := f.EXIFThumbJPEG(700)
			if err != nil {
				continue
			}
			ni, err := jpeg.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			if p.previewW > 0 && p.previewH > 0 {
				ni = cropToAspect(ni, p.previewW, p.previewH)
			}
			oldImg, _ := t33preview(t, p)
			if oldImg == nil {
				continue
			}
			om, err := jpeg.Decode(bytes.NewReader(thumbnailJPEG(oldImg)))
			if err != nil {
				t.Fatal(err)
			}
			nm, err := jpeg.Decode(bytes.NewReader(thumbnailJPEG(ni)))
			if err != nil {
				t.Fatal(err)
			}
			ob, nb := om.Bounds(), nm.Bounds()
			if ob.Dx() != nb.Dx() || ob.Dy() != nb.Dy() {
				t.Fatalf("%s: old %v new %v", p.Name, ob, nb)
			}
			var maxD, sumD int
			for y := 0; y < ob.Dy(); y++ {
				for x := 0; x < ob.Dx(); x++ {
					r1, g1, b1, _ := om.At(ob.Min.X+x, ob.Min.Y+y).RGBA()
					r2, g2, b2, _ := nm.At(nb.Min.X+x, nb.Min.Y+y).RGBA()
					for _, d := range []int{int(r1>>8) - int(r2>>8), int(g1>>8) - int(g2>>8), int(b1>>8) - int(b2>>8)} {
						if d < 0 {
							d = -d
						}
						if d > maxD {
							maxD = d
						}
						sumD += d
					}
				}
			}
			mean := float64(sumD) / float64(ob.Dx()*ob.Dy()*3)
			fmt.Printf("THUMBDIFF %-28s %-12s old/new %dx%d: max %d mean %.3f\n",
				p.Name, p.Folder, ob.Dx(), ob.Dy(), maxD, mean)
			if maxD > worstMax {
				worstMax = maxD
			}
			if maxD > 2 {
				over2++
			}
			sumMean += mean
			n++
			seen[p.Folder]++
			if seen[p.Folder] == 1 {
				var pb bytes.Buffer
				png.Encode(&pb, om)
				os.WriteFile(filepath.Join("zz-out", "task33", "thumb-old-"+p.Name+".png"), pb.Bytes(), 0o644)
				pb.Reset()
				png.Encode(&pb, nm)
				os.WriteFile(filepath.Join("zz-out", "task33", "thumb-new-"+p.Name+".png"), pb.Bytes(), 0o644)
			}
		}
		if n > 0 {
			fmt.Printf("THUMBDIFF summary: %d photos, worst max %d, mean of means %.3f, %d over 2\n",
				n, worstMax, sumMean/float64(n), over2)
		}
	})

	t.Run("click-during-open", func(t *testing.T) {
		t33clearThumbCache()
		// The person opens the first photo while the six thumbnail
		// connections are still pulling the folder. The click's 1600 px view
		// must not wait for the remaining tiles.
		s := &server{thumbs: newLRU(4000), byID: map[string]*Photo{},
			sources: newLRU(6), bases: newLRU(40), sem: make(chan struct{}, 4),
			rawState: map[string]string{}, rawBusy: map[string]bool{},
			rawQueue: make(chan *Photo, 64), rawQueueLow: make(chan *Photo, 64)}
		s.setPhotos([]string{dir}, false)
		list := s.photoList()
		jobs := make(chan *Photo)
		var wg sync.WaitGroup
		for k := 0; k < 6; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range jobs {
					rec := httptest.NewRecorder()
					s.handleThumb(rec, httptest.NewRequest("GET", "/api/thumb?id="+p.ID, nil))
				}
			}()
		}
		go func() {
			for _, p := range list {
				jobs <- p
			}
			close(jobs)
		}()
		time.Sleep(250 * time.Millisecond)
		rec := httptest.NewRecorder()
		t0 := time.Now()
		s.handleRender(rec, httptest.NewRequest("GET", "/api/render?id="+list[0].ID+"&w=1600&m=jpg&comp=0", nil))
		click := time.Since(t0)
		wg.Wait()
		fmt.Printf("CLICK during folder open: first view %s (thumbnails still loading)\n", click.Round(time.Millisecond))
	})

	t.Run("thumb-stages", func(t *testing.T) {
		names := os.Getenv("FOVEON_T33_NAMES")
		if names == "" {
			names = photos[0].Name
		}
		for _, name := range strings.Split(names, ";") {
			var ph *Photo
			for _, p := range photos {
				if strings.EqualFold(p.Name, name) {
					ph = p
				}
			}
			if ph == nil {
				continue
			}
			f, err := x3f.Open(ph.path)
			if err != nil {
				t.Fatal(err)
			}
			t0 := time.Now()
			b, err := f.EXIFThumbJPEG(700)
			if err != nil {
				fmt.Printf("THUMBSTAGE %s: %v\n", name, err)
				continue
			}
			read := time.Since(t0)
			t0 = time.Now()
			img, err := jpeg.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			decode := time.Since(t0)
			t0 = time.Now()
			if ph.previewW > 0 && ph.previewH > 0 {
				img = cropToAspect(img, ph.previewW, ph.previewH)
			}
			crop := time.Since(t0)
			t0 = time.Now()
			out := thumbnailJPEG(img)
			thumb := time.Since(t0)
			fmt.Printf("THUMBSTAGE %-28s exif read %s decode %s crop %s thumbnailJPEG %s bytes %d\n",
				name, read.Round(time.Millisecond), decode.Round(time.Millisecond),
				crop.Round(time.Millisecond), thumb.Round(time.Millisecond), len(out))
		}
	})

	t.Run("first-open", func(t *testing.T) {
		t33clearThumbCache()
		// The page's first click on a folder: scan the headers, list them, then
		// six browser connections pull the 183 list tiles in folder order,
		// while the first photo's 1600 px view is asked for.
		s := &server{thumbs: newLRU(4000), byID: map[string]*Photo{},
			sources: newLRU(6), bases: newLRU(40), sem: make(chan struct{}, 4),
			rawState: map[string]string{}, rawBusy: map[string]bool{},
			rawQueue: make(chan *Photo, 64), rawQueueLow: make(chan *Photo, 64)}
		peak := startMemPeak()
		t0 := time.Now()
		s.setPhotos([]string{dir}, false)
		scan := time.Since(t0)
		list := s.photoList()
		t0 = time.Now()
		w := httptest.NewRecorder()
		s.handlePhotos(w, httptest.NewRequest("GET", "/api/photos", nil))
		listJSON := time.Since(t0)
		// Six connections, in list order: what the browser does with
		// loading="lazy" once the folder is open and scrolled. The first
		// photo's view starts while they are still loading, as it does in the
		// page, so the visit's wall time is the later of the two.
		jobs := make(chan *Photo)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var tileMax time.Duration
		tileStart := time.Now()
		visitStart := tileStart
		for k := 0; k < 6; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range jobs {
					r := httptest.NewRequest("GET", "/api/thumb?id="+p.ID, nil)
					rec := httptest.NewRecorder()
					t1 := time.Now()
					s.handleThumb(rec, r)
					d := time.Since(t1)
					if rec.Code != 200 {
						t.Errorf("%s: %d", p.Name, rec.Code)
					}
					mu.Lock()
					if d > tileMax {
						tileMax = d
					}
					mu.Unlock()
				}
			}()
		}
		go func() {
			for _, p := range list {
				jobs <- p
			}
			close(jobs)
		}()
		time.Sleep(100 * time.Millisecond)
		first := list[0]
		rec := httptest.NewRecorder()
		t0 = time.Now()
		s.handleRender(rec, httptest.NewRequest("GET", "/api/render?id="+first.ID+"&w=1600&m=jpg&comp=0", nil))
		firstView := time.Since(t0)
		wg.Wait()
		thumbs := time.Since(tileStart)
		visitWall := time.Since(visitStart)
		if len(list) > 1 {
			s.prefetchBase(list[1])
		}
		fmt.Printf("FIRSTOPEN %d photos: scan %s, list %s, %d tiles over 6 connections %s (slowest tile %s), first view %s\n",
			len(list), scan.Round(time.Millisecond), listJSON.Round(time.Millisecond), len(list),
			thumbs.Round(time.Millisecond), tileMax.Round(time.Millisecond), firstView.Round(time.Millisecond))
		fmt.Printf("FIRSTOPEN visit total %s (overlapped: tiles %s, first view %s), heap peak %.1f MB\n",
			(scan + listJSON + visitWall).Round(time.Millisecond), thumbs.Round(time.Millisecond),
			firstView.Round(time.Millisecond), float64(peak.stop())/1e6)
	})

	t.Run("cold-next-prefetched", func(t *testing.T) {
		// The page shows A, then asks for B to be prepared while A is still on
		// screen; the step to B then finds the Base in the LRU.
		a, b := photos[0], photos[1]
		s := &server{sources: newLRU(6), bases: newLRU(40), thumbs: newLRU(8),
			sem: make(chan struct{}, 4), byID: map[string]*Photo{a.ID: a, b.ID: b},
			rawState: map[string]string{}, rawBusy: map[string]bool{},
			rawQueue: make(chan *Photo, 8), rawQueueLow: make(chan *Photo, 8)}
		show := func(p *Photo) time.Duration {
			r := httptest.NewRequest("GET", "/api/render?id="+p.ID+"&w=1600&m=jpg&comp=0", nil)
			w := httptest.NewRecorder()
			t0 := time.Now()
			s.handleRender(w, r)
			return time.Since(t0)
		}
		fmt.Printf("PREFETCH show A %s\n", show(a).Round(time.Millisecond))
		t0 := time.Now()
		if _, err := s.prefetchBase(b); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PREFETCH build B in %s; then step B %s\n",
			time.Since(t0).Round(time.Millisecond), show(b).Round(time.Millisecond))
	})

	t.Run("preset-grid", func(t *testing.T) {
		ph := photos[0]
		semCap := 4
		if v, err := strconv.Atoi(os.Getenv("FOVEON_T33_SEM")); err == nil && v > 0 {
			semCap = v
		}
		srv := &server{sources: newLRU(6), bases: newLRU(40), thumbs: newLRU(8),
			sem: make(chan struct{}, semCap), byID: map[string]*Photo{ph.ID: ph},
			pmu: sync.RWMutex{}, pByID: map[string]preset.Preset{},
			rawState: map[string]string{}, rawBusy: map[string]bool{},
			rawQueue: make(chan *Photo, 8), rawQueueLow: make(chan *Photo, 8)}
		ps, _ := preset.Load(preset.Source{Label: "Built-in", FS: preset.Builtin()})
		for i := range ps {
			ps[i].Film = render.FilmFor(ps[i].Name)
		}
		gen := append(append(filmPresets(), cameraModePresets()...), wbPresets()...)
		all := append(append(gen, irPresets()...), ps...)
		for _, p := range all {
			srv.pByID[p.ID] = p
		}
		srv.presets = all
		w := 360                // the default 340 CSS tile at DPR 1, rounded to 40
		ids := []string{"", ""} // Original (src=camera) and Neutral
		for _, p := range all {
			ids = append(ids, p.ID)
		}
		url := func(id string) string {
			u := fmt.Sprintf("/api/render?id=%s&w=%d&comp=0&m=jpg", ph.ID, w)
			if id != "" {
				u += "&preset=" + id
			}
			return u
		}
		// Serial: stage totals.
		var baseS, renderS, encodeS, wallS time.Duration
		type modeStat struct {
			n      int
			base   time.Duration
			render time.Duration
			enc    time.Duration
		}
		modes := map[string]*modeStat{}
		filmID := ""
		for i, id := range ids {
			r := httptest.NewRequest("GET", "/api/render?id="+ph.ID, nil)
			if id == "" && i == 0 {
				r = httptest.NewRequest("GET", "/api/render?id="+ph.ID+"&w=360&comp=0&m=jpg&src=camera", nil)
			} else {
				r = httptest.NewRequest("GET", url(id), nil)
			}
			rec := httptest.NewRecorder()
			t0 := time.Now()
			srv.handleRender(rec, r)
			wallS += time.Since(t0)
			if rec.Code != 200 {
				t.Fatalf("%s: %d %s", id, rec.Code, rec.Body.String())
			}
			b, rr, e := t33serverTiming(rec)
			baseS += b
			renderS += rr
			encodeS += e
			mode := rec.Header().Get("X-Base-Source")
			ms := modes[mode]
			if ms == nil {
				ms = &modeStat{}
				modes[mode] = ms
			}
			ms.n++
			ms.base += b
			ms.render += rr
			ms.enc += e
			if mode == "rawlayers:film" {
				if filmID == "" {
					filmID = id
				}
			}
			if mode == "rawlayers:film" || mode == "rawmaxdetail" || strings.HasPrefix(mode, "rawlayers:ir") {
				fmt.Printf("  tile %-24s %-30s base %6s render %6s enc %5s\n", mode, id,
					b.Round(time.Millisecond), rr.Round(time.Millisecond), e.Round(time.Millisecond))
			}
		}
		// A second render of the same film reuses the prepared film model and
		// the Base: tells a per-pixel cost from a one-time model build.
		if filmID != "" {
			rec := httptest.NewRecorder()
			t0 := time.Now()
			srv.handleRender(rec, httptest.NewRequest("GET", url(filmID), nil))
			_, rr, _ := t33serverTiming(rec)
			fmt.Printf("  repeat film tile %s: wall %s (srv render %s)\n", filmID,
				time.Since(t0).Round(time.Millisecond), rr.Round(time.Millisecond))
		}
		var modeNames []string
		for m := range modes {
			modeNames = append(modeNames, m)
		}
		sort.Slice(modeNames, func(i, j int) bool { return modes[modeNames[i]].base > modes[modeNames[j]].base })
		for _, m := range modeNames {
			ms := modes[m]
			fmt.Printf("  mode %-24s n=%3d base %s render %s encode %s\n", m, ms.n,
				ms.base.Round(time.Millisecond), ms.render.Round(time.Millisecond), ms.enc.Round(time.Millisecond))
		}
		fmt.Printf("GRID serial %d tiles: wall %s | base %s render %s encode %s\n",
			len(ids), wallS.Round(time.Millisecond), baseS.Round(time.Millisecond),
			renderS.Round(time.Millisecond), encodeS.Round(time.Millisecond))

		// Concurrent, the way the page asks for them: a fresh server, six
		// browser connections, the server's own semaphore inside.
		srv2 := &server{sources: newLRU(6), bases: newLRU(40), thumbs: newLRU(8),
			sem: make(chan struct{}, semCap), byID: map[string]*Photo{ph.ID: ph},
			pmu: sync.RWMutex{}, pByID: srv.pByID,
			rawState: map[string]string{}, rawBusy: map[string]bool{},
			rawQueue: make(chan *Photo, 8), rawQueueLow: make(chan *Photo, 8)}
		srv2.presets = all
		jobs := make(chan string)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var lat []time.Duration
		var waitSum, baseSum, renderSum, encSum time.Duration
		t0 := time.Now()
		for k := 0; k < 6; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for id := range jobs {
					u := url(id)
					if id == "" {
						u = "/api/render?id=" + ph.ID + "&w=360&comp=0&m=jpg&src=camera"
					}
					r := httptest.NewRequest("GET", u, nil)
					rec := httptest.NewRecorder()
					t1 := time.Now()
					srv2.handleRender(rec, r)
					mu.Lock()
					lat = append(lat, time.Since(t1))
					waitSum += t33timing(rec, "wait")
					baseSum += t33timing(rec, "base")
					renderSum += t33timing(rec, "render")
					encSum += t33timing(rec, "encode")
					mu.Unlock()
				}
			}()
		}
		for _, id := range ids {
			jobs <- id
		}
		close(jobs)
		wg.Wait()
		wall := time.Since(t0)
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		fmt.Printf("GRID concurrent 6: %d tiles in %s (p50 %s p95 %s max %s) | stages incl. waiting: wait %s base %s render %s encode %s\n",
			len(ids), wall.Round(time.Millisecond), lat[len(lat)/2].Round(time.Millisecond),
			lat[len(lat)*95/100].Round(time.Millisecond), lat[len(lat)-1].Round(time.Millisecond),
			waitSum.Round(time.Millisecond), baseSum.Round(time.Millisecond),
			renderSum.Round(time.Millisecond), encSum.Round(time.Millisecond))

		// The first screen: the reference tiles and the first presets, the
		// only ones a person sees before scrolling. A fresh server, six
		// connections, twelve tiles.
		srv3 := &server{sources: newLRU(6), bases: newLRU(40), thumbs: newLRU(8),
			sem: make(chan struct{}, semCap), byID: map[string]*Photo{ph.ID: ph},
			pmu: sync.RWMutex{}, pByID: srv.pByID,
			rawState: map[string]string{}, rawBusy: map[string]bool{},
			rawQueue: make(chan *Photo, 8), rawQueueLow: make(chan *Photo, 8)}
		srv3.presets = all
		first := ids[:min(12, len(ids))]
		fjobs := make(chan string)
		var fwg sync.WaitGroup
		t0 = time.Now()
		for k := 0; k < 6; k++ {
			fwg.Add(1)
			go func() {
				defer fwg.Done()
				for id := range fjobs {
					u := url(id)
					if id == "" {
						u = "/api/render?id=" + ph.ID + "&w=360&comp=0&m=jpg&src=camera"
					}
					srv3.handleRender(httptest.NewRecorder(), httptest.NewRequest("GET", u, nil))
				}
			}()
		}
		for _, id := range first {
			fjobs <- id
		}
		close(fjobs)
		fwg.Wait()
		fmt.Printf("GRID first screen (%d tiles) over 6 connections: %s\n", len(first), time.Since(t0).Round(time.Millisecond))
	})
}

func t33serverTiming(w *httptest.ResponseRecorder) (base, rnd, enc time.Duration) {
	return t33timing(w, "base"), t33timing(w, "render"), t33timing(w, "encode")
}

func t33timing(w *httptest.ResponseRecorder, key string) time.Duration {
	h := w.Header().Get("Server-Timing")
	i := strings.Index(h, key+";dur=")
	if i < 0 {
		return 0
	}
	s := h[i+len(key)+5:]
	j := strings.IndexAny(s, ", ")
	if j < 0 {
		j = len(s)
	}
	v, _ := strconv.ParseFloat(s[:j], 64)
	return time.Duration(v * float64(time.Millisecond))
}

type t33stage struct {
	name string
	d    time.Duration
}

type memPeak struct {
	stopCh chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	max    uint64
}

func startMemPeak() *memPeak {
	p := &memPeak{stopCh: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			p.mu.Lock()
			if m.HeapAlloc > p.max {
				p.max = m.HeapAlloc
			}
			p.mu.Unlock()
			select {
			case <-p.stopCh:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	return p
}

func (p *memPeak) stop() uint64 {
	close(p.stopCh)
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

func t33clearThumbCache() {
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	p := filepath.Join(dir, "foveon-lab", "thumbs")
	// Only clear a cache the harness has redirected into this folder; a run
	// with the real LOCALAPPDATA must not delete the user's tiles.
	if cwd, err := os.Getwd(); err != nil || !strings.HasPrefix(strings.ToLower(p), strings.ToLower(cwd)) {
		return
	}
	os.RemoveAll(p)
}

func presetNeutral() preset.Params { return preset.Neutral() }
