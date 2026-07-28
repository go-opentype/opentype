// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"errors"
	"image"
	"strings"
	"testing"
)

// --- geometry helpers -------------------------------------------------------

// sqCCW returns a counter-clockwise (y-up) axis-aligned square.
func sqCCW(a, b int) []point {
	return []point{{a, a, true}, {b, a, true}, {b, b, true}, {a, b, true}}
}

// sqCW returns a clockwise (y-up) axis-aligned square (opposite winding).
func sqCW(a, b int) []point {
	return []point{{a, a, true}, {a, b, true}, {b, b, true}, {b, a, true}}
}

// offCurveContour is a contour that starts with an off-curve point and has two
// consecutive off-curve points (to exercise implied-midpoint insertion and the
// rotate-to-on-curve step).
func offCurveContour() []point {
	return []point{{400, 100, false}, {400, 400, false}, {100, 400, true}, {100, 100, true}}
}

// repeatGlyphRaw hand-encodes a simple glyph whose flags use the REPEAT
// mechanism with a count larger than the remaining points.
func repeatGlyphRaw() []byte {
	w := &bw{}
	w.i16(1) // one contour
	w.bbox()
	w.u16(3) // endPts[0]=3 -> 4 points
	w.u16(0) // instructionLength
	w.u8(flagOnCurve | flagXShort | flagYShort | flagRepeat | flagXSamePosX | flagYSamePosY)
	w.u8(10)                                    // repeat 10 times (capped at the 3 remaining points)
	for _, d := range []uint8{30, 10, 10, 10} { // x deltas
		w.u8(d)
	}
	for _, d := range []uint8{30, 10, 50, 10} { // y deltas
		w.u8(d)
	}
	return w.bytes()
}

// stdGlyphs builds the 11-glyph outline set of the standard test font.
func stdGlyphs() [][]byte {
	return [][]byte{
		simpleGlyphBytes([][]point{sqCCW(0, 40)}),                                                                                    // 0 .notdef box
		simpleGlyphBytes([][]point{sqCCW(100, 400)}),                                                                                 // 1 'A' big square
		simpleGlyphBytes([][]point{sqCCW(100, 400), sqCW(200, 300)}),                                                                 // 2 'B' square with hole
		simpleGlyphBytes([][]point{offCurveContour()}),                                                                               // 3 'C' off-curve
		simpleGlyphBytes([][]point{sqCCW(10, 20)}),                                                                                   // 4 'D' small square
		compositeGlyphBytes([]component{{glyphIndex: 1, arg1: 50, arg2: 50, argsAreXY: true, hasScale: true, scale: 0.5}}),           // 5 'E'
		compositeGlyphBytes([]component{{glyphIndex: 1, arg1: 50, arg2: 50, argsAreXY: true, hasXY: true, sx: 0.5, sy: 0.75}}),       // 6 'F'
		compositeGlyphBytes([]component{{glyphIndex: 1, arg1: 10, arg2: 10, argsAreXY: true, has2x2: true, a: 1, b: 0, c: 0, d: 1}}), // 7 'G'
		compositeGlyphBytes([]component{ // 8 'H': words args + MORE_COMPONENTS
			{glyphIndex: 4, arg1: 300, arg2: 0, argsAreXY: true, useWords: true, more: true},
			{glyphIndex: 1, arg1: 0, arg2: 0, argsAreXY: true},
		}),
		repeatGlyphRaw(), // 9 'R'
		nil,              // 10 empty (space)
	}
}

func std4Map() map[rune]uint16 {
	return map[rune]uint16{'A': 1, 'B': 2, 'C': 3, 'D': 4, 'E': 5, 'F': 6, 'G': 7, 'H': 8, 'R': 9, ' ': 10}
}

func std12Map() map[rune]uint16 {
	m := std4Map()
	m[0x1F600] = 1 // an astral-plane rune, only representable in format 12
	return m
}

// stdTables assembles the standard font's table set.
func stdTables(longLoca, useCmap12 bool) map[string][]byte {
	loca, glyf := glyfAndLoca(stdGlyphs(), longLoca)
	locFmt := int16(0)
	if longLoca {
		locFmt = 1
	}
	advances := []int{250, 500, 500, 450, 120, 500, 500, 510, 520, 140, 300}
	lsbs := []int{10, 20, 30, 40, 5, 15, 25, 35, 45, 55, 65}
	var sub []byte
	if useCmap12 {
		sub = cmap12FromMap(std12Map())
	} else {
		sub = cmap4FromMap(std4Map())
	}
	return map[string][]byte{
		"head": headTable(1000, locFmt),
		"maxp": maxpTable(11),
		"hhea": hheaTable(800, -200, 100, 9),
		"hmtx": hmtxTable(advances, lsbs, 9),
		"cmap": cmapTable([][]byte{sub}),
		"loca": loca,
		"glyf": glyf,
	}
}

func stdBytes(longLoca, useCmap12 bool) []byte {
	return assemble(versionTrueType, stdTables(longLoca, useCmap12))
}

func mustParse(t *testing.T, b []byte) *Font {
	t.Helper()
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	return f
}

// fontWithGlyphs builds a minimal font whose glyph 1 (and onward) are the
// provided blobs, mapping 'A','B',... to glyphs 1,2,... Glyph 0 is empty.
func fontWithGlyphs(t *testing.T, blobs [][]byte) *Font {
	t.Helper()
	glyphs := append([][]byte{nil}, blobs...)
	n := len(glyphs)
	loca, glyf := glyfAndLoca(glyphs, false)
	m := map[rune]uint16{}
	adv := make([]int, n)
	lsb := make([]int, n)
	for i := 0; i < len(blobs); i++ {
		m[rune('A'+i)] = uint16(i + 1)
		adv[i] = 400
		lsb[i] = 10
	}
	adv[n-1], lsb[n-1] = 400, 10
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 100, n),
		"hmtx": hmtxTable(adv, lsb, n),
		"cmap": cmapTable([][]byte{cmap4FromMap(m)}),
		"loca": loca,
		"glyf": glyf,
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

