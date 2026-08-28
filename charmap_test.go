// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

// cmapRecord is one encoding record of a synthetic cmap table: which platform
// and encoding a subtable claims to be written for.
type cmapRecord struct {
	platform uint16
	encoding uint16
	sub      []byte
}

// cmapTableWith builds a cmap table whose records carry the platform and
// encoding each caller asks for, which cmapTable does not let a test choose.
func cmapTableWith(records []cmapRecord) []byte {
	w := &bw{}
	w.u16(0)
	w.u16(uint16(len(records)))
	off := 4 + 8*len(records)
	body := &bw{}
	for _, rec := range records {
		w.u16(rec.platform)
		w.u16(rec.encoding)
		w.u32(uint32(off))
		body.b = append(body.b, rec.sub...)
		off += len(rec.sub)
	}
	return append(w.bytes(), body.b...)
}

// symbolAndUnicodeFont is the shape this API exists for: a symbolic TrueType
// font addressed through a Microsoft Symbol subtable at 0xF000 + code, whose
// Microsoft Unicode subtable says which character each of those glyphs is.
func symbolAndUnicodeFont(t *testing.T) *Font {
	t.Helper()
	tb := stdTables(false, false)
	tb["cmap"] = cmapTableWith([]cmapRecord{
		{3, 0, cmap4FromMap(map[rune]uint16{0xF041: 1, 0xF042: 2})},
		{1, 0, cmap0Bytes(func() (a [256]byte) { a['A'] = 1; return }())},
		{3, 1, cmap4FromMap(map[rune]uint16{'A': 1, 'B': 2})},
	})
	return mustParse(t, assemble(versionTrueType, tb))
}

func TestCharacterMapsListed(t *testing.T) {
	f := symbolAndUnicodeFont(t)
	if n := f.NumCharacterMaps(); n != 3 {
		t.Fatalf("NumCharacterMaps = %d want 3", n)
	}
	want := [][3]uint16{{3, 0, 4}, {1, 0, 0}, {3, 1, 4}}
	for i, w := range want {
		p, e, format, ok := f.CharacterMap(i)
		if !ok || p != w[0] || e != w[1] || format != w[2] {
			t.Errorf("CharacterMap(%d) = (%d,%d,%d,%v) want (%d,%d,%d,true)", i, p, e, format, ok, w[0], w[1], w[2])
		}
	}
}

func TestCharacterMapIndexOutOfRange(t *testing.T) {
	f := symbolAndUnicodeFont(t)
	for _, i := range []int{-1, 3} {
		if _, _, _, ok := f.CharacterMap(i); ok {
			t.Errorf("CharacterMap(%d) reported a subtable", i)
		}
		if _, ok := f.GlyphIndexInMap(i, 'A'); ok {
			t.Errorf("GlyphIndexInMap(%d) reported a glyph", i)
		}
		if _, ok := f.RuneOfGlyphInMap(i, 1); ok {
			t.Errorf("RuneOfGlyphInMap(%d) reported a rune", i)
		}
	}
}

func TestGlyphIndexInMapAddressesOneSubtable(t *testing.T) {
	f := symbolAndUnicodeFont(t)
	// The symbol subtable knows 0xF041, not 'A'; the Unicode one is the other
	// way about. Addressing them apart is the whole point.
	if g, ok := f.GlyphIndexInMap(0, 0xF041); !ok || g != 1 {
		t.Errorf("symbol subtable 0xF041 = (%d,%v) want (1,true)", g, ok)
	}
	if _, ok := f.GlyphIndexInMap(0, 'A'); ok {
		t.Errorf("symbol subtable should not know 'A'")
	}
	if g, ok := f.GlyphIndexInMap(2, 'B'); !ok || g != 2 {
		t.Errorf("unicode subtable 'B' = (%d,%v) want (2,true)", g, ok)
	}
}

func TestRuneOfGlyphInMapInvertsAndCaches(t *testing.T) {
	f := symbolAndUnicodeFont(t)
	if r, ok := f.RuneOfGlyphInMap(2, 1); !ok || r != 'A' {
		t.Errorf("RuneOfGlyphInMap(2, 1) = (%q,%v) want ('A',true)", r, ok)
	}
	// The second call answers from the inverse built by the first.
	if f.cmaps[2].inverse == nil {
		t.Fatal("inverse was not kept")
	}
	if r, ok := f.RuneOfGlyphInMap(2, 2); !ok || r != 'B' {
		t.Errorf("RuneOfGlyphInMap(2, 2) = (%q,%v) want ('B',true)", r, ok)
	}
	if _, ok := f.RuneOfGlyphInMap(2, 9); ok {
		t.Errorf("no code reaches glyph 9; it should not be inverted")
	}
}

