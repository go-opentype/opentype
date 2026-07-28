// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file decodes the GDEF (Glyph Definition) table and implements the
// OpenType Layout lookup flags that GSUB and GPOS honour during matching. GDEF
// classifies glyphs (base / ligature / mark / component), assigns marks to
// attachment classes and defines mark glyph sets; the lookupFlag of each lookup
// then selects, via those classifications, which glyphs a lookup skips as it
// walks the run. Without a GDEF table (or with a zero lookupFlag) nothing is
// skipped and matching is identical to a plain contiguous walk.
//
// The flag bits (OpenType "LookupFlag") are:
//
//   - RightToLeft (0x0001): cursive attachment joins right-to-left.
//   - IgnoreBaseGlyphs (0x0002): skip GDEF class 1 (base) glyphs.
//   - IgnoreLigatures (0x0004): skip GDEF class 2 (ligature) glyphs.
//   - IgnoreMarks (0x0008): skip GDEF class 3 (mark) glyphs.
//   - UseMarkFilteringSet (0x0010): skip marks absent from the referenced
//     mark glyph set (its index is the lookup's markFilteringSet field).
//   - MarkAttachmentType (0xFF00): skip marks whose GDEF mark-attachment class
//     differs from the type in the flag's high byte.

// lookupFlag bit masks.
const (
	flagRightToLeft         = 0x0001
	flagIgnoreBaseGlyphs    = 0x0002
	flagIgnoreLigatures     = 0x0004
	flagIgnoreMarks         = 0x0008
	flagUseMarkFilteringSet = 0x0010
	flagMarkAttachTypeMask  = 0xFF00
)

// GDEF GlyphClassDef class values.
const (
	gdefClassBase      = 1 // simple glyph (a letter)
	gdefClassLigature  = 2 // ligature glyph (multiple combined glyphs)
	gdefClassMark      = 3 // combining mark (diacritic)
	gdefClassComponent = 4 // ligature component (part of a ligature)
)

// gdefTable is a decoded GDEF table. All three maps/slices are optional (an
// absent subtable yields an empty map or nil slice); a font may carry a GDEF
// table with only some of them present.
type gdefTable struct {
	glyphClass    map[GlyphIndex]int   // GlyphClassDef: base/ligature/mark/component
	markAttach    map[GlyphIndex]int   // MarkAttachClassDef: mark-attachment class per mark
	markGlyphSets []map[GlyphIndex]int // MarkGlyphSets: one Coverage per set
}

// parseGDEF decodes a GDEF table. AttachList and LigCaretList are tolerated but
// not decoded (they carry hinting/caret data GSUB/GPOS matching does not need).
func parseGDEF(b []byte) (*gdefTable, error) {
	r := reader{b: b}
	r.skip(2) // majorVersion
	minor := r.u16()
	glyphClassOff := int(r.u16())
	r.skip(2) // attachListOffset (tolerated, not decoded)
	r.skip(2) // ligCaretListOffset (tolerated, not decoded)
	markAttachOff := int(r.u16())
	markSetsOff := 0
	if minor >= 2 {
		markSetsOff = int(r.u16())
	}
	// minor >= 3 adds an itemVarStore Offset32 here; it is not needed for
	// matching and is left unread.
	if r.err != nil {
		return nil, fmt.Errorf("opentype: gdef header: %w", r.err)
	}
	t := &gdefTable{glyphClass: map[GlyphIndex]int{}, markAttach: map[GlyphIndex]int{}}
	if glyphClassOff != 0 {
		cd, err := parseClassDef(subslice(b, glyphClassOff))
		if err != nil {
			return nil, err
		}
		t.glyphClass = cd
	}
	if markAttachOff != 0 {
		cd, err := parseClassDef(subslice(b, markAttachOff))
		if err != nil {
			return nil, err
		}
		t.markAttach = cd
	}
	if markSetsOff != 0 {
		sets, err := parseMarkGlyphSets(subslice(b, markSetsOff))
		if err != nil {
			return nil, err
		}
		t.markGlyphSets = sets
	}
	return t, nil
}

// parseMarkGlyphSets decodes a MarkGlyphSetsDef table: a format, a set count and
// that many Offset32 to Coverage tables (from the start of b).
func parseMarkGlyphSets(b []byte) ([]map[GlyphIndex]int, error) {
	r := reader{b: b}
	r.skip(2) // format (1)
	n := int(r.u16())
	offs := make([]int, n)
	for i := 0; i < n; i++ {
		offs[i] = int(r.u32())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: markGlyphSets: %w", r.err)
	}
	sets := make([]map[GlyphIndex]int, n)
	for i, off := range offs {
		cov, err := parseCoverage(subslice(b, off))
		if err != nil {
			return nil, err
		}
		sets[i] = cov
	}
	return sets, nil
}