// --- happy-path parsing -----------------------------------------------------

func TestParseVariants(t *testing.T) {
	for _, longLoca := range []bool{false, true} {
		for _, c12 := range []bool{false, true} {
			f := mustParse(t, stdBytes(longLoca, c12))
			if f.NumGlyphs() != 11 {
				t.Errorf("NumGlyphs=%d want 11", f.NumGlyphs())
			}
			gid, ok := f.GlyphIndex('A')
			if !ok || gid != 1 {
				t.Errorf("GlyphIndex('A')=%d,%v want 1,true", gid, ok)
			}
			if _, ok := f.GlyphIndex('Z'); ok {
				t.Errorf("GlyphIndex('Z') should be unmapped")
			}
			// An astral rune is unmappable by format 4 (>0xFFFF) but mappable
			// by format 12.
			if _, ok := f.GlyphIndex(0x1F600); ok != c12 {
				t.Errorf("GlyphIndex(astral) ok=%v want %v", ok, c12)
			}
		}
	}
}

func TestParseTrueMagic(t *testing.T) {
	b := stdBytes(false, false)
	binary.BigEndian.PutUint32(b, versionTrue)
	if _, err := Parse(b); err != nil {
		t.Fatalf("'true' magic should parse: %v", err)
	}
}

func TestHmtxTrailingRun(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	// numberOfHMetrics=9, so glyphs 9 and 10 share advance index 8 (=520).
	if f.advances[8] != 520 || f.advances[9] != 520 || f.advances[10] != 520 {
		t.Errorf("trailing-run advances = %d,%d,%d want 520,520,520",
			f.advances[8], f.advances[9], f.advances[10])
	}
	if f.lsbs[10] != 65 {
		t.Errorf("trailing lsb=%d want 65", f.lsbs[10])
	}
}

// --- metrics / advance / measure --------------------------------------------

func TestMetrics(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(100) // scale 0.1
	m := fc.Metrics()
	if m.Ascent != 80 || m.Descent != 20 || m.Height != 110 {
		t.Errorf("Metrics=%+v want {80 20 110}", m)
	}
	if got := fc.Advance('A'); got != 50 {
		t.Errorf("Advance('A')=%d want 50", got)
	}
	if got := fc.Advance('Z'); got != 0 {
		t.Errorf("Advance(unmapped)=%d want 0", got)
	}
	if got := fc.Measure("AB"); got != 100 {
		t.Errorf("Measure(\"AB\")=%d want 100", got)
	}
	if got := fc.Measure("A" + "￿"); got != 50 {
		t.Errorf("Measure with unmapped rune=%d want 50", got)
	}
}

// --- rasterisation ----------------------------------------------------------

func scanAlpha(m *image.Alpha) (max uint8, hasPartial bool) {
	for _, a := range m.Pix {
		if a > max {
			max = a
		}
		if a > 0 && a < 255 {
			hasPartial = true
		}
	}
	return
}

func TestGlyphMaskFilledSquare(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64) // non-integer edges -> anti-aliasing
	bounds, mask, maskp, adv, ok := fc.GlyphMask('A', 10, 40)
	if !ok || mask == nil {
		t.Fatalf("GlyphMask('A') ok=%v mask=%v", ok, mask)
	}
	if maskp != (image.Point{}) {
		t.Errorf("maskp=%v want origin", maskp)
	}
	if bounds.Empty() {
		t.Errorf("bounds empty")
	}
	if adv <= 0 {
		t.Errorf("advance=%d want positive", adv)
	}
	max, partial := scanAlpha(mask)
	if max != 255 {
		t.Errorf("interior max alpha=%d want 255", max)
	}
	if !partial {
		t.Errorf("expected anti-aliased edge pixels (0<alpha<255)")
	}
}

func TestGlyphMaskCaching(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64)
	_, m1, _, _, _ := fc.GlyphMask('A', 0, 0)
	_, m2, _, _, _ := fc.GlyphMask('A', 5, 5) // second call: cache hit
	if m1 != m2 {
		t.Errorf("cached glyph mask should be reused")
	}
}

func TestGlyphMaskHole(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(80)
	_, mask, _, _, ok := fc.GlyphMask('B', 0, 0)
	if !ok || mask == nil {
		t.Fatalf("GlyphMask('B') ok=%v", ok)
	}
	// The hole means at least one interior pixel is fully transparent while
	// the ring is fully opaque.
	var hasOpaque, hasClear bool
	for _, a := range mask.Pix {
		if a == 255 {
			hasOpaque = true
		}
		if a == 0 {
			hasClear = true
		}
	}
	if !hasOpaque || !hasClear {
		t.Errorf("hole glyph: hasOpaque=%v hasClear=%v", hasOpaque, hasClear)
	}
}

func TestGlyphMaskComposites(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(200)
	for _, r := range []rune{'E', 'F', 'G', 'H'} {
		_, mask, _, _, ok := fc.GlyphMask(r, 0, 0)
		if !ok || mask == nil {
			t.Errorf("composite %q: ok=%v mask=%v", r, ok, mask)
		}
	}
}

