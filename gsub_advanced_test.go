// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"reflect"
	"testing"
)

// This file synthesises GSUB subtables for the advanced substitution lookup
// types (2 multiple, 3 alternate, 5 contextual, 6 chaining, 7 extension and
// 8 reverse-chaining) and exercises every parse and apply branch, in the spirit
// of gsub_test.go and layout_test.go.

// --- builders ---------------------------------------------------------------

// seqRec is one SequenceLookupRecord for the builders.
type seqRec struct{ seqIndex, lookupIndex uint16 }

func writeRecs(w *bw, recs []seqRec) {
	for _, rc := range recs {
		w.u16(rc.seqIndex)
		w.u16(rc.lookupIndex)
	}
}

// buildSequence builds a Sequence / AlternateSet table (a count then glyph ids).
func buildSequence(glyphs ...GlyphIndex) []byte {
	w := &bw{}
	w.u16(uint16(len(glyphs)))
	for _, g := range glyphs {
		w.u16(uint16(g))
	}
	return w.bytes()
}

// buildSetListSubst builds the shared layout of multiple- and alternate-
// substitution format 1: format, coverageOffset, count, offsets[], sets, cov.
func buildSetListSubst(cov []byte, sets [][]byte) []byte {
	header := 6 + 2*len(sets)
	body := &bw{}
	var offs []int
	for _, s := range sets {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, s...)
	}
	covOff := header + len(body.b)
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(uint16(len(sets)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	return w.bytes()
}

// buildSeqRule builds a SequenceRule / ClassSequenceRule table. input holds the
// values for positions after the first (glyph ids or class values).
func buildSeqRule(input []uint16, recs []seqRec) []byte {
	w := &bw{}
	w.u16(uint16(len(input) + 1))
	w.u16(uint16(len(recs)))
	for _, v := range input {
		w.u16(v)
	}
	writeRecs(w, recs)
	return w.bytes()
}

// buildRuleSet builds a (Class)SequenceRuleSet or ChainSequenceRuleSet table:
// a count, offsets, then the rule bodies.
func buildRuleSet(rules [][]byte) []byte {
	header := 2 + 2*len(rules)
	body := &bw{}
	var offs []int
	for _, r := range rules {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, r...)
	}
	w := &bw{}
	w.u16(uint16(len(rules)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildCovSetSubst builds contextual-format-1 / chaining-format-1 (both share a
// layout: format(1), coverageOffset, count, offsets[], sets, cov). A nil set
// yields a null (zero) offset.
func buildCovSetSubst(cov []byte, sets [][]byte) []byte {
	header := 6 + 2*len(sets)
	body := &bw{}
	offs := make([]int, len(sets))
	for i, s := range sets {
		if s == nil {
			continue
		}
		offs[i] = header + len(body.b)
		body.b = append(body.b, s...)
	}
	covOff := header + len(body.b)
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(uint16(len(sets)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	return w.bytes()
}

// buildContext2 builds contextual-substitution format 2.
func buildContext2(cov, classDef []byte, sets [][]byte) []byte {
	header := 8 + 2*len(sets)
	body := &bw{}
	offs := make([]int, len(sets))
	for i, s := range sets {
		if s == nil {
			continue
		}
		offs[i] = header + len(body.b)
		body.b = append(body.b, s...)
	}
	covOff := header + len(body.b)
	classOff := covOff + len(cov)
	w := &bw{}
	w.u16(2)
	w.u16(uint16(covOff))
	w.u16(uint16(classOff))
	w.u16(uint16(len(sets)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	w.b = append(w.b, classDef...)
	return w.bytes()
}

// buildContext3 builds contextual-substitution format 3.
func buildContext3(covs [][]byte, recs []seqRec) []byte {
	header := 6 + 2*len(covs) + 4*len(recs)
	body := &bw{}
	var offs []int
	for _, c := range covs {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, c...)
	}
	w := &bw{}
	w.u16(3)
	w.u16(uint16(len(covs)))
	w.u16(uint16(len(recs)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	writeRecs(w, recs)
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildChainRule builds a Chain(Class)SequenceRule table.
func buildChainRule(bt, input, la []uint16, recs []seqRec) []byte {
	w := &bw{}
	w.u16(uint16(len(bt)))
	for _, v := range bt {
		w.u16(v)
	}
	w.u16(uint16(len(input) + 1))
	for _, v := range input {
		w.u16(v)
	}
	w.u16(uint16(len(la)))
	for _, v := range la {
		w.u16(v)
	}
	w.u16(uint16(len(recs)))
	writeRecs(w, recs)
	return w.bytes()
}

// buildChain2 builds chaining-substitution format 2.
func buildChain2(cov, btCD, inCD, laCD []byte, sets [][]byte) []byte {
	header := 12 + 2*len(sets)
	body := &bw{}
	offs := make([]int, len(sets))
	for i, s := range sets {
		if s == nil {
			continue
		}
		offs[i] = header + len(body.b)
		body.b = append(body.b, s...)
	}
	covOff := header + len(body.b)
	btOff := covOff + len(cov)
	inOff := btOff + len(btCD)
	laOff := inOff + len(inCD)
	w := &bw{}
	w.u16(2)
	w.u16(uint16(covOff))
	w.u16(uint16(btOff))
	w.u16(uint16(inOff))
	w.u16(uint16(laOff))
	w.u16(uint16(len(sets)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	w.b = append(w.b, btCD...)
	w.b = append(w.b, inCD...)
	w.b = append(w.b, laCD...)
	return w.bytes()
}

// buildChain3 builds chaining-substitution format 3.
func buildChain3(bt, input, la [][]byte, recs []seqRec) []byte {
	header := 10 + 2*(len(bt)+len(input)+len(la)) + 4*len(recs)
	body := &bw{}
	off := func(c []byte) uint16 {
		o := header + len(body.b)
		body.b = append(body.b, c...)
		return uint16(o)
	}
	var btOffs, inOffs, laOffs []uint16
	for _, c := range bt {
		btOffs = append(btOffs, off(c))
	}
	for _, c := range input {
		inOffs = append(inOffs, off(c))
	}
	for _, c := range la {
		laOffs = append(laOffs, off(c))
	}
	w := &bw{}
	w.u16(3)
	w.u16(uint16(len(bt)))
	for _, o := range btOffs {
		w.u16(o)
	}
	w.u16(uint16(len(input)))
	for _, o := range inOffs {
		w.u16(o)
	}
	w.u16(uint16(len(la)))
	for _, o := range laOffs {
		w.u16(o)
	}
	w.u16(uint16(len(recs)))
	writeRecs(w, recs)
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildExtension builds an extension-substitution (format 1) subtable wrapping
// inner, which is placed right after the 8-byte header.
func buildExtension(extType uint16, inner []byte) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(extType)
	w.u32(8)
	w.b = append(w.b, inner...)
	return w.bytes()
}

// buildReverseChain builds a reverse-chaining single-substitution subtable.
func buildReverseChain(cov []byte, bt, la [][]byte, subst []GlyphIndex) []byte {
	header := 10 + 2*len(bt) + 2*len(la) + 2*len(subst)
	body := &bw{}
	covOff := header
	body.b = append(body.b, cov...)
	off := func(c []byte) uint16 {
		o := header + len(body.b)
		body.b = append(body.b, c...)
		return uint16(o)
	}
	var btOffs, laOffs []uint16
	for _, c := range bt {
		btOffs = append(btOffs, off(c))
	}
	for _, c := range la {
		laOffs = append(laOffs, off(c))
	}
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(uint16(len(bt)))
	for _, o := range btOffs {
		w.u16(o)
	}
	w.u16(uint16(len(la)))
	for _, o := range laOffs {
		w.u16(o)
	}
	w.u16(uint16(len(subst)))
	for _, s := range subst {
		w.u16(uint16(s))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// applyOne builds a GSUB whose sole "test" feature activates lookupIdx (which
// need not equal the feature index) and applies it to in.
func applyOne(t *testing.T, lookups [][]byte, lookupIdx int, in []GlyphIndex) []GlyphIndex {
	t.Helper()
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "test", lookups: []uint16{uint16(lookupIdx)}}}
	blob := buildLayoutTable(scripts, feats, lookups)
	g, err := parseGSUB(blob)
	if err != nil {
		t.Fatalf("parseGSUB: %v", err)
	}
	return g.Apply(in, "test")
}

// --- Type 2: multiple substitution ------------------------------------------

func TestGSUBMultiple(t *testing.T) {
	// Glyph 5 -> [6,7]; glyph 8 -> [] (deletion). Others pass through.
	sub := buildSetListSubst(buildCoverage1(5, 8), [][]byte{
		buildSequence(6, 7),
		buildSequence(),
	})
	lk := buildLookup(2, [][]byte{sub})
	got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{5, 8, 9})
	if want := []GlyphIndex{6, 7, 9}; !reflect.DeepEqual(got, want) {
		t.Errorf("multiple = %v want %v", got, want)
	}
	// A run with no covered glyph is unchanged.
	if got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{9}); !reflect.DeepEqual(got, []GlyphIndex{9}) {
		t.Errorf("multiple no-match = %v want [9]", got)
	}
}

// --- Type 3: alternate substitution -----------------------------------------

func TestGSUBAlternate(t *testing.T) {
	// Glyph 5 -> first alternate 60; glyph 8 has an empty set (no change).
	sub := buildSetListSubst(buildCoverage1(5, 8), [][]byte{
		buildSequence(60, 61),
		buildSequence(),
	})
	lk := buildLookup(3, [][]byte{sub})
	got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{5, 8, 9})
	if want := []GlyphIndex{60, 8, 9}; !reflect.DeepEqual(got, want) {
		t.Errorf("alternate = %v want %v", got, want)
	}
}

// --- Type 5: contextual substitution ----------------------------------------

func TestGSUBContext1(t *testing.T) {
	// Lookup 0: single subst mapping 11->12 (nested target).
	// Lookup 1: contextual: for glyph 10 followed by 11, apply lookup 0 at pos 1.
	// The ruleset holds a non-matching rule first to exercise the continue path.
	nested := buildLookup(1, [][]byte{buildSingle1(1, buildCoverage1(11))})
	rules := buildRuleSet([][]byte{
		buildSeqRule([]uint16{99}, []seqRec{{0, 0}}), // won't match
		buildSeqRule([]uint16{11}, []seqRec{{1, 0}}), // matches, apply lookup 0 at +1
	})
	ctx := buildLookup(5, [][]byte{buildCovSetSubst(buildCoverage1(10), [][]byte{rules})})
	lookups := [][]byte{nested, ctx}

	// Activate lookup 1 (the contextual lookup): 10,11 -> 10,12.
	got := applyOne(t, lookups, 1, []GlyphIndex{10, 11})
	if want := []GlyphIndex{10, 12}; !reflect.DeepEqual(got, want) {
		t.Errorf("context1 = %v want %v", got, want)
	}

	// Covered first glyph but no following glyph: matchGlyphSeq bounds-fail.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10}); !reflect.DeepEqual(got, []GlyphIndex{10}) {
		t.Errorf("context1 bounds = %v want [10]", got)
	}
	// Covered first glyph, following glyph matches no rule: final no-match.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 50}); !reflect.DeepEqual(got, []GlyphIndex{10, 50}) {
		t.Errorf("context1 no-rule = %v want [10 50]", got)
	}
	// Uncovered first glyph.
	if got := applyOne(t, lookups, 1, []GlyphIndex{7, 11}); !reflect.DeepEqual(got, []GlyphIndex{7, 11}) {
		t.Errorf("context1 uncovered = %v want [7 11]", got)
	}
}

func TestGSUBContext2(t *testing.T) {
	// classDef: 21->1, 22->2, 23->5. Coverage gates on 20,21,23.
	nested := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(22))})
	cd := buildClassDef1(20, 0, 1, 2, 5) // 20->0,21->1,22->2,23->5
	// Ruleset for class 1: input class [2], apply lookup 0 at +1. Include a
	// mismatching rule first (class 9) to hit matchClassSeq mismatch + continue.
	set1 := buildRuleSet([][]byte{
		buildSeqRule([]uint16{9}, []seqRec{{0, 0}}),
		buildSeqRule([]uint16{2}, []seqRec{{1, 0}}),
	})
	ctx := buildLookup(5, [][]byte{buildContext2(buildCoverage1(20, 21, 23), cd, [][]byte{nil, set1, nil})})
	lookups := [][]byte{nested, ctx}

	// [21,22]: 21 is class1 -> set1; 22 is class2 matches input -> 22->122.
	got := applyOne(t, lookups, 1, []GlyphIndex{21, 22})
	if want := []GlyphIndex{21, 122}; !reflect.DeepEqual(got, want) {
		t.Errorf("context2 = %v want %v", got, want)
	}
	// [23,...]: 23 is covered but class 5 >= setCount(3): the class-range guard.
	if got := applyOne(t, lookups, 1, []GlyphIndex{23, 22}); !reflect.DeepEqual(got, []GlyphIndex{23, 22}) {
		t.Errorf("context2 class-range = %v want unchanged", got)
	}
	// [20,22]: 20 covered, class 0 -> set0 is empty (nil) -> no rule.
	if got := applyOne(t, lookups, 1, []GlyphIndex{20, 22}); !reflect.DeepEqual(got, []GlyphIndex{20, 22}) {
		t.Errorf("context2 empty-set = %v want unchanged", got)
	}
	// Uncovered glyph.
	if got := applyOne(t, lookups, 1, []GlyphIndex{99, 22}); !reflect.DeepEqual(got, []GlyphIndex{99, 22}) {
		t.Errorf("context2 uncovered = %v want unchanged", got)
	}
	// class match but bounds fail (no following glyph).
	if got := applyOne(t, lookups, 1, []GlyphIndex{21}); !reflect.DeepEqual(got, []GlyphIndex{21}) {
		t.Errorf("context2 bounds = %v want [21]", got)
	}
}

func TestGSUBContext3(t *testing.T) {
	// input coverage sequence [{10},{11}] applies lookup 0 (11->12) at pos 1.
	nested := buildLookup(1, [][]byte{buildSingle1(1, buildCoverage1(11))})
	ctx := buildLookup(5, [][]byte{buildContext3(
		[][]byte{buildCoverage1(10), buildCoverage1(11)},
		[]seqRec{{1, 0}},
	)})
	lookups := [][]byte{nested, ctx}
	got := applyOne(t, lookups, 1, []GlyphIndex{10, 11})
	if want := []GlyphIndex{10, 12}; !reflect.DeepEqual(got, want) {
		t.Errorf("context3 = %v want %v", got, want)
	}
	// No match (second glyph not covered).
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 99}); !reflect.DeepEqual(got, []GlyphIndex{10, 99}) {
		t.Errorf("context3 no-match = %v want unchanged", got)
	}
	// bounds fail: single covered glyph, sequence needs two.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10}); !reflect.DeepEqual(got, []GlyphIndex{10}) {
		t.Errorf("context3 bounds = %v want [10]", got)
	}
	// Empty input sequence (glyphCount 0) is a no-op guard.
	empty := buildLookup(5, [][]byte{buildContext3(nil, []seqRec{{0, 0}})})
	if got := applyOne(t, [][]byte{nested, empty}, 1, []GlyphIndex{10, 11}); !reflect.DeepEqual(got, []GlyphIndex{10, 11}) {
		t.Errorf("context3 empty = %v want unchanged", got)
	}
}

// --- Type 6: chaining-contextual substitution -------------------------------

func TestGSUBChain1(t *testing.T) {
	// For glyph 30 with backtrack 10, input [31], lookahead 40: apply lookup 0
	// (31->131) at pos 1. A leading non-matching rule exercises the continue.
	nested := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(31))})
	rules := buildRuleSet([][]byte{
		buildChainRule([]uint16{99}, []uint16{31}, []uint16{40}, []seqRec{{1, 0}}), // backtrack mismatch
		buildChainRule([]uint16{10}, []uint16{31}, []uint16{40}, []seqRec{{1, 0}}), // matches
	})
	chain := buildLookup(6, [][]byte{buildCovSetSubst(buildCoverage1(30), [][]byte{rules})})
	lookups := [][]byte{nested, chain}

	got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 31, 40})
	if want := []GlyphIndex{10, 30, 131, 40}; !reflect.DeepEqual(got, want) {
		t.Errorf("chain1 = %v want %v", got, want)
	}
	// At position 0 the backtrack underflows the run (matchBacktrackGlyphs bounds).
	// Put a covered 30 at the very start with no backtrack available.
	if got := applyOne(t, lookups, 1, []GlyphIndex{30, 31, 40}); !reflect.DeepEqual(got, []GlyphIndex{30, 31, 40}) {
		t.Errorf("chain1 bt-bounds = %v want unchanged", got)
	}
	// Input mismatch: 30 present, backtrack ok, but next glyph is not 31.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 99, 40}); !reflect.DeepEqual(got, []GlyphIndex{10, 30, 99, 40}) {
		t.Errorf("chain1 input-mismatch = %v want unchanged", got)
	}
	// Lookahead mismatch / missing: no glyph after input.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 31}); !reflect.DeepEqual(got, []GlyphIndex{10, 30, 31}) {
		t.Errorf("chain1 la-miss = %v want unchanged", got)
	}
	// Uncovered first glyph.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 99, 31, 40}); !reflect.DeepEqual(got, []GlyphIndex{10, 99, 31, 40}) {
		t.Errorf("chain1 uncovered = %v want unchanged", got)
	}
}

