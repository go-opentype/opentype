// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// This file tests the GDEF table and the OpenType Layout lookup flags: parsing
// GDEF, the glyph skipper the flags drive, and the skip-aware matching threaded
// through every GSUB and GPOS lookup type.

// --- byte builders ----------------------------------------------------------

// buildLookupFlagged wraps subtables into a Lookup table with an explicit
// lookupFlag and (when UseMarkFilteringSet is set) a markFilteringSet.
func buildLookupFlagged(lookupType, flag, markSet uint16, subtables [][]byte) []byte {
	hasFilter := flag&flagUseMarkFilteringSet != 0
	header := 6 + 2*len(subtables)
	if hasFilter {
		header += 2
	}
	body := &bw{}
	var offs []int
	for _, st := range subtables {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, st...)
	}
	w := &bw{}
	w.u16(lookupType)
	w.u16(flag)
	w.u16(uint16(len(subtables)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	if hasFilter {
		w.u16(markSet)
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildMarkGlyphSets builds a MarkGlyphSetsDef table (format 1) from a list of
// Coverage blobs.
func buildMarkGlyphSets(covs [][]byte) []byte {
	n := len(covs)
	header := 4 + 4*n
	body := &bw{}
	offs := make([]int, n)
	for i, c := range covs {
		offs[i] = header + len(body.b)
		body.b = append(body.b, c...)
	}
	w := &bw{}
	w.u16(1)
	w.u16(uint16(n))
	for _, o := range offs {
		w.u32(uint32(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildGDEF assembles a GDEF table of the given minorVersion. A nil glyphClass
// or markAttach leaves its offset NULL; markSets is only emitted when
// minorVersion >= 2.
func buildGDEF(minor uint16, glyphClass, markAttach []byte, markSets [][]byte) []byte {
	headerLen := 4 + 8 // versions + 4 offsets
	if minor >= 2 {
		headerLen += 2
	}
	body := &bw{}
	glyphClassOff := 0
	if glyphClass != nil {
		glyphClassOff = headerLen + len(body.b)
		body.b = append(body.b, glyphClass...)
	}
	markAttachOff := 0
	if markAttach != nil {
		markAttachOff = headerLen + len(body.b)
		body.b = append(body.b, markAttach...)
	}
	markSetsOff := 0
	if minor >= 2 && markSets != nil {
		markSetsOff = headerLen + len(body.b)
		body.b = append(body.b, buildMarkGlyphSets(markSets)...)
	}
	w := &bw{}
	w.u16(1)     // majorVersion
	w.u16(minor) // minorVersion
	w.u16(uint16(glyphClassOff))
	w.u16(0) // attachListOffset
	w.u16(0) // ligCaretListOffset
	w.u16(uint16(markAttachOff))
	if minor >= 2 {
		w.u16(uint16(markSetsOff))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// --- GDEF parsing -----------------------------------------------------------

func TestParseGDEF(t *testing.T) {
	// A minor-2 GDEF carrying a GlyphClassDef, a MarkAttachClassDef and one mark
	// glyph set exercises every populated field.
	gc := buildClassDef1(10, gdefClassBase, gdefClassMark) // 10=base, 11=mark
	ma := buildClassDef1(11, 1)                            // mark 11 -> attach class 1
	ms := [][]byte{buildCoverage1(11)}                     // set 0 = {11}
	gd, err := parseGDEF(buildGDEF(2, gc, ma, ms))
	if err != nil {
		t.Fatalf("parseGDEF: %v", err)
	}
	if gd.glyphClass[10] != gdefClassBase || gd.glyphClass[11] != gdefClassMark {
		t.Errorf("glyphClass = %v", gd.glyphClass)
	}
	if gd.markAttach[11] != 1 {
		t.Errorf("markAttach = %v", gd.markAttach)
	}
	if !gd.markSetCovers(0, 11) || gd.markSetCovers(0, 10) || gd.markSetCovers(5, 11) {
		t.Errorf("markSetCovers wrong: %v", gd.markGlyphSets)
	}

	// A minor-0 GDEF with only a GlyphClassDef leaves markAttach empty and
	// markGlyphSets nil (the version-2 field is absent).
	gd0, err := parseGDEF(buildGDEF(0, gc, nil, nil))
	if err != nil {
		t.Fatalf("parseGDEF minor0: %v", err)
	}
	if len(gd0.markAttach) != 0 || gd0.markGlyphSets != nil {
		t.Errorf("minor0 unexpected marks: %v %v", gd0.markAttach, gd0.markGlyphSets)
	}

	// A GDEF with every subtable absent parses to an empty table.
	gdE, err := parseGDEF(buildGDEF(2, nil, nil, nil))
	if err != nil {
		t.Fatalf("parseGDEF empty: %v", err)
	}
	if len(gdE.glyphClass) != 0 || len(gdE.markAttach) != 0 || gdE.markGlyphSets != nil {
		t.Errorf("empty GDEF not empty: %+v", gdE)
	}
}

func TestParseGDEFErrors(t *testing.T) {
	cases := map[string][]byte{
		"truncated header": {0, 1, 0, 2},                         // stops before glyphClassOffset
		"bad glyphClass":   buildGDEF(0, []byte{0, 9}, nil, nil), // ClassDef format 9
		"bad markAttach":   buildGDEF(0, nil, []byte{0, 9}, nil),
		"bad markSets cov": buildGDEF(2, nil, nil, [][]byte{{0, 9}}), // Coverage format 9
	}
	for name, blob := range cases {
		if _, err := parseGDEF(blob); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// A truncated MarkGlyphSetsDef (only a format field, no count) errors inside
	// parseMarkGlyphSets: build a minor-2 header pointing markSets at a 2-byte body.
	w := &bw{}
	w.u16(1)  // major
	w.u16(2)  // minor
	w.u16(0)  // glyphClassOffset
	w.u16(0)  // attachList
	w.u16(0)  // ligCaret
	w.u16(0)  // markAttach
	w.u16(14) // markSetsOffset -> the two bytes below
	w.u16(1)  // a lone format field; the count read runs past the end
	if _, err := parseGDEF(w.bytes()); err == nil {
		t.Error("truncated markGlyphSets: expected error")
	}
}

// --- skipper unit tests -----------------------------------------------------

func TestSkipper(t *testing.T) {
	gd := &gdefTable{
		glyphClass: map[GlyphIndex]int{
			1: gdefClassBase, 2: gdefClassLigature, 3: gdefClassMark,
			4: gdefClassMark, 5: gdefClassMark,
		},
		markAttach:    map[GlyphIndex]int{3: 1, 4: 2, 5: 0},
		markGlyphSets: []map[GlyphIndex]int{{4: 0}},
	}
	// No GDEF: nothing is skipped.
	if (skipper{}).skip(3) {
		t.Error("zero skipper skipped a glyph")
	}
	tests := []struct {
		name    string
		flag    uint16
		markSet uint16
		g       GlyphIndex
		want    bool
	}{
		{"ignore base", flagIgnoreBaseGlyphs, 0, 1, true},
		{"ignore base misses mark", flagIgnoreBaseGlyphs, 0, 3, false},
		{"ignore ligature", flagIgnoreLigatures, 0, 2, true},
		{"ignore mark", flagIgnoreMarks, 0, 3, true},
		{"attach type match keeps", 0x0100, 0, 3, false},   // want class 1, glyph 3 is class 1
		{"attach type mismatch skips", 0x0200, 0, 3, true}, // want class 2, glyph 3 is class 1
		{"filter set member kept", flagUseMarkFilteringSet, 0, 4, false},
		{"filter set non-member skipped", flagUseMarkFilteringSet, 0, 3, true},
		{"plain mark not skipped", 0, 0, 5, false},
	}
	for _, tc := range tests {
		sk := skipper{gdef: gd, flag: tc.flag, markFilterSet: tc.markSet}
		if got := sk.skip(tc.g); got != tc.want {
			t.Errorf("%s: skip(%d)=%v want %v", tc.name, tc.g, got, tc.want)
		}
	}
	// rtl and isMark.
	if !(skipper{flag: flagRightToLeft}).rtl() || (skipper{}).rtl() {
		t.Error("rtl flag wrong")
	}
	sk := skipper{gdef: gd}
	if !sk.isMark(3) || sk.isMark(1) {
		t.Error("isMark wrong")
	}
	// next/prev walk over a skipped run.
	skm := skipper{gdef: gd, flag: flagIgnoreMarks}
	run := []GlyphIndex{1, 3, 3, 1}
	if skm.next(run, 1) != 3 { // skip the two marks at 1,2
		t.Errorf("next skipped wrong: %d", skm.next(run, 1))
	}
	if skm.prev(run, 2) != 0 { // skip marks back to the base at 0
		t.Errorf("prev skipped wrong: %d", skm.prev(run, 2))
	}
}

// --- GDEF threaded through a font -------------------------------------------

func TestGDEFThreadedThroughFont(t *testing.T) {
	gc := buildClassDef1(2, gdefClassMark)
	extra := map[string][]byte{
		"GDEF": buildGDEF(0, gc, nil, nil),
		"GSUB": buildLayoutTable(simpleScripts(0),
			[]tFeature{{tag: "liga", lookups: []uint16{0}}},
			[][]byte{buildLookup(1, [][]byte{buildSingle1(1, buildCoverage1(1))})}),
		"GPOS": buildLayoutTable(simpleScripts(0),
			[]tFeature{{tag: "kern", lookups: []uint16{0}}},
			[][]byte{buildLookup(1, [][]byte{buildSinglePos1(buildCoverage1(1), vfXAdvance, valueRecFull(vfXAdvance, 0, 0, -5, 0))})}),
	}
	f := makeFont(t, [][]byte{{}, {}, {}}, map[rune]uint16{'a': 1}, extra)
	if f.gdef == nil {
		t.Fatal("font GDEF not parsed")
	}
	if f.gsub.gdef != f.gdef || f.gpos.gdef != f.gdef {
		t.Error("GDEF not threaded into GSUB/GPOS")
	}
	if f.gdef.glyphClass[2] != gdefClassMark {
		t.Errorf("threaded GDEF class = %v", f.gdef.glyphClass)
	}
}

// --- GSUB with lookup flags -------------------------------------------------

// gsubGDEF applies a single flagged lookup with a GDEF table attached.
func gsubGDEF(t *testing.T, gd *gdefTable, flag, markSet, ltype uint16, subs [][]byte, in []GlyphIndex) []GlyphIndex {
	t.Helper()
	lk := buildLookupFlagged(ltype, flag, markSet, subs)
	g, err := parseGSUB(buildLayoutTable(simpleScripts(0), []tFeature{{tag: "test", lookups: []uint16{0}}}, [][]byte{lk}))
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	g.gdef = gd
	return g.Apply(in, "test")
}

func TestGSUBLigatureIgnoreMarks(t *testing.T) {
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	// f(10) + i(11) -> fi(30); the mark 20 sits between them.
	sub := buildLigatureSubst(buildCoverage1(10), [][]byte{
		buildLigatureSet([][]byte{buildLigature(30, 11)}),
	})
	// With IgnoreMarks the intervening mark is skipped and the ligature forms,
	// leaving the mark after it.
	got := gsubGDEF(t, gd, flagIgnoreMarks, 0, 4, [][]byte{sub}, []GlyphIndex{10, 20, 11})
	if want := []GlyphIndex{30, 20}; !reflect.DeepEqual(got, want) {
		t.Errorf("ligature+ignoremarks = %v want %v", got, want)
	}
	// Without the flag the mark blocks the ligature.
	got = gsubGDEF(t, gd, 0, 0, 4, [][]byte{sub}, []GlyphIndex{10, 20, 11})
	if want := []GlyphIndex{10, 20, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("ligature+noflag = %v want %v", got, want)
	}
}

func TestGSUBLigatureTrackedClusters(t *testing.T) {
	// The tracked path splices the parallel cluster slice across a ligature that
	// skipped an intervening mark: fi keeps f's cluster, the mark keeps its own.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	sub := buildLigatureSubst(buildCoverage1(10), [][]byte{
		buildLigatureSet([][]byte{buildLigature(30, 11)}),
	})
	lk := buildLookupFlagged(4, flagIgnoreMarks, 0, [][]byte{sub})
	g, err := parseGSUB(buildLayoutTable(simpleScripts(0), []tFeature{{tag: "liga", lookups: []uint16{0}}}, [][]byte{lk}))
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	g.gdef = gd
	out, clusters := g.ApplyMaskedTracked([]GlyphIndex{10, 20, 11}, nil, []FeatureApp{{Tag: "liga"}})
	if want := []GlyphIndex{30, 20}; !reflect.DeepEqual(out, want) {
		t.Errorf("tracked ligature = %v want %v", out, want)
	}
	if want := []int{0, 1}; !reflect.DeepEqual(clusters, want) {
		t.Errorf("tracked clusters = %v want %v", clusters, want)
	}
}

func TestGSUBSingleSkippedAnchor(t *testing.T) {
	// A single substitution whose coverage includes a mark: with IgnoreMarks the
	// mark is not a starting position, so it is left unchanged.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{20: gdefClassMark}}
	sub := buildSingle1(5, buildCoverage1(20)) // 20 -> 25
	if got := gsubGDEF(t, gd, flagIgnoreMarks, 0, 1, [][]byte{sub}, []GlyphIndex{20}); !reflect.DeepEqual(got, []GlyphIndex{20}) {
		t.Errorf("single skipped = %v want [20]", got)
	}
	if got := gsubGDEF(t, gd, 0, 0, 1, [][]byte{sub}, []GlyphIndex{20}); !reflect.DeepEqual(got, []GlyphIndex{25}) {
		t.Errorf("single applied = %v want [25]", got)
	}
}

func TestGSUBReverseSkip(t *testing.T) {
	// Reverse chaining single sub: 10 -> 99 when followed by 11, with an ignored
	// mark between. The reverse walk also skips the mark as a starting position.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	sub := buildReverseChain(buildCoverage1(10), nil, [][]byte{buildCoverage1(11)}, []GlyphIndex{99})
	got := gsubGDEF(t, gd, flagIgnoreMarks, 0, 8, [][]byte{sub}, []GlyphIndex{10, 20, 11})
	if want := []GlyphIndex{99, 20, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("reverse skip = %v want %v", got, want)
	}
}

func TestGSUBChainContextSkip(t *testing.T) {
	// A chaining context (format 3) matching 10 with lookahead 11, applying a
	// single substitution on the first glyph, only when the mark between is
	// ignored. Lookup 0 is the chain; lookup 1 the nested single sub.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	chain := buildChain3(nil, [][]byte{buildCoverage1(10)}, [][]byte{buildCoverage1(11)}, []seqRec{{0, 1}})
	nested := buildSingle1(5, buildCoverage1(10)) // 10 -> 15
	lk0 := buildLookupFlagged(6, flagIgnoreMarks, 0, [][]byte{chain})
	lk1 := buildLookup(1, [][]byte{nested})
	g, err := parseGSUB(buildLayoutTable(simpleScripts(0), []tFeature{{tag: "test", lookups: []uint16{0}}}, [][]byte{lk0, lk1}))
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	g.gdef = gd
	if got := g.Apply([]GlyphIndex{10, 20, 11}, "test"); !reflect.DeepEqual(got, []GlyphIndex{15, 20, 11}) {
		t.Errorf("chain skip = %v want [15 20 11]", got)
	}
}

func TestGSUBContextClassSkip(t *testing.T) {
	// A class-based context (format 2) input [class-of-11] after covered 10, with
	// an ignored mark in between, running a single sub on the first glyph.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	classDef := buildClassDef1(10, 1, 2) // glyph 10 -> class 1, glyph 11 -> class 2
	rule := buildSeqRule([]uint16{2}, []seqRec{{0, 1}})
	ctx := buildContext2(buildCoverage1(10), classDef, [][]byte{nil, buildRuleSet([][]byte{rule})})
	lk0 := buildLookupFlagged(5, flagIgnoreMarks, 0, [][]byte{ctx})
	lk1 := buildLookup(1, [][]byte{buildSingle1(90, buildCoverage1(10))}) // 10 -> 100
	g, err := parseGSUB(buildLayoutTable(simpleScripts(0), []tFeature{{tag: "test", lookups: []uint16{0}}}, [][]byte{lk0, lk1}))
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	g.gdef = gd
	if got := g.Apply([]GlyphIndex{10, 20, 11}, "test"); !reflect.DeepEqual(got, []GlyphIndex{100, 20, 11}) {
		t.Errorf("context2 skip = %v want [100 20 11]", got)
	}
}

func TestGSUBChain2Lookahead(t *testing.T) {
	// A class-based chaining context whose lookahead class must also match: the
	// lookahead-fail path leaves the run unchanged.
	inCD := buildClassDef1(10, 1, 2) // 10->class 1, 11->class 2
	laCD := buildClassDef1(12, 5)    // 12->class 5
	rule := buildChainRule(nil, []uint16{2}, []uint16{5}, []seqRec{{0, 1}})
	chain := buildChain2(buildCoverage1(10), buildClassDef1(0), inCD, laCD, [][]byte{nil, buildRuleSet([][]byte{rule})})
	lk0 := buildLookup(6, [][]byte{chain})
	lk1 := buildLookup(1, [][]byte{buildSingle1(90, buildCoverage1(10))}) // 10 -> 100
	g, err := parseGSUB(buildLayoutTable(simpleScripts(0), []tFeature{{tag: "test", lookups: []uint16{0}}}, [][]byte{lk0, lk1}))
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	if got := g.Apply([]GlyphIndex{10, 11, 12}, "test"); !reflect.DeepEqual(got, []GlyphIndex{100, 11, 12}) {
		t.Errorf("chain2 match = %v want [100 11 12]", got)
	}
	// Lookahead 99 is class 0, not 5: no match.
	if got := g.Apply([]GlyphIndex{10, 11, 99}, "test"); !reflect.DeepEqual(got, []GlyphIndex{10, 11, 99}) {
		t.Errorf("chain2 lookahead-fail = %v want unchanged", got)
	}
}

func TestGSUBContext3Empty(t *testing.T) {
	// A format-3 context with zero coverages never matches (matchContext empty
	// guard), and a format-3 chain likewise.
	if _, _, ok := (&gsubContext3{}).sub(&gsubApplier{}, []GlyphIndex{1}, 0); ok {
		t.Error("empty context3 matched")
	}
	if _, _, ok := (&gsubChain3{}).sub(&gsubApplier{}, []GlyphIndex{1}, 0); ok {
		t.Error("empty chain3 matched")
	}
}

func TestGSUBUseMarkFilteringSet(t *testing.T) {
	// A single substitution under UseMarkFilteringSet: mark 20 is absent from set
	// 0, so it is skipped as a starting position and not substituted; mark 21 is
	// in the set and is substituted. This also drives the markFilteringSet parse.
	gd := &gdefTable{
		glyphClass:    map[GlyphIndex]int{20: gdefClassMark, 21: gdefClassMark},
		markGlyphSets: []map[GlyphIndex]int{{21: 0}},
	}
	sub := buildSingle1(5, buildCoverage1(20, 21)) // +5
	got := gsubGDEF(t, gd, flagUseMarkFilteringSet, 0, 1, [][]byte{sub}, []GlyphIndex{20, 21})
	if want := []GlyphIndex{20, 26}; !reflect.DeepEqual(got, want) {
		t.Errorf("markfilter single = %v want %v", got, want)
	}
}

// --- GPOS with lookup flags -------------------------------------------------

// gposGDEF builds a gpos from lookups with a GDEF table attached.
func gposGDEF(t *testing.T, gd *gdefTable, lookups [][]byte) *gpos {
	t.Helper()
	g := gposWith(t, "test", []uint16{0}, lookups)
	g.gdef = gd
	return g
}

func TestGPOSPairIgnoreMarks(t *testing.T) {
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	pp := buildPairPos1(buildCoverage1(10), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 11, xa1: -40}}),
	})
	lk := buildLookupFlagged(2, flagIgnoreMarks, 0, [][]byte{pp})
	g := gposGDEF(t, gd, [][]byte{lk})
	// The intervening mark is skipped, so the pair 10..11 kerns.
	if pos := g.position([]GlyphIndex{10, 20, 11}, nil, "test"); pos[0].XAdvance != -40 {
		t.Errorf("pair+ignoremarks pos=%+v want -40", pos)
	}
	// Without GDEF the mark blocks the pair.
	g.gdef = nil
	if pos := g.position([]GlyphIndex{10, 20, 11}, nil, "test"); pos[0].XAdvance != 0 {
		t.Errorf("pair no-gdef pos=%+v want 0", pos)
	}
}

func TestGPOSCursiveRTL(t *testing.T) {
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{40: gdefClassBase, 41: gdefClassBase, 20: gdefClassMark}}
	cur := buildCursivePos(buildCoverage1(40, 41), []eeRec{
		{entry: nil, exit: buildAnchor1(100, 50)},
		{entry: buildAnchor1(10, 20), exit: nil},
	})
	// Right-to-left with an ignored mark between the joined glyphs: the current
	// glyph (index 0) is moved by entry-exit.
	lk := buildLookupFlagged(3, flagRightToLeft|flagIgnoreMarks, 0, [][]byte{cur})
	g := gposGDEF(t, gd, [][]byte{lk})
	pos := g.position([]GlyphIndex{40, 20, 41}, nil, "test")
	if pos[0].XOffset != -90 || pos[0].YOffset != -30 {
		t.Errorf("cursive rtl pos[0]=%+v want XOffset=-90 YOffset=-30", pos[0])
	}
	if pos[2].XOffset != 0 {
		t.Errorf("cursive rtl moved wrong glyph: pos[2]=%+v", pos[2])
	}
	// Left-to-right (no flag): the following glyph is moved instead.
	lk = buildLookupFlagged(3, 0, 0, [][]byte{cur})
	g = gposGDEF(t, gd, [][]byte{lk})
	pos = g.position([]GlyphIndex{40, 41}, nil, "test")
	if pos[1].XOffset != 90 || pos[1].YOffset != 30 {
		t.Errorf("cursive ltr pos[1]=%+v want XOffset=90 YOffset=30", pos[1])
	}
}

func TestGPOSMarkBaseSkipsMarks(t *testing.T) {
	// Mark-to-base base finding skips an intervening GDEF mark to reach the base.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 20: gdefClassMark, 21: gdefClassMark}}
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(5, 7)}})
	baseArr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(3, 4)}})
	st := buildMarkPosSubtable(buildCoverage1(21), buildCoverage1(10), 1, markArr, baseArr)
	g := gposGDEF(t, gd, [][]byte{buildLookup(4, [][]byte{st})})
	pos := g.position([]GlyphIndex{10, 20, 21}, nil, "test")
	if pos[2].XOffset != -2 || pos[2].YOffset != -3 {
		t.Errorf("markbase skip pos[2]=%+v want (-2,-3)", pos[2])
	}
}