// GlyphMaskIndex renders a glyph selected by its index, and — for a glyph the
// cmap maps from a rune — yields the identical mask, bounds and advance that
// GlyphMask produces for that rune. This is the property a shaper relies on:
// the glyph indices it emits blit exactly like the rune-mapped glyphs.
func TestGlyphMaskIndexMatchesGlyphMask(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64)
	gid, ok := f.GlyphIndex('A')
	if !ok {
		t.Fatal("GlyphIndex('A') not mapped")
	}
	rb, rm, rp, ra, rok := fc.GlyphMask('A', 10, 40)
	ib, im, ip, ia, iok := fc.GlyphMaskIndex(gid, 10, 40)
	if !iok || !rok {
		t.Fatalf("ok mismatch: byIndex=%v byRune=%v", iok, rok)
	}
	if ib != rb || ip != rp || ia != ra {
		t.Fatalf("bounds/maskp/advance mismatch: index=(%v,%v,%d) rune=(%v,%v,%d)", ib, ip, ia, rb, rp, ra)
	}
	if im != rm { // same cached mask pointer
		t.Fatalf("mask mismatch: index=%p rune=%p", im, rm)
	}
	if ip != (image.Point{}) {
		t.Errorf("maskp=%v want origin", ip)
	}
}

// An out-of-range glyph index renders nothing and reports ok=false, mirroring
// GlyphMask's blank-for-unmapped contract.
func TestGlyphMaskIndexOutOfRange(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64)
	bounds, mask, _, adv, ok := fc.GlyphMaskIndex(GlyphIndex(f.NumGlyphs()+100), 0, 0)
	if ok || mask != nil || adv != 0 || !bounds.Empty() {
		t.Fatalf("out-of-range gid: ok=%v mask=%v adv=%d bounds=%v, want blank", ok, mask, adv, bounds)
	}
}

func TestGlyphMaskOffCurveSizes(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	// Large size: many flatten steps; tiny size: the steps<1 clamp.
	for _, size := range []int{300, 4} {
		fc := f.NewFace(size)
		if _, _, _, _, ok := fc.GlyphMask('C', 0, 0); !ok {
			t.Errorf("GlyphMask('C') at size %d not ok", size)
		}
	}
}

func TestGlyphMaskSpaceEmpty(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64)
	bounds, mask, _, adv, ok := fc.GlyphMask(' ', 3, 3)
	if !ok {
		t.Fatalf("space should be ok (mapped, empty)")
	}
	if mask != nil {
		t.Errorf("space mask=%v want nil", mask)
	}
	if !bounds.Empty() {
		t.Errorf("space bounds=%v want empty", bounds)
	}
	if adv <= 0 {
		t.Errorf("space advance=%d want positive", adv)
	}
}

func TestGlyphMaskUnmapped(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(64)
	if _, _, _, _, ok := fc.GlyphMask('Z', 0, 0); ok {
		t.Errorf("unmapped rune should give ok=false")
	}
}

func TestRepeatGlyphParses(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	if _, err := f.glyphContours(9); err != nil {
		t.Errorf("repeat glyph decode: %v", err)
	}
}