func TestGSUBChain2(t *testing.T) {
	// Classes: backtrack class of 10 = 1; input class of 30 = 1, of 31 = 2;
	// lookahead class of 40 = 1. Rule: bt[1], input[2], la[1] -> lookup 0.
	nested := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(31))})
	btCD := buildClassDef1(10, 1)    // 10->1
	inCD := buildClassDef1(30, 1, 2) // 30->1, 31->2
	laCD := buildClassDef1(40, 1)    // 40->1
	set1 := buildRuleSet([][]byte{
		buildChainRule([]uint16{9}, []uint16{2}, []uint16{1}, []seqRec{{1, 0}}), // bt class mismatch
		buildChainRule([]uint16{1}, []uint16{2}, []uint16{1}, []seqRec{{1, 0}}), // matches
	})
	chain := buildLookup(6, [][]byte{buildChain2(buildCoverage1(30), btCD, inCD, laCD, [][]byte{nil, set1})})
	lookups := [][]byte{nested, chain}

	got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 31, 40})
	if want := []GlyphIndex{10, 30, 131, 40}; !reflect.DeepEqual(got, want) {
		t.Errorf("chain2 = %v want %v", got, want)
	}
	// Covered glyph whose input class >= setCount: give 30 an out-of-range set by
	// shrinking sets. Here class of 30 is 1 and setCount is 2, so use a glyph
	// mapped to a higher class via a wider classdef in a second fixture.
	inCD2 := buildClassDef1(30, 3) // 30->3
	chain2 := buildLookup(6, [][]byte{buildChain2(buildCoverage1(30), btCD, inCD2, laCD, [][]byte{nil, set1})})
	if got := applyOne(t, [][]byte{nested, chain2}, 1, []GlyphIndex{10, 30, 31, 40}); !reflect.DeepEqual(got, []GlyphIndex{10, 30, 31, 40}) {
		t.Errorf("chain2 class-range = %v want unchanged", got)
	}
	// Uncovered first glyph.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 99, 31, 40}); !reflect.DeepEqual(got, []GlyphIndex{10, 99, 31, 40}) {
		t.Errorf("chain2 uncovered = %v want unchanged", got)
	}
	// Backtrack class mismatch at run start (bounds too): 30 at index 0.
	if got := applyOne(t, lookups, 1, []GlyphIndex{30, 31, 40}); !reflect.DeepEqual(got, []GlyphIndex{30, 31, 40}) {
		t.Errorf("chain2 bt = %v want unchanged", got)
	}
}

