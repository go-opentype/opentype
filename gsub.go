// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file decodes and applies the GSUB (Glyph Substitution) table. All eight
// substitution lookup types defined by OpenType are implemented:
//
//   - Type 1, single substitution (formats 1 and 2): replace one glyph with
//     another, used by features such as small caps or stylistic swaps.
//   - Type 2, multiple substitution (format 1): replace one glyph with a
//     sequence of glyphs (one -> many), e.g. decomposing a ligature.
//   - Type 3, alternate substitution (format 1): choose an alternate glyph; this
//     implementation exposes the first (default) alternate.
//   - Type 4, ligature substitution (format 1): replace a sequence of glyphs
//     with a single ligature glyph, used by "liga" (for example f+i -> fi).
//   - Type 5, contextual substitution (formats 1/2/3): apply nested lookups when
//     the glyph run matches a glyph-, class- or coverage-based context.
//   - Type 6, chaining-contextual substitution (formats 1/2/3): like type 5 but
//     with backtrack and lookahead context, the backbone of Arabic
//     init/medi/fina contextual forms and Indic reordering.
//   - Type 7, extension substitution (format 1): a 32-bit indirection to a
//     subtable of another type, used to reach past the 16-bit offset limit.
//   - Type 8, reverse chaining single substitution (format 1): a single
//     substitution with backtrack and lookahead context, applied back-to-front.
//
// Contextual and chaining lookups (types 5 and 6) drive nested lookups through
// SequenceLookupRecords; that recursion is bounded by maxGSUBNest. A lookup of
// an undefined type (there is none above 8) parses to a no-op and is skipped.

// maxGSUBNest bounds the recursion depth of nested (contextual) lookup
// application, guarding against a font whose lookups reference one another in a
// cycle.
const maxGSUBNest = 64

// gsub is a decoded GSUB table.
type gsub struct {
	layoutHeader
	lookups []gsubLookup
}

// gsubLookup is one decoded GSUB Lookup: its (supported) subtables. reverse is
// set when the lookup performs reverse chaining single substitution (type 8),
// which is applied to the glyph run back-to-front.
type gsubLookup struct {
	subtables []gsubSubtable
	reverse   bool
}

// gsubApplier carries the state threaded through a GSUB pass: the full lookup
// list (so contextual lookups can recurse into nested lookups) and the current
// recursion depth. clusters, when non-nil, is a parallel per-glyph slice kept
// in lock-step with the run as it is rewritten: the length-changing subtables
// (multiple and ligature substitution) splice it exactly as they splice the
// glyphs, so each output glyph records the input position it derives from. It
// is nil for plain (untracked) application, which leaves those subtables' fast
// path unchanged.
type gsubApplier struct {
	lookups  []gsubLookup
	depth    int
	clusters []int
}

// gsubSubtable attempts a substitution at position i of a glyph run. On success
// it returns the resulting run, the index to continue from, and true; otherwise
// it returns the run unchanged and false. Contextual subtables use the applier
// to run their nested lookups.
type gsubSubtable interface {
	sub(a *gsubApplier, glyphs []GlyphIndex, i int) (out []GlyphIndex, next int, ok bool)
}

// seqLookupRecord binds a position within a matched sequence (seqIndex, counted
// from the first input glyph) to the lookup applied there.
type seqLookupRecord struct {
	seqIndex    uint16
	lookupIndex uint16
}

// readU16s reads n consecutive big-endian uint16 values; a non-positive n
// yields nil (used where a count field may underflow, e.g. inputCount-1).
func readU16s(r *reader, n int) []uint16 {
	if n <= 0 {
		return nil
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = r.u16()
	}
	return out
}

// parseSeqLookupRecords reads n SequenceLookupRecords.
func parseSeqLookupRecords(r *reader, n int) []seqLookupRecord {
	if n <= 0 {
		return nil
	}
	recs := make([]seqLookupRecord, n)
	for i := range recs {
		recs[i].seqIndex = r.u16()
		recs[i].lookupIndex = r.u16()
	}
	return recs
}