// --- container / table error paths ------------------------------------------

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		make func() []byte
		want string
	}{
		{"short header", func() []byte { return []byte{0, 1, 0, 0} }, "short header"},
		{"bad magic", func() []byte {
			b := stdBytes(false, false)
			binary.BigEndian.PutUint32(b, 0xDEADBEEF)
			return b
		}, "bad sfnt version"},
		{"OTTO without CFF", func() []byte {
			// An OTTO sfnt with no 'CFF ' table is malformed: it needs CFF
			// outlines but carries none (and no longer needs glyf/loca).
			b := stdBytes(false, false)
			binary.BigEndian.PutUint32(b, versionOTTO)
			return b
		}, `missing required table "CFF "`},
		{"truncated directory", func() []byte {
			b := stdBytes(false, false)
			binary.BigEndian.PutUint16(b[4:], 0xFFFF)
			return b
		}, "table directory"},
		{"table out of range", func() []byte {
			b := stdBytes(false, false)
			// glyf is directory record index 1; its length field is at 12+16+12.
			binary.BigEndian.PutUint32(b[12+16+12:], 0xFFFFFFFF)
			return b
		}, "out of range"},
		{"missing table", func() []byte {
			tb := stdTables(false, false)
			delete(tb, "glyf")
			return assemble(versionTrueType, tb)
		}, "missing required table"},
		{"head truncated", func() []byte {
			tb := stdTables(false, false)
			tb["head"] = tb["head"][:10]
			return assemble(versionTrueType, tb)
		}, "head table"},
		{"head zero unitsPerEm", func() []byte {
			tb := stdTables(false, false)
			tb["head"] = headTable(0, 0)
			return assemble(versionTrueType, tb)
		}, "unitsPerEm is zero"},
		{"head bad locformat", func() []byte {
			tb := stdTables(false, false)
			tb["head"] = headTable(1000, 2)
			return assemble(versionTrueType, tb)
		}, "indexToLocFormat"},
		{"maxp truncated", func() []byte {
			tb := stdTables(false, false)
			tb["maxp"] = tb["maxp"][:4]
			return assemble(versionTrueType, tb)
		}, "maxp table"},
		{"maxp zero glyphs", func() []byte {
			tb := stdTables(false, false)
			tb["maxp"] = maxpTable(0)
			return assemble(versionTrueType, tb)
		}, "zero glyphs"},
		{"hhea truncated", func() []byte {
			tb := stdTables(false, false)
			tb["hhea"] = tb["hhea"][:20]
			return assemble(versionTrueType, tb)
		}, "hhea table"},
		{"hhea bad numHMetrics", func() []byte {
			tb := stdTables(false, false)
			tb["hhea"] = hheaTable(800, -200, 100, 0)
			return assemble(versionTrueType, tb)
		}, "numberOfHMetrics"},
		{"hmtx truncated", func() []byte {
			tb := stdTables(false, false)
			tb["hmtx"] = tb["hmtx"][:10]
			return assemble(versionTrueType, tb)
		}, "hmtx table"},
		{"loca short truncated", func() []byte {
			tb := stdTables(false, false)
			tb["loca"] = tb["loca"][:4]
			return assemble(versionTrueType, tb)
		}, "loca table (short)"},
		{"loca long truncated", func() []byte {
			tb := stdTables(true, false)
			tb["loca"] = tb["loca"][:4]
			return assemble(versionTrueType, tb)
		}, "loca table (long)"},
		{"cmap header truncated", func() []byte {
			tb := stdTables(false, false)
			tb["cmap"] = []byte{0, 0}
			return assemble(versionTrueType, tb)
		}, "cmap header"},
		{"cmap record truncated", func() []byte {
			tb := stdTables(false, false)
			tb["cmap"] = []byte{0, 0, 0, 1} // numTables=1, no record
			return assemble(versionTrueType, tb)
		}, "cmap record"},
		{"cmap subtable offset", func() []byte {
			tb := stdTables(false, false)
			w := &bw{}
			w.u16(0)
			w.u16(1)
			w.u16(3)
			w.u16(1)
			w.u32(0xFFFF) // offset well past the table
			tb["cmap"] = w.bytes()
			return assemble(versionTrueType, tb)
		}, "cmap subtable offset"},
		{"cmap no supported subtable", func() []byte {
			tb := stdTables(false, false)
			tb["cmap"] = cmapTable([][]byte{{0, 1}}) // format 1, unsupported
			return assemble(versionTrueType, tb)
		}, "no supported cmap subtable"},
		{"cmap4 truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap4FromMap(std4Map())
			tb["cmap"] = cmapTable([][]byte{sub[:8]})
			return assemble(versionTrueType, tb)
		}, "cmap format 4"},
		{"cmap12 header truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap12FromMap(std12Map())
			tb["cmap"] = cmapTable([][]byte{sub[:8]})
			return assemble(versionTrueType, tb)
		}, "cmap format 12 header"},
		{"cmap12 groups truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap12FromMap(std12Map())
			tb["cmap"] = cmapTable([][]byte{sub[:16]})
			return assemble(versionTrueType, tb)
		}, "cmap format 12 groups"},
		{"cmap13 header truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap13Bytes([][3]uint32{{0x41, 0x43, 1}})
			tb["cmap"] = cmapTable([][]byte{sub[:8]})
			return assemble(versionTrueType, tb)
		}, "cmap format 13 header"},
		{"cmap13 groups truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap13Bytes([][3]uint32{{0x41, 0x43, 1}})
			tb["cmap"] = cmapTable([][]byte{sub[:16]})
			return assemble(versionTrueType, tb)
		}, "cmap format 13 groups"},
		{"cmap0 truncated", func() []byte {
			tb := stdTables(false, false)
			var arr [256]byte
			sub := cmap0Bytes(arr)
			tb["cmap"] = cmapTable([][]byte{sub[:10]})
			return assemble(versionTrueType, tb)
		}, "cmap format 0"},
		{"cmap6 header truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap6Bytes(0x41, []uint16{1, 2, 3})
			tb["cmap"] = cmapTable([][]byte{sub[:8]})
			return assemble(versionTrueType, tb)
		}, "cmap format 6 header"},
		{"cmap6 array truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap6Bytes(0x41, []uint16{1, 2, 3})
			tb["cmap"] = cmapTable([][]byte{sub[:len(sub)-1]})
			return assemble(versionTrueType, tb)
		}, "cmap format 6"},
		{"cmap2 keys truncated", func() []byte {
			tb := stdTables(false, false)
			var keys [256]uint16
			sub := cmap2Bytes(keys, []cmap2SubHeaderSpec{{}}, nil)
			tb["cmap"] = cmapTable([][]byte{sub[:10]})
			return assemble(versionTrueType, tb)
		}, "cmap format 2 keys"},
		{"cmap2 subheaders truncated", func() []byte {
			tb := stdTables(false, false)
			var keys [256]uint16
			sub := cmap2Bytes(keys, []cmap2SubHeaderSpec{{}}, nil)
			tb["cmap"] = cmapTable([][]byte{sub[:6+512+2]})
			return assemble(versionTrueType, tb)
		}, "cmap format 2 subheaders"},
		{"cmap14 header truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00}})
			tb["cmap"] = cmapTable([][]byte{sub[:8]})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 header"},
		{"cmap14 records truncated", func() []byte {
			tb := stdTables(false, false)
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00}})
			tb["cmap"] = cmapTable([][]byte{sub[:12]})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 records"},
		{"cmap14 default UVS offset out of range", func() []byte {
			tb := stdTables(false, false)
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00}})
			binary.BigEndian.PutUint32(sub[10+3:], 0xFFFFFF) // defaultUVSOffset
			tb["cmap"] = cmapTable([][]byte{sub})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 default UVS offset"},
		{"cmap14 default UVS header truncated", func() []byte {
			tb := stdTables(false, false)
			// One record (headerLen=21) with only a default table: defOff=21.
			// Leaving only 2 of the 4 numRanges bytes fails the header read.
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00, defaultRanges: [][2]uint32{{0x41, 0}}}})
			tb["cmap"] = cmapTable([][]byte{sub[:23]})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 default UVS header"},
		{"cmap14 default UVS ranges truncated", func() []byte {
			tb := stdTables(false, false)
			// numRanges (4 bytes at 21..25) is readable, but the one range
			// record (4 bytes at 25..29) is cut short.
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00, defaultRanges: [][2]uint32{{0x41, 0}}}})
			tb["cmap"] = cmapTable([][]byte{sub[:27]})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 default UVS ranges"},
		{"cmap14 non-default UVS offset out of range", func() []byte {
			tb := stdTables(false, false)
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00}})
			binary.BigEndian.PutUint32(sub[10+7:], 0xFFFFFF) // nonDefaultUVSOffset
			tb["cmap"] = cmapTable([][]byte{sub})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 non-default UVS offset"},
		{"cmap14 non-default UVS header truncated", func() []byte {
			tb := stdTables(false, false)
			// One record (headerLen=21) with only a non-default table:
			// nonDefOff=21. Leaving only 2 of the 4 numMappings bytes fails
			// the header read.
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00, nonDefault: map[uint32]uint16{0x41: 7}}})
			tb["cmap"] = cmapTable([][]byte{sub[:23]})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 non-default UVS header"},
		{"cmap14 non-default UVS mappings truncated", func() []byte {
			tb := stdTables(false, false)
			// numMappings (4 bytes at 21..25) is readable, but the one
			// mapping record (5 bytes at 25..30) is cut short.
			sub := cmap14Bytes([]cmap14Selector{{varSelector: 0xFE00, nonDefault: map[uint32]uint16{0x41: 7}}})
			tb["cmap"] = cmapTable([][]byte{sub[:27]})
			return assemble(versionTrueType, tb)
		}, "cmap format 14 non-default UVS mappings"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.make())
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// --- cmap selection ---------------------------------------------------------