func TestGSUBChain3(t *testing.T) {
	// backtrack {10}, input {30}{31}, lookahead {40}: apply lookup 0 (31->131).
	nested := buildLookup(1, [][]byte{buildSingle1(100, buildCoverage1(31))})
	chain := buildLookup(6, [][]byte{buildChain3(
		[][]byte{buildCoverage1(10)},
		[][]byte{buildCoverage1(30), buildCoverage1(31)},
		[][]byte{buildCoverage1(40)},
		[]seqRec{{1, 0}},
	)})
	lookups := [][]byte{nested, chain}

	got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 31, 40})
	if want := []GlyphIndex{10, 30, 131, 40}; !reflect.DeepEqual(got, want) {
		t.Errorf("chain3 = %v want %v", got, want)
	}
	// input mismatch.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 99, 40}); !reflect.DeepEqual(got, []GlyphIndex{10, 30, 99, 40}) {
		t.Errorf("chain3 input-mismatch = %v want unchanged", got)
	}
	// backtrack mismatch.
	if got := applyOne(t, lookups, 1, []GlyphIndex{99, 30, 31, 40}); !reflect.DeepEqual(got, []GlyphIndex{99, 30, 31, 40}) {
		t.Errorf("chain3 bt-mismatch = %v want unchanged", got)
	}
	// lookahead mismatch.
	if got := applyOne(t, lookups, 1, []GlyphIndex{10, 30, 31, 99}); !reflect.DeepEqual(got, []GlyphIndex{10, 30, 31, 99}) {
		t.Errorf("chain3 la-mismatch = %v want unchanged", got)
	}
	// Empty input coverage (inputGlyphCount 0) is a no-op guard.
	empty := buildLookup(6, [][]byte{buildChain3(nil, nil, nil, []seqRec{{0, 0}})})
	if got := applyOne(t, [][]byte{nested, empty}, 1, []GlyphIndex{30}); !reflect.DeepEqual(got, []GlyphIndex{30}) {
		t.Errorf("chain3 empty = %v want [30]", got)
	}
}

