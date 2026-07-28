// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// --- GSUB subtable builders -------------------------------------------------

// buildSingle1 builds a single-substitution format 1 subtable: format(2),
// coverageOffset(2), deltaGlyphID(2), then the coverage table.
func buildSingle1(delta int16, cov []byte) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(6) // coverage offset (right after the 6-byte header)
	w.i16(delta)
	w.b = append(w.b, cov...)
	return w.bytes()
}

// buildSingle2 builds a single-substitution format 2 subtable.
func buildSingle2(subst []GlyphIndex, cov []byte) []byte {
	header := 6 + 2*len(subst)
	w := &bw{}
	w.u16(2)
	w.u16(uint16(header))
	w.u16(uint16(len(subst)))
	for _, s := range subst {
		w.u16(uint16(s))
	}
	w.b = append(w.b, cov...)
	return w.bytes()
}

func buildLigature(glyph GlyphIndex, rest ...GlyphIndex) []byte {
	w := &bw{}
	w.u16(uint16(glyph))
	w.u16(uint16(len(rest) + 1)) // componentCount includes the first component
	for _, c := range rest {
		w.u16(uint16(c))
	}
	return w.bytes()
}

func buildLigatureSet(ligs [][]byte) []byte {
	header := 2 + 2*len(ligs)
	body := &bw{}
	var offs []int
	for _, l := range ligs {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, l...)
	}
	w := &bw{}
	w.u16(uint16(len(ligs)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildLigatureSubst builds a ligature-substitution format 1 subtable.
func buildLigatureSubst(cov []byte, sets [][]byte) []byte {
	header := 6 + 2*len(sets)
	body := &bw{}
	var setOffs []int
	for _, s := range sets {
		setOffs = append(setOffs, header+len(body.b))
		body.b = append(body.b, s...)
	}
	covOff := header + len(body.b)
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(uint16(len(sets)))
	for _, o := range setOffs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	return w.bytes()
}

// gsubFixture builds a GSUB with a single-1, a single-2, a ligature and an
// unsupported (type 2) lookup, plus features smcp/liga/bad.
func gsubFixture(t *testing.T) *gsub {
	t.Helper()
	single1 := buildLookup(1, [][]byte{buildSingle1(10, buildCoverage1(1))})
	single2 := buildLookup(1, [][]byte{buildSingle2([]GlyphIndex{22}, buildCoverage2([3]int{2, 2, 0}))})
	// Ligature set for first component glyph 3: a too-long rule (length guard),
	// a same-length mismatching rule, and the matching rule 3+4 -> 100.
	set := buildLigatureSet([][]byte{
		buildLigature(200, 4, 4, 4), // too long for the run
		buildLigature(201, 9),       // right length, wrong component
		buildLigature(100, 4),       // matches 3,4
	})
	lig := buildLookup(4, [][]byte{buildLigatureSubst(buildCoverage1(3), [][]byte{set})})
	unsupported := buildLookup(9, [][]byte{{0, 0}}) // undefined type (>8), parsed as no-op

	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0, 1, 2}}}}
	feats := []tFeature{
		{tag: "smcp", lookups: []uint16{0, 1}},
		{tag: "liga", lookups: []uint16{2}},
		{tag: "bad ", lookups: []uint16{99}}, // out-of-range lookup index
	}
	blob := buildLayoutTable(scripts, feats, [][]byte{single1, single2, lig, unsupported})
	g, err := parseGSUB(blob)
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	return g
}

func TestGSUBApply(t *testing.T) {
	g := gsubFixture(t)

	// Single substitution then ligature: 1->11, 2->22, 3+4->100.
	in := []GlyphIndex{1, 2, 3, 4}
	got := g.Apply(in, "smcp", "liga")
	want := []GlyphIndex{11, 22, 100}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Apply = %v want %v", got, want)
	}
	// The input run must be left untouched.
	if !reflect.DeepEqual(in, []GlyphIndex{1, 2, 3, 4}) {
		t.Errorf("input mutated: %v", in)
	}
}

func TestGSUBApplyLigatureNoMatch(t *testing.T) {
	g := gsubFixture(t)
	// Glyph 3 is the first component of a ligature but the following glyph (7)
	// matches none of the rules in its set, so the run is left unchanged.
	in := []GlyphIndex{3, 7}
	if got := g.Apply(in, "liga"); !reflect.DeepEqual(got, []GlyphIndex{3, 7}) {
		t.Errorf("Apply = %v want unchanged", got)
	}
}