// parseCoverageList decodes the Coverage tables at the given offsets (measured
// from the start of b) into a slice of glyph->coverage-index maps.
func parseCoverageList(b []byte, offs []uint16) ([]map[GlyphIndex]int, error) {
	out := make([]map[GlyphIndex]int, len(offs))
	for i, off := range offs {
		cov, err := parseCoverage(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		out[i] = cov
	}
	return out, nil
}

// parseGSUB decodes a GSUB table.
func parseGSUB(b []byte) (*gsub, error) {
	hdr, raw, err := parseLayoutCommon(b)
	if err != nil {
		return nil, err
	}
	lookups := make([]gsubLookup, len(raw))
	for i, lk := range raw {
		gl, err := parseGSUBLookup(lk)
		if err != nil {
			return nil, err
		}
		lookups[i] = gl
	}
	return &gsub{layoutHeader: hdr, lookups: lookups}, nil
}

// parseGSUBLookup decodes one Lookup table, keeping only subtables of a
// supported type and flagging reverse-chaining lookups.
func parseGSUBLookup(b []byte) (gsubLookup, error) {
	r := reader{b: b}
	lookupType := r.u16()
	r.skip(2) // lookupFlag
	subCount := int(r.u16())
	offs := make([]int, subCount)
	for i := 0; i < subCount; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return gsubLookup{}, fmt.Errorf("opentype: gsub lookup: %w", r.err)
	}
	var subs []gsubSubtable
	for _, off := range offs {
		st, err := parseGSUBSubtable(lookupType, subslice(b, off))
		if err != nil {
			return gsubLookup{}, err
		}
		if st != nil {
			subs = append(subs, st)
		}
	}
	gl := gsubLookup{subtables: subs}
	for _, st := range subs {
		if _, ok := st.(*gsubReverseChain1); ok {
			gl.reverse = true
			break
		}
	}
	return gl, nil
}

// parseGSUBSubtable decodes one subtable of the given lookup type; an undefined
// type yields a nil subtable that the caller drops.
func parseGSUBSubtable(lookupType uint16, b []byte) (gsubSubtable, error) {
	switch lookupType {
	case 1:
		return parseGSUBSingle(b)
	case 2:
		return parseGSUBMultiple(b)
	case 3:
		return parseGSUBAlternate(b)
	case 4:
		return parseGSUBLigature(b)
	case 5:
		return parseGSUBContext(b)
	case 6:
		return parseGSUBChain(b)
	case 7:
		return parseGSUBExtension(b)
	case 8:
		return parseGSUBReverse(b)
	default:
		return nil, nil // no lookup type above 8 is defined (documented)
	}
}

// --- Type 1: single substitution --------------------------------------------

// gsubSingle1 is a single-substitution format 1 subtable: covered glyphs are
// shifted by a constant delta.
type gsubSingle1 struct {
	cov   map[GlyphIndex]int
	delta int16
}

// gsubSingle2 is a single-substitution format 2 subtable: covered glyphs are
// replaced by the glyph at their coverage index.
type gsubSingle2 struct {
	cov   map[GlyphIndex]int
	subst []GlyphIndex
}

