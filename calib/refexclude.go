package calib

// The reference exclusion list: SPP exports that must not be used as a
// neutral reference for a photo, with the evidence for each. spp-check and
// fit-raw both apply it, and both print every entry they skip, so a photo
// is never dropped silently.
//
// One entry per line:
//
//	NAME            exclude NAME's export in every group
//	NAME@GROUP      exclude it only for that camera group (SD1, DPM, ...)
//	# comment       ignored
//
// The reason is the text after '#'; every entry must have one. The file is
// embedded so the tools do not depend on the working directory.

import (
	_ "embed"
	"strings"
)

//go:embed refexclude.txt
var refExcludeText string

// ReferenceExclusion is one line of refexclude.txt.
type ReferenceExclusion struct {
	Name   string // upper-case photo name
	Group  string // upper-case camera group, or "" for every group
	Reason string
}

var refExclusions = parseRefExclusions(refExcludeText)

// parseRefExclusions reads the list. Names and groups are matched without
// case; the reason is everything after the first '#'.
func parseRefExclusions(text string) []ReferenceExclusion {
	var out []ReferenceExclusion
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, reason, _ := strings.Cut(line, "#")
		key = strings.TrimSpace(key)
		reason = strings.TrimSpace(reason)
		if key == "" {
			continue
		}
		name, group, _ := strings.Cut(key, "@")
		e := ReferenceExclusion{
			Name:   strings.ToUpper(strings.TrimSpace(name)),
			Group:  strings.ToUpper(strings.TrimSpace(group)),
			Reason: reason,
		}
		if e.Name != "" {
			out = append(out, e)
		}
	}
	return out
}

// ReferenceExclusions returns the list of excluded references, in file
// order. The caller prints it so a user can see what the tools ignore.
func ReferenceExclusions() []ReferenceExclusion { return refExclusions }

// ExcludedReference reports whether name's export is excluded for group,
// and returns the matching entry (with its reason).
func ExcludedReference(name, group string) (ReferenceExclusion, bool) {
	name = strings.ToUpper(strings.TrimSpace(name))
	group = strings.ToUpper(strings.TrimSpace(group))
	for _, e := range refExclusions {
		if e.Name != name {
			continue
		}
		if e.Group == "" || e.Group == group {
			return e, true
		}
	}
	return ReferenceExclusion{}, false
}