func TestNoCharacterMapsWithoutACmapTable(t *testing.T) {
	tb := stdTables(false, false)
	delete(tb, "cmap")
	f := mustParse(t, assemble(versionTrueType, tb))
	if n := f.NumCharacterMaps(); n != 0 {
		t.Errorf("NumCharacterMaps = %d want 0", n)
	}
}

// --- inverting each format --------------------------------------------------

func TestReverseFormat4(t *testing.T) {
	// idRangeOffset segments as well as delta ones, an unmapped code inside a
	// segment, and two codes on one glyph so the lowest has to win.
	c, err := parseCmap4(rawCmap4(
		[]uint16{0x43, 0xFFFF},
		[]uint16{0x41, 0xFFFF},
		[]int16{0, 1},
		[]uint16{4, 0},
		[]uint16{7, 0, 7}, // 'A'->7, 'B'->notdef, 'C'->7
	))
	if err != nil {
		t.Fatalf("parseCmap4: %v", err)
	}
	inv := c.reverse(maxReverseCodes)
	if got, ok := inv[7]; !ok || got != 'A' {
		t.Errorf("inverse[7] = (%q,%v) want ('A',true)", got, ok)
	}
	if len(inv) != 1 {
		t.Errorf("inverse has %d entries want 1: %v", len(inv), inv)
	}
}

func TestReverseFormat4SkipsBackwardsSegmentAndSpendsItsBudget(t *testing.T) {
	// A segment whose start is past its end describes nothing, and a walk with
	// no budget left describes nothing more.
	c, err := parseCmap4(rawCmap4(
		[]uint16{0x30, 0x43, 0xFFFF},
		[]uint16{0x40, 0x41, 0xFFFF}, // first segment runs backwards
		[]int16{0, 0, 1},
		[]uint16{0, 0, 0},
		nil,
	))
	if err != nil {
		t.Fatalf("parseCmap4: %v", err)
	}
	if inv := c.reverse(maxReverseCodes); len(inv) != 3 {
		t.Errorf("inverse has %d entries want 3: %v", len(inv), inv)
	}
	if inv := c.reverse(2); len(inv) != 2 {
		t.Errorf("budget 2 gave %d entries want 2: %v", len(inv), inv)
	}
	if inv := c.reverse(0); len(inv) != 0 {
		t.Errorf("budget 0 gave %d entries want 0: %v", len(inv), inv)
	}
}

func TestReverseFormat12(t *testing.T) {
	c, err := parseCmap12(cmap12Bytes([][3]uint32{
		{0x41, 0x43, 5}, // 'A'..'C' onto glyphs 5..7
		{0x50, 0x40, 9}, // backwards: describes nothing
		{0x60, 0x62, 5}, // the same glyphs again, by higher codes
		{0x70, 0x71, 0}, // starting at .notdef, which is no character
	}))
	if err != nil {
		t.Fatalf("parseCmap12: %v", err)
	}
	inv := c.reverse(maxReverseCodes)
	for gid, want := range map[GlyphIndex]rune{5: 'A', 6: 'B', 7: 'C', 1: 0x71} {
		if got, ok := inv[gid]; !ok || got != want {
			t.Errorf("inverse[%d] = (%#x,%v) want (%#x,true)", gid, got, ok, want)
		}
	}
	if _, ok := inv[0]; ok {
		t.Error(".notdef should not be inverted")
	}
	if inv := c.reverse(2); len(inv) != 2 {
		t.Errorf("budget 2 gave %d entries want 2: %v", len(inv), inv)
	}
	if inv := c.reverse(0); len(inv) != 0 {
		t.Errorf("budget 0 gave %d entries want 0: %v", len(inv), inv)
	}
}