// parseGSUBSingle decodes a single-substitution subtable (format 1 or 2).
func parseGSUBSingle(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	covOff := int(r.u16())
	switch format {
	case 1:
		delta := r.i16()
		if r.err != nil {
			return nil, fmt.Errorf("opentype: single subst 1: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		return &gsubSingle1{cov: cov, delta: delta}, nil
	case 2:
		n := int(r.u16())
		subst := make([]GlyphIndex, n)
		for i := 0; i < n; i++ {
			subst[i] = GlyphIndex(r.u16())
		}
		if r.err != nil {
			return nil, fmt.Errorf("opentype: single subst 2: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		return &gsubSingle2{cov: cov, subst: subst}, nil
	default:
		return nil, fmt.Errorf("opentype: single subst format %d", format)
	}
}

func (s *gsubSingle1) sub(_ *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	if _, ok := s.cov[g[i]]; !ok {
		return g, i, false
	}
	g[i] = GlyphIndex(uint16(g[i]) + uint16(s.delta))
	return g, i + 1, true
}

func (s *gsubSingle2) sub(_ *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	idx, ok := s.cov[g[i]]
	if !ok {
		return g, i, false
	}
	g[i] = s.subst[idx]
	return g, i + 1, true
}

// --- Type 2: multiple substitution ------------------------------------------

// gsubMultiple1 replaces a covered glyph with the sequence at its coverage
// index (one -> many; an empty sequence deletes the glyph).
type gsubMultiple1 struct {
	cov  map[GlyphIndex]int
	seqs [][]GlyphIndex
}

// parseSequenceTable decodes a Sequence/AlternateSet table: a count followed by
// that many glyph ids.
func parseSequenceTable(b []byte) ([]GlyphIndex, error) {
	r := reader{b: b}
	n := int(r.u16())
	out := make([]GlyphIndex, n)
	for i := 0; i < n; i++ {
		out[i] = GlyphIndex(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: sequence: %w", r.err)
	}
	return out, nil
}

// parseGSUBMultiple decodes a multiple-substitution (format 1) subtable.
func parseGSUBMultiple(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	if format != 1 {
		return nil, fmt.Errorf("opentype: multiple subst format %d", format)
	}
	covOff := int(r.u16())
	n := int(r.u16())
	seqOffs := readU16s(&r, n)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: multiple subst: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	seqs := make([][]GlyphIndex, n)
	for i, off := range seqOffs {
		s, err := parseSequenceTable(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		seqs[i] = s
	}
	return &gsubMultiple1{cov: cov, seqs: seqs}, nil
}

func (s *gsubMultiple1) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	idx, ok := s.cov[g[i]]
	if !ok {
		return g, i, false
	}
	seq := s.seqs[idx]
	out := make([]GlyphIndex, 0, len(g)-1+len(seq))
	out = append(out, g[:i]...)
	out = append(out, seq...)
	out = append(out, g[i+1:]...)
	a.spliceClusters(i, 1, len(seq))
	return out, i + len(seq), true
}

// --- Type 3: alternate substitution -----------------------------------------

// gsubAlternate1 replaces a covered glyph with the first (default) alternate of
// its AlternateSet.
type gsubAlternate1 struct {
	cov  map[GlyphIndex]int
	sets [][]GlyphIndex
}

// parseGSUBAlternate decodes an alternate-substitution (format 1) subtable; its
// binary layout matches multiple substitution (Coverage + a list of sets).
func parseGSUBAlternate(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	if format != 1 {
		return nil, fmt.Errorf("opentype: alternate subst format %d", format)
	}
	covOff := int(r.u16())
	n := int(r.u16())
	setOffs := readU16s(&r, n)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: alternate subst: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	sets := make([][]GlyphIndex, n)
	for i, off := range setOffs {
		s, err := parseSequenceTable(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		sets[i] = s
	}
	return &gsubAlternate1{cov: cov, sets: sets}, nil
}

func (s *gsubAlternate1) sub(_ *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	idx, ok := s.cov[g[i]]
	if !ok {
		return g, i, false
	}
	alts := s.sets[idx]
	if len(alts) == 0 {
		return g, i, false
	}
	g[i] = alts[0]
	return g, i + 1, true
}

// --- Type 4: ligature substitution ------------------------------------------

// ligature is one ligature rule: the glyphs after the first component (which is
// keyed through coverage) and the ligature glyph they collapse into.
type ligature struct {
	glyph GlyphIndex
	rest  []GlyphIndex
}

// gsubLigature1 is a ligature-substitution format 1 subtable. sets[i] holds the
// ligatures whose first component is the glyph with coverage index i.
type gsubLigature1 struct {
	cov  map[GlyphIndex]int
	sets [][]ligature
}

// parseGSUBLigature decodes a ligature-substitution (format 1) subtable.
func parseGSUBLigature(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	r.skip(2) // substFormat (always 1)
	covOff := int(r.u16())
	setCount := int(r.u16())
	setOffs := make([]int, setCount)
	for i := 0; i < setCount; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: ligature subst: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	sets := make([][]ligature, setCount)
	for i, so := range setOffs {
		ls, err := parseLigatureSet(subslice(b, so))
		if err != nil {
			return nil, err
		}
		sets[i] = ls
	}
	return &gsubLigature1{cov: cov, sets: sets}, nil
}

// parseLigatureSet decodes a LigatureSet table.
func parseLigatureSet(b []byte) ([]ligature, error) {
	r := reader{b: b}
	count := int(r.u16())
	offs := make([]int, count)
	for i := 0; i < count; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: ligatureSet: %w", r.err)
	}
	ligs := make([]ligature, count)
	for i, off := range offs {
		lg, err := parseLigature(subslice(b, off))
		if err != nil {
			return nil, err
		}
		ligs[i] = lg
	}
	return ligs, nil
}

// parseLigature decodes a Ligature table. componentCount counts the first
// component too, so componentCount-1 trailing glyph ids follow.
func parseLigature(b []byte) (ligature, error) {
	r := reader{b: b}
	glyph := GlyphIndex(r.u16())
	compCount := int(r.u16())
	rest := make([]GlyphIndex, 0, compCount)
	for i := 1; i < compCount; i++ {
		rest = append(rest, GlyphIndex(r.u16()))
	}
	if r.err != nil {
		return ligature{}, fmt.Errorf("opentype: ligature: %w", r.err)
	}
	return ligature{glyph: glyph, rest: rest}, nil
}

func (s *gsubLigature1) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	ci, ok := s.cov[g[i]]
	if !ok {
		return g, i, false
	}
	for _, lig := range s.sets[ci] {
		n := len(lig.rest)
		if i+1+n > len(g) {
			continue
		}
		match := true
		for k, c := range lig.rest {
			if g[i+1+k] != c {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		out := make([]GlyphIndex, 0, len(g)-n)
		out = append(out, g[:i]...)
		out = append(out, lig.glyph)
		out = append(out, g[i+1+n:]...)
		a.spliceClusters(i, 1+n, 1)
		return out, i + 1, true
	}
	return g, i, false
}

// spliceClusters mirrors, on the tracked cluster slice, a substitution that
// replaced `consumed` input glyphs at position i with `produced` output glyphs.
// The produced glyphs all inherit the cluster of the first consumed glyph, so a
// decomposition's parts share their source position and a ligature's result
// keeps its first component's. It is a no-op when tracking is off (clusters
// nil), which is the case for plain Apply/ApplyMasked.
func (a *gsubApplier) spliceClusters(i, consumed, produced int) {
	if a.clusters == nil {
		return
	}
	c := a.clusters
	src := c[i]
	nc := make([]int, 0, len(c)-consumed+produced)
	nc = append(nc, c[:i]...)
	for k := 0; k < produced; k++ {
		nc = append(nc, src)
	}
	nc = append(nc, c[i+consumed:]...)
	a.clusters = nc
}

// --- Sequence-matching helpers (shared by types 5, 6 and 8) -----------------

// classOf returns the class of glyph g in a ClassDef map; glyphs absent from the
// map belong to class 0.
func classOf(m map[GlyphIndex]int, g GlyphIndex) int { return m[g] }

// matchGlyphSeq reports whether the glyphs at g[start:] equal seq. Callers only
// ever pass a non-negative start (a position at or after the current glyph).
func matchGlyphSeq(g []GlyphIndex, start int, seq []uint16) bool {
	if start+len(seq) > len(g) {
		return false
	}
	for k, v := range seq {
		if uint16(g[start+k]) != v {
			return false
		}
	}
	return true
}

// matchBacktrackGlyphs matches seq against g[i-1], g[i-2], ... (reverse order).
func matchBacktrackGlyphs(g []GlyphIndex, i int, seq []uint16) bool {
	if i-len(seq) < 0 {
		return false
	}
	for k, v := range seq {
		if uint16(g[i-1-k]) != v {
			return false
		}
	}
	return true
}

// matchClassSeq matches the classes of g[start:] against seq.
func matchClassSeq(g []GlyphIndex, start int, seq []uint16, cd map[GlyphIndex]int) bool {
	if start+len(seq) > len(g) {
		return false
	}
	for k, v := range seq {
		if classOf(cd, g[start+k]) != int(v) {
			return false
		}
	}
	return true
}

// matchBacktrackClass matches seq against the classes of g[i-1], g[i-2], ...
func matchBacktrackClass(g []GlyphIndex, i int, seq []uint16, cd map[GlyphIndex]int) bool {
	if i-len(seq) < 0 {
		return false
	}
	for k, v := range seq {
		if classOf(cd, g[i-1-k]) != int(v) {
			return false
		}
	}
	return true
}

// matchCovSeq reports whether each g[start+k] is covered by covs[k].
func matchCovSeq(g []GlyphIndex, start int, covs []map[GlyphIndex]int) bool {
	if start < 0 || start+len(covs) > len(g) {
		return false
	}
	for k, c := range covs {
		if _, ok := c[g[start+k]]; !ok {
			return false
		}
	}
	return true
}

// matchBacktrackCov matches covs against g[i-1], g[i-2], ... (reverse order).
func matchBacktrackCov(g []GlyphIndex, i int, covs []map[GlyphIndex]int) bool {
	if i-len(covs) < 0 {
		return false
	}
	for k, c := range covs {
		if _, ok := c[g[i-1-k]]; !ok {
			return false
		}
	}
	return true
}

// --- Nested lookup application ----------------------------------------------

// runRecords applies the SequenceLookupRecords of a matched context. base is the
// run index of the first input glyph; each record's seqIndex is relative to it.
func (a *gsubApplier) runRecords(recs []seqLookupRecord, g []GlyphIndex, base int) []GlyphIndex {
	for _, rec := range recs {
		pos := base + int(rec.seqIndex)
		if pos < 0 || pos >= len(g) {
			continue
		}
		g = a.applyLookupAt(int(rec.lookupIndex), g, pos)
	}
	return g
}

// applyLookupAt applies lookup li at a single position, taking the first
// matching subtable. Out-of-range indices and excessive recursion are no-ops.
func (a *gsubApplier) applyLookupAt(li int, g []GlyphIndex, pos int) []GlyphIndex {
	if li < 0 || li >= len(a.lookups) || a.depth >= maxGSUBNest {
		return g
	}
	a.depth++
	for _, st := range a.lookups[li].subtables {
		if out, _, ok := st.sub(a, g, pos); ok {
			g = out
			break
		}
	}
	a.depth--
	return g
}

// --- Type 5: contextual substitution ----------------------------------------

// seqRule is one (Class)SequenceRule: the input sequence (positions after the
// first, as glyph ids for format 1 or class values for format 2) and the nested
// lookup records to run on a match.
type seqRule struct {
	input   []uint16
	records []seqLookupRecord
}

// parseSeqRule decodes a SequenceRule or ClassSequenceRule table.
func parseSeqRule(b []byte) (seqRule, error) {
	r := reader{b: b}
	glyphCount := int(r.u16())
	lookupCount := int(r.u16())
	input := readU16s(&r, glyphCount-1)
	recs := parseSeqLookupRecords(&r, lookupCount)
	if r.err != nil {
		return seqRule{}, fmt.Errorf("opentype: seq rule: %w", r.err)
	}
	return seqRule{input: input, records: recs}, nil
}

// parseSeqRuleSet decodes a SequenceRuleSet / ClassSequenceRuleSet table.
func parseSeqRuleSet(b []byte) ([]seqRule, error) {
	r := reader{b: b}
	n := int(r.u16())
	offs := readU16s(&r, n)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: seq rule set: %w", r.err)
	}
	rules := make([]seqRule, n)
	for i, off := range offs {
		ru, err := parseSeqRule(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		rules[i] = ru
	}
	return rules, nil
}

// parseSeqRuleSets decodes a list of (Class)SequenceRuleSet offsets; a zero
// offset denotes an absent (empty) set.
func parseSeqRuleSets(b []byte, offs []uint16) ([][]seqRule, error) {
	sets := make([][]seqRule, len(offs))
	for i, off := range offs {
		if off == 0 {
			continue
		}
		rs, err := parseSeqRuleSet(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		sets[i] = rs
	}
	return sets, nil
}

// gsubContext1 is contextual substitution format 1 (glyph-based).
type gsubContext1 struct {
	cov  map[GlyphIndex]int
	sets [][]seqRule
}

// gsubContext2 is contextual substitution format 2 (class-based).
type gsubContext2 struct {
	cov      map[GlyphIndex]int
	classDef map[GlyphIndex]int
	sets     [][]seqRule
}

// gsubContext3 is contextual substitution format 3 (coverage-based).
type gsubContext3 struct {
	covs    []map[GlyphIndex]int
	records []seqLookupRecord
}

// parseGSUBContext decodes a contextual-substitution subtable (format 1/2/3).
func parseGSUBContext(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	switch format {
	case 1:
		covOff := int(r.u16())
		n := int(r.u16())
		setOffs := readU16s(&r, n)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: context 1: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		sets, err := parseSeqRuleSets(b, setOffs)
		if err != nil {
			return nil, err
		}
		return &gsubContext1{cov: cov, sets: sets}, nil
	case 2:
		covOff := int(r.u16())
		classOff := int(r.u16())
		n := int(r.u16())
		setOffs := readU16s(&r, n)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: context 2: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		cd, err := parseClassDef(subslice(b, classOff))
		if err != nil {
			return nil, err
		}
		sets, err := parseSeqRuleSets(b, setOffs)
		if err != nil {
			return nil, err
		}
		return &gsubContext2{cov: cov, classDef: cd, sets: sets}, nil
	case 3:
		glyphCount := int(r.u16())
		lookupCount := int(r.u16())
		covOffs := readU16s(&r, glyphCount)
		recs := parseSeqLookupRecords(&r, lookupCount)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: context 3: %w", r.err)
		}
		covs, err := parseCoverageList(b, covOffs)
		if err != nil {
			return nil, err
		}
		return &gsubContext3{covs: covs, records: recs}, nil
	default:
		return nil, fmt.Errorf("opentype: context format %d", format)
	}
}

func (s *gsubContext1) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	ci, ok := s.cov[g[i]]
	if !ok || ci >= len(s.sets) {
		return g, i, false
	}
	for _, rule := range s.sets[ci] {
		if !matchGlyphSeq(g, i+1, rule.input) {
			continue
		}
		out := a.runRecords(rule.records, g, i)
		return out, i + 1 + len(rule.input), true
	}
	return g, i, false
}

func (s *gsubContext2) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	if _, ok := s.cov[g[i]]; !ok {
		return g, i, false
	}
	cls := classOf(s.classDef, g[i])
	if cls >= len(s.sets) {
		return g, i, false
	}
	for _, rule := range s.sets[cls] {
		if !matchClassSeq(g, i+1, rule.input, s.classDef) {
			continue
		}
		out := a.runRecords(rule.records, g, i)
		return out, i + 1 + len(rule.input), true
	}
	return g, i, false
}

func (s *gsubContext3) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	if len(s.covs) == 0 || !matchCovSeq(g, i, s.covs) {
		return g, i, false
	}
	out := a.runRecords(s.records, g, i)
	return out, i + len(s.covs), true
}

// --- Type 6: chaining-contextual substitution -------------------------------

// chainRule is one Chain(Class)SequenceRule: backtrack, input (positions after
// the first) and lookahead sequences, plus the nested lookup records.
type chainRule struct {
	backtrack []uint16
	input     []uint16
	lookahead []uint16
	records   []seqLookupRecord
}

// parseChainRule decodes a ChainSequenceRule or ChainClassSequenceRule table.
func parseChainRule(b []byte) (chainRule, error) {
	r := reader{b: b}
	btCount := int(r.u16())
	backtrack := readU16s(&r, btCount)
	inCount := int(r.u16())
	input := readU16s(&r, inCount-1)
	laCount := int(r.u16())
	lookahead := readU16s(&r, laCount)
	lookupCount := int(r.u16())
	recs := parseSeqLookupRecords(&r, lookupCount)
	if r.err != nil {
		return chainRule{}, fmt.Errorf("opentype: chain rule: %w", r.err)
	}
	return chainRule{backtrack: backtrack, input: input, lookahead: lookahead, records: recs}, nil
}

// parseChainRuleSet decodes a ChainSequenceRuleSet table.
func parseChainRuleSet(b []byte) ([]chainRule, error) {
	r := reader{b: b}
	n := int(r.u16())
	offs := readU16s(&r, n)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: chain rule set: %w", r.err)
	}
	rules := make([]chainRule, n)
	for i, off := range offs {
		ru, err := parseChainRule(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		rules[i] = ru
	}
	return rules, nil
}

// parseChainRuleSets decodes a list of ChainSequenceRuleSet offsets; a zero
// offset denotes an absent (empty) set.
func parseChainRuleSets(b []byte, offs []uint16) ([][]chainRule, error) {
	sets := make([][]chainRule, len(offs))
	for i, off := range offs {
		if off == 0 {
			continue
		}
		rs, err := parseChainRuleSet(subslice(b, int(off)))
		if err != nil {
			return nil, err
		}
		sets[i] = rs
	}
	return sets, nil
}

// gsubChain1 is chaining-contextual substitution format 1 (glyph-based).
type gsubChain1 struct {
	cov  map[GlyphIndex]int
	sets [][]chainRule
}

// gsubChain2 is chaining-contextual substitution format 2 (class-based).
type gsubChain2 struct {
	cov       map[GlyphIndex]int
	backtrack map[GlyphIndex]int
	input     map[GlyphIndex]int
	lookahead map[GlyphIndex]int
	sets      [][]chainRule
}

// gsubChain3 is chaining-contextual substitution format 3 (coverage-based).
type gsubChain3 struct {
	backtrack []map[GlyphIndex]int
	input     []map[GlyphIndex]int
	lookahead []map[GlyphIndex]int
	records   []seqLookupRecord
}

// parseGSUBChain decodes a chaining-contextual subtable (format 1/2/3).
func parseGSUBChain(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	switch format {
	case 1:
		covOff := int(r.u16())
		n := int(r.u16())
		setOffs := readU16s(&r, n)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: chain 1: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		sets, err := parseChainRuleSets(b, setOffs)
		if err != nil {
			return nil, err
		}
		return &gsubChain1{cov: cov, sets: sets}, nil
	case 2:
		covOff := int(r.u16())
		btOff := int(r.u16())
		inOff := int(r.u16())
		laOff := int(r.u16())
		n := int(r.u16())
		setOffs := readU16s(&r, n)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: chain 2: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		btCD, err := parseClassDef(subslice(b, btOff))
		if err != nil {
			return nil, err
		}
		inCD, err := parseClassDef(subslice(b, inOff))
		if err != nil {
			return nil, err
		}
		laCD, err := parseClassDef(subslice(b, laOff))
		if err != nil {
			return nil, err
		}
		sets, err := parseChainRuleSets(b, setOffs)
		if err != nil {
			return nil, err
		}
		return &gsubChain2{cov: cov, backtrack: btCD, input: inCD, lookahead: laCD, sets: sets}, nil
	case 3:
		btCount := int(r.u16())
		btOffs := readU16s(&r, btCount)
		inCount := int(r.u16())
		inOffs := readU16s(&r, inCount)
		laCount := int(r.u16())
		laOffs := readU16s(&r, laCount)
		lookupCount := int(r.u16())
		recs := parseSeqLookupRecords(&r, lookupCount)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: chain 3: %w", r.err)
		}
		bt, err := parseCoverageList(b, btOffs)
		if err != nil {
			return nil, err
		}
		in, err := parseCoverageList(b, inOffs)
		if err != nil {
			return nil, err
		}
		la, err := parseCoverageList(b, laOffs)
		if err != nil {
			return nil, err
		}
		return &gsubChain3{backtrack: bt, input: in, lookahead: la, records: recs}, nil
	default:
		return nil, fmt.Errorf("opentype: chain format %d", format)
	}
}

func (s *gsubChain1) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	ci, ok := s.cov[g[i]]
	if !ok || ci >= len(s.sets) {
		return g, i, false
	}
	for _, rule := range s.sets[ci] {
		inLen := 1 + len(rule.input)
		if !matchGlyphSeq(g, i+1, rule.input) ||
			!matchBacktrackGlyphs(g, i, rule.backtrack) ||
			!matchGlyphSeq(g, i+inLen, rule.lookahead) {
			continue
		}
		out := a.runRecords(rule.records, g, i)
		return out, i + inLen, true
	}
	return g, i, false
}

func (s *gsubChain2) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	if _, ok := s.cov[g[i]]; !ok {
		return g, i, false
	}
	cls := classOf(s.input, g[i])
	if cls >= len(s.sets) {
		return g, i, false
	}
	for _, rule := range s.sets[cls] {
		inLen := 1 + len(rule.input)
		if !matchClassSeq(g, i+1, rule.input, s.input) ||
			!matchBacktrackClass(g, i, rule.backtrack, s.backtrack) ||
			!matchClassSeq(g, i+inLen, rule.lookahead, s.lookahead) {
			continue
		}
		out := a.runRecords(rule.records, g, i)
		return out, i + inLen, true
	}
	return g, i, false
}

