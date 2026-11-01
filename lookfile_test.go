package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

func validLookText(t *testing.T, name string) []byte {
	t.Helper()
	p := preset.Neutral()
	b, err := json.Marshal(lookFile{Format: lookFormat, Version: lookVersion, Name: name, Camera: "ALL", Params: p})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLookDecodeRejectsEveryBadField(t *testing.T) {
	good := validLookText(t, "My look")
	if _, err := decodeLook(good); err != nil {
		t.Fatalf("a valid look was refused: %v", err)
	}
	cases := []struct {
		name string
		body string
	}{
		{"format", `{"format":"other","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4}}`},
		{"version", `{"format":"foveonlook","version":2,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4}}`},
		{"name slash", `{"format":"foveonlook","version":1,"name":"a/b","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4}}`},
		{"name empty", `{"format":"foveonlook","version":1,"name":"  ","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4}}`},
		{"camera", `{"format":"foveonlook","version":1,"name":"x","camera":"SD2","params":{"r":1,"g":1,"b":1,"colorMode":4}}`},
		{"r range", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":0,"g":1,"b":1,"colorMode":4}}`},
		{"exposure", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"exposure":99}}`},
		{"colorMode", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":8}}`},
		{"film", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"film":"No Such Film"}}`},
		{"paper", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"paper":"No Such Paper"}}`},
		{"grade", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"grade":"9"}}`},
		{"filter", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"filter":"make-it-up"}}`},
		{"nd", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"nd":"nope"}}`},
		{"unknown field", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4,"command":"rm -rf /"}}`},
		{"trailing json", `{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":1,"g":1,"b":1,"colorMode":4}} {}`},
		{"not json", `not json`},
	}
	for _, c := range cases {
		if _, err := decodeLook([]byte(c.body)); err == nil {
			t.Errorf("%s: accepted an invalid look", c.name)
		}
	}
}

func TestLookRoundTripCarriesFilmPushPaper(t *testing.T) {
	dir := t.TempDir()
	var film string
	var dev string
	for _, f := range render.Films() {
		if f.Curve != nil && len(f.Curve.Developers) > 0 {
			film, dev = f.Maker+" "+f.Name, f.Curve.Developers[0].Name
			break
		}
	}
	if film == "" {
		t.Fatal("the film library has no film with a developer")
	}
	papers := render.PaperList()
	if len(papers) == 0 {
		t.Fatal("the paper library is empty")
	}
	p := preset.Neutral()
	p.Film, p.Developer, p.DevTime, p.Push = film, dev, 8.5, 1.5
	p.Paper, p.Grade = papers[0].Paper, "3"
	p.CCY, p.CCM, p.CCC, p.PrintExp = 10, -5, 0, 0.1667
	p.Grain, p.FilmWidth = 24, 60
	l := newLookFile("Test Film Look", "SD1", p)
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeLook(b)
	if err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	path, err := importLook(dir, decoded, "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	ps, err := preset.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "Test Film Look" {
		t.Fatalf("XML setting: %+v", ps)
	}
	sc := readFLSidecar(dir)
	if len(sc.Presets) != 1 {
		t.Fatalf("sidecar: %+v", sc)
	}
	got := sc.Presets[0].Params
	if got.Film != film || got.Developer != dev || got.Push != 1.5 || got.Paper != papers[0].Paper || got.Grade != "3" {
		t.Fatalf("film fields did not survive: %+v", got)
	}
	// A second import of the same name is refused, not silently overwritten.
	if _, err := importLook(dir, decoded, ""); !errors.Is(err, errLookNameExists) {
		t.Fatalf("second import: got %v, want the name-exists error", err)
	}
	// The page may ask for another name; that import is allowed.
	if _, err := importLook(dir, decoded, "Test Film Look 2"); err != nil {
		t.Fatalf("import under another name: %v", err)
	}
}

func TestLookImportEndpointRefusesExistingName(t *testing.T) {
	dir := t.TempDir()
	cullTestConfig(t)
	s := &server{presetsDir: dir, byID: map[string]*Photo{}}
	post := func(name string, body []byte) *httptest.ResponseRecorder {
		path := "/api/look/import"
		if name != "" {
			path += "?name=" + url.QueryEscape(name)
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		s.handleLookImport(w, r)
		return w
	}
	if w := post("", validLookText(t, "A look")); w.Code != 200 {
		t.Fatalf("first import: %d %s", w.Code, w.Body)
	}
	if w := post("", validLookText(t, "A look")); w.Code != http.StatusConflict {
		t.Fatalf("duplicate import: %d %s", w.Code, w.Body)
	}
	if w := post("Another look", validLookText(t, "A look")); w.Code != 200 {
		t.Fatalf("renamed import: %d %s", w.Code, w.Body)
	}
	if w := post("", []byte(`{"format":"foveonlook","version":1,"name":"x","camera":"ALL","params":{"r":0}}`)); w.Code != 400 {
		t.Fatalf("invalid import: %d %s", w.Code, w.Body)
	}
}
