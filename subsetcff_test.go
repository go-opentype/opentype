// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"testing"
)

// otfFromCFF wraps a raw 'CFF ' table in an OTTO sfnt with the minimal companion
// tables Parse requires, for round-tripping subset output through the full parser.
func otfFromCFF(t *testing.T, cff []byte, nGlyphs int, runes map[rune]uint16) []byte {
	t.Helper()
	adv := make([]int, nGlyphs)
	lsb := make([]int, nGlyphs)
	for i := range adv {
		adv[i] = 500
	}
	tables := map[string][]byte{
		"head": descHead(1000, 0, 0, 1000, 1000, 0),
		"maxp": maxpTable(nGlyphs),
		"hhea": hheaTable(800, -200, 0, nGlyphs),
		"hmtx": hmtxTable(adv, lsb, nGlyphs),
		"cmap": cmapTable([][]byte{cmap4FromMap(runes)}),
		"CFF ": cff,
	}
	return assemble(versionOTTO, tables)
}

// syntheticCFFFont builds a 4-glyph CFF OpenType font: glyph 0 empty, glyphs 1-3
// each draw a small triangle, with a custom charset and a Private DICT carrying a
// (kept-whole) local subr, so subsetting exercises the charset and private blobs.
func syntheticCFFFont(t *testing.T) *Font {
	t.Helper()
	draw := func(dx, dy int) []byte {
		return (&csb{}).num(0).num(0).op(21). // rmoveto
							num(dx).num(0).op(5). // rlineto
							num(0).num(dy).op(5). // rlineto
							op(14).b              // endchar
	}
	g0 := (&csb{}).op(14).b
	lsub := (&csb{}).op(11).b // return
	cff := buildCFF(cffOptions{
		glyphs:      [][]byte{g0, draw(100, 100), draw(200, 150), draw(120, 300)},
		lsubrs:      [][]byte{lsub},
		includePriv: true,
		charType:    2,
		charsetSIDs: []int{1, 2, 3},
	})
	otf := otfFromCFF(t, cff, 4, map[rune]uint16{'A': 1, 'B': 2, 'C': 3})
	return mustParse(t, otf)
}

func TestSubsetCFFSynthetic(t *testing.T) {
	f := syntheticCFFFont(t)

	sub, err := f.SubsetCFF([]GlyphIndex{1, 3})
	if err != nil {
		t.Fatalf("SubsetCFF: %v", err)
	}
	// Re-parse the subset CFF (wrapped in an OTF) and check the kept glyphs render
	// identically while the dropped glyph is now empty.
	subF := mustParse(t, otfFromCFF(t, sub, f.NumGlyphs(), map[rune]uint16{'A': 1}))
	for _, gid := range []int{1, 3} {
		want, err := f.cff.outline(gid)
		if err != nil {
			t.Fatalf("orig outline(%d): %v", gid, err)
		}
		got, err := subF.cff.outline(gid)
		if err != nil {
			t.Fatalf("subset outline(%d): %v", gid, err)
		}
		if !reflectContoursEqual(want, got) {
			t.Errorf("glyph %d: outline changed by subsetting\n want %v\n got  %v", gid, want, got)
		}
	}
	// Glyph 2 was not requested: its charstring is now an empty endchar.
	if got, err := subF.cff.outline(2); err != nil || len(got) != 0 {
		t.Errorf("dropped glyph 2 should be empty, got %v (err %v)", got, err)
	}
}

func TestSubsetCFFWithoutCharsetOrPrivate(t *testing.T) {
	// No custom charset (predefined) and no Private DICT exercises those absent
	// branches.
	g0 := (&csb{}).op(14).b
	g1 := (&csb{}).num(0).num(0).op(21).num(50).num(50).op(5).op(14).b
	cff := buildCFF(cffOptions{glyphs: [][]byte{g0, g1}, charType: 2})
	f := mustParse(t, otfFromCFF(t, cff, 2, map[rune]uint16{'A': 1}))

	sub, err := f.SubsetCFF([]GlyphIndex{1})
	if err != nil {
		t.Fatalf("SubsetCFF: %v", err)
	}
	subF := mustParse(t, otfFromCFF(t, sub, 2, map[rune]uint16{'A': 1}))
	want, _ := f.cff.outline(1)
	got, _ := subF.cff.outline(1)
	if !reflectContoursEqual(want, got) {
		t.Errorf("outline changed: want %v got %v", want, got)
	}
}

