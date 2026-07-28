// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// TestApplyMaskedTrackedMultiple: a multiple substitution (ccmp-style
// decomposition, 1->2) makes both output glyphs inherit the source glyph's
// cluster; untouched glyphs keep theirs.
func TestApplyMaskedTrackedMultiple(t *testing.T) {
	// glyph 10 -> [20, 21].
	sub := buildSetListSubst(buildCoverage1(10), [][]byte{buildSequence(20, 21)})
	lk := buildLookup(2, [][]byte{sub})
	g := gsubArab(t, []tFeature{{tag: "ccmp", lookups: []uint16{0}}}, [][]byte{lk})

	gl, cl := g.ApplyMaskedTracked([]GlyphIndex{10, 11}, nil, []FeatureApp{{Tag: "ccmp"}})
	if want := []GlyphIndex{20, 21, 11}; !reflect.DeepEqual(gl, want) {
		t.Errorf("glyphs = %v want %v", gl, want)
	}
	if want := []int{0, 0, 1}; !reflect.DeepEqual(cl, want) {
		t.Errorf("clusters = %v want %v", cl, want)
	}
}

// TestApplyMaskedTrackedLigature: a ligature (m->1) collapses its components'
// clusters onto the first component's.
func TestApplyMaskedTrackedLigature(t *testing.T) {
	// 10 + 11 -> 99.
	set := buildLigatureSet([][]byte{buildLigature(99, 11)})
	lk := buildLookup(4, [][]byte{buildLigatureSubst(buildCoverage1(10), [][]byte{set})})
	g := gsubArab(t, []tFeature{{tag: "liga", lookups: []uint16{0}}}, [][]byte{lk})

	gl, cl := g.ApplyMaskedTracked([]GlyphIndex{10, 11, 12}, nil, []FeatureApp{{Tag: "liga"}})
	if want := []GlyphIndex{99, 12}; !reflect.DeepEqual(gl, want) {
		t.Errorf("glyphs = %v want %v", gl, want)
	}
	if want := []int{0, 2}; !reflect.DeepEqual(cl, want) {
		t.Errorf("clusters = %v want %v", cl, want)
	}
}

// TestApplyMaskedTrackedInitialClusters: an explicit starting cluster slice is
// carried through the decomposition (not reset to 0,1,...).
func TestApplyMaskedTrackedInitialClusters(t *testing.T) {
	sub := buildSetListSubst(buildCoverage1(10), [][]byte{buildSequence(20, 21)})
	lk := buildLookup(2, [][]byte{sub})
	g := gsubArab(t, []tFeature{{tag: "ccmp", lookups: []uint16{0}}}, [][]byte{lk})

	gl, cl := g.ApplyMaskedTracked([]GlyphIndex{10, 11}, []int{5, 7}, []FeatureApp{{Tag: "ccmp"}})
	if want := []GlyphIndex{20, 21, 11}; !reflect.DeepEqual(gl, want) {
		t.Errorf("glyphs = %v want %v", gl, want)
	}
	if want := []int{5, 5, 7}; !reflect.DeepEqual(cl, want) {
		t.Errorf("clusters = %v want %v", cl, want)
	}
	// The caller's cluster slice is not mutated.
	orig := []int{5, 7}
	g.ApplyMaskedTracked([]GlyphIndex{10, 11}, orig, []FeatureApp{{Tag: "ccmp"}})
	if !reflect.DeepEqual(orig, []int{5, 7}) {
		t.Errorf("input clusters mutated: %v", orig)
	}
}

// TestApplyMaskedTrackedEmpty: with no features the run and its clusters are
// returned unchanged.
func TestApplyMaskedTrackedEmpty(t *testing.T) {
	g := gsubArab(t, []tFeature{{tag: "ccmp", lookups: []uint16{0}}},
		[][]byte{buildLookup(1, [][]byte{buildSingle1(1, buildCoverage1(10))})})
	gl, cl := g.ApplyMaskedTracked([]GlyphIndex{10}, []int{3}, nil)
	if !reflect.DeepEqual(gl, []GlyphIndex{10}) || !reflect.DeepEqual(cl, []int{3}) {
		t.Errorf("empty-feats tracked = %v / %v want [10] / [3]", gl, cl)
	}
}
