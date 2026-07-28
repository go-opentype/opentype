// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"testing"
)

// triangleGlyph is a second simple glyph distinct from squareGlyph.
func triangleGlyph() []byte {
	return simpleGlyphBytes([][]point{{
		{x: 0, y: 0, on: true},
		{x: 400, y: 0, on: true},
		{x: 200, y: 400, on: true},
	}})
}

// squareGlyph2 is a third simple glyph, offset from the first.
func squareGlyph2() []byte {
	return simpleGlyphBytes([][]point{{
		{x: 100, y: 100, on: true},
		{x: 500, y: 100, on: true},
		{x: 500, y: 500, on: true},
		{x: 100, y: 500, on: true},
	}})
}

// subsetTestFont builds a glyf font with a mix of simple and composite glyphs:
//
//	gid0 empty, gid1 square, gid2 triangle (dropped by the subset below),
//	gid3 square2, gid4 composite referencing gid1 and gid3.
func subsetTestFont(t *testing.T) *Font {
	t.Helper()
	comp := compositeGlyphBytes([]component{
		{glyphIndex: 1, arg1: 0, arg2: 0, argsAreXY: true, more: true},
		{glyphIndex: 3, arg1: 120, arg2: 60, useWords: true, argsAreXY: true},
	})
	glyphs := [][]byte{nil, squareGlyph(), triangleGlyph(), squareGlyph2(), comp}
	loca, glyf := glyfAndLoca(glyphs, false)
	adv := []int{0, 500, 400, 500, 700}
	lsb := []int{0, 0, 0, 100, 0}
	tables := map[string][]byte{
		"head": descHead(1000, 0, 0, 1000, 1000, 0),
		"maxp": maxpTable(5),
		"hhea": hheaTable(800, -200, 0, 5),
		"hmtx": hmtxTable(adv, lsb, 5),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1, 'B': 4})}),
		"loca": loca,
		"glyf": glyf,
		"cvt ": {0, 0},
		"fpgm": {0xB0},
		"prep": {0xB0},
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

func TestSubsetTrueTypeClosureAndRemap(t *testing.T) {
	f := subsetTestFont(t)

	// Request only the composite (gid4); the closure must pull in its components
	// (gid1, gid3) and glyph 0, and drop the unreferenced gid2.
	data, remap, err := f.SubsetTrueType([]GlyphIndex{4})
	if err != nil {
		t.Fatalf("SubsetTrueType: %v", err)
	}
	wantRemap := map[GlyphIndex]GlyphIndex{0: 0, 1: 1, 3: 2, 4: 3}
	if len(remap) != len(wantRemap) {
		t.Fatalf("remap = %v, want %v", remap, wantRemap)
	}
	for old, nw := range wantRemap {
		if remap[old] != nw {
			t.Fatalf("remap[%d] = %d, want %d (full %v)", old, remap[old], nw, remap)
		}
	}

	sub, err := Parse(data)
	if err != nil {
		t.Fatalf("re-parse subset: %v", err)
	}
	if sub.NumGlyphs() != 4 {
		t.Errorf("subset NumGlyphs = %d, want 4", sub.NumGlyphs())
	}

	// Every kept glyph must render identically at its new id.
	for old, nw := range wantRemap {
		want, err := f.glyphContours(old)
		if err != nil {
			t.Fatalf("orig glyphContours(%d): %v", old, err)
		}
		got, err := sub.glyphContours(nw)
		if err != nil {
			t.Fatalf("subset glyphContours(%d): %v", nw, err)
		}
		if !reflectContoursEqual(want, got) {
			t.Errorf("glyph old %d -> new %d: contours differ\n want %v\n got  %v", old, nw, want, got)
		}
	}

	// The composite's advance and lsb survive the remap.
	if sub.GlyphAdvance(3) != 700 {
		t.Errorf("subset advance of composite = %d, want 700", sub.GlyphAdvance(3))
	}
	// And the instruction tables were carried over.
	if _, ok := sub.Table("fpgm"); !ok {
		t.Error("subset should carry fpgm")
	}
	// The subset renders.
	if _, mask, _, _, ok := sub.NewFace(20).GlyphMaskIndex(1, 0, 0); !ok || mask == nil {
		t.Error("subset glyph did not render")
	}
}

func TestSubsetTrueTypeOverlappingRequest(t *testing.T) {
	f := subsetTestFont(t)
	// Requesting the composite (gid4) and one of its own components (gid3) makes
	// the closure walk gid3 twice; the second visit hits the membership fast path.
	data, remap, err := f.SubsetTrueType([]GlyphIndex{4, 3})
	if err != nil {
		t.Fatalf("SubsetTrueType: %v", err)
	}
	if remap[3] != 2 {
		t.Errorf("remap[3] = %d, want 2", remap[3])
	}
	if _, err := Parse(data); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
}