// --- Type 7: extension substitution -----------------------------------------

func TestGSUBExtension(t *testing.T) {
	// Extension wrapping a single substitution 5->15.
	inner := buildSingle1(10, buildCoverage1(5))
	lk := buildLookup(7, [][]byte{buildExtension(1, inner)})
	got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{5, 6})
	if want := []GlyphIndex{15, 6}; !reflect.DeepEqual(got, want) {
		t.Errorf("extension = %v want %v", got, want)
	}
}

func TestGSUBExtensionWrapsReverse(t *testing.T) {
	// Extension wrapping a reverse-chaining single subst: verifies the reverse
	// flag is detected through the extension indirection.
	rev := buildReverseChain(buildCoverage1(50), nil, nil, []GlyphIndex{60})
	lk := buildLookup(7, [][]byte{buildExtension(8, rev)})
	got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{50, 50})
	if want := []GlyphIndex{60, 60}; !reflect.DeepEqual(got, want) {
		t.Errorf("extension-reverse = %v want %v", got, want)
	}
}

// --- Type 8: reverse chaining single substitution ---------------------------

func TestGSUBReverse(t *testing.T) {
	// Glyph 50 -> 60 when preceded by 10 and followed by 70.
	rev := buildReverseChain(buildCoverage1(50, 51),
		[][]byte{buildCoverage1(10)},
		[][]byte{buildCoverage1(70)},
		[]GlyphIndex{60}) // only one substitute: coverage index 1 (glyph 51) is out of range
	lk := buildLookup(8, [][]byte{rev})
	got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{10, 50, 70})
	if want := []GlyphIndex{10, 60, 70}; !reflect.DeepEqual(got, want) {
		t.Errorf("reverse = %v want %v", got, want)
	}
	// Substitute-index out of range (glyph 51 -> coverage index 1 >= len(subst)).
	if got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{10, 51, 70}); !reflect.DeepEqual(got, []GlyphIndex{10, 51, 70}) {
		t.Errorf("reverse idx-range = %v want unchanged", got)
	}
	// Backtrack fails (missing preceding 10).
	if got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{99, 50, 70}); !reflect.DeepEqual(got, []GlyphIndex{99, 50, 70}) {
		t.Errorf("reverse bt-fail = %v want unchanged", got)
	}
	// Lookahead fails (missing following 70).
	if got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{10, 50, 99}); !reflect.DeepEqual(got, []GlyphIndex{10, 50, 99}) {
		t.Errorf("reverse la-fail = %v want unchanged", got)
	}
	// Uncovered glyph.
	if got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{10, 55, 70}); !reflect.DeepEqual(got, []GlyphIndex{10, 55, 70}) {
		t.Errorf("reverse uncovered = %v want unchanged", got)
	}
	// Backtrack underflows the run start (matchBacktrackCov bounds guard): the
	// covered 50 sits at index 0 with no preceding glyph, followed by 70.
	if got := applyOne(t, [][]byte{lk}, 0, []GlyphIndex{50, 70}); !reflect.DeepEqual(got, []GlyphIndex{50, 70}) {
		t.Errorf("reverse bt-bounds = %v want unchanged", got)
	}
}

