// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"testing"
)

func postNames2(indices []uint16, names ...string) []byte {
	b := postTable(-9, true)
	b = binary.BigEndian.AppendUint16(b, uint16(len(indices)))
	for _, index := range indices {
		b = binary.BigEndian.AppendUint16(b, index)
	}
	for _, name := range names {
		b = append(b, byte(len(name)))
		b = append(b, name...)
	}
	return b
}

func postNames25(offsets []int8) []byte {
	b := postTable(0, false)
	binary.BigEndian.PutUint32(b, 0x00025000)
	b = binary.BigEndian.AppendUint16(b, uint16(len(offsets)))
	for _, offset := range offsets {
		b = append(b, byte(offset))
	}
	return b
}

func postFontTables(n int, post []byte) map[string][]byte {
	glyphs := make([][]byte, n)
	glyphs[1] = squareGlyph()
	loca, glyf := glyfAndLoca(glyphs, false)
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 0, n),
		"hmtx": hmtxTable(make([]int, n), make([]int, n), n),
		"loca": loca,
		"glyf": glyf,
	}
	if post != nil {
		tables["post"] = post
	}
	return tables
}

func checkPostName(t *testing.T, f *Font, gid GlyphIndex, name string) {
	t.Helper()
	if got, ok := f.GlyphName(gid); !ok || got != name {
		t.Errorf("GlyphName(%d) = %q, %v; want %q, true", gid, got, ok, name)
	}
	if got, ok := f.GlyphIndexByName(name); !ok || got != gid {
		t.Errorf("GlyphIndexByName(%q) = %d, %v; want %d, true", name, got, ok, gid)
	}
}

func TestTrueTypePostQuoterightWithoutCmap(t *testing.T) {
	// PDF simple fonts can address glyphs by name even with no Unicode cmap.
	// quoteright and quotesingle are different Macintosh standard names.
	f := mustParse(t, assemble(versionTrueType, postFontTables(4,
		postNames2([]uint16{0, 183, 10, 258}, "quoteright.alt"))))
	checkPostName(t, f, 0, ".notdef")
	checkPostName(t, f, 1, "quoteright")
	checkPostName(t, f, 2, "quotesingle")
	checkPostName(t, f, 3, "quoteright.alt")
	if f.HasCharacterMap() {
		t.Error("font unexpectedly has a character map")
	}
	if _, ok := f.GlyphIndexByName("missing"); ok {
		t.Error("missing name resolved")
	}
	if _, ok := f.GlyphName(4); ok {
		t.Error("out-of-range glyph has a name")
	}
	if _, ok := f.GlyphIndexByCode(39); ok {
		t.Error("post names unexpectedly supplied a built-in encoding")
	}
	if outline, ok := f.NewFace(1000).GlyphOutline(1); !ok || len(outline) == 0 {
		t.Error("named glyph has no outline")
	}
	if f.ItalicAngle() != -9 || !f.IsFixedPitch() {
		t.Error("post descriptor fields changed")
	}
}

func TestTrueTypePostNamesDoNotChangeCmapOrOutlines(t *testing.T) {
	tables := postFontTables(3, nil)
	tables["cmap"] = cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'\u2019': 2})})
	before := mustParse(t, assemble(versionTrueType, tables))
	tables["post"] = postNames2([]uint16{0, 183, 10})
	after := mustParse(t, assemble(versionTrueType, tables))
	checkPostName(t, after, 1, "quoteright")
	if gid, ok := after.GlyphIndex('\u2019'); !ok || gid != 2 {
		t.Fatalf("post changed cmap lookup: %d, %v", gid, ok)
	}
	a, _ := before.NewFace(1000).GlyphOutline(1)
	b, _ := after.NewFace(1000).GlyphOutline(1)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(before.advances, after.advances) {
		t.Error("post changed outlines or advances")
	}
}

func TestTrueTypePostRealFont(t *testing.T) {
	f := mustParseFile(t, ttfOracle)
	for _, tc := range []struct {
		r    rune
		name string
	}{{'\u2019', "quoteright"}, {'\'', "quotesingle"}, {'A', "A"}} {
		gid, ok := f.GlyphIndex(tc.r)
		if !ok {
			t.Fatalf("fixture has no glyph for %q", tc.r)
		}
		checkPostName(t, f, gid, tc.name)
	}
}

func TestTrueTypePostFormat1(t *testing.T) {
	post := postTable(0, false)
	binary.BigEndian.PutUint32(post, 0x00010000)
	f := mustParse(t, assemble(versionTrueType, postFontTables(258, post)))
	for gid, name := range map[GlyphIndex]string{
		0: ".notdef", 1: ".null", 2: "nonmarkingreturn", 3: "space",
		10: "quotesingle", 36: "A", 67: "grave", 68: "a",
		97: "asciitilde", 183: "quoteright", 257: "dcroat",
	} {
		checkPostName(t, f, gid, name)
	}
}