func (s *gsubChain3) sub(a *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	inLen := len(s.input)
	if inLen == 0 ||
		!matchCovSeq(g, i, s.input) ||
		!matchBacktrackCov(g, i, s.backtrack) ||
		!matchCovSeq(g, i+inLen, s.lookahead) {
		return g, i, false
	}
	out := a.runRecords(s.records, g, i)
	return out, i + inLen, true
}

// --- Type 7: extension substitution -----------------------------------------

// parseGSUBExtension decodes an extension-substitution (format 1) subtable and
// resolves the indirection to the real subtable.
func parseGSUBExtension(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	if format != 1 {
		return nil, fmt.Errorf("opentype: extension subst format %d", format)
	}
	extType := r.u16()
	extOff := r.u32()
	if r.err != nil {
		return nil, fmt.Errorf("opentype: extension subst: %w", r.err)
	}
	if extType == 7 {
		return nil, fmt.Errorf("opentype: extension subst nested (type 7)")
	}
	return parseGSUBSubtable(extType, subslice(b, int(extOff)))
}

// --- Type 8: reverse chaining single substitution ---------------------------

// gsubReverseChain1 is a reverse chaining single substitution: a covered glyph
// is replaced by substitute[coverageIndex] when its backtrack and lookahead
// contexts match. The enclosing lookup is applied to the run back-to-front.
type gsubReverseChain1 struct {
	cov       map[GlyphIndex]int
	backtrack []map[GlyphIndex]int
	lookahead []map[GlyphIndex]int
	subst     []GlyphIndex
}