func TestGSUBContextNoRecords(t *testing.T) {
	// A contextual match carrying no SequenceLookupRecords is a no-op that still
	// consumes its input (exercises the empty-records path).
	ctx := buildLookup(5, [][]byte{buildContext3([][]byte{buildCoverage1(10)}, nil)})
	if got := applyOne(t, [][]byte{ctx}, 0, []GlyphIndex{10, 11}); !reflect.DeepEqual(got, []GlyphIndex{10, 11}) {
		t.Errorf("context no-records = %v want unchanged", got)
	}
}

// --- nested lookup edge cases -----------------------------------------------

func TestGSUBNestedOutOfRange(t *testing.T) {
	// A contextual rule whose record targets an out-of-range lookup index and
	// another whose record sits past the run end: both are silent no-ops but the
	// context still matches (and thus advances).
	ctx := buildLookup(5, [][]byte{buildContext3(
		[][]byte{buildCoverage1(10)},
		[]seqRec{{0, 9}, {5, 0}}, // lookup 9 out of range; seqIndex 5 past run
	)})
	got := applyOne(t, [][]byte{ctx}, 0, []GlyphIndex{10})
	if !reflect.DeepEqual(got, []GlyphIndex{10}) {
		t.Errorf("nested oob = %v want [10]", got)
	}
}