func TestSubsetCFFErrors(t *testing.T) {
	// A TrueType font has no CFF table.
	if _, err := subsetTestFont(t).SubsetCFF([]GlyphIndex{1}); err == nil {
		t.Error("SubsetCFF on a glyf font should error")
	}
	// A CFF2 (variable) font is rejected up front.
	if _, err := (&Font{cff2: &cff2Table{}}).SubsetCFF(nil); err == nil {
		t.Error("SubsetCFF on a CFF2 font should error")
	}
	// A font with neither CFF nor CFF2.
	if _, err := (&Font{}).SubsetCFF(nil); err == nil {
		t.Error("SubsetCFF on a font with no CFF should error")
	}
	// An out-of-range glyph id.
	if _, err := syntheticCFFFont(t).SubsetCFF([]GlyphIndex{99}); err == nil {
		t.Error("out-of-range gid should error")
	}
}

func TestSubsetCFFRealSourceSerif(t *testing.T) {
	f := loadSourceSerif(t) // skips when the font file is absent
	gids := []GlyphIndex{}
	for _, r := range []rune{'H', 'I', 'g'} {
		gid, ok := f.GlyphIndex(r)
		if !ok {
			t.Fatalf("cmap has no glyph for %q", r)
		}
		gids = append(gids, gid)
	}
	sub, err := f.SubsetCFF(gids)
	if err != nil {
		t.Fatalf("SubsetCFF(SourceSerif): %v", err)
	}
	if len(sub) >= len(f.tables["CFF "]) {
		t.Errorf("subset CFF (%d bytes) is not smaller than the original (%d)", len(sub), len(f.tables["CFF "]))
	}

	// Wrap and re-parse; glyph numbering is preserved, so each kept glyph renders
	// identically to the original at the same id.
	subF, err := Parse(otfFromCFF(t, sub, f.NumGlyphs(), map[rune]uint16{'H': uint16(gids[0])}))
	if err != nil {
		t.Fatalf("re-parse subset SourceSerif: %v", err)
	}
	for _, gid := range gids {
		want, err := f.cff.outline(int(gid))
		if err != nil {
			t.Fatalf("orig outline(%d): %v", gid, err)
		}
		got, err := subF.cff.outline(int(gid))
		if err != nil {
			t.Fatalf("subset outline(%d): %v", gid, err)
		}
		if !reflectContoursEqual(want, got) {
			t.Errorf("glyph %d: SourceSerif outline changed by subsetting", gid)
		}
	}
}

// --- direct unit tests for the CFF subsetting primitives --------------------

func TestParseDictRawEdges(t *testing.T) {
	// Operands (two-byte int 28, then int) followed by a two-byte operator.
	b := append([]byte{28, 0x01, 0x2c}, dictOperator(1206)...)
	entries, err := parseDictRaw(b)
	if err != nil {
		t.Fatalf("parseDictRaw: %v", err)
	}
	if len(entries) != 1 || entries[0].op != 1206 || len(entries[0].values) != 1 || entries[0].values[0] != 300 {
		t.Fatalf("entries = %+v", entries)
	}
	// A trailing escape byte with no second byte is a truncated operator.
	if _, err := parseDictRaw([]byte{12}); err == nil {
		t.Error("truncated two-byte operator should error")
	}
	// A truncated operand (28 needs two more bytes) errors.
	if _, err := parseDictRaw([]byte{28, 0x01}); err == nil {
		t.Error("truncated operand should error")
	}
}

func TestFindEntry(t *testing.T) {
	entries := []dictRawEntry{{op: 17}, {op: 18}}
	if findEntry(entries, 18) == nil {
		t.Error("findEntry(18) should find it")
	}
	if findEntry(entries, 15) != nil {
		t.Error("findEntry(15) should be nil")
	}
}

func TestWriteCFFIndex(t *testing.T) {
	if got := writeCFFIndex(nil); len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Errorf("empty index = %v, want [0 0]", got)
	}
	// A large item forces a two-byte offset size.
	big := make([]byte, 300)
	out := writeCFFIndex([][]byte{big})
	if out[2] != 2 { // offSize byte
		t.Errorf("offSize = %d, want 2 for a 300-byte item", out[2])
	}
}