func TestPackGlyfOddBlob(t *testing.T) {
	// An odd-length glyph blob forces the 2-byte alignment padding.
	loca, glyf := packGlyf([][]byte{{1, 2, 3}, {4}})
	if len(glyf)%2 != 0 {
		t.Errorf("glyf length %d is not even", len(glyf))
	}
	// loca has numGlyphs+1 offsets, all even.
	if len(loca) != 3*4 {
		t.Fatalf("loca length = %d, want 12", len(loca))
	}
	off1 := uint32(loca[4])<<24 | uint32(loca[5])<<16 | uint32(loca[6])<<8 | uint32(loca[7])
	if off1 != 4 { // first glyph padded from 3 to 4 bytes
		t.Errorf("second glyph offset = %d, want 4", off1)
	}
}

func TestSubsetTrueTypeErrors(t *testing.T) {
	f := subsetTestFont(t)
	if _, _, err := f.SubsetTrueType([]GlyphIndex{99}); err == nil {
		t.Error("out-of-range gid should error")
	}
	// A CFF font has no glyf table.
	cff := loadSourceSerif(t)
	if _, _, err := cff.SubsetTrueType([]GlyphIndex{1}); err == nil {
		t.Error("SubsetTrueType on a CFF font should error")
	}
}

func TestSubsetTrueTypeBrokenComponentClosure(t *testing.T) {
	// A composite whose component is out of range makes the closure walk fail;
	// here glyph 0 itself is the broken composite, so walk(0) surfaces the error.
	bad := compositeGlyphBytes([]component{{glyphIndex: 99, argsAreXY: true}})
	glyphs := [][]byte{bad, squareGlyph()}
	loca, glyf := glyfAndLoca(glyphs, false)
	f := mustParse(t, assemble(versionTrueType, map[string][]byte{
		"head": descHead(1000, 0, 0, 1000, 1000, 0),
		"maxp": maxpTable(2),
		"hhea": hheaTable(800, -200, 0, 2),
		"hmtx": hmtxTable([]int{500, 500}, []int{0, 0}, 2),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1})}),
		"loca": loca,
		"glyf": glyf,
	}))
	if _, _, err := f.SubsetTrueType([]GlyphIndex{1}); err == nil {
		t.Error("a broken component in the closure should error")
	}
}

func TestComponentRefsEdges(t *testing.T) {
	// A simple glyph has no component references.
	if refs := componentRefs(squareGlyph()); refs != nil {
		t.Errorf("simple glyph componentRefs = %v, want nil", refs)
	}
	// Too-short data yields nil.
	if refs := componentRefs([]byte{0xFF}); refs != nil {
		t.Errorf("short glyph componentRefs = %v, want nil", refs)
	}
	// A truncated composite (header says composite, body ends mid-record) yields
	// only the components decodable before the truncation.
	trunc := compositeGlyphBytes([]component{
		{glyphIndex: 2, argsAreXY: true, more: true},
		{glyphIndex: 3, argsAreXY: true},
	})
	full := componentRefs(trunc)
	if len(full) != 2 {
		t.Fatalf("full composite has %d refs, want 2", len(full))
	}
	// Components carrying each transform variant (scale, x/y-scale, 2x2) exercise
	// the record-size switch.
	scaled := compositeGlyphBytes([]component{
		{glyphIndex: 1, argsAreXY: true, hasScale: true, scale: 0.5, more: true},
		{glyphIndex: 2, argsAreXY: true, hasXY: true, sx: 0.5, sy: 0.5, more: true},
		{glyphIndex: 3, argsAreXY: true, has2x2: true, a: 1, b: 0, c: 0, d: 1, useWords: true},
	})
	if refs := componentRefs(scaled); len(refs) != 3 {
		t.Fatalf("scaled composite refs = %d, want 3", len(refs))
	}
	if got := componentRefs(trunc[:len(trunc)-3]); len(got) >= 2 {
		t.Errorf("truncated composite should decode fewer refs, got %d", len(got))
	}
}

func TestRemapGlyphKeepsUnknownComponent(t *testing.T) {
	// A component absent from the map is left unchanged (defensive path).
	comp := compositeGlyphBytes([]component{{glyphIndex: 7, argsAreXY: true}})
	out := remapGlyph(comp, map[GlyphIndex]GlyphIndex{0: 0})
	if refs := componentRefs(out); len(refs) != 1 || refs[0].gid != 7 {
		t.Errorf("unknown component should stay 7, got %v", refs)
	}
	// A simple glyph is copied unchanged.
	if out := remapGlyph(squareGlyph(), nil); !bytesEqual(out, squareGlyph()) {
		t.Error("simple glyph should be copied unchanged")
	}
}

func TestRawGlyphEmpty(t *testing.T) {
	f := subsetTestFont(t)
	if g := f.rawGlyph(0); g != nil { // gid 0 is the empty .notdef
		t.Errorf("rawGlyph(0) = %v, want nil", g)
	}
	if g := f.rawGlyph(1); g == nil {
		t.Error("rawGlyph(1) should return the square glyph bytes")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