func TestGSUBNestedNoMatch(t *testing.T) {
	// Context matches and invokes lookup 0, but at that position lookup 0's
	// subtable does not cover the glyph, so the nested application is a no-op
	// (applyLookupAt's subtable loop completes without a match).
	nested := buildLookup(1, [][]byte{buildSingle1(1, buildCoverage1(999))})
	ctx := buildLookup(5, [][]byte{buildContext3(
		[][]byte{buildCoverage1(10)},
		[]seqRec{{0, 0}},
	)})
	got := applyOne(t, [][]byte{nested, ctx}, 1, []GlyphIndex{10})
	if !reflect.DeepEqual(got, []GlyphIndex{10}) {
		t.Errorf("nested no-match = %v want [10]", got)
	}
}

func TestGSUBNestedDepthGuard(t *testing.T) {
	// A contextual lookup (index 0) whose record references itself recurses until
	// the depth guard stops it, leaving the run unchanged (no infinite loop).
	ctx := buildLookup(5, [][]byte{buildContext3(
		[][]byte{buildCoverage1(10)},
		[]seqRec{{0, 0}}, // references lookup 0 (itself) at the same position
	)})
	got := applyOne(t, [][]byte{ctx}, 0, []GlyphIndex{10})
	if !reflect.DeepEqual(got, []GlyphIndex{10}) {
		t.Errorf("depth guard = %v want [10]", got)
	}
}

// --- parse error paths ------------------------------------------------------

// corruptAt returns a copy of b with the big-endian uint16 at pos set to v.
func corruptAt(b []byte, pos int, v uint16) []byte {
	c := append([]byte(nil), b...)
	c[pos] = byte(v >> 8)
	c[pos+1] = byte(v)
	return c
}

// perr collapses a (subtable, error) parse result to just its error.
func perr(_ gsubSubtable, err error) error { return err }

