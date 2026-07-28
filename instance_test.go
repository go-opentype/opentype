// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"testing"
)

// instanceVarFont builds a two-outline variable font: glyph 1 (a square) varies
// on the weight axis via gvar; glyph 2 (a triangle) has no variation data. Two
// non-empty glyphs exercise the bounding-box union in InstanceBytes.
func instanceVarFont(t *testing.T) *Font {
	t.Helper()
	fv := buildFvar(wghtAxis(), nil, false)
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{10, 20, 30, 40, 0, 0, 0, 0},
		dY: []int{5, 6, 7, 8, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd, nil}, false)
	return makeVarFont(t, [][]byte{nil, squareGlyph(), triangleGlyph()}, fv, gv, nil)
}

func TestInstanceBakesGlyfAndDropsVariation(t *testing.T) {
	f := instanceVarFont(t)

	inst, err := f.Instance(map[string]float64{"wght": 900})
	if err != nil {
		t.Fatalf("Instance: %v", err)
	}
	// The instance is static: no axes, and the variation tables are gone.
	if inst.Axes() != nil {
		t.Errorf("instanced font still reports axes: %v", inst.Axes())
	}
	for _, tag := range inst.TableTags() {
		switch tag {
		case "fvar", "gvar", "avar", "HVAR", "VVAR", "MVAR", "STAT":
			t.Errorf("instance still carries variation table %q", tag)
		}
	}

	// Each glyph's baked outline matches the variable font evaluated at wght=900.
	for _, gid := range []int{1, 2} {
		want, err := f.InstancePoints(gid, map[string]float64{"wght": 900})
		if err != nil {
			t.Fatalf("InstancePoints(%d): %v", gid, err)
		}
		got, err := inst.glyphContours(GlyphIndex(gid))
		if err != nil {
			t.Fatalf("instance glyphContours(%d): %v", gid, err)
		}
		if !reflectContoursEqual(want, got) {
			t.Errorf("glyph %d baked outline differs\n want %v\n got  %v", gid, want, got)
		}
	}

	// The recomputed font bounding box spans the instanced square and triangle.
	// Square at wght=900: {10,5},{120,6},{130,107},{40,108}; triangle unchanged:
	// {0,0},{400,0},{200,400}.
	if x0, y0, x1, y1 := inst.FontBBox(); x0 != 0 || y0 != 0 || x1 != 400 || y1 != 400 {
		t.Errorf("FontBBox = %d %d %d %d, want 0 0 400 400", x0, y0, x1, y1)
	}
	// The advance is unchanged (no advance variation), and the lsb tracks xMin.
	if inst.GlyphAdvance(1) != 500 {
		t.Errorf("instance advance = %d, want 500", inst.GlyphAdvance(1))
	}
	if _, mask, _, _, ok := inst.NewFace(32).GlyphMaskIndex(1, 0, 0); !ok || mask == nil {
		t.Error("instanced glyph did not render")
	}
}

func TestInstanceErrors(t *testing.T) {
	// Non-variable font.
	if _, err := descFont(t, descHead(1000, 0, 0, 0, 0, 0), nil, nil).Instance(nil); err == nil {
		t.Error("Instance of a non-variable font should error")
	}
	// CFF2 (variable CFF) is out of scope.
	if _, err := (&Font{cff2: &cff2Table{}}).InstanceBytes(nil); err == nil {
		t.Error("Instance of a CFF2 font should error")
	}
	// A variable font declaring an axis but with no glyf outlines to instance.
	if _, err := (&Font{fvar: &fvarTable{}}).InstanceBytes(nil); err == nil {
		t.Error("Instance with no glyf outlines should error")
	}
	// Unknown axis.
	if _, err := instanceVarFont(t).Instance(map[string]float64{"wdth": 100}); err == nil {
		t.Error("Instance with an unknown axis should error")
	}
}