func TestCmapPreferFormat12(t *testing.T) {
	for _, order := range [][][]byte{
		{cmap4FromMap(std4Map()), cmap12FromMap(std12Map())},
		{cmap12FromMap(std12Map()), cmap4FromMap(std4Map())},
	} {
		tb := stdTables(false, false)
		tb["cmap"] = cmapTable(order)
		f := mustParse(t, assemble(versionTrueType, tb))
		if _, ok := f.GlyphIndex(0x1F600); !ok {
			t.Errorf("format 12 should be selected (astral rune mappable)")
		}
	}
}

func TestCmap12Lookup(t *testing.T) {
	f := mustParse(t, stdBytes(false, true))
	cases := []struct {
		r    rune
		want bool
	}{
		{'A', true},       // found
		{0x1F600, true},   // found in a high group
		{0x30, false},     // gap between groups
		{0x10FFFF, false}, // above all groups
		{0x00, false},     // below all groups
	}
	for _, c := range cases {
		if _, ok := f.GlyphIndex(c.r); ok != c.want {
			t.Errorf("cmap12 lookup %#x = %v want %v", c.r, ok, c.want)
		}
	}
}

func TestCmap13Lookup(t *testing.T) {
	tb := stdTables(false, false)
	// Two constant-map groups: 0x41..0x43 all map to glyph 1; a high fallback
	// block 0xF0000..0xF00FF collapses onto a single glyph 2 (the format-13
	// LastResort/fallback use case).
	tb["cmap"] = cmapTable([][]byte{cmap13Bytes([][3]uint32{{0x41, 0x43, 1}, {0xF0000, 0xF00FF, 2}})})
	f := mustParse(t, assemble(versionTrueType, tb))
	cases := []struct {
		r    rune
		want GlyphIndex
		ok   bool
	}{
		{'A', 1, true},       // start of the first group
		{'C', 1, true},       // constant glyph across the whole range
		{0xF0050, 2, true},   // constant glyph in the high fallback block
		{0x40, 0, false},     // below all groups
		{0x44, 0, false},     // gap between groups
		{0x10FFFF, 0, false}, // above all groups
	}
	for _, c := range cases {
		g, ok := f.GlyphIndex(c.r)
		if ok != c.ok || (ok && g != c.want) {
			t.Errorf("cmap13 lookup %#x = (%d,%v) want (%d,%v)", c.r, g, ok, c.want, c.ok)
		}
	}
}

