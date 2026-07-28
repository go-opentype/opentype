// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"os"
	"reflect"
	"testing"
)

// This file exercises the MATH table decoder (math.go): every parse branch and
// error path on synthesised tables, plus the pixel-scaled Face accessors. The
// synthetic cases give deterministic 100% coverage; TestRealMathFontSTIX adds an
// end-to-end smoke test against the real OFL font STIX Two Math when present.

// --- byte builders -----------------------------------------------------------

// buildMathValue writes a MathValueRecord: the int16 value plus a device offset
// (nonzero here to prove the decoder tolerates and ignores a device table).
func buildMathValue(w *bw, v int16, device uint16) {
	w.i16(v)
	w.u16(device)
}

// buildMathConstants builds a full MathConstants record whose every field equals
// 100 plus its MathConstant index, so a test can read any constant back by name.
func buildMathConstants() []byte {
	w := &bw{}
	w.i16(int16(100 + ScriptPercentScaleDown))        // idx 0 (percentage)
	w.i16(int16(100 + ScriptScriptPercentScaleDown))  // idx 1 (percentage)
	w.u16(uint16(100 + DelimitedSubFormulaMinHeight)) // idx 2 (UFWORD)
	w.u16(uint16(100 + DisplayOperatorMinHeight))     // idx 3 (UFWORD)
	for i := MathLeading; i <= RadicalKernAfterDegree; i++ {
		device := uint16(0)
		if i == AxisHeight {
			device = 0x2222 // a tolerated (ignored) device table offset
		}
		buildMathValue(w, int16(100+i), device)
	}
	w.i16(int16(100 + RadicalDegreeBottomRaisePercent)) // idx 55 (percentage)
	return w.bytes()
}

// buildPerGlyphValues builds a MathItalicsCorrectionInfo / MathTopAccentAttachment
// table: a Coverage plus one MathValueRecord per covered glyph.
func buildPerGlyphValues(cov []byte, vals ...int16) []byte {
	covOff := 4 + len(vals)*4
	w := &bw{}
	w.u16(uint16(covOff))
	w.u16(uint16(len(vals)))
	for _, v := range vals {
		buildMathValue(w, v, 0)
	}
	w.b = append(w.b, cov...)
	return w.bytes()
}

// buildMathKern builds a MathKern subtable: heightCount correction heights and
// heightCount+1 kern values.
func buildMathKern(heights []int16, kerns []int16) []byte {
	w := &bw{}
	w.u16(uint16(len(heights)))
	for _, h := range heights {
		buildMathValue(w, h, 0)
	}
	for _, k := range kerns {
		buildMathValue(w, k, 0)
	}
	return w.bytes()
}