func TestEncodeTopDictAllCases(t *testing.T) {
	entries := []dictRawEntry{
		{op: 15, operands: dictLong(3)}, // charset (predefined path uses operands)
		{op: 16, operands: dictLong(5)}, // Encoding -> forced to 0
		{op: 17, operands: dictLong(0)}, // CharStrings
		{op: 18, operands: append(dictLong(1), dictLong(2)...)},
		{op: 1206, operands: dictLong(2)}, // default (verbatim) two-byte operator
	}
	// Predefined charset: operands copied verbatim.
	pre := encodeTopDict(entries, topDictOffsets{charsetIsPre: true, charStrings: 111, hasPrivate: true, privateSize: 7, privateOff: 222})
	got, err := parseDictRaw(pre)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if e := findEntry(got, 17); e == nil || int(e.values[0]) != 111 {
		t.Errorf("CharStrings offset not encoded: %+v", got)
	}
	if e := findEntry(got, 18); e == nil || int(e.values[0]) != 7 || int(e.values[1]) != 222 {
		t.Errorf("Private not encoded: %+v", got)
	}
	if e := findEntry(got, 16); e == nil || e.values[0] != 0 {
		t.Errorf("Encoding not forced to 0: %+v", got)
	}
	if e := findEntry(got, 1206); e == nil || e.values[0] != 2 {
		t.Errorf("verbatim operator lost: %+v", got)
	}
	// Custom charset: offset substituted.
	cust := encodeTopDict(entries, topDictOffsets{charset: 999, charStrings: 1})
	got2, _ := parseDictRaw(cust)
	if e := findEntry(got2, 15); e == nil || int(e.values[0]) != 999 {
		t.Errorf("custom charset offset not encoded: %+v", got2)
	}
}

func TestCffCharsetLength(t *testing.T) {
	// format 0: one uint16 SID per glyph 1..n-1.
	if got, err := cffCharsetLength([]byte{0, 0, 1, 0, 2}, 0, 3); err != nil || got != 5 {
		t.Errorf("format 0 length = %d (err %v), want 5", got, err)
	}
	// format 1: range record (first uint16, nLeft uint8).
	if got, err := cffCharsetLength([]byte{1, 0, 1, 1}, 0, 3); err != nil || got != 4 {
		t.Errorf("format 1 length = %d (err %v), want 4", got, err)
	}
	// format 2: range record (first uint16, nLeft uint16).
	if got, err := cffCharsetLength([]byte{2, 0, 1, 0, 1}, 0, 3); err != nil || got != 5 {
		t.Errorf("format 2 length = %d (err %v), want 5", got, err)
	}
	// unsupported format.
	if _, err := cffCharsetLength([]byte{9}, 0, 3); err == nil {
		t.Error("format 9 should error")
	}
	// offset out of range.
	if _, err := cffCharsetLength([]byte{0}, 5, 3); err == nil {
		t.Error("out-of-range offset should error")
	}
	// truncated (format 0 needs two SIDs but has none).
	if _, err := cffCharsetLength([]byte{0}, 0, 3); err == nil {
		t.Error("truncated charset should error")
	}
}

func TestCffPrivateBlob(t *testing.T) {
	// A Private DICT with no Subrs operator: the blob is just the DICT.
	noSubr := append(dictLong(100), dictOperator(20)...) // defaultWidthX, no Subrs
	blob, err := cffPrivateBlob(noSubr, 0, len(noSubr))
	if err != nil || len(blob) != len(noSubr) {
		t.Fatalf("no-subrs blob len=%d err=%v, want %d", len(blob), err, len(noSubr))
	}

	// A Private DICT declaring Subrs immediately after it: the blob spans both.
	priv := append(dictLong(6), dictOperator(19)...) // Subrs at relative offset 6
	subrs := cffIndex([][]byte{{11}})                // one return-only subr
	withSubr := append(append([]byte(nil), priv...), subrs...)
	blob, err = cffPrivateBlob(withSubr, 0, len(priv))
	if err != nil || len(blob) != len(withSubr) {
		t.Fatalf("with-subrs blob len=%d err=%v, want %d", len(blob), err, len(withSubr))
	}

	// Errors: Private range out of range; Subrs offset out of range; truncated
	// Subr INDEX; malformed Private DICT.
	if _, err := cffPrivateBlob([]byte{0}, 0, 5); err == nil {
		t.Error("private range out of range should error")
	}
	farSubr := append(dictLong(1000), dictOperator(19)...)
	if _, err := cffPrivateBlob(farSubr, 0, len(farSubr)); err == nil {
		t.Error("subrs offset out of range should error")
	}
	// Subrs offset points at the end where no INDEX bytes remain.
	if _, err := cffPrivateBlob(priv, 0, len(priv)); err == nil {
		t.Error("truncated subr index should error")
	}
	if _, err := cffPrivateBlob([]byte{12}, 0, 1); err == nil {
		t.Error("malformed private dict should error")
	}
}