// markSetCovers reports whether mark glyph g is a member of mark glyph set i.
func (t *gdefTable) markSetCovers(i int, g GlyphIndex) bool {
	if i < 0 || i >= len(t.markGlyphSets) {
		return false
	}
	_, ok := t.markGlyphSets[i][g]
	return ok
}

// skipper decides which glyphs a lookup ignores while matching, from the
// lookup's flag (and mark-filtering-set index) against the font's GDEF table. A
// zero-value skipper (no GDEF) skips nothing, so matching degrades to a plain
// contiguous walk and behaviour is identical to a font without GDEF.
type skipper struct {
	gdef          *gdefTable
	flag          uint16
	markFilterSet uint16
}

// rtl reports whether the lookup's RightToLeft flag is set (consulted by
// cursive attachment).
func (sk skipper) rtl() bool { return sk.flag&flagRightToLeft != 0 }

// isMark reports whether g is a GDEF mark glyph (class 3). It is false without a
// GDEF table, so mark-aware base finding degrades to the plain search.
func (sk skipper) isMark(g GlyphIndex) bool {
	return sk.gdef != nil && sk.gdef.glyphClass[g] == gdefClassMark
}

// skip reports whether glyph g is ignored by this lookup: a base/ligature/mark
// the corresponding Ignore flag suppresses, or a mark filtered out by the
// mark-attachment type or the mark-filtering set.
func (sk skipper) skip(g GlyphIndex) bool {
	if sk.gdef == nil {
		return false
	}
	class := sk.gdef.glyphClass[g]
	if sk.flag&flagIgnoreBaseGlyphs != 0 && class == gdefClassBase {
		return true
	}
	if sk.flag&flagIgnoreLigatures != 0 && class == gdefClassLigature {
		return true
	}
	if sk.flag&flagIgnoreMarks != 0 && class == gdefClassMark {
		return true
	}
	if class == gdefClassMark {
		if sk.flag&flagUseMarkFilteringSet != 0 {
			return !sk.gdef.markSetCovers(int(sk.markFilterSet), g)
		}
		if attach := sk.flag & flagMarkAttachTypeMask; attach != 0 {
			return sk.gdef.markAttach[g] != int(attach>>8)
		}
	}
	return false
}

// next returns the index of the first non-skipped glyph at position >= from, or
// len(g) when none remains.
func (sk skipper) next(g []GlyphIndex, from int) int {
	for from < len(g) && sk.skip(g[from]) {
		from++
	}
	return from
}

// prev returns the index of the first non-skipped glyph at position <= from, or
// -1 when none remains.
func (sk skipper) prev(g []GlyphIndex, from int) int {
	for from >= 0 && sk.skip(g[from]) {
		from--
	}
	return from
}

// glyphPred tests one glyph during sequence matching (by glyph id, class or
// coverage membership, depending on the sub-format).
type glyphPred func(GlyphIndex) bool

// matchForward matches preds against successive non-skipped positions starting
// at the first non-skipped glyph at or after `from`. It returns the matched
// absolute positions, the index just past the last match (where a following
// lookahead search begins), and whether every predicate matched. An empty preds
// list matches trivially at `from`.
func (sk skipper) matchForward(g []GlyphIndex, from int, preds []glyphPred) (pos []int, end int, ok bool) {
	pos = make([]int, len(preds))
	p := from
	for k, pred := range preds {
		p = sk.next(g, p)
		if p >= len(g) || !pred(g[p]) {
			return nil, 0, false
		}
		pos[k] = p
		p++
	}
	return pos, p, true
}

// matchBackward matches preds against successive non-skipped positions walking
// backward from `from` (used for backtrack sequences, which are stored in the
// reverse of text order).
func (sk skipper) matchBackward(g []GlyphIndex, from int, preds []glyphPred) bool {
	p := from
	for _, pred := range preds {
		p = sk.prev(g, p)
		if p < 0 || !pred(g[p]) {
			return false
		}
		p--
	}
	return true
}

// matchContext matches a format-3 coverage input sequence: covs[0] must cover
// the anchor glyph g[i], and covs[1:] must cover successive non-skipped
// positions after i. It returns the matched absolute positions (starting with
// i), the index just past the input and whether it matched. An empty covs list
// does not match.
func (sk skipper) matchContext(g []GlyphIndex, i int, covs []map[GlyphIndex]int) (pos []int, end int, ok bool) {
	if len(covs) == 0 {
		return nil, 0, false
	}
	if _, ok := covs[0][g[i]]; !ok {
		return nil, 0, false
	}
	rest, end, ok := sk.matchForward(g, i+1, covPreds(covs[1:]))
	if !ok {
		return nil, 0, false
	}
	return append([]int{i}, rest...), end, true
}

// covPreds builds coverage-membership predicates from a list of Coverage maps.
func covPreds(covs []map[GlyphIndex]int) []glyphPred {
	preds := make([]glyphPred, len(covs))
	for k := range covs {
		c := covs[k]
		preds[k] = func(g GlyphIndex) bool { _, ok := c[g]; return ok }
	}
	return preds
}