func TestGSUBApplyNoFeature(t *testing.T) {
	g := gsubFixture(t)
	in := []GlyphIndex{1, 2, 3}
	// A feature with no matching lookups leaves the run unchanged (same slice).
	got := g.Apply(in, "zzzz")
	if !reflect.DeepEqual(got, in) {
		t.Errorf("Apply(zzzz) = %v want unchanged", got)
	}
	// No features at all.
	if got := g.Apply(in); !reflect.DeepEqual(got, in) {
		t.Errorf("Apply() = %v want unchanged", got)
	}
}

func TestGSUBApplyOutOfRangeLookup(t *testing.T) {
	g := gsubFixture(t)
	in := []GlyphIndex{1, 2}
	got := g.Apply(in, "bad ") // selects lookup index 99, skipped
	if !reflect.DeepEqual(got, []GlyphIndex{1, 2}) {
		t.Errorf("Apply(bad) = %v want unchanged", got)
	}
}

func TestGSUBApplyEmptyRun(t *testing.T) {
	g := gsubFixture(t)
	if got := g.Apply(nil, "smcp", "liga"); len(got) != 0 {
		t.Errorf("Apply(nil) = %v want empty", got)
	}
}

func TestGSUBParseErrors(t *testing.T) {
	// parseGSUB propagates a layout-header error.
	if _, err := parseGSUB([]byte{0, 1}); err == nil {
		t.Error("truncated GSUB should error")
	}
	// parseGSUB propagates a lookup error (single subst with bad format).
	badLookup := buildLookup(1, [][]byte{{0, 3, 0, 0}}) // substFormat 3
	blob := buildLayoutTable(simpleScripts(0),
		[]tFeature{{tag: "liga", lookups: []uint16{0}}}, [][]byte{badLookup})
	if _, err := parseGSUB(blob); err == nil {
		t.Error("bad lookup should propagate error")
	}
}

func TestGSUBLookupTruncated(t *testing.T) {
	if _, err := parseGSUBLookup([]byte{0, 1, 0, 0, 0, 5}); err == nil {
		t.Error("truncated lookup header should error")
	}
}

func TestParseGSUBSingleErrors(t *testing.T) {
	// Bad subst format.
	if _, err := parseGSUBSingle([]byte{0, 3, 0, 6}); err == nil {
		t.Error("format 3 should error")
	}
	// Format 1 truncated before delta.
	if _, err := parseGSUBSingle([]byte{0, 1, 0, 6}); err == nil {
		t.Error("truncated single-1 should error")
	}
	// Format 1 with a bad coverage offset.
	if _, err := parseGSUBSingle([]byte{0, 1, 0xFF, 0xFF, 0, 10}); err == nil {
		t.Error("single-1 bad coverage should error")
	}
	// Format 2 truncated in the substitute array.
	if _, err := parseGSUBSingle([]byte{0, 2, 0, 8, 0, 2, 0, 5}); err == nil {
		t.Error("truncated single-2 should error")
	}
	// Format 2 with a bad coverage offset.
	if _, err := parseGSUBSingle([]byte{0, 2, 0xFF, 0xFF, 0, 1, 0, 5}); err == nil {
		t.Error("single-2 bad coverage should error")
	}
}

func TestParseGSUBLigatureErrors(t *testing.T) {
	// Truncated ligature-subst header (setOffsets run off the end).
	if _, err := parseGSUBLigature([]byte{0, 1, 0, 6, 0, 1}); err == nil {
		t.Error("truncated ligature subst should error")
	}
	// A well-formed table, corrupted one offset at a time. Layout: header 8
	// bytes (format, coverageOffset@2, setCount, setOffset@6), LigatureSet at
	// offset 8 (ligOffset@10), then coverage.
	valid := buildLigatureSubst(buildCoverage1(3), [][]byte{
		buildLigatureSet([][]byte{buildLigature(100, 4)}),
	})
	corrupt := func(pos int, v uint16) []byte {
		b := append([]byte(nil), valid...)
		b[pos] = byte(v >> 8)
		b[pos+1] = byte(v)
		return b
	}
	if _, err := parseGSUBLigature(corrupt(2, 0xFFFF)); err == nil {
		t.Error("ligature bad coverage should error")
	}
	// setOffset -> end of table: parseLigatureSet reads its count off the end.
	if _, err := parseGSUBLigature(corrupt(6, uint16(len(valid)))); err == nil {
		t.Error("truncated ligature set should error")
	}
	// ligOffset -> past the end: parseLigature reads off the end.
	if _, err := parseGSUBLigature(corrupt(10, 0xFFFF)); err == nil {
		t.Error("truncated ligature should error")
	}
}