func TestCffIndexEndError(t *testing.T) {
	if _, err := cffIndexEnd([]byte{0x00}, 0); err == nil {
		t.Error("truncated index should error")
	}
}

// craftCFFPrefix assembles the header and a chosen prefix of the top-level CFF
// structures, for driving subsetCFFTable's early parse-error branches.
func craftCFFPrefix(parts ...[]byte) []byte {
	out := []byte{1, 0, 4, 1} // header, hdrSize 4
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestSubsetCFFTableParseErrors(t *testing.T) {
	dummy := &cffTable{charStrings: [][]byte{{14}, {14}}}
	empty := []byte{0, 0} // an empty INDEX
	oneDict := cffIndex([][]byte{{0x8b}})

	cases := map[string][]byte{
		"short":        {1, 0, 4},
		"hdrSizeSmall": {1, 0, 3, 1},
		"hdrSizeBig":   {1, 0, 20, 1},
		"nameTrunc":    {1, 0, 4, 1, 0},
		"topTrunc":     craftCFFPrefix(empty, []byte{0}),
		"topEmpty":     craftCFFPrefix(empty, empty),
		"stringTrunc":  craftCFFPrefix(empty, oneDict, []byte{0}),
		"gsubrTrunc":   craftCFFPrefix(empty, oneDict, empty, []byte{0}),
		"dictTrunc":    craftCFFPrefix(empty, cffIndex([][]byte{{12}}), empty, empty),
	}
	for name, data := range cases {
		if _, err := subsetCFFTable(data, dummy, nil); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSubsetCFFRejectsCID(t *testing.T) {
	dummy := &cffTable{charStrings: [][]byte{{14}}}
	empty := []byte{0, 0}

	// A Top DICT carrying a ROS operator marks a CID-keyed font.
	ros := append(append(append(dictLong(391), dictLong(392)...), dictLong(0)...), dictOperator(1230)...)
	rosData := craftCFFPrefix(empty, cffIndex([][]byte{ros}), empty, empty)
	if _, err := subsetCFFTable(rosData, dummy, nil); err == nil {
		t.Error("ROS (CID-keyed) CFF should be rejected")
	}
	// An FDArray operator likewise.
	fda := append(dictLong(0), dictOperator(1236)...)
	fdaData := craftCFFPrefix(empty, cffIndex([][]byte{fda}), empty, empty)
	if _, err := subsetCFFTable(fdaData, dummy, nil); err == nil {
		t.Error("FDArray (CID-keyed) CFF should be rejected")
	}
}

func TestSubsetCFFBadCharset(t *testing.T) {
	// A Top DICT charset offset (op 15 > 2) pointing past the table end makes the
	// charset-length probe fail.
	dummy := &cffTable{charStrings: [][]byte{{14}}}
	empty := []byte{0, 0}
	badCharset := append(dictLong(9999), dictOperator(15)...)
	data := craftCFFPrefix(empty, cffIndex([][]byte{badCharset}), empty, empty)
	if _, err := subsetCFFTable(data, dummy, nil); err == nil {
		t.Error("out-of-range charset offset should error")
	}
}

func TestSubsetCFFBadPrivateEntry(t *testing.T) {
	dummy := &cffTable{charStrings: [][]byte{{14}}}
	empty := []byte{0, 0}
	// A Private operator with a single operand (needs two) is malformed.
	badPriv := append(dictLong(10), dictOperator(18)...) // only one operand before op 18
	data := craftCFFPrefix(empty, cffIndex([][]byte{badPriv}), empty, empty)
	if _, err := subsetCFFTable(data, dummy, nil); err == nil {
		t.Error("Private DICT entry with one operand should error")
	}
	// A Private DICT whose [size, offset] points past the table end fails the blob
	// extraction.
	farPriv := append(append(dictLong(10), dictLong(99999)...), dictOperator(18)...)
	data = craftCFFPrefix(empty, cffIndex([][]byte{farPriv}), empty, empty)
	if _, err := subsetCFFTable(data, dummy, nil); err == nil {
		t.Error("out-of-range Private DICT should error")
	}
}
