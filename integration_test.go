// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// This file exercises the phase-2 features once they are wired end-to-end into
// the public Font/Face API: CFF/OTTO rendering, variable-font instancing,
// kerning, GSUB shaping and TrueType hinting. It reuses the in-memory table
// builders from synth_test.go, cff_test.go, variable_test.go, gsub_test.go,
// gpos_test.go, kern_test.go and hint_test.go so no external font file is used.

// makeFont assembles a TrueType font from glyph blobs, a cmap and optional
// extra tables (GSUB/GPOS/kern/fpgm/prep/...), mapping the given runes.
func makeFont(t *testing.T, glyphs [][]byte, m map[rune]uint16, extra map[string][]byte) *Font {
	t.Helper()
	n := len(glyphs)
	loca, glyf := glyfAndLoca(glyphs, false)
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
		"loca": loca,
		"glyf": glyf,
	}
	for k, v := range extra {
		tables[k] = v
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

// cffSquare is a Type2 charstring drawing a filled 100..400 square.
func cffSquare() []byte {
	g := &csb{}
	g.num(100).num(100).op(21)                               // rmoveto -> (100,100)
	g.num(300).num(0).num(0).num(300).num(-300).num(0).op(5) // rlineto (3 segments)
	g.op(14)                                                 // endchar
	return g.b
}

// cffTables assembles the head/maxp/hhea/hmtx/cmap tables plus a 'CFF ' table
// wrapping the given charstrings.
func cffTables(charstrings [][]byte, m map[rune]uint16) map[string][]byte {
	n := len(charstrings)
	adv := make([]int, n)
	lsb := make([]int, n)
	for i := range adv {
		adv[i] = 500
		lsb[i] = 10
	}
	return map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 100, n),
		"hmtx": hmtxTable(adv, lsb, n),
		"cmap": cmapTable([][]byte{cmap4FromMap(m)}),
		"CFF ": buildCFF(cffOptions{glyphs: charstrings, includePriv: true, charType: 2}),
	}
}

// --- 1. CFF / OTTO end-to-end -----------------------------------------------

func TestCFFOTTOEndToEnd(t *testing.T) {
	empty := (&csb{}).op(14).b // glyph 0: endchar only
	tables := cffTables([][]byte{empty, cffSquare()}, map[rune]uint16{'A': 1, 'Z': 5})
	f := mustParse(t, assemble(versionOTTO, tables))
	if f.cff == nil {
		t.Fatal("OTTO font parsed without a CFF table")
	}
	fc := f.NewFace(1000) // scale 1: the square is 300x300 device units
	bounds, mask, _, adv, ok := fc.GlyphMask('A', 0, 0)
	if !ok || mask == nil || bounds.Empty() {
		t.Fatalf("GlyphMask('A') for CFF: ok=%v mask=%v bounds=%v", ok, mask, bounds)
	}
	if adv != 500 {
		t.Errorf("advance = %d want 500", adv)
	}
	// A rune mapped to a glyph beyond the CharStrings INDEX renders as nothing
	// (the CFF outline routine reports an out-of-range glyph).
	if _, _, _, _, ok := fc.GlyphMask('Z', 0, 0); ok {
		t.Error("GlyphMask('Z') should fail for an out-of-range CFF glyph")
	}
}

func TestCFFViaTrueTypeMagic(t *testing.T) {
	// A sfnt with the TrueType version tag but a 'CFF ' table (and no glyf/loca)
	// is still recognised as a CFF font.
	tables := cffTables([][]byte{(&csb{}).op(14).b, cffSquare()}, map[rune]uint16{'A': 1})
	f := mustParse(t, assemble(versionTrueType, tables))
	if f.cff == nil || f.glyf != nil {
		t.Fatalf("CFF-via-TrueType: cff=%v glyf=%v", f.cff != nil, f.glyf != nil)
	}
	if _, mask, _, _, ok := f.NewFace(1000).GlyphMask('A', 0, 0); !ok || mask == nil {
		t.Fatal("CFF-via-TrueType font did not render")
	}
}

func TestParseCFFTableError(t *testing.T) {
	tables := cffTables([][]byte{(&csb{}).op(14).b}, map[rune]uint16{'A': 1})
	tables["CFF "] = []byte{0, 0} // too short: parseCFF fails
	if _, err := Parse(assemble(versionOTTO, tables)); err == nil {
		t.Fatal("Parse should reject a malformed CFF table")
	}
}

// --- 2. Variable instancing (SetVariation) ----------------------------------

