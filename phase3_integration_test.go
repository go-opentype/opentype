// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// This file exercises the phase-3 features once they are wired end-to-end into
// the public Font/Face API: CFF2 (variable Compact Font Format) rendering with
// SetVariation, and GPOS-positioned shaping via Face.ShapePositioned. It reuses
// the in-memory table builders from synth_test.go, cff_test.go, cff2_test.go,
// variable_test.go, gpos_test.go, gpos_advanced_test.go, gsub_test.go and
// gsub_advanced_test.go so no external font file is used.

// --- CFF2 end-to-end (Parse + GlyphMask + SetVariation) ---------------------

// cff2OTTOTables assembles the head/maxp/hhea/hmtx/cmap tables plus a 'CFF2'
// table wrapping the given charstrings, and any extra tables (e.g. 'fvar').
func cff2OTTOTables(charStrings [][]byte, vstore []byte, m map[rune]uint16, extra map[string][]byte) map[string][]byte {
	n := len(charStrings)
	adv := make([]int, n)
	lsb := make([]int, n)
	for i := range adv {
		adv[i] = 500
		lsb[i] = 10
	}
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 100, n),
		"hmtx": hmtxTable(adv, lsb, n),
		"cmap": cmapTable([][]byte{cmap4FromMap(m)}),
		"CFF2": buildCFF2(charStrings, nil, vstore, nil, nil),
	}
	for k, v := range extra {
		tables[k] = v
	}
	return tables
}

func TestCFF2OTTOEndToEnd(t *testing.T) {
	// One wght axis, one region peaking at the axis maximum (normalized +1.0).
	vstore := buildVstore(1, [][]regionAxis{{{start: 0, peak: 1, end: 1}}}, [][]int{{0}})

	// Glyph 0 is empty; glyph 1 draws a square whose start point's y is blended:
	// default (100,100), moving to (100,400) as wght rises to its maximum.
	empty := &csb{}
	g := &csb{}
	g.num(100).num(100).num(0).num(300).num(2).op(16)             // blend [x=100, y=100] with deltas [0, 300]
	g.op(21)                                                      // rmoveto
	g.num(200).num(0).num(0).num(200).num(-200).num(0).op(5)      // rlineto: close a 200x200 box
	tables := cff2OTTOTables([][]byte{empty.b, g.b}, vstore,
		map[rune]uint16{'A': 1}, map[string][]byte{"fvar": buildFvar(wghtAxis(), nil, false)})

	f := mustParse(t, assemble(versionOTTO, tables))
	if f.cff2 == nil {
		t.Fatal("OTTO/CFF2 font parsed without a cff2 table")
	}
	if f.cff != nil || f.glyf != nil {
		t.Fatalf("CFF2 font should carry neither cff nor glyf: cff=%v glyf=%v", f.cff != nil, f.glyf != nil)
	}

	fc := f.NewFace(1000) // scale 1: font units == device units
	def, mask, _, adv, ok := fc.GlyphMask('A', 0, 0)
	if !ok || mask == nil || def.Empty() {
		t.Fatalf("default GlyphMask('A') for CFF2: ok=%v mask=%v bounds=%v", ok, mask, def)
	}
	if adv != 500 {
		t.Errorf("advance = %d want 500", adv)
	}

	// Instancing at the axis maximum lifts the blended point, moving the bounds.
	fc.SetVariation(map[string]float64{"wght": 900})
	varied, _, _, _, ok := fc.GlyphMask('A', 0, 0)
	if !ok {
		t.Fatal("varied GlyphMask('A') for CFF2 failed")
	}
	if def == varied {
		t.Fatalf("CFF2 variation did not change the outline: bounds still %v", def)
	}

	// Returning to nil restores the default master exactly.
	fc.SetVariation(nil)
	back, _, _, _, _ := fc.GlyphMask('A', 0, 0)
	if back != def {
		t.Errorf("SetVariation(nil) did not restore the CFF2 default: %v vs %v", back, def)
	}
}

func TestCFF2NonVariableFont(t *testing.T) {
	// A CFF2 font with no variation store and no fvar still renders (the default
	// master), exercising the cff2 outline path with nil normalized coords.
	g := &csb{}
	g.num(100).num(100).op(21)
	g.num(200).num(0).num(0).num(200).num(-200).num(0).op(5)
	tables := cff2OTTOTables([][]byte{(&csb{}).b, g.b}, nil, map[rune]uint16{'A': 1}, nil)
	f := mustParse(t, assemble(versionTrueType, tables)) // CFF2 via the TrueType magic
	if f.cff2 == nil {
		t.Fatal("CFF2-via-TrueType font parsed without a cff2 table")
	}
	if _, mask, _, _, ok := f.NewFace(1000).GlyphMask('A', 0, 0); !ok || mask == nil {
		t.Fatal("non-variable CFF2 font did not render")
	}
}

func TestParseCFF2TableError(t *testing.T) {
	tables := cff2OTTOTables([][]byte{(&csb{}).b}, nil, map[rune]uint16{'A': 1}, nil)
	tables["CFF2"] = []byte{2, 0} // too short: parseCFF2 fails
	if _, err := Parse(assemble(versionOTTO, tables)); err == nil {
		t.Fatal("Parse should reject a malformed CFF2 table")
	}
}

// --- GPOS-positioned shaping (Face.ShapePositioned) -------------------------