// parseGSUBReverse decodes a reverse-chaining single-substitution subtable.
func parseGSUBReverse(b []byte) (gsubSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	if format != 1 {
		return nil, fmt.Errorf("opentype: reverse chain format %d", format)
	}
	covOff := int(r.u16())
	btCount := int(r.u16())
	btOffs := readU16s(&r, btCount)
	laCount := int(r.u16())
	laOffs := readU16s(&r, laCount)
	substCount := int(r.u16())
	substRaw := readU16s(&r, substCount)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: reverse chain: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	bt, err := parseCoverageList(b, btOffs)
	if err != nil {
		return nil, err
	}
	la, err := parseCoverageList(b, laOffs)
	if err != nil {
		return nil, err
	}
	subst := make([]GlyphIndex, len(substRaw))
	for i, v := range substRaw {
		subst[i] = GlyphIndex(v)
	}
	return &gsubReverseChain1{cov: cov, backtrack: bt, lookahead: la, subst: subst}, nil
}

func (s *gsubReverseChain1) sub(_ *gsubApplier, g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	idx, ok := s.cov[g[i]]
	if !ok || idx >= len(s.subst) {
		return g, i, false
	}
	if !matchBacktrackCov(g, i, s.backtrack) || !matchCovSeq(g, i+1, s.lookahead) {
		return g, i, false
	}
	g[i] = s.subst[idx]
	return g, i, true
}