func TestFaceSetVariation(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// One tuple at peak wght=1.0 pushing point 2 out by (+300,+300) at all-points.
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{0, 0, 300, 0, 0, 0, 0, 0},
		dY: []int{0, 0, 300, 0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)

	fc := f.NewFace(1000) // scale 1
	// 'B' is glyph 1 (the square); 'A' is the empty glyph 0.
	def, _, _, _, ok := fc.GlyphMask('B', 0, 0)
	if !ok {
		t.Fatal("default GlyphMask('B') failed")
	}

	fc.SetVariation(map[string]float64{"wght": 900})
	varied, _, _, _, ok := fc.GlyphMask('B', 0, 0)
	if !ok {
		t.Fatal("varied GlyphMask('B') failed")
	}
	if def == varied {
		t.Fatalf("variation did not change the outline: bounds still %v", def)
	}
	if varied.Dx() <= def.Dx() {
		t.Errorf("varied width %d not larger than default %d", varied.Dx(), def.Dx())
	}

	// Returning to nil restores the default outline exactly.
	fc.SetVariation(nil)
	back, _, _, _, _ := fc.GlyphMask('B', 0, 0)
	if back != def {
		t.Errorf("SetVariation(nil) did not restore default: %v vs %v", back, def)
	}
}

// --- 3. Kerning -------------------------------------------------------------

// kernGPOS builds a GPOS blob with one pair-kern lookup under the "kern"
// feature adjusting the first glyph's advance for the (left,right) pair.
func kernGPOS(left, right GlyphIndex, adj int16) []byte {
	sub := buildPairPos1(buildCoverage1(left), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: right, xa1: adj}}),
	})
	lk := buildLookup(2, [][]byte{sub})
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "kern", lookups: []uint16{0}}}
	return buildLayoutTable(scripts, feats, [][]byte{lk})
}

func TestFaceKernGPOS(t *testing.T) {
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'B': 2},
		map[string][]byte{"GPOS": kernGPOS(1, 2, -200)})
	fc := f.NewFace(1000) // scale 1
	if k := fc.Kern('A', 'B'); k != -200 {
		t.Errorf("Kern('A','B') = %d want -200", k)
	}
	// MeasureKerned = Measure + the kern between the two runes.
	if got, want := fc.MeasureKerned("AB"), fc.Measure("AB")-200; got != want {
		t.Errorf("MeasureKerned = %d want %d", got, want)
	}
	// An unmapped rune on either side yields no kerning.
	if fc.Kern('Z', 'B') != 0 || fc.Kern('A', 'Z') != 0 {
		t.Error("Kern with an unmapped rune should be 0")
	}

	// The font-unit accessors return the unrounded adjustment; at scale 1 they
	// equal the pixel value, and Kern is exactly their scaled, rounded form.
	if got := fc.KernUnits('A', 'B'); got != -200 {
		t.Errorf("KernUnits('A','B') = %v want -200", got)
	}
	if got := fc.KernIndexUnits(1, 2); got != -200 {
		t.Errorf("KernIndexUnits(1,2) = %v want -200", got)
	}
	// Unmapped runes yield no kerning through KernUnits too (both branches).
	if fc.KernUnits('Z', 'B') != 0 || fc.KernUnits('A', 'Z') != 0 {
		t.Error("KernUnits with an unmapped rune should be 0")
	}
}

// TestFaceKernSubPixel is the regression guard for the width-inflation bug: at
// (or near) the font's own em a whole-pixel Kern quantises the pair adjustment
// to zero, over-widening kern-rich text, while KernUnits keeps the real value a
// text engine needs. The kern of -30 units at a 10px face (scale 0.01) is
// -0.30px, which Kern rounds to 0; KernUnits reports the true -30 units.
func TestFaceKernSubPixel(t *testing.T) {
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'B': 2},
		map[string][]byte{"GPOS": kernGPOS(1, 2, -30)})
	fc := f.NewFace(10) // scale 0.01: one pixel is a whole em/100 quantum
	if k := fc.Kern('A', 'B'); k != 0 {
		t.Errorf("Kern at 10px = %d, want 0 (the quantisation the bug exposed)", k)
	}
	if u := fc.KernUnits('A', 'B'); u != -30 {
		t.Errorf("KernUnits at 10px = %v, want -30 (unrounded font units)", u)
	}
	// Kern is exactly the scaled, rounded KernUnits — the documented relation.
	if got, want := fc.Kern('A', 'B'), roundInt(fc.KernUnits('A', 'B')*fc.Scale()); got != want {
		t.Errorf("Kern = %d, want roundInt(KernUnits*scale) = %d", got, want)
	}
}