// markKernGPOS builds a GPOS table with two lookups: a mark-to-base lookup under
// the "mark" feature attaching mark glyph markGID onto base glyph baseGID, and a
// pair-kern lookup under the "kern" feature adjusting the (left,right) advance.
func markKernGPOS(baseGID, markGID, left, right GlyphIndex, kern int16, baseAnchorX, baseAnchorY, markAnchorX int16) []byte {
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(markAnchorX, 0)}})
	baseArr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(baseAnchorX, baseAnchorY)}})
	markSub := buildMarkPosSubtable(buildCoverage1(markGID), buildCoverage1(baseGID), 1, markArr, baseArr)
	markLk := buildLookup(4, [][]byte{markSub})

	pairSub := buildPairPos1(buildCoverage1(left), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: right, xa1: kern}}),
	})
	kernLk := buildLookup(2, [][]byte{pairSub})

	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0, 1}}}}
	feats := []tFeature{{tag: "mark", lookups: []uint16{0}}, {tag: "kern", lookups: []uint16{1}}}
	return buildLayoutTable(scripts, feats, [][]byte{markLk, kernLk})
}

// positionedFont builds a font with GSUB (an f+i -> fi ligature) and GPOS
// (mark-to-base + pair kern) so ShapePositioned drives both stages.
func positionedFont(t *testing.T) *Face {
	t.Helper()
	// glyphs: 0 empty, 1 'A', 2 combining-acute, 3 'V', 4 'f', 5 'i', 6 'fi'.
	glyphs := [][]byte{nil, squareGlyph(), squareGlyph(), squareGlyph(), squareGlyph(), squareGlyph(), squareGlyph()}
	f := makeFont(t, glyphs,
		map[rune]uint16{'A': 1, '́': 2, 'V': 3, 'f': 4, 'i': 5},
		map[string][]byte{
			"GSUB": ligaGSUB(4, []GlyphIndex{5}, 6),
			"GPOS": markKernGPOS(1, 2, 1, 3, -200, 300, 600, 50),
		})
	return f.NewFace(1000) // scale 1
}

func TestShapePositionedMarkAttach(t *testing.T) {
	fc := positionedFont(t)
	// "A" + combining acute: the mark (glyph 2) attaches onto the base, lifted to
	// the base anchor's y (600 font units == 600 px at scale 1).
	got := fc.ShapePositioned("Á")
	if len(got) != 2 {
		t.Fatalf("positioned run length = %d, want 2", len(got))
	}
	if got[1].Glyph != 2 {
		t.Errorf("mark glyph = %d, want 2", got[1].Glyph)
	}
	if got[1].YOffset != 600 {
		t.Errorf("mark YOffset = %d, want 600 (attached to base anchor)", got[1].YOffset)
	}
	// The base anchor x (300) minus mark anchor x (50) minus the base advance
	// (500) pulls the mark back: XOffset = 300-50-500 = -250.
	if got[1].XOffset != -250 {
		t.Errorf("mark XOffset = %d, want -250", got[1].XOffset)
	}
}

func TestShapePositionedPairKern(t *testing.T) {
	fc := positionedFont(t)
	// "AV": the kern pair reduces the first glyph's advance by 200 (500 -> 300).
	got := fc.ShapePositioned("AV", "kern")
	if len(got) != 2 || got[0].Glyph != 1 || got[1].Glyph != 3 {
		t.Fatalf("positioned run = %+v, want glyphs [1 3]", got)
	}
	if got[0].XAdvance != 300 {
		t.Errorf("kerned XAdvance = %d, want 300 (500-200)", got[0].XAdvance)
	}
	if got[0].YOffset != 0 {
		t.Errorf("unexpected YOffset on base = %d", got[0].YOffset)
	}
}

func TestShapePositionedLigature(t *testing.T) {
	fc := positionedFont(t)
	// The GSUB stage collapses f+i into the single ligature glyph 6.
	got := fc.ShapePositioned("fi", "liga")
	if len(got) != 1 || got[0].Glyph != 6 {
		t.Fatalf("positioned ligature = %+v, want single glyph 6", got)
	}
	if got[0].XAdvance != 500 {
		t.Errorf("ligature XAdvance = %d, want 500", got[0].XAdvance)
	}
}

func TestShapePositionedNoLayoutTables(t *testing.T) {
	// A font with neither GSUB nor GPOS: the run is the plain cmap mapping at its
	// unadjusted advances, exercising both nil-table branches.
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'B': 2}, nil)
	got := f.NewFace(1000).ShapePositioned("AB")
	want := []PositionedGlyph{
		{Glyph: 1, XAdvance: 500},
		{Glyph: 2, XAdvance: 500},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ShapePositioned with no layout tables = %+v, want %+v", got, want)
	}
	// An empty string yields an empty run.
	if got := f.NewFace(1000).ShapePositioned(""); len(got) != 0 {
		t.Errorf("ShapePositioned(\"\") = %+v, want empty", got)
	}
}

// --- GSUB advanced types reachable through Face.Shape ------------------------

func TestFaceShapeAdvancedGSUB(t *testing.T) {
	// A multiple-substitution (GSUB type 2) lookup, activated via Face.Shape,
	// confirms the advanced GSUB lookup types are reachable through the public
	// shaping API (Face.Shape is a thin pass-through to gsub.Apply, which the
	// gsub_advanced_test.go suite exercises for every lookup type).
	sub := buildSetListSubst(buildCoverage1(1), [][]byte{buildSequence(2, 3)})
	lk := buildLookup(2, [][]byte{sub})
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "ccmp", lookups: []uint16{0}}}
	gsubBlob := buildLayoutTable(scripts, feats, [][]byte{lk})

	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph(), squareGlyph()},
		map[rune]uint16{'x': 1}, map[string][]byte{"GSUB": gsubBlob})
	fc := f.NewFace(1000)
	if got := fc.Shape("x", "ccmp"); !reflect.DeepEqual(got, []GlyphIndex{2, 3}) {
		t.Errorf("Shape(x, ccmp) = %v, want [2 3] (one->many)", got)
	}
}