func TestTrueTypePostFormat25(t *testing.T) {
	offsets := make([]int8, 185)
	offsets[1] = 35   // A: 1 + 35 = 36
	offsets[184] = -1 // quoteright: 184 - 1 = 183
	f := mustParse(t, assemble(versionTrueType, postFontTables(len(offsets), postNames25(offsets))))
	checkPostName(t, f, 1, "A")
	if name, ok := f.GlyphName(184); !ok || name != "quoteright" {
		t.Errorf("negative offset gave %q, %v", name, ok)
	}
	// Duplicate names resolve to the lowest glyph index, as for bare CFF.
	checkPostName(t, f, 183, "quoteright")
}

func TestTrueTypePostCustomNamesAndDuplicates(t *testing.T) {
	f := mustParse(t, assemble(versionTrueType, postFontTables(7,
		postNames2([]uint16{0, 260, 258, 260, 259, 261, 183},
			"custom", "", "quoteright", "custom"))))
	checkPostName(t, f, 1, "quoteright")
	checkPostName(t, f, 2, "custom")
	for _, gid := range []GlyphIndex{3, 6} {
		if name, ok := f.GlyphName(gid); !ok || name != "quoteright" {
			t.Errorf("duplicate glyph %d has name %q, %v", gid, name, ok)
		}
	}
	if name, ok := f.GlyphName(4); ok || name != "" {
		t.Errorf("empty name = %q, %v", name, ok)
	}
	if _, ok := f.GlyphIndexByName(""); ok {
		t.Error("empty name resolved")
	}
}

func TestTrueTypePostMalformedNamesRemainOptional(t *testing.T) {
	format1 := postTable(-9, true)
	binary.BigEndian.PutUint32(format1, 0x00010000)
	format3 := postTable(-9, true)
	binary.BigEndian.PutUint32(format3, 0x00030000)
	unknown := postTable(-9, true)
	binary.BigEndian.PutUint32(unknown, 0x00040000)
	badOffset := make([]int8, 258)
	badOffset[257] = 1
	for _, tc := range []struct {
		name string
		n    int
		post []byte
	}{
		{"absent", 2, nil},
		{"short header", 2, format1[:15]},
		{"format 1 wrong count", 2, format1},
		{"format 3", 2, format3},
		{"unknown format", 2, unknown},
		{"format 2 no count", 2, postTable(-9, true)},
		{"format 2 count mismatch", 3, postNames2([]uint16{0, 183})},
		{"format 2 short indices", 2, postNames2([]uint16{0, 183})[:37]},
		{"format 2 missing string", 2, postNames2([]uint16{0, 258})},
		{"format 2 short string", 2, postNames2([]uint16{0, 258}, "custom")[:40]},
		{"format 2 missing intermediate string", 2, postNames2([]uint16{0, 259}, "custom")},
		{"format 25 no count", 2, postNames25(nil)[:33]},
		{"format 25 count mismatch", 3, postNames25([]int8{0, 35})},
		{"format 25 short offsets", 2, postNames25([]int8{0, 35})[:35]},
		{"format 25 negative index", 2, postNames25([]int8{-1, 35})},
		{"format 25 large index", 258, postNames25(badOffset)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mustParse(t, assemble(versionTrueType, postFontTables(tc.n, tc.post)))
			for gid := 0; gid < f.NumGlyphs(); gid++ {
				if name, ok := f.GlyphName(GlyphIndex(gid)); ok {
					t.Fatalf("invalid names gave glyph %d name %q", gid, name)
				}
			}
			if _, ok := f.GlyphIndexByName("quoteright"); ok {
				t.Error("invalid names resolved quoteright")
			}
			if len(tc.post) >= 16 && be32(tc.post[4:]) != 0 && f.ItalicAngle() != -9 {
				t.Error("invalid names discarded valid descriptor fields")
			}
		})
	}
}

func TestTrueTypePostTruncation(t *testing.T) {
	post := postNames2([]uint16{0, 183, 258}, "custom")
	for end := 0; end < len(post); end++ {
		t.Run(fmt.Sprint(end), func(t *testing.T) {
			f := mustParse(t, assemble(versionTrueType, postFontTables(3, post[:end])))
			if _, ok := f.GlyphIndexByName("quoteright"); ok {
				t.Error("partially decoded names escaped a truncated table")
			}
		})
	}
}