// --- Apply ------------------------------------------------------------------

// Apply runs the lookups activated by the given feature tags (for example
// "liga" and "calt") over the glyph run, returning the substituted run. The
// input slice is not modified. Feature tags with no matching lookups, and an
// empty run, leave the input unchanged.
func (g *gsub) Apply(glyphs []GlyphIndex, features ...string) []GlyphIndex {
	want := make(map[string]bool, len(features))
	for _, f := range features {
		want[f] = true
	}
	idxs := g.selectLookups("DFLT", "", want)
	if len(idxs) == 0 {
		return glyphs
	}
	out := append([]GlyphIndex(nil), glyphs...)
	a := &gsubApplier{lookups: g.lookups}
	for _, li := range idxs {
		if li >= len(g.lookups) {
			continue
		}
		out = a.applyLookup(g.lookups[li], out)
	}
	return out
}

// applyLookup applies one lookup across the run. Reverse-chaining lookups are
// applied back-to-front; all others left-to-right, taking the first subtable
// that matches at each position.
func (a *gsubApplier) applyLookup(lk gsubLookup, glyphs []GlyphIndex) []GlyphIndex {
	if lk.reverse {
		for i := len(glyphs) - 1; i >= 0; i-- {
			for _, st := range lk.subtables {
				if out, _, ok := st.sub(a, glyphs, i); ok {
					glyphs = out
					break
				}
			}
		}
		return glyphs
	}
	i := 0
	for i < len(glyphs) {
		applied := false
		for _, st := range lk.subtables {
			if out, next, ok := st.sub(a, glyphs, i); ok {
				glyphs = out
				i = next
				applied = true
				break
			}
		}
		if !applied {
			i++
		}
	}
	return glyphs
}
