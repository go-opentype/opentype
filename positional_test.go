// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// gsubArab parses a GSUB whose given features (in order, feature index i for
// feats[i]) are all enabled under an "arab" script default LangSys, so the
// positional-application tests mirror how real Arabic fonts file init/medi/fina.
func gsubArab(t *testing.T, feats []tFeature, lookups [][]byte) *gsub {
	t.Helper()
	idx := make([]uint16, len(feats))
	for i := range idx {
		idx[i] = uint16(i)
	}
	scripts := []tScript{{tag: "arab", def: &tLangSys{required: 0xFFFF, features: idx}}}
	g, err := parseGSUB(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	return g
}

// TestApplyMaskedPositional applies init at only its position and fina at only
// its position, the core Arabic mechanism: each single substitution fires just
// where its joining-form mask is set.
func TestApplyMaskedPositional(t *testing.T) {
	initLk := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(10, 11, 12))})
	finaLk := buildLookup(1, [][]byte{buildSingle1(200, buildCoverage1(10, 11, 12))})
	g := gsubArab(t, []tFeature{
		{tag: "init", lookups: []uint16{0}},
		{tag: "fina", lookups: []uint16{1}},
	}, [][]byte{initLk, finaLk})

	got := g.ApplyMasked([]GlyphIndex{10, 11, 12}, []FeatureApp{
		{Tag: "init", Positions: []bool{true, false, false}},
		{Tag: "fina", Positions: []bool{false, false, true}},
	})
	if want := []GlyphIndex{110, 11, 212}; !reflect.DeepEqual(got, want) {
		t.Errorf("masked positional = %v want %v", got, want)
	}
	// The input slice is not mutated.
	in := []GlyphIndex{10, 11, 12}
	g.ApplyMasked(in, []FeatureApp{{Tag: "init", Positions: []bool{true, true, true}}})
	if !reflect.DeepEqual(in, []GlyphIndex{10, 11, 12}) {
		t.Errorf("input mutated: %v", in)
	}
}

// TestApplyMaskedNilWholeRun: a nil mask applies over the whole run, identical
// to Apply.
func TestApplyMaskedNilWholeRun(t *testing.T) {
	lk := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(10, 11, 12))})
	g := gsubArab(t, []tFeature{{tag: "init", lookups: []uint16{0}}}, [][]byte{lk})
	got := g.ApplyMasked([]GlyphIndex{10, 11, 12}, []FeatureApp{{Tag: "init", Positions: nil}})
	if want := []GlyphIndex{110, 111, 112}; !reflect.DeepEqual(got, want) {
		t.Errorf("nil-mask whole run = %v want %v", got, want)
	}
}

// TestApplyMaskedMaskedTrueNoMatch: a masked position whose glyph the lookup
// does not cover advances without substituting.
func TestApplyMaskedMaskedTrueNoMatch(t *testing.T) {
	lk := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(10))}) // covers 10 only
	g := gsubArab(t, []tFeature{{tag: "init", lookups: []uint16{0}}}, [][]byte{lk})
	got := g.ApplyMasked([]GlyphIndex{99}, []FeatureApp{{Tag: "init", Positions: []bool{true}}})
	if want := []GlyphIndex{99}; !reflect.DeepEqual(got, want) {
		t.Errorf("masked no-match = %v want %v", got, want)
	}
}

// TestApplyMaskedOutOfRangeLookup: a feature referencing a lookup index past
// the lookup list is skipped, leaving the run unchanged.
func TestApplyMaskedOutOfRangeLookup(t *testing.T) {
	lk := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(10))})
	g := gsubArab(t, []tFeature{{tag: "init", lookups: []uint16{5}}}, [][]byte{lk})
	got := g.ApplyMasked([]GlyphIndex{10}, []FeatureApp{{Tag: "init", Positions: []bool{true}}})
	if want := []GlyphIndex{10}; !reflect.DeepEqual(got, want) {
		t.Errorf("out-of-range lookup = %v want %v", got, want)
	}
}