func TestGPOSMarkLigSkipsMarks(t *testing.T) {
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{30: gdefClassLigature, 20: gdefClassMark, 21: gdefClassMark}}
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(5, 7)}})
	ligAttach := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(3, 4)}})
	ligArr := buildLigatureArray([][]byte{ligAttach})
	st := buildMarkPosSubtable(buildCoverage1(21), buildCoverage1(30), 1, markArr, ligArr)
	g := gposGDEF(t, gd, [][]byte{buildLookup(5, [][]byte{st})})
	pos := g.position([]GlyphIndex{30, 20, 21}, nil, "test")
	if pos[2].XOffset != -2 || pos[2].YOffset != -3 {
		t.Errorf("marklig skip pos[2]=%+v want (-2,-3)", pos[2])
	}
}

func TestGPOSMarkMarkSkip(t *testing.T) {
	// Mark-to-mark attaches 22 onto the preceding base mark 21, skipping a base
	// glyph 10 that the IgnoreBaseGlyphs flag ignores.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{21: gdefClassMark, 22: gdefClassMark, 10: gdefClassBase}}
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(5, 7)}})
	mark2Arr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(3, 4)}})
	st := buildMarkPosSubtable(buildCoverage1(22), buildCoverage1(21), 1, markArr, mark2Arr)
	lk := buildLookupFlagged(6, flagIgnoreBaseGlyphs, 0, [][]byte{st})
	g := gposGDEF(t, gd, [][]byte{lk})
	pos := g.position([]GlyphIndex{21, 10, 22}, nil, "test")
	if pos[2].XOffset != -2 || pos[2].YOffset != -3 {
		t.Errorf("markmark skip pos[2]=%+v want (-2,-3)", pos[2])
	}
}

