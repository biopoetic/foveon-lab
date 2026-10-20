package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Deleting sends the photo's file to the trash and drops it from the list;
// an unknown id or a failed trash leaves everything as it was.
func TestHandleDelete(t *testing.T) {
	var trashed []string
	fail := false
	trashFile = func(p string) error {
		if fail {
			return http.ErrAbortHandler
		}
		trashed = append(trashed, p)
		return nil
	}
	defer func() { trashFile = moveToTrash }()

	a := &Photo{ID: "a", Name: "A.X3F", path: "/photos/A.X3F"}
	b := &Photo{ID: "b", Name: "B.X3F", path: "/photos/B.X3F"}
	s := &server{photos: []*Photo{a, b}, byID: map[string]*Photo{"a": a, "b": b}}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		s.handleDelete(rec, httptest.NewRequest("POST", "/api/delete", strings.NewReader(body)))
		return rec.Code
	}

	if c := post(`{"id":"nope"}`); c != 404 {
		t.Errorf("unknown id: %d, want 404", c)
	}
	fail = true
	if c := post(`{"id":"a"}`); c != 500 || len(s.photos) != 2 {
		t.Errorf("failed trash: code %d, %d photos left; want 500 and 2", c, len(s.photos))
	}
	fail = false
	if c := post(`{"id":"a"}`); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	if len(trashed) != 1 || trashed[0] != "/photos/A.X3F" {
		t.Errorf("trashed %v, want [/photos/A.X3F]", trashed)
	}
	if len(s.photos) != 1 || s.photos[0] != b || s.byID["a"] != nil {
		t.Errorf("photo a still listed: %v", s.photos)
	}
}