func mustErr(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: expected error, got nil", name)
	}
}

func TestGSUBMultipleParseErrors(t *testing.T) {
	// format@0, coverageOffset@2, count@4, sequenceOffset@6, seq@8, cov@14.
	valid := buildSetListSubst(buildCoverage1(5), [][]byte{buildSequence(6, 7)})
	mustErr(t, "bad format", perr(parseGSUBMultiple(corruptAt(valid, 0, 2))))
	mustErr(t, "bad coverage", perr(parseGSUBMultiple(corruptAt(valid, 2, 0xFFFF))))
	mustErr(t, "bad sequence offset", perr(parseGSUBMultiple(corruptAt(valid, 6, 0xFFFF))))
	mustErr(t, "truncated", perr(parseGSUBMultiple([]byte{0, 1, 0, 6, 0, 1})))
}

func TestGSUBAlternateParseErrors(t *testing.T) {
	valid := buildSetListSubst(buildCoverage1(5), [][]byte{buildSequence(60, 61)})
	mustErr(t, "bad format", perr(parseGSUBAlternate(corruptAt(valid, 0, 2))))
	mustErr(t, "bad coverage", perr(parseGSUBAlternate(corruptAt(valid, 2, 0xFFFF))))
	mustErr(t, "bad set offset", perr(parseGSUBAlternate(corruptAt(valid, 6, 0xFFFF))))
	mustErr(t, "truncated", perr(parseGSUBAlternate([]byte{0, 1, 0, 6, 0, 1})))
}

func TestParseSequenceTableError(t *testing.T) {
	if _, err := parseSequenceTable([]byte{0, 2, 0, 5}); err == nil { // count 2, one glyph
		t.Error("truncated sequence should error")
	}
}

func TestGSUBContextParseErrors(t *testing.T) {
	if _, err := parseGSUBContext([]byte{0, 9}); err == nil { // format 9
		t.Error("bad context format should error")
	}
	// Format 1: format@0, covOff@2, count@4, setOff@6.
	v1 := buildCovSetSubst(buildCoverage1(10), [][]byte{
		buildRuleSet([][]byte{buildSeqRule([]uint16{11}, []seqRec{{1, 0}})}),
	})
	mustErr(t, "ctx1 coverage", perr(parseGSUBContext(corruptAt(v1, 2, 0xFFFF))))
	mustErr(t, "ctx1 set", perr(parseGSUBContext(corruptAt(v1, 6, 0xFFFF))))
	mustErr(t, "ctx1 truncated", perr(parseGSUBContext([]byte{0, 1, 0, 6, 0, 1})))
	// Format 2: format@0, covOff@2, classOff@4, count@6, setOff@8.
	v2 := buildContext2(buildCoverage1(20), buildClassDef1(20, 1), [][]byte{
		buildRuleSet([][]byte{buildSeqRule([]uint16{2}, []seqRec{{1, 0}})}),
	})
	mustErr(t, "ctx2 coverage", perr(parseGSUBContext(corruptAt(v2, 2, 0xFFFF))))
	mustErr(t, "ctx2 classdef", perr(parseGSUBContext(corruptAt(v2, 4, 0xFFFF))))
	mustErr(t, "ctx2 set", perr(parseGSUBContext(corruptAt(v2, 8, 0xFFFF))))
	mustErr(t, "ctx2 truncated", perr(parseGSUBContext([]byte{0, 2, 0, 8, 0, 12, 0, 1})))
	// Format 3: format@0, glyphCount@2, lookupCount@4, covOff@6.
	v3 := buildContext3([][]byte{buildCoverage1(10)}, []seqRec{{0, 0}})
	mustErr(t, "ctx3 coverage", perr(parseGSUBContext(corruptAt(v3, 6, 0xFFFF))))
	mustErr(t, "ctx3 truncated", perr(parseGSUBContext([]byte{0, 3, 0, 1, 0, 0})))
}

func TestParseSeqRuleErrors(t *testing.T) {
	if _, err := parseSeqRule([]byte{0, 2, 0, 0}); err == nil { // needs 1 input glyph
		t.Error("truncated seq rule should error")
	}
	// SequenceRuleSet: count@0, ruleOff@2.
	rs := buildRuleSet([][]byte{buildSeqRule([]uint16{11}, []seqRec{{1, 0}})})
	if _, err := parseSeqRuleSet(corruptAt(rs, 2, 0xFFFF)); err == nil {
		t.Error("bad rule offset should error")
	}
	if _, err := parseSeqRuleSet([]byte{0, 1}); err == nil { // count 1, no offset
		t.Error("truncated rule set should error")
	}
}