func TestGPOSContextPos3Skip(t *testing.T) {
	// Contextual positioning (format 3) input [10,11] with an ignored mark
	// between, applying a single adjustment on the first glyph. A second record
	// targets a sequence index past the matched input and is a no-op.
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	ctx := buildContextPos3([][]byte{buildCoverage1(10), buildCoverage1(11)}, []plr{{seq: 0, lookup: 1}, {seq: 9, lookup: 1}})
	lk := buildLookupFlagged(7, flagIgnoreMarks, 0, [][]byte{ctx})
	g := gposGDEF(t, gd, [][]byte{lk, singleAdvLookup(10, -30)})
	pos := g.position([]GlyphIndex{10, 20, 11}, nil, "test")
	if pos[0].XAdvance != -30 {
		t.Errorf("contextpos3 skip pos=%+v want -30", pos)
	}
}

func TestGPOSChainPos1BacktrackSkip(t *testing.T) {
	// Chaining positioning (format 1) with a backtrack glyph that must be matched
	// across an ignored mark (exercises the backward skip walk).
	gd := &gdefTable{glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark}}
	rule := buildPosChainRule([]GlyphIndex{10}, nil, nil, []plr{{seq: 0, lookup: 1}})
	chain := buildContextPos1(buildCoverage1(11), [][]byte{buildPosRuleSet([][]byte{rule})})
	lk := buildLookupFlagged(8, flagIgnoreMarks, 0, [][]byte{chain})
	g := gposGDEF(t, gd, [][]byte{lk, singleAdvLookup(11, -25)})
	// Run: 10 (backtrack), 20 (ignored mark), 11 (input). Backtrack 10 is reached
	// by skipping the mark.
	pos := g.position([]GlyphIndex{10, 20, 11}, nil, "test")
	if pos[2].XAdvance != -25 {
		t.Errorf("chainpos1 backtrack skip pos=%+v want pos[2].XAdvance=-25", pos)
	}
}

