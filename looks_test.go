package main

import (
	"reflect"
	"testing"
)

func TestGroupLooksCompleteLinkage(t *testing.T) {
	// 1-D features: a chain 0, 1, 2, 3 (neighbours 1 apart) and a far 10.
	feat := [][]float64{{0}, {1}, {2}, {3}, {10}}
	got := groupLooks(feat, 1.5)
	// Complete linkage must not chain 0..3 into one group (0 and 3 are 3 apart).
	for _, g := range got {
		if len(g) > 2 {
			t.Fatalf("chained group %v in %v", g, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("groups %v", got)
	}
	if got := groupLooks(feat, 100); len(got) != 1 || len(got[0]) != 5 {
		t.Fatalf("all-in-one: %v", got)
	}
	// The leader of a group is its medoid.
	if got := groupLooks([][]float64{{0}, {1}, {2}}, 5); !reflect.DeepEqual(got, [][]int{{1, 0, 2}}) {
		t.Fatalf("medoid: %v", got)
	}
}