// buildMathKernInfo builds a MathKernInfo table from a Coverage and, per covered
// glyph, four corner MathKern blobs (a nil corner means that corner is absent).
func buildMathKernInfo(cov []byte, records [][4][]byte) []byte {
	n := len(records)
	header := 4 + n*8
	body := &bw{}
	covOff := header + len(body.b)
	body.b = append(body.b, cov...)
	offs := make([][4]uint16, n)
	for i := range records {
		for c := 0; c < 4; c++ {
			if records[i][c] == nil {
				continue
			}
			offs[i][c] = uint16(header + len(body.b))
			body.b = append(body.b, records[i][c]...)
		}
	}
	w := &bw{}
	w.u16(uint16(covOff))
	w.u16(uint16(n))
	for i := 0; i < n; i++ {
		for c := 0; c < 4; c++ {
			w.u16(offs[i][c])
		}
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildMathGlyphInfo builds a MathGlyphInfo table from its four optional
// sub-tables (a nil sub-table yields a zero offset).
func buildMathGlyphInfo(italic, topAccent, extShape, kern []byte) []byte {
	header := 8
	body := &bw{}
	place := func(sub []byte) uint16 {
		if sub == nil {
			return 0
		}
		off := header + len(body.b)
		body.b = append(body.b, sub...)
		return uint16(off)
	}
	io := place(italic)
	to := place(topAccent)
	eo := place(extShape)
	ko := place(kern)
	w := &bw{}
	w.u16(io)
	w.u16(to)
	w.u16(eo)
	w.u16(ko)
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// partSpec describes one GlyphPart for the assembly builder.
type partSpec struct {
	glyph, start, end, full, flags uint16
}

// buildGlyphAssembly builds a GlyphAssembly: an italics-correction MathValue and
// a list of GlyphParts.
func buildGlyphAssembly(italic int16, parts ...partSpec) []byte {
	w := &bw{}
	buildMathValue(w, italic, 0)
	w.u16(uint16(len(parts)))
	for _, p := range parts {
		w.u16(p.glyph)
		w.u16(p.start)
		w.u16(p.end)
		w.u16(p.full)
		w.u16(p.flags)
	}
	return w.bytes()
}

// buildGlyphConstruction builds a MathGlyphConstruction: an optional (possibly
// raw/invalid) GlyphAssembly blob and a list of {variantGlyph, advance} records.
func buildGlyphConstruction(assembly []byte, variants ...[2]uint16) []byte {
	asmOff := uint16(0)
	if assembly != nil {
		asmOff = uint16(4 + len(variants)*4)
	}
	w := &bw{}
	w.u16(asmOff)
	w.u16(uint16(len(variants)))
	for _, v := range variants {
		w.u16(v[0])
		w.u16(v[1])
	}
	if assembly != nil {
		w.b = append(w.b, assembly...)
	}
	return w.bytes()
}

// buildMathVariants builds a MathVariants table from the minimum connector
// overlap, the vertical/horizontal coverages and their glyph constructions (a
// nil coverage yields a zero offset).
func buildMathVariants(minOverlap uint16, vertCov, horizCov []byte, vertConstr, horizConstr [][]byte) []byte {
	vn, hn := len(vertConstr), len(horizConstr)
	header := 10 + vn*2 + hn*2
	body := &bw{}
	place := func(sub []byte) uint16 {
		if sub == nil {
			return 0
		}
		off := header + len(body.b)
		body.b = append(body.b, sub...)
		return uint16(off)
	}
	vco := place(vertCov)
	hco := place(horizCov)
	vOffs := make([]uint16, vn)
	for i := range vertConstr {
		vOffs[i] = place(vertConstr[i])
	}
	hOffs := make([]uint16, hn)
	for i := range horizConstr {
		hOffs[i] = place(horizConstr[i])
	}
	w := &bw{}
	w.u16(minOverlap)
	w.u16(vco)
	w.u16(hco)
	w.u16(uint16(vn))
	w.u16(uint16(hn))
	for _, o := range vOffs {
		w.u16(o)
	}
	for _, o := range hOffs {
		w.u16(o)
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildMATH wraps the three optional sub-tables into a MATH table.
func buildMATH(constants, glyphInfo, variants []byte) []byte {
	header := 10 // version(4) + 3 Offset16
	body := &bw{}
	place := func(sub []byte) uint16 {
		if sub == nil {
			return 0
		}
		off := header + len(body.b)
		body.b = append(body.b, sub...)
		return uint16(off)
	}
	co := place(constants)
	gio := place(glyphInfo)
	vo := place(variants)
	w := &bw{}
	w.u16(1) // majorVersion
	w.u16(0) // minorVersion
	w.u16(co)
	w.u16(gio)
	w.u16(vo)
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// fontWithMATH assembles a minimal TrueType font carrying the given MATH blob
// (or none when math is nil) and parses it.
func fontWithMATH(t *testing.T, math []byte) *Font {
	t.Helper()
	loca, glyf := glyfAndLoca([][]byte{{}}, false)
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(1),
		"hhea": hheaTable(800, -200, 100, 1),
		"hmtx": hmtxTable([]int{500}, []int{10}, 1),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'a': 0})}),
		"loca": loca,
		"glyf": glyf,
	}
	if math != nil {
		tables["MATH"] = math
	}
	f, err := Parse(assemble(versionTrueType, tables))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

// populatedMATH builds a MATH table exercising every sub-structure: full
// constants; italic correction, top accent, extended shape and a four-corner
// math kern for glyph 0; a vertical construction with an assembly and a
// horizontal construction without one.
func populatedMATH() []byte {
	constants := buildMathConstants()

	italic := buildPerGlyphValues(buildCoverage1(0), 40)
	topAccent := buildPerGlyphValues(buildCoverage1(0), 70)
	extShape := buildCoverage1(0)
	kern := buildMathKernInfo(buildCoverage1(0), [][4][]byte{{
		buildMathKern([]int16{100, 200}, []int16{5, 10, 15}), // TopRight
		nil,                            // TopLeft absent
		buildMathKern(nil, []int16{7}), // BottomRight (no heights)
		buildMathKern([]int16{300}, []int16{1, 2}), // BottomLeft
	}})
	glyphInfo := buildMathGlyphInfo(italic, topAccent, extShape, kern)

	assembly := buildGlyphAssembly(30,
		partSpec{glyph: 10, start: 50, end: 60, full: 700, flags: 0},
		partSpec{glyph: 11, start: 50, end: 60, full: 300, flags: partFlagExtender},
	)
	vertConstr := buildGlyphConstruction(assembly, [2]uint16{10, 600}, [2]uint16{11, 900})
	horizConstr := buildGlyphConstruction(nil, [2]uint16{12, 700})
	variants := buildMathVariants(20,
		buildCoverage1(0), buildCoverage1(0),
		[][]byte{vertConstr}, [][]byte{horizConstr})

	return buildMATH(constants, glyphInfo, variants)
}

// --- integration + accessor tests --------------------------------------------

func TestHasMath(t *testing.T) {
	if f := fontWithMATH(t, nil); f.HasMath() {
		t.Error("HasMath = true for a font without a MATH table")
	}
	if f := fontWithMATH(t, populatedMATH()); !f.HasMath() {
		t.Error("HasMath = false for a font with a MATH table")
	}
}

func TestMathConstants(t *testing.T) {
	f := fontWithMATH(t, populatedMATH())
	fc := f.NewFace(1000) // scale 1.0: pixel value == design value

	// A scaled (design-unit) constant.
	if got := fc.MathConstant(AxisHeight); got != int(100+AxisHeight) {
		t.Errorf("AxisHeight = %d, want %d", got, int(100+AxisHeight))
	}
	if got := fc.MathConstant(RadicalRuleThickness); got != int(100+RadicalRuleThickness) {
		t.Errorf("RadicalRuleThickness = %d, want %d", got, int(100+RadicalRuleThickness))
	}
	// The two UFWORD minimum heights.
	if got := fc.MathConstant(DisplayOperatorMinHeight); got != int(100+DisplayOperatorMinHeight) {
		t.Errorf("DisplayOperatorMinHeight = %d", got)
	}
	// Percentage constants are returned unscaled.
	if got := fc.MathConstant(ScriptPercentScaleDown); got != int(100+ScriptPercentScaleDown) {
		t.Errorf("ScriptPercentScaleDown = %d", got)
	}
	if got := fc.MathConstant(RadicalDegreeBottomRaisePercent); got != int(100+RadicalDegreeBottomRaisePercent) {
		t.Errorf("RadicalDegreeBottomRaisePercent = %d", got)
	}

	// Scaling: at half size a design-unit constant halves (round half away).
	half := f.NewFace(2000) // scale 2.0
	if got := half.MathConstant(AxisHeight); got != 2*int(100+AxisHeight) {
		t.Errorf("scaled AxisHeight = %d, want %d", got, 2*int(100+AxisHeight))
	}
	// A percentage constant is unaffected by size.
	if got := half.MathConstant(ScriptPercentScaleDown); got != int(100+ScriptPercentScaleDown) {
		t.Errorf("scaled percentage = %d", got)
	}

	// Out-of-range indices return 0.
	if got := fc.MathConstant(-1); got != 0 {
		t.Errorf("MathConstant(-1) = %d, want 0", got)
	}
	if got := fc.MathConstant(mathConstCount); got != 0 {
		t.Errorf("MathConstant(count) = %d, want 0", got)
	}
}

func TestMathConstantsAbsent(t *testing.T) {
	// A face over a font without a MATH table returns 0.
	f := fontWithMATH(t, nil)
	if got := f.NewFace(1000).MathConstant(AxisHeight); got != 0 {
		t.Errorf("no-MATH MathConstant = %d, want 0", got)
	}
	// A MATH table without a constants sub-table (glyphInfo only) also yields 0.
	glyphInfo := buildMathGlyphInfo(buildPerGlyphValues(buildCoverage1(0), 40), nil, nil, nil)
	f2 := fontWithMATH(t, buildMATH(nil, glyphInfo, nil))
	if got := f2.NewFace(1000).MathConstant(AxisHeight); got != 0 {
		t.Errorf("no-constants MathConstant = %d, want 0", got)
	}
}

func TestItalicCorrection(t *testing.T) {
	fc := fontWithMATH(t, populatedMATH()).NewFace(1000)
	if got := fc.ItalicCorrection(0); got != 40 {
		t.Errorf("ItalicCorrection(0) = %d, want 40", got)
	}
	if got := fc.ItalicCorrection(99); got != 0 {
		t.Errorf("ItalicCorrection(uncovered) = %d, want 0", got)
	}
	if got := fontWithMATH(t, nil).NewFace(1000).ItalicCorrection(0); got != 0 {
		t.Errorf("no-MATH ItalicCorrection = %d, want 0", got)
	}
}

func TestTopAccentAttachment(t *testing.T) {
	fc := fontWithMATH(t, populatedMATH()).NewFace(1000)
	if v, ok := fc.TopAccentAttachment(0); !ok || v != 70 {
		t.Errorf("TopAccentAttachment(0) = %d,%v want 70,true", v, ok)
	}
	if _, ok := fc.TopAccentAttachment(99); ok {
		t.Error("TopAccentAttachment(uncovered) ok = true, want false")
	}
	if _, ok := fontWithMATH(t, nil).NewFace(1000).TopAccentAttachment(0); ok {
		t.Error("no-MATH TopAccentAttachment ok = true, want false")
	}
}

func TestExtendedShape(t *testing.T) {
	f := fontWithMATH(t, populatedMATH())
	if !f.IsExtendedShapeGlyph(0) {
		t.Error("IsExtendedShapeGlyph(0) = false, want true")
	}
	if f.IsExtendedShapeGlyph(99) {
		t.Error("IsExtendedShapeGlyph(uncovered) = true, want false")
	}
	if fontWithMATH(t, nil).IsExtendedShapeGlyph(0) {
		t.Error("no-MATH IsExtendedShapeGlyph = true, want false")
	}
}

func TestMathKern(t *testing.T) {
	fc := fontWithMATH(t, populatedMATH()).NewFace(1000)

	// TopRight: heights [100,200], kerns [5,10,15].
	cases := []struct {
		h    int
		want int
	}{
		{50, 5},   // below the first height
		{150, 10}, // between the heights
		{250, 15}, // at or above the last height
	}
	for _, c := range cases {
		if got := fc.MathKern(0, MathKernTopRight, c.h); got != c.want {
			t.Errorf("MathKern(TopRight, %d) = %d, want %d", c.h, got, c.want)
		}
	}
	// BottomRight has a single kern (no heights).
	if got := fc.MathKern(0, MathKernBottomRight, 123); got != 7 {
		t.Errorf("MathKern(BottomRight) = %d, want 7", got)
	}
	// TopLeft corner is absent for glyph 0.
	if got := fc.MathKern(0, MathKernTopLeft, 100); got != 0 {
		t.Errorf("MathKern(TopLeft absent) = %d, want 0", got)
	}
	// An uncovered glyph, an out-of-range corner, and a no-MATH face all give 0.
	if got := fc.MathKern(99, MathKernTopRight, 100); got != 0 {
		t.Errorf("MathKern(uncovered glyph) = %d, want 0", got)
	}
	if got := fc.MathKern(0, MathKernCorner(9), 100); got != 0 {
		t.Errorf("MathKern(bad corner) = %d, want 0", got)
	}
	if got := fc.MathKern(0, MathKernCorner(-1), 100); got != 0 {
		t.Errorf("MathKern(negative corner) = %d, want 0", got)
	}
	if got := fontWithMATH(t, nil).NewFace(1000).MathKern(0, MathKernTopRight, 100); got != 0 {
		t.Errorf("no-MATH MathKern = %d, want 0", got)
	}
}

func TestMathVariants(t *testing.T) {
	fc := fontWithMATH(t, populatedMATH()).NewFace(1000)

	// Vertical: two variants plus an assembly.
	variants, asm := fc.MathVariants(0, true)
	wantVars := []MathVariant{{Glyph: 10, Advance: 600}, {Glyph: 11, Advance: 900}}
	if !reflect.DeepEqual(variants, wantVars) {
		t.Errorf("vertical variants = %+v, want %+v", variants, wantVars)
	}
	if asm == nil {
		t.Fatal("vertical assembly = nil, want non-nil")
	}
	wantAsm := &MathAssembly{
		ItalicsCorrection:   30,
		MinConnectorOverlap: 20,
		Parts: []MathAssemblyPart{
			{Glyph: 10, StartConnector: 50, EndConnector: 60, FullAdvance: 700, Extender: false},
			{Glyph: 11, StartConnector: 50, EndConnector: 60, FullAdvance: 300, Extender: true},
		},
	}
	if !reflect.DeepEqual(asm, wantAsm) {
		t.Errorf("vertical assembly = %+v, want %+v", asm, wantAsm)
	}

	// Horizontal: one variant, no assembly.
	hVars, hAsm := fc.MathVariants(0, false)
	if !reflect.DeepEqual(hVars, []MathVariant{{Glyph: 12, Advance: 700}}) {
		t.Errorf("horizontal variants = %+v", hVars)
	}
	if hAsm != nil {
		t.Errorf("horizontal assembly = %+v, want nil", hAsm)
	}

	// A glyph with no construction, and a no-MATH face, give nil, nil.
	if v, a := fc.MathVariants(99, true); v != nil || a != nil {
		t.Errorf("uncovered variants = %+v, %+v, want nil, nil", v, a)
	}
	if v, a := fontWithMATH(t, nil).NewFace(1000).MathVariants(0, true); v != nil || a != nil {
		t.Errorf("no-MATH variants = %+v, %+v, want nil, nil", v, a)
	}
}

// --- parse error paths -------------------------------------------------------

func TestParseMathErrors(t *testing.T) {
	badCov := []byte{0, 9} // Coverage format 9 is invalid

	tests := []struct {
		name string
		math []byte
	}{
		{"header truncated", []byte{0, 1, 0}},
		{"constants truncated", buildMATH([]byte{0x00}, nil, nil)},
		{"glyphInfo header truncated", buildMATH(nil, []byte{0x00}, nil)},
		{"variants header truncated", buildMATH(nil, nil, []byte{0, 0, 0, 0})},

		// MathGlyphInfo sub-table failures.
		{"italic record truncated",
			buildMATH(nil, buildMathGlyphInfo([]byte{0, 0, 0, 1}, nil, nil, nil), nil)},
		{"topAccent coverage bad",
			buildMATH(nil, buildMathGlyphInfo(nil, []byte{0, 4, 0, 0, 0, 9}, nil, nil), nil)},
		{"extendedShape coverage bad",
			buildMATH(nil, buildMathGlyphInfo(nil, nil, badCov, nil), nil)},
		{"kernInfo header truncated",
			buildMATH(nil, buildMathGlyphInfo(nil, nil, nil, []byte{0x00}), nil)},

		// MathKernInfo failures.
		{"math kern truncated",
			buildMATH(nil, buildMathGlyphInfo(nil, nil, nil,
				buildMathKernInfo(buildCoverage1(0), [][4][]byte{{[]byte{0, 5}, nil, nil, nil}})), nil)},
		{"kernInfo coverage bad",
			buildMATH(nil, buildMathGlyphInfo(nil, nil, nil,
				buildMathKernInfo(badCov, [][4][]byte{{nil, nil, nil, nil}})), nil)},

		// MathVariants failures.
		{"vert construction truncated",
			buildMATH(nil, nil, buildMathVariants(0, buildCoverage1(0), nil, [][]byte{{0, 1}}, nil))},
		{"vert coverage bad",
			buildMATH(nil, nil, buildMathVariants(0, badCov, nil, nil, nil))},
		{"horiz construction truncated",
			buildMATH(nil, nil, buildMathVariants(0, nil, buildCoverage1(0), nil, [][]byte{{0, 1}}))},
		{"assembly truncated",
			buildMATH(nil, nil, buildMathVariants(0, buildCoverage1(0), nil,
				[][]byte{buildGlyphConstruction([]byte{0x00})}, nil))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseMATH(tt.math); err == nil {
				t.Errorf("parseMATH(%s) = nil error, want failure", tt.name)
			}
		})
	}
}

func TestParseFontBadMATH(t *testing.T) {
	// A malformed MATH table fails the whole font parse.
	loca, glyf := glyfAndLoca([][]byte{{}}, false)
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(1),
		"hhea": hheaTable(800, -200, 100, 1),
		"hmtx": hmtxTable([]int{500}, []int{10}, 1),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'a': 0})}),
		"loca": loca,
		"glyf": glyf,
		"MATH": []byte{0, 0, 0}, // truncated header
	}
	if _, err := Parse(assemble(versionTrueType, tables)); err == nil {
		t.Error("expected error for malformed MATH")
	}
}