func TestReverseFormat8(t *testing.T) {
	c, err := parseCmap8(cmap8Bytes([][3]uint32{
		{0x41, 0x42, 5}, // 'A','B' onto glyphs 5,6
		{0x50, 0x40, 9}, // backwards: describes nothing
	}))
	if err != nil {
		t.Fatalf("parseCmap8: %v", err)
	}
	inv := c.reverse(maxReverseCodes)
	if got, ok := inv[5]; !ok || got != 'A' {
		t.Errorf("inverse[5] = (%q,%v) want ('A',true)", got, ok)
	}
	if len(inv) != 2 {
		t.Errorf("inverse has %d entries want 2: %v", len(inv), inv)
	}
	if inv := c.reverse(1); len(inv) != 1 {
		t.Errorf("budget 1 gave %d entries want 1: %v", len(inv), inv)
	}
	if inv := c.reverse(0); len(inv) != 0 {
		t.Errorf("budget 0 gave %d entries want 0: %v", len(inv), inv)
	}
}

func TestReverseFormat13(t *testing.T) {
	c, err := parseCmap13(cmap13Bytes([][3]uint32{
		{0xE000, 0xF8FF, 4}, // a whole private-use block on one glyph
		{0x50, 0x40, 9},     // backwards: describes nothing
		{0x60, 0x61, 0},     // onto .notdef
		{0x70, 0x71, 4},     // the same glyph again, by higher codes
	}))
	if err != nil {
		t.Fatalf("parseCmap13: %v", err)
	}
	inv := c.reverse(maxReverseCodes)
	if got, ok := inv[4]; !ok || got != 0xE000 {
		t.Errorf("inverse[4] = (%#x,%v) want (0xE000,true)", got, ok)
	}
	if len(inv) != 1 {
		t.Errorf("inverse has %d entries want 1: %v", len(inv), inv)
	}
}

func TestReverseFormat0(t *testing.T) {
	var a [256]byte
	a['A'] = 3
	a['B'] = 0 // .notdef
	a['C'] = 3 // the same glyph by a higher code
	c, err := parseCmap0(cmap0Bytes(a))
	if err != nil {
		t.Fatalf("parseCmap0: %v", err)
	}
	inv := c.reverse(0)
	if got, ok := inv[3]; !ok || got != 'A' {
		t.Errorf("inverse[3] = (%q,%v) want ('A',true)", got, ok)
	}
	if len(inv) != 1 {
		t.Errorf("inverse has %d entries want 1: %v", len(inv), inv)
	}
}

func TestReverseFormat6(t *testing.T) {
	c, err := parseCmap6(cmap6Bytes(0x41, []uint16{3, 0, 3}))
	if err != nil {
		t.Fatalf("parseCmap6: %v", err)
	}
	inv := c.reverse(0)
	if got, ok := inv[3]; !ok || got != 'A' {
		t.Errorf("inverse[3] = (%q,%v) want ('A',true)", got, ok)
	}
	if len(inv) != 1 {
		t.Errorf("inverse has %d entries want 1: %v", len(inv), inv)
	}
}

func TestReverseFormat10(t *testing.T) {
	c, err := parseCmap10(cmap10Bytes(0x1F600, []uint16{3, 0, 3}))
	if err != nil {
		t.Fatalf("parseCmap10: %v", err)
	}
	inv := c.reverse(0)
	if got, ok := inv[3]; !ok || got != 0x1F600 {
		t.Errorf("inverse[3] = (%#x,%v) want (0x1F600,true)", got, ok)
	}
	if len(inv) != 1 {
		t.Errorf("inverse has %d entries want 1: %v", len(inv), inv)
	}
}

func TestReverseFormat2(t *testing.T) {
	// One single-byte subHeader covering 'A'..'C': 'A' and 'C' share glyph 4,
	// 'B' is unmapped. Inverting it has to keep 'A', the lower of the two.
	var keys [256]uint16
	subs := []cmap2SubHeaderSpec{{firstCode: 0x41, entryCount: 3, idRangeOffset: 2}}
	c, err := parseCmap2(cmap2Bytes(keys, subs, []uint16{4, 0, 4}))
	if err != nil {
		t.Fatalf("parseCmap2: %v", err)
	}
	inv := c.reverse(0)
	if got, ok := inv[4]; !ok || got != 'A' {
		t.Errorf("inverse[4] = (%q,%v) want ('A',true)", got, ok)
	}
	if len(inv) != 1 {
		t.Errorf("inverse has %d entries want 1: %v", len(inv), inv)
	}
}