func TestCmap4IdRangeOffset(t *testing.T) {
	// Segment 0 covers 0x41..0x43 via idRangeOffset into glyphIdArray;
	// segment 1 is the 0xFFFF sentinel.
	ends := []uint16{0x43, 0xFFFF}
	starts := []uint16{0x41, 0xFFFF}
	deltas := []int16{0, 1}
	ranges := []uint16{4, 0} // 4/2 - (2-0) + (c-0x41) => glyphIdArray index
	glyphIDs := []uint16{5, 0, 7}
	sub := rawCmap4(ends, starts, deltas, ranges, glyphIDs)

	tb := stdTables(false, false)
	tb["cmap"] = cmapTable([][]byte{sub})
	f := mustParse(t, assemble(versionTrueType, tb))

	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 5 {
		t.Errorf("0x41 -> %d,%v want 5,true", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x42); ok { // glyphIdArray entry 0 -> unmapped
		t.Errorf("0x42 maps to glyph 0, want unmapped")
	}
	if gid, ok := f.GlyphIndex(0x43); !ok || gid != 7 {
		t.Errorf("0x43 -> %d,%v want 7,true", gid, ok)
	}
}

func TestCmap4IdRangeOutOfBounds(t *testing.T) {
	ends := []uint16{0x43, 0xFFFF}
	starts := []uint16{0x41, 0xFFFF}
	deltas := []int16{0, 1}
	ranges := []uint16{4, 0}
	glyphIDs := []uint16{5} // too short: 0x43 indexes past the end
	sub := rawCmap4(ends, starts, deltas, ranges, glyphIDs)

	tb := stdTables(false, false)
	tb["cmap"] = cmapTable([][]byte{sub})
	f := mustParse(t, assemble(versionTrueType, tb))
	if _, ok := f.GlyphIndex(0x43); ok {
		t.Errorf("0x43 index out of range should be unmapped")
	}
}

func TestCmap4NoSentinel(t *testing.T) {
	// Without the 0xFFFF sentinel, a lookup past the last endCode exhausts
	// the segment loop.
	ends := []uint16{0x43}
	starts := []uint16{0x41}
	deltas := []int16{int16(1 - 0x41)}
	ranges := []uint16{0}
	sub := rawCmap4(ends, starts, deltas, ranges, nil)

	tb := stdTables(false, false)
	tb["cmap"] = cmapTable([][]byte{sub})
	f := mustParse(t, assemble(versionTrueType, tb))
	if _, ok := f.GlyphIndex(0x50); ok {
		t.Errorf("rune past last segment should be unmapped")
	}
	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 1 {
		t.Errorf("0x41 -> %d,%v want 1,true", gid, ok)
	}
}

// cmapOnlyFont builds a minimal 3-glyph font whose cmap table is exactly the
// provided subtables, so a lookup exercises a chosen format in isolation.
func cmapOnlyFont(t *testing.T, subtables [][]byte) *Font {
	t.Helper()
	glyphs := [][]byte{nil, nil, nil} // 3 empty glyphs (0..2 valid indices)
	loca, glyf := glyfAndLoca(glyphs, false)
	adv := []int{300, 300, 300}
	lsb := []int{10, 10, 10}
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(len(glyphs)),
		"hhea": hheaTable(800, -200, 100, len(glyphs)),
		"hmtx": hmtxTable(adv, lsb, len(glyphs)),
		"cmap": cmapTable(subtables),
		"loca": loca,
		"glyf": glyf,
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

func TestCmap0Lookup(t *testing.T) {
	var arr [256]byte
	arr[0x41] = 1 // 'A' -> glyph 1
	arr[0x42] = 2 // 'B' -> glyph 2
	// 0x43 left 0 -> unmapped
	f := cmapOnlyFont(t, [][]byte{cmap0Bytes(arr)})

	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 1 {
		t.Errorf("0x41 -> %d,%v want 1,true", gid, ok)
	}
	if gid, ok := f.GlyphIndex(0x42); !ok || gid != 2 {
		t.Errorf("0x42 -> %d,%v want 2,true", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x43); ok {
		t.Errorf("0x43 maps to glyph 0, want unmapped")
	}
	if _, ok := f.GlyphIndex(0x100); ok { // out of the 0..255 range
		t.Errorf("0x100 out of range, want unmapped")
	}
}

func TestCmap6Lookup(t *testing.T) {
	// Range 0x41..0x43 -> glyphs {1, 0, 2}; a 0 entry is unmapped.
	f := cmapOnlyFont(t, [][]byte{cmap6Bytes(0x41, []uint16{1, 0, 2})})

	if _, ok := f.GlyphIndex(0x40); ok { // below firstCode
		t.Errorf("0x40 below range, want unmapped")
	}
	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 1 {
		t.Errorf("0x41 -> %d,%v want 1,true", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x42); ok { // entry 0 -> unmapped
		t.Errorf("0x42 maps to glyph 0, want unmapped")
	}
	if gid, ok := f.GlyphIndex(0x43); !ok || gid != 2 {
		t.Errorf("0x43 -> %d,%v want 2,true", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x44); ok { // past the last entry
		t.Errorf("0x44 past range, want unmapped")
	}
}

func TestCmap6Empty(t *testing.T) {
	// An empty trimmed range: every lookup falls out via the length check.
	f := cmapOnlyFont(t, [][]byte{cmap6Bytes(0x41, nil), cmap0Bytes([256]byte{})})
	if _, ok := f.GlyphIndex(0x41); ok {
		t.Errorf("empty format-6 range: 0x41 should be unmapped")
	}
}

func TestCmap2SingleAndDoubleByte(t *testing.T) {
	// subHeader 0 (single-byte, high byte 0): firstCode=0x41, entryCount=2.
	// subHeader 1 (double-byte, high byte 0x81): firstCode=0x40, entryCount=1.
	var keys [256]uint16
	keys[0x81] = 8 // high byte 0x81 -> subHeader index 1 (offset 8)

	subs := []cmap2SubHeaderSpec{
		{firstCode: 0x41, entryCount: 2, idDelta: 0},
		{firstCode: 0x40, entryCount: 1, idDelta: 0},
	}
	// Two subHeaders: subHeader 0's idRangeOffset field precedes
	// glyphIndexArray by 2*8-6 = 10 bytes; to point at array[0] set 10.
	// subHeader 1's field precedes array by 8-6 = 2 bytes; to point at
	// array[2] set 2 + 2*2 = 6.
	subs[0].idRangeOffset = 10
	subs[1].idRangeOffset = 6
	glyphIndexArray := []uint16{1, 0, 5} // array[0]=1('A'), array[1]=0(unmapped), array[2]=5

	f := cmapOnlyFont(t, [][]byte{cmap2Bytes(keys, subs, glyphIndexArray)})

	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 1 { // single-byte
		t.Errorf("0x41 -> %d,%v want 1,true", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x42); ok { // array entry 0 -> unmapped
		t.Errorf("0x42 maps to glyph 0, want unmapped")
	}
	if _, ok := f.GlyphIndex(0x43); ok { // outside entryCount
		t.Errorf("0x43 outside entryCount, want unmapped")
	}
	if gid, ok := f.GlyphIndex(0x8140); !ok || gid != 5 { // double-byte
		t.Errorf("0x8140 -> %d,%v want 5,true", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x8141); ok { // outside entryCount
		t.Errorf("0x8141 outside entryCount, want unmapped")
	}
	if _, ok := f.GlyphIndex(0x10000); ok { // above 0xFFFF
		t.Errorf("0x10000 out of range, want unmapped")
	}
}

func TestCmap2IdDeltaAndNoRange(t *testing.T) {
	// subHeader 0 applies idDelta; high byte 0x82 has idRangeOffset==0
	// (unmapped) and 0x83 indexes past glyphIndexArray (out of bounds).
	var keys [256]uint16
	keys[0x82] = 8  // -> subHeader 1: idRangeOffset 0 (unmapped)
	keys[0x83] = 16 // -> subHeader 2: idRangeOffset points past array (oob)

	subs := []cmap2SubHeaderSpec{
		{firstCode: 0x41, entryCount: 1, idDelta: 3}, // idRangeOffset set below
		{firstCode: 0x00, entryCount: 1, idDelta: 0}, // idRangeOffset 0 => unmapped
		{firstCode: 0x00, entryCount: 1, idDelta: 0, idRangeOffset: 0xFFFF},
	}
	// 3 subHeaders: subHeader 0's field precedes array by 3*8-6 = 18 bytes;
	// point at array[0] with 18.
	subs[0].idRangeOffset = 18
	glyphIndexArray := []uint16{10} // 'A' -> array[0]=10, +idDelta 3 = 13

	f := cmapOnlyFont(t, [][]byte{cmap2Bytes(keys, subs, glyphIndexArray)})

	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 13 {
		t.Errorf("0x41 -> %d,%v want 13,true (idDelta)", gid, ok)
	}
	if _, ok := f.GlyphIndex(0x8200); ok { // idRangeOffset==0
		t.Errorf("0x8200 idRangeOffset 0, want unmapped")
	}
	if _, ok := f.GlyphIndex(0x8300); ok { // index past glyphIndexArray
		t.Errorf("0x8300 index out of range, want unmapped")
	}
}

func TestCmap2ZeroGlyphMaps(t *testing.T) {
	// A glyphIndexArray entry of 0 (after selection) maps to .notdef.
	var keys [256]uint16
	// One subHeader: its idRangeOffset field precedes glyphIndexArray by
	// 1*8-6 = 2 bytes, so idRangeOffset 2 points at array[0].
	subs := []cmap2SubHeaderSpec{{firstCode: 0x41, entryCount: 1, idDelta: 0, idRangeOffset: 2}}
	glyphIndexArray := []uint16{0}
	f := cmapOnlyFont(t, [][]byte{cmap2Bytes(keys, subs, glyphIndexArray), cmap0Bytes([256]byte{})})
	if _, ok := f.GlyphIndex(0x41); ok {
		t.Errorf("0x41 maps to glyph 0, want unmapped")
	}
}

func TestCmap14Variation(t *testing.T) {
	// Base cmap (format 4) maps 'A'(0x41)->1, 'B'(0x42)->2.
	base := cmap4FromMap(map[rune]uint16{0x41: 1, 0x42: 2})
	// VS 0xFE00: non-default 0x41 -> glyph 2; default range covering 0x42.
	// VS 0xFE01: default range covering 0x50 (which the base cmap lacks).
	vs := cmap14Bytes([]cmap14Selector{
		{
			varSelector:   0xFE00,
			defaultRanges: [][2]uint32{{0x42, 0}}, // 0x42 default -> base cmap
			nonDefault:    map[uint32]uint16{0x41: 2},
		},
		{
			varSelector:   0xFE01,
			defaultRanges: [][2]uint32{{0x50, 0}}, // base cmap has no 0x50
		},
	})
	f := cmapOnlyFont(t, [][]byte{base, vs})

	// Ordinary GlyphIndex is unaffected by the format-14 side table.
	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 1 {
		t.Errorf("plain 0x41 -> %d,%v want 1,true", gid, ok)
	}

	// Non-default UVS: (0x41, VS 0xFE00) -> explicit glyph 2.
	if gid, ok := f.GlyphIndexVariation(0x41, 0xFE00); !ok || gid != 2 {
		t.Errorf("(0x41,FE00) -> %d,%v want 2,true", gid, ok)
	}
	// Default UVS: (0x42, VS 0xFE00) -> resolve through base cmap -> glyph 2.
	if gid, ok := f.GlyphIndexVariation(0x42, 0xFE00); !ok || gid != 2 {
		t.Errorf("(0x42,FE00) -> %d,%v want 2,true", gid, ok)
	}
	// A base rune in neither the non-default nor default sets: not-ok.
	if _, ok := f.GlyphIndexVariation(0x43, 0xFE00); ok {
		t.Errorf("(0x43,FE00) should be unmapped")
	}
	// Default UVS whose base rune the ordinary cmap lacks: falls back and
	// reports the ordinary cmap's not-ok.
	if _, ok := f.GlyphIndexVariation(0x50, 0xFE01); ok {
		t.Errorf("(0x50,FE01) base rune absent from cmap, want unmapped")
	}
	// An unregistered variation selector: not-ok.
	if _, ok := f.GlyphIndexVariation(0x41, 0xFE0F); ok {
		t.Errorf("(0x41,FE0F) unknown selector, want unmapped")
	}
}

func TestCmap14NoVSTable(t *testing.T) {
	// A font with no format-14 subtable: GlyphIndexVariation is always not-ok.
	f := cmapOnlyFont(t, [][]byte{cmap4FromMap(map[rune]uint16{0x41: 1})})
	if _, ok := f.GlyphIndexVariation(0x41, 0xFE00); ok {
		t.Errorf("no format-14 table: GlyphIndexVariation should be unmapped")
	}
}

func TestCmapSelectionPrefersRicherFormat(t *testing.T) {
	// When several base formats coexist, the richest (12 > 4 > 6 > 2 > 0) wins.
	// Formats 0 and 6 both map 'A', but the format-6 mapping ('A'->2) should
	// be selected over format 0 ('A'->1).
	var arr [256]byte
	arr[0x41] = 1
	f := cmapOnlyFont(t, [][]byte{cmap0Bytes(arr), cmap6Bytes(0x41, []uint16{2})})
	if gid, ok := f.GlyphIndex(0x41); !ok || gid != 2 {
		t.Errorf("richer format-6 should win: 0x41 -> %d,%v want 2,true", gid, ok)
	}
}

// --- glyf-level error paths -------------------------------------------------

func TestGlyfErrors(t *testing.T) {
	// zero-contour simple glyph decodes to no contours.
	zero := &bw{}
	zero.i16(0)
	zero.bbox()
	zero.u16(0) // instructionLength

	// simple glyph truncated after the header (endPts unreadable).
	truncSimple := &bw{}
	truncSimple.i16(1)
	truncSimple.bbox()

	// flags unreadable.
	truncFlags := &bw{}
	truncFlags.i16(1)
	truncFlags.bbox()
	truncFlags.u16(0) // endPts[0]=0 -> 1 point
	truncFlags.u16(0) // instructionLength

	// oversized instructionLength -> skip past the end.
	skipFail := &bw{}
	skipFail.i16(1)
	skipFail.bbox()
	skipFail.u16(0)    // endPts[0]=0
	skipFail.u16(9999) // instructionLength huge

	// composite truncated before its scale value.
	truncComp := &bw{}
	truncComp.i16(-1)
	truncComp.bbox()
	truncComp.u16(compArgsAreXY | compWeHaveScale)
	truncComp.u16(1) // child glyph index
	truncComp.u8(0)
	truncComp.u8(0) // args, but no scale bytes follow

	pointMatch := compositeGlyphBytes([]component{{glyphIndex: 1, argsAreXY: false}})
	outOfRange := compositeGlyphBytes([]component{{glyphIndex: 200, argsAreXY: true}})
	cyclic := compositeGlyphBytes([]component{{glyphIndex: 1, argsAreXY: true}}) // refers to itself

	tests := []struct {
		name string
		blob []byte
		want string
	}{
		{"trunc simple", truncSimple.bytes(), "simple glyph"},
		{"trunc flags", truncFlags.bytes(), "simple glyph"},
		{"skip fail", skipFail.bytes(), "simple glyph"},
		{"trunc composite", truncComp.bytes(), "composite glyph"},
		{"point matching", pointMatch, "point matching"},
		{"out of range", outOfRange, "out of range"},
		{"cyclic", cyclic, "cyclic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := fontWithGlyphs(t, [][]byte{tc.blob})
			_, err := f.glyphContours(1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("glyphContours error=%v want containing %q", err, tc.want)
			}
		})
	}

	// zero-contour glyph: no error, no contours.
	f := fontWithGlyphs(t, [][]byte{zero.bytes()})
	cs, err := f.glyphContours(1)
	if err != nil || len(cs) != 0 {
		t.Fatalf("zero-contour glyph: contours=%v err=%v", cs, err)
	}
}

func TestCompositeDepthLimit(t *testing.T) {
	// Build a non-cyclic chain glyph1 -> glyph2 -> ... deeper than the limit.
	const chain = maxCompositeDepth + 3
	blobs := make([][]byte, chain)
	for i := 0; i < chain-1; i++ {
		blobs[i] = compositeGlyphBytes([]component{{glyphIndex: uint16(i + 2), argsAreXY: true}})
	}
	blobs[chain-1] = simpleGlyphBytes([][]point{sqCCW(0, 10)}) // terminal
	f := fontWithGlyphs(t, blobs)
	if _, err := f.glyphContours(1); err == nil ||
		!strings.Contains(err.Error(), "nesting too deep") {
		t.Fatalf("deep chain error=%v want nesting too deep", err)
	}
}

func TestLocaCorruption(t *testing.T) {
	f := fontWithGlyphs(t, [][]byte{simpleGlyphBytes([][]point{sqCCW(0, 10)})})
	f.loca[1] = f.loca[2] + 4 // start > end
	if _, err := f.glyphContours(1); err == nil ||
		!strings.Contains(err.Error(), "not monotonic") {
		t.Fatalf("loca non-monotonic error=%v", err)
	}

	g := fontWithGlyphs(t, [][]byte{simpleGlyphBytes([][]point{sqCCW(0, 10)})})
	g.loca[2] = uint32(len(g.glyf) + 100) // end past glyf
	if _, err := g.glyphContours(1); err == nil ||
		!strings.Contains(err.Error(), "unexpected end") {
		t.Fatalf("loca past glyf error=%v", err)
	}
}

func TestRenderDegenerateContour(t *testing.T) {
	// A glyph with a normal contour plus a single-point contour: the degenerate
	// contour is dropped by the rasteriser (len(poly) < 3).
	blob := simpleGlyphBytes([][]point{sqCCW(10, 60), {{5, 5, true}}})
	f := fontWithGlyphs(t, [][]byte{blob})
	fc := f.NewFace(64)
	cg := fc.render(1)
	if !cg.ok || cg.mask == nil {
		t.Fatalf("render degenerate: ok=%v mask=%v", cg.ok, cg.mask)
	}
}

func TestRenderCorruptGlyphNotOK(t *testing.T) {
	f := fontWithGlyphs(t, [][]byte{compositeGlyphBytes([]component{{glyphIndex: 1, argsAreXY: true}})})
	fc := f.NewFace(64)
	// 'A' -> glyph 1 which is a self-referential composite: render fails, so
	// GlyphMask reports not-ok.
	if _, _, _, _, ok := fc.GlyphMask('A', 0, 0); ok {
		t.Errorf("corrupt glyph should give ok=false")
	}
}

// --- low-level unit coverage ------------------------------------------------

func TestFlattenDegenerate(t *testing.T) {
	if p := flattenContour(contour{{x: 1, y: 2, on: true}}, 1.0); p != nil {
		t.Errorf("single-point contour should flatten to nil, got %v", p)
	}
}

func TestRasterizeEmpty(t *testing.T) {
	r, m := rasterize(nil)
	if !r.Empty() || m != nil {
		t.Errorf("rasterize(nil)=%v,%v want empty,nil", r, m)
	}
}

func TestErrorsUnwrap(t *testing.T) {
	_, err := Parse([]byte{0, 0, 1, 0})
	if !errors.Is(err, errTruncated) {
		t.Errorf("short header should wrap errTruncated")
	}
}