// TestRealMathFontSTIX is an end-to-end check against a real OpenType math font,
// STIX Two Math (SIL Open Font License; the font and its licence live in
// testdata/). It is not part of the deterministic coverage set — the synthetic
// cases above cover every branch — but it confirms the decoder reads a
// production MATH table: the math axis height is a sane positive metric, and the
// left parenthesis, a classic stretchy delimiter, exposes both size variants and
// a glyph assembly a math engine can grow to any height. It skips cleanly when
// the font file is not present.
func TestRealMathFontSTIX(t *testing.T) {
	b, err := os.ReadFile("testdata/STIXTwoMath-Regular.otf")
	if err != nil {
		t.Skipf("real math font not available: %v", err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(STIXTwoMath): %v", err)
	}
	if !f.HasMath() {
		t.Fatal("STIXTwoMath-Regular.otf is expected to carry a MATH table")
	}
	fc := f.NewFace(f.unitsPerEm) // scale 1.0: pixels == design units

	if ax := fc.MathConstant(AxisHeight); ax <= 0 {
		t.Errorf("AxisHeight = %d, want a positive metric", ax)
	}

	gid, ok := f.GlyphIndex('(')
	if !ok {
		t.Fatal("cmap has no glyph for '('")
	}
	variants, asm := fc.MathVariants(gid, true)
	if len(variants) == 0 {
		t.Error("'(' has no vertical size variants")
	}
	if asm == nil || len(asm.Parts) == 0 {
		t.Error("'(' has no stretchy glyph assembly")
	}
	// A stretchy assembly must have at least one extender part to be growable.
	hasExtender := false
	for _, p := range asm.Parts {
		if p.Extender {
			hasExtender = true
		}
	}
	if !hasExtender {
		t.Error("'(' assembly has no extender part")
	}
}
