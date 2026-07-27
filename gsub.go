// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file decodes and applies the GSUB (Glyph Substitution) table. Two lookup
// types are implemented:
//
//   - Type 1, single substitution (formats 1 and 2): replace one glyph with
//     another, used by features such as small caps or stylistic swaps.
//   - Type 4, ligature substitution: replace a sequence of glyphs with a single
//     ligature glyph, used by the "liga" feature (for example f+i -> fi).
//
// The contextual and chaining-contextual types (2 alternate, 3 multiple,
// 5/6 contextual, 7 extension, 8 reverse-chaining) are intentionally NOT
// implemented: a lookup of an unsupported type is parsed as a no-op and its
// substitutions are skipped. Apply therefore performs a best-effort GSUB pass
// sufficient for ligatures and single substitution.

// gsub is a decoded GSUB table.
type gsub struct {
	layoutHeader
	lookups []gsubLookup
}

// gsubLookup is one decoded GSUB Lookup: its (supported) subtables.
type gsubLookup struct {
	subtables []gsubSubtable
}

// gsubSubtable attempts a substitution at position i of a glyph run. On success
// it returns the resulting run, the index to continue from, and true; otherwise
// it returns the run unchanged and false.
type gsubSubtable interface {
	sub(glyphs []GlyphIndex, i int) (out []GlyphIndex, next int, ok bool)
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
// supported type.
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
	return gsubLookup{subtables: subs}, nil
}

// parseGSUBSubtable decodes one subtable of the given lookup type; unsupported
// types yield a nil subtable that the caller drops.
func parseGSUBSubtable(lookupType uint16, b []byte) (gsubSubtable, error) {
	switch lookupType {
	case 1:
		return parseGSUBSingle(b)
	case 4:
		return parseGSUBLigature(b)
	default:
		return nil, nil // types 2,3,5,6,7,8 are not supported (documented)
	}
}

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

func (s *gsubSingle1) sub(g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	if _, ok := s.cov[g[i]]; !ok {
		return g, i, false
	}
	g[i] = GlyphIndex(uint16(g[i]) + uint16(s.delta))
	return g, i + 1, true
}

func (s *gsubSingle2) sub(g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
	idx, ok := s.cov[g[i]]
	if !ok {
		return g, i, false
	}
	g[i] = s.subst[idx]
	return g, i + 1, true
}

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

func (s *gsubLigature1) sub(g []GlyphIndex, i int) ([]GlyphIndex, int, bool) {
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
		return out, i + 1, true
	}
	return g, i, false
}

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
	for _, li := range idxs {
		if li >= len(g.lookups) {
			continue
		}
		out = applyGSUBLookup(g.lookups[li], out)
	}
	return out
}

// applyGSUBLookup applies one lookup left-to-right across the run, taking the
// first subtable that matches at each position.
func applyGSUBLookup(lk gsubLookup, glyphs []GlyphIndex) []GlyphIndex {
	i := 0
	for i < len(glyphs) {
		applied := false
		for _, st := range lk.subtables {
			if out, next, ok := st.sub(glyphs, i); ok {
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