func TestGPOSUseMarkFilteringSet(t *testing.T) {
	// A GPOS pair kern under UseMarkFilteringSet: mark 20 is absent from set 0, so
	// it is skipped and the pair 10..11 kerns. Also drives the markFilteringSet
	// field in parseGPOSLookup.
	gd := &gdefTable{
		glyphClass:    map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark},
		markGlyphSets: []map[GlyphIndex]int{{21: 0}},
	}
	pp := buildPairPos1(buildCoverage1(10), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 11, xa1: -40}}),
	})
	lk := buildLookupFlagged(2, flagUseMarkFilteringSet, 0, [][]byte{pp})
	g := gposGDEF(t, gd, [][]byte{lk})
	if pos := g.position([]GlyphIndex{10, 20, 11}, nil, "test"); pos[0].XAdvance != -40 {
		t.Errorf("markfilter pair pos=%+v want -40", pos)
	}
}

func TestParseFontBadGDEF(t *testing.T) {
	// A malformed GDEF (a ClassDef of an unknown format) fails the whole parse.
	loca, glyf := glyfAndLoca([][]byte{{}}, false)
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(1),
		"hhea": hheaTable(800, -200, 100, 1),
		"hmtx": hmtxTable([]int{500}, []int{10}, 1),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'a': 0})}),
		"loca": loca,
		"glyf": glyf,
		"GDEF": buildGDEF(0, []byte{0, 9}, nil, nil),
	}
	if _, err := Parse(assemble(versionTrueType, tables)); err == nil {
		t.Error("expected error for malformed GDEF")
	}
}

func TestGPOSMarkAttachType(t *testing.T) {
	// MarkAttachmentType filtering: a pair kern skips a mark whose attachment
	// class differs from the type in the flag's high byte.
	gd := &gdefTable{
		glyphClass: map[GlyphIndex]int{10: gdefClassBase, 11: gdefClassBase, 20: gdefClassMark},
		markAttach: map[GlyphIndex]int{20: 1},
	}
	pp := buildPairPos1(buildCoverage1(10), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 11, xa1: -40}}),
	})
	// Flag wants attachment type 2 (0x0200); mark 20 is class 1, so it is skipped.
	lk := buildLookupFlagged(2, 0x0200, 0, [][]byte{pp})
	g := gposGDEF(t, gd, [][]byte{lk})
	if pos := g.position([]GlyphIndex{10, 20, 11}, nil, "test"); pos[0].XAdvance != -40 {
		t.Errorf("markattach pair pos=%+v want -40", pos)
	}
}
