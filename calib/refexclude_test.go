package calib

import "testing"

func TestParseRefExclusions(t *testing.T) {
	text := `
# a comment
SDIM0290  # non-neutral: the export is 1.5 EV off
sdim0076@sd1	# wrong camera: DP2 Merrill export
SDIM0001 # wrong frame
`
	got := parseRefExclusions(text)
	want := []ReferenceExclusion{
		{Name: "SDIM0290", Reason: "non-neutral: the export is 1.5 EV off"},
		{Name: "SDIM0076", Group: "SD1", Reason: "wrong camera: DP2 Merrill export"},
		{Name: "SDIM0001", Reason: "wrong frame"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestExcludedReference(t *testing.T) {
	old := refExclusions
	refExclusions = []ReferenceExclusion{
		{Name: "ALLGROUPS", Reason: "every group"},
		{Name: "ONEGROUP", Group: "SD1", Reason: "only SD1"},
	}
	defer func() { refExclusions = old }()

	if e, ok := ExcludedReference("allgroups", "DPM"); !ok || e.Reason != "every group" {
		t.Errorf("name-only entry did not match another group: %+v %v", e, ok)
	}
	if _, ok := ExcludedReference("ONEGROUP", "SD1"); !ok {
		t.Error("group entry did not match its group")
	}
	if _, ok := ExcludedReference("ONEGROUP", "DPM"); ok {
		t.Error("group entry matched another group")
	}
	if _, ok := ExcludedReference("OTHER", "SD1"); ok {
		t.Error("unrelated name matched")
	}
}

func TestEmbeddedRefExclusionsHaveReasons(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range ReferenceExclusions() {
		if e.Name == "" {
			t.Errorf("entry with an empty name: %+v", e)
		}
		if e.Reason == "" {
			t.Errorf("%s: excluded without a reason", e.Name)
		}
		key := e.Name + "@" + e.Group
		if seen[key] {
			t.Errorf("%s: listed twice", key)
		}
		seen[key] = true
	}
}