func TestGSUBChainParseErrors(t *testing.T) {
	if _, err := parseGSUBChain([]byte{0, 9}); err == nil {
		t.Error("bad chain format should error")
	}
	// Format 1.
	v1 := buildCovSetSubst(buildCoverage1(30), [][]byte{
		buildRuleSet([][]byte{buildChainRule([]uint16{10}, []uint16{31}, []uint16{40}, []seqRec{{1, 0}})}),
	})
	mustErr(t, "chain1 coverage", perr(parseGSUBChain(corruptAt(v1, 2, 0xFFFF))))
	mustErr(t, "chain1 set", perr(parseGSUBChain(corruptAt(v1, 6, 0xFFFF))))
	mustErr(t, "chain1 truncated", perr(parseGSUBChain([]byte{0, 1, 0, 6, 0, 1})))
	// Format 2: covOff@2, btOff@4, inOff@6, laOff@8, count@10, setOff@12.
	v2 := buildChain2(buildCoverage1(30), buildClassDef1(10, 1), buildClassDef1(30, 1, 2),
		buildClassDef1(40, 1), [][]byte{
			buildRuleSet([][]byte{buildChainRule([]uint16{1}, []uint16{2}, []uint16{1}, []seqRec{{1, 0}})}),
		})
	mustErr(t, "chain2 coverage", perr(parseGSUBChain(corruptAt(v2, 2, 0xFFFF))))
	mustErr(t, "chain2 bt classdef", perr(parseGSUBChain(corruptAt(v2, 4, 0xFFFF))))
	mustErr(t, "chain2 in classdef", perr(parseGSUBChain(corruptAt(v2, 6, 0xFFFF))))
	mustErr(t, "chain2 la classdef", perr(parseGSUBChain(corruptAt(v2, 8, 0xFFFF))))
	mustErr(t, "chain2 set", perr(parseGSUBChain(corruptAt(v2, 12, 0xFFFF))))
	mustErr(t, "chain2 truncated", perr(parseGSUBChain([]byte{0, 2, 0, 12, 0, 14, 0, 16, 0, 18, 0, 1})))
	// Format 3: btOff@4, inOff1@8, laOff@14.
	v3 := buildChain3([][]byte{buildCoverage1(10)},
		[][]byte{buildCoverage1(30), buildCoverage1(31)},
		[][]byte{buildCoverage1(40)}, []seqRec{{1, 0}})
	mustErr(t, "chain3 bt coverage", perr(parseGSUBChain(corruptAt(v3, 4, 0xFFFF))))
	mustErr(t, "chain3 in coverage", perr(parseGSUBChain(corruptAt(v3, 8, 0xFFFF))))
	mustErr(t, "chain3 la coverage", perr(parseGSUBChain(corruptAt(v3, 14, 0xFFFF))))
	mustErr(t, "chain3 truncated", perr(parseGSUBChain([]byte{0, 3, 0, 1, 0, 8})))
}

func TestParseChainRuleErrors(t *testing.T) {
	if _, err := parseChainRule([]byte{0, 1}); err == nil { // backtrack count 1, no glyph
		t.Error("truncated chain rule should error")
	}
	rs := buildRuleSet([][]byte{buildChainRule(nil, []uint16{31}, nil, []seqRec{{0, 0}})})
	if _, err := parseChainRuleSet(corruptAt(rs, 2, 0xFFFF)); err == nil {
		t.Error("bad chain rule offset should error")
	}
	if _, err := parseChainRuleSet([]byte{0, 1}); err == nil {
		t.Error("truncated chain rule set should error")
	}
}

func TestGSUBExtensionParseErrors(t *testing.T) {
	mustErr(t, "bad format", perr(parseGSUBExtension([]byte{0, 2, 0, 1, 0, 0, 0, 8})))
	mustErr(t, "truncated", perr(parseGSUBExtension([]byte{0, 1, 0, 1})))
	mustErr(t, "nested extension", perr(parseGSUBExtension(buildExtension(7, []byte{0, 0}))))
}

func TestGSUBReverseParseErrors(t *testing.T) {
	// format@0, covOff@2, btCount@4, btOff@6, laCount@8, laOff@10, substCount@12.
	valid := buildReverseChain(buildCoverage1(50),
		[][]byte{buildCoverage1(10)}, [][]byte{buildCoverage1(70)}, []GlyphIndex{60})
	mustErr(t, "bad format", perr(parseGSUBReverse(corruptAt(valid, 0, 2))))
	mustErr(t, "bad coverage", perr(parseGSUBReverse(corruptAt(valid, 2, 0xFFFF))))
	mustErr(t, "bad backtrack", perr(parseGSUBReverse(corruptAt(valid, 6, 0xFFFF))))
	mustErr(t, "bad lookahead", perr(parseGSUBReverse(corruptAt(valid, 10, 0xFFFF))))
	mustErr(t, "truncated", perr(parseGSUBReverse([]byte{0, 1, 0, 16, 0, 1})))
}