// TestApplyMaskedEmpty: empty feature list or empty run leaves the input
// unchanged (returned as-is).
func TestApplyMaskedEmpty(t *testing.T) {
	lk := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(10))})
	g := gsubArab(t, []tFeature{{tag: "init", lookups: []uint16{0}}}, [][]byte{lk})
	in := []GlyphIndex{10}
	if got := g.ApplyMasked(in, nil); !reflect.DeepEqual(got, in) {
		t.Errorf("empty feats = %v want %v", got, in)
	}
	if got := g.ApplyMasked(nil, []FeatureApp{{Tag: "init"}}); got != nil {
		t.Errorf("empty run = %v want nil", got)
	}
}

// TestApplyMaskedReverse exercises masked application of a reverse-chaining
// lookup: substitution fires at masked positions (scanned back-to-front),
// skips unmasked ones, and leaves masked-but-uncovered glyphs unchanged.
func TestApplyMaskedReverse(t *testing.T) {
	rev := buildReverseChain(buildCoverage1(50), nil, nil, []GlyphIndex{60})
	lk := buildLookup(8, [][]byte{rev})
	g := gsubArab(t, []tFeature{{tag: "test", lookups: []uint16{0}}}, [][]byte{lk})
	// pos2 (glyph 50, masked) -> 60; pos1 (masked off) skipped; pos0 (glyph 99,
	// masked but uncovered) unchanged.
	got := g.ApplyMasked([]GlyphIndex{99, 50, 50}, []FeatureApp{
		{Tag: "test", Positions: []bool{true, false, true}},
	})
	if want := []GlyphIndex{99, 50, 60}; !reflect.DeepEqual(got, want) {
		t.Errorf("masked reverse = %v want %v", got, want)
	}
}

// TestGPOSPosition drives the exported Position wrapper: a "kern" pair
// adjustment reaches the first glyph's XAdvance.
func TestGPOSPosition(t *testing.T) {
	gp, err := parseGPOS(kernGPOS(1, 2, -200))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	pos := gp.Position([]GlyphIndex{1, 2}, []int{500, 500}, "kern")
	if len(pos) != 2 || pos[0].XAdvance != -200 {
		t.Errorf("Position = %+v want first XAdvance -200", pos)
	}
}

// TestFontLayoutAccessors covers Font.GSUB/GPOS/GlyphAdvance and Face.Font/Scale
// on a font carrying both layout tables, and the nil returns on one carrying
// neither.
func TestFontLayoutAccessors(t *testing.T) {
	glyphs := [][]byte{nil, squareGlyph(), squareGlyph(), squareGlyph()}
	f := makeFont(t, glyphs, map[rune]uint16{'f': 1, 'i': 2},
		map[string][]byte{
			"GSUB": ligaGSUB(1, []GlyphIndex{2}, 3),
			"GPOS": kernGPOS(1, 2, -200),
		})
	if f.GSUB() == nil {
		t.Error("GSUB() = nil, want non-nil")
	}
	if f.GPOS() == nil {
		t.Error("GPOS() = nil, want non-nil")
	}
	if got := f.GlyphAdvance(1); got != 500 {
		t.Errorf("GlyphAdvance(1) = %d want 500", got)
	}
	if got := f.GlyphAdvance(60000); got != 0 {
		t.Errorf("GlyphAdvance(out-of-range) = %d want 0", got)
	}
	fc := f.NewFace(500) // scale 0.5 at unitsPerEm 1000
	if fc.Font() != f {
		t.Error("Face.Font() did not return the source font")
	}
	if got := fc.Scale(); got != 0.5 {
		t.Errorf("Face.Scale() = %v want 0.5", got)
	}

	// A font with neither table returns nil handles.
	bare := makeFont(t, glyphs, map[rune]uint16{'f': 1}, nil)
	if bare.GSUB() != nil || bare.GPOS() != nil {
		t.Errorf("bare font: GSUB=%v GPOS=%v, want both nil", bare.GSUB(), bare.GPOS())
	}
}