func TestFaceKernLegacyTable(t *testing.T) {
	// A font with only the legacy kern table (no GPOS) uses it as the source.
	blob := buildKernTable([][]byte{buildKernFormat0([]kernPair{{left: 1, right: 2, value: -50}})})
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'B': 2}, map[string][]byte{"kern": blob})
	if f.kern == nil || f.gpos != nil {
		t.Fatalf("expected kern-only font: kern=%v gpos=%v", f.kern != nil, f.gpos != nil)
	}
	fc := f.NewFace(1000)
	if k := fc.Kern('A', 'B'); k != -50 {
		t.Errorf("legacy Kern('A','B') = %d want -50", k)
	}
	// The font-unit accessors read through to the legacy table as well.
	if got := fc.KernUnits('A', 'B'); got != -50 {
		t.Errorf("legacy KernUnits('A','B') = %v want -50", got)
	}
	if got := fc.KernIndexUnits(1, 2); got != -50 {
		t.Errorf("legacy KernIndexUnits(1,2) = %v want -50", got)
	}
}

// --- 4. GSUB shaping --------------------------------------------------------

// ligaGSUB builds a GSUB blob whose "liga" feature collapses first+rest into
// the ligature glyph.
func ligaGSUB(first GlyphIndex, rest []GlyphIndex, ligGlyph GlyphIndex) []byte {
	set := buildLigatureSet([][]byte{buildLigature(ligGlyph, rest...)})
	lig := buildLookup(4, [][]byte{buildLigatureSubst(buildCoverage1(first), [][]byte{set})})
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "liga", lookups: []uint16{0}}}
	return buildLayoutTable(scripts, feats, [][]byte{lig})
}

func TestFaceShapeLigature(t *testing.T) {
	// glyphs: 0 empty, 1 'f', 2 'i', 3 'fi'. cmap maps only f and i.
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph(), squareGlyph()},
		map[rune]uint16{'f': 1, 'i': 2},
		map[string][]byte{"GSUB": ligaGSUB(1, []GlyphIndex{2}, 3)})
	fc := f.NewFace(1000)
	if got := fc.Shape("fi", "liga"); !reflect.DeepEqual(got, []GlyphIndex{3}) {
		t.Errorf("Shape(fi, liga) = %v want [3]", got)
	}
	// Without activating the feature the run is the plain cmap mapping. An
	// unmapped rune ('x') becomes glyph 0 (.notdef).
	if got := fc.Shape("fix"); !reflect.DeepEqual(got, []GlyphIndex{1, 2, 0}) {
		t.Errorf("Shape(fix) = %v want [1 2 0]", got)
	}
}

func TestFaceShapeNoGSUB(t *testing.T) {
	// A font with no GSUB table returns the plain cmap mapping regardless of
	// requested features.
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'f': 1, 'i': 2}, nil)
	if got := f.NewFace(1000).Shape("fi", "liga"); !reflect.DeepEqual(got, []GlyphIndex{1, 2}) {
		t.Errorf("Shape(fi) with no GSUB = %v want [1 2]", got)
	}
}

// --- 5. Hinting (opt-in) ----------------------------------------------------

// instrGlyph encodes a single-contour simple glyph carrying instruction bytes.
func instrGlyph(pts []point, instr []byte) []byte {
	w := &bw{}
	w.i16(1) // one contour
	w.bbox()
	w.u16(uint16(len(pts) - 1)) // endPtsOfContours[0]
	w.u16(uint16(len(instr)))
	w.b = append(w.b, instr...)
	var flags, xd, yd []byte
	px, py := 0, 0
	for _, p := range pts {
		fl := uint8(0)
		if p.on {
			fl |= flagOnCurve
		}
		fx, xb := encodeAxis(p.x-px, flagXShort, flagXSamePosX)
		fy, yb := encodeAxis(p.y-py, flagYShort, flagYSamePosY)
		fl |= fx | fy
		flags = append(flags, fl)
		xd = append(xd, xb...)
		yd = append(yd, yb...)
		px, py = p.x, p.y
	}
	w.b = append(w.b, flags...)
	w.b = append(w.b, xd...)
	w.b = append(w.b, yd...)
	return w.bytes()
}