func TestInstanceGlyphDecodeError(t *testing.T) {
	// A composite glyph referencing a non-existent component makes loadGlyph fail
	// while InstanceBytes walks the glyphs.
	fv := buildFvar(wghtAxis(), nil, false)
	bad := compositeGlyphBytes([]component{{glyphIndex: 99, argsAreXY: true}})
	f := makeVarFont(t, [][]byte{nil, squareGlyph(), bad}, fv, nil, nil)
	if _, err := f.InstanceBytes(map[string]float64{"wght": 900}); err == nil {
		t.Error("Instance of a font with a broken glyph should error")
	}
}

func TestEncodeSimpleGlyphEmpty(t *testing.T) {
	if g := encodeSimpleGlyph(nil); g != nil {
		t.Errorf("nil contours should encode to nil, got %v", g)
	}
	// A contour with no points is skipped, leaving an empty glyph.
	if g := encodeSimpleGlyph([]contour{{}}); g != nil {
		t.Errorf("empty contour should encode to nil, got %v", g)
	}
}

func TestSimpleGlyphStats(t *testing.T) {
	// A re-encoded square: 4 points, 1 contour.
	sq := encodeSimpleGlyph([]contour{{
		{x: 0, y: 0, on: true}, {x: 100, y: 0, on: true},
		{x: 100, y: 100, on: true}, {x: 0, y: 100, on: true},
	}})
	if p, c := simpleGlyphStats(sq); p != 4 || c != 1 {
		t.Errorf("stats = (%d,%d), want (4,1)", p, c)
	}
	// Too-short blob.
	if p, c := simpleGlyphStats([]byte{0, 1}); p != 0 || c != 0 {
		t.Errorf("short glyph stats = (%d,%d), want (0,0)", p, c)
	}
	// Composite marker (numberOfContours < 0).
	composite := make([]byte, 12)
	binary.BigEndian.PutUint16(composite, uint16(0xFFFF)) // numberOfContours = -1
	if p, c := simpleGlyphStats(composite); p != 0 || c != 0 {
		t.Errorf("composite stats = (%d,%d), want (0,0)", p, c)
	}
	// numberOfContours claims more contours than the blob can hold.
	trunc := make([]byte, 10)
	binary.BigEndian.PutUint16(trunc, 5)
	if p, c := simpleGlyphStats(trunc); p != 0 || c != 0 {
		t.Errorf("truncated stats = (%d,%d), want (0,0)", p, c)
	}
}

func TestInstanceMaxp(t *testing.T) {
	sq := encodeSimpleGlyph([]contour{{
		{x: 0, y: 0, on: true}, {x: 10, y: 0, on: true}, {x: 10, y: 10, on: true},
	}})
	// A version-0.5 maxp (too short for the stats fields) is returned unchanged.
	short := maxpTable(2)
	if got := instanceMaxp(short, [][]byte{nil, sq}); !bytesEqual(got, short) {
		t.Error("short maxp should be returned unchanged")
	}
	// A full 32-byte maxp gets its point/contour maxima rewritten.
	full := make([]byte, 32)
	binary.BigEndian.PutUint32(full, 0x00010000)
	binary.BigEndian.PutUint16(full[4:], 2)
	out := instanceMaxp(full, [][]byte{nil, sq})
	if binary.BigEndian.Uint16(out[6:]) != 3 || binary.BigEndian.Uint16(out[8:]) != 1 {
		t.Errorf("maxPoints=%d maxContours=%d, want 3,1", binary.BigEndian.Uint16(out[6:]), binary.BigEndian.Uint16(out[8:]))
	}
	if binary.BigEndian.Uint16(out[10:]) != 0 || binary.BigEndian.Uint16(out[12:]) != 0 {
		t.Error("composite maxima should be zeroed")
	}
}

func TestContourBoundsEmpty(t *testing.T) {
	if _, _, _, _, ok := contourBounds(nil); ok {
		t.Error("empty contours should report ok=false")
	}
}
