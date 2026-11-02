package main

// Synthetic checks for the task 43 chroma-structure measure. They pin what the
// number is for: structure, not a uniform colour shift, and a cloud/block must
// score above an equal amount of unfiltered per-pixel grain.

import (
	"math"
	"testing"
)

func t43Field(w, h int, a, b float32) *lab8 {
	l := &lab8{w: w, h: h, y: make([]float32, w*h), a: make([]float32, w*h), b: make([]float32, w*h)}
	for i := range l.y {
		l.y[i] = 128
		l.a[i], l.b[i] = a, b
	}
	return l
}

func TestArtefactIgnoresUniformShift(t *testing.T) {
	ref := t43Field(160, 160, 0, 0)
	target := t43Field(160, 160, 6, -3)
	var m artefactMetric
	m.measureArtefact(target, ref)
	if m.Total > 0.05 {
		t.Fatalf("a uniform colour shift scored %.3f; the measure must see structure, not a mean", m.Total)
	}
}

func TestArtefactSeesCloud(t *testing.T) {
	ref := t43Field(160, 160, 0, 0)
	target := t43Field(160, 160, 0, 0)
	// A 10 dE chroma cloud with a 12 px sigma: no reference structure, one
	// object-sized new field. This is the kind of thing the task asks about.
	for y := 0; y < 160; y++ {
		for x := 0; x < 160; x++ {
			dx, dy := float64(x-80), float64(y-80)
			v := 10 * math.Exp(-(dx*dx+dy*dy)/(2*12*12))
			target.a[y*160+x] = float32(v)
			target.b[y*160+x] = float32(-0.5 * v)
		}
	}
	var m artefactMetric
	m.measureArtefact(target, ref)
	if m.Total < 0.5 {
		t.Fatalf("a 10 dE, 12 px chroma cloud scored %.3f; the measure must see it", m.Total)
	}
	if m.Bands[1] < m.Bands[0] {
		t.Fatalf("cloud scores more at 4-8 px (%.2f) than at 8-16 px (%.2f); it is 12 px wide", m.Bands[0], m.Bands[1])
	}
}

func TestArtefactCloudBeatsGrain(t *testing.T) {
	ref := t43Field(160, 160, 0, 0)
	cloud := t43Field(160, 160, 0, 0)
	grain := t43Field(160, 160, 0, 0)
	seed := uint32(7)
	rnd := func() float64 {
		seed = seed*1664525 + 1013904223
		return float64(seed>>8)/float64(1<<24)*2 - 1
	}
	for y := 0; y < 160; y++ {
		for x := 0; x < 160; x++ {
			dx, dy := float64(x-80), float64(y-80)
			cloud.a[y*160+x] = float32(6 * math.Exp(-(dx*dx+dy*dy)/(2*10*10)))
			// Same per-pixel amplitude as the cloud, but white in space.
			grain.a[y*160+x] = float32(6 * rnd())
		}
	}
	var mc, mg artefactMetric
	mc.measureArtefact(cloud, ref)
	mg.measureArtefact(grain, ref)
	if mc.Total <= mg.Total {
		t.Fatalf("cloud %.2f does not score above equal-amplitude grain %.2f; the measure is not structural", mc.Total, mg.Total)
	}
}