func squarePts() []point {
	return []point{{0, 0, true}, {100, 0, true}, {100, 100, true}, {0, 100, true}}
}

func TestHintingMovesPoints(t *testing.T) {
	// Instructions: shift point 0 by +1280 F26Dot6 (= +20px) along the x
	// freedom vector, which widens the glyph's bounds when hinting is on.
	instr := cat(pb(0), pw(1280), []byte{opSHPIX})
	f := makeFont(t, [][]byte{nil, instrGlyph(squarePts(), instr), instrGlyph(squarePts(), instr)},
		map[rune]uint16{'A': 1, 'B': 2}, nil)
	fc := f.NewFace(100) // scale 0.1

	unhinted, _, _, _, ok := fc.GlyphMask('A', 0, 0)
	if !ok {
		t.Fatal("unhinted GlyphMask('A') failed")
	}
	fc.SetHinting(true)
	hinted, _, _, _, ok := fc.GlyphMask('A', 0, 0)
	if !ok {
		t.Fatal("hinted GlyphMask('A') failed")
	}
	if hinted == unhinted {
		t.Fatalf("hinting did not move points: bounds still %v", unhinted)
	}
	// A second glyph reuses the already-built interpreter (cache hit).
	if _, _, _, _, ok := fc.GlyphMask('B', 0, 0); !ok {
		t.Fatal("hinted GlyphMask('B') failed")
	}
}

func TestHintingUninstructedGlyphs(t *testing.T) {
	// With hinting on, glyphs that carry no instructions render identically to
	// unhinted: a simple glyph without instructions, an empty glyph (space) and
	// a composite glyph. These exercise glyphInstructions' early returns.
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64)
	fc.SetHinting(true)
	for _, r := range []rune{'A', ' ', 'E'} { // simple / empty / composite
		if _, _, _, _, ok := fc.GlyphMask(r, 0, 0); !ok && r != ' ' {
			t.Errorf("hinted GlyphMask(%q) failed", r)
		}
	}
}

func TestHintingInterpBuildFailure(t *testing.T) {
	// A broken font program makes newInterp fail; hinting then falls back to the
	// unhinted outline rather than erroring.
	instr := cat(pb(0), pw(1280), []byte{opSHPIX})
	f := makeFont(t, [][]byte{nil, instrGlyph(squarePts(), instr)},
		map[rune]uint16{'A': 1}, map[string][]byte{"fpgm": {0x56}}) // 0x56: unimplemented
	fc := f.NewFace(100)
	unhinted, _, _, _, _ := fc.GlyphMask('A', 0, 0)
	fc.SetHinting(true)
	hinted, _, _, _, ok := fc.GlyphMask('A', 0, 0)
	if !ok || hinted != unhinted {
		t.Fatalf("build-failure fallback: ok=%v hinted=%v unhinted=%v", ok, hinted, unhinted)
	}
}

func TestHintingRunError(t *testing.T) {
	// The interpreter builds fine but the glyph's own program hits an
	// unimplemented opcode; hinting falls back to the unhinted outline.
	f := makeFont(t, [][]byte{nil, instrGlyph(squarePts(), []byte{0x56})},
		map[rune]uint16{'A': 1}, nil)
	fc := f.NewFace(100)
	unhinted, _, _, _, _ := fc.GlyphMask('A', 0, 0)
	fc.SetHinting(true)
	hinted, _, _, _, ok := fc.GlyphMask('A', 0, 0)
	if !ok || hinted != unhinted {
		t.Fatalf("run-error fallback: ok=%v hinted=%v unhinted=%v", ok, hinted, unhinted)
	}
}

// --- optional-layout-table parse errors -------------------------------------

func TestParseLayoutErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table string
	}{
		{"GSUB", "GSUB"},
		{"GPOS", "GPOS"},
		{"kern", "kern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			glyphs := [][]byte{nil, squareGlyph()}
			loca, glyf := glyfAndLoca(glyphs, false)
			tables := map[string][]byte{
				"head":   headTable(1000, 0),
				"maxp":   maxpTable(2),
				"hhea":   hheaTable(800, -200, 100, 2),
				"hmtx":   hmtxTable([]int{500, 500}, []int{0, 0}, 2),
				"cmap":   cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1})}),
				"loca":   loca,
				"glyf":   glyf,
				tc.table: {0}, // one byte: too short to parse
			}
			if _, err := Parse(assemble(versionTrueType, tables)); err == nil {
				t.Fatalf("Parse should reject a malformed %s table", tc.table)
			}
		})
	}
}
