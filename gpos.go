// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file decodes and applies the GPOS (Glyph Positioning) table, limited to
// lookup type 2, pair adjustment (kerning), in both formats:
//
//   - Format 1: explicit (first, second) glyph pairs.
//   - Format 2: pairs classified by a ClassDef on each side.
//
// Only the X advance of the first glyph's ValueRecord is extracted; the rest of
// the ValueRecord (placement, Y advance, device offsets) is parsed and skipped.
// This is what horizontal left-to-right kerning needs.
//
// Single positioning (type 1), cursive attachment (type 3), and the mark
// positioning lookups (types 4 mark-to-base, 5 mark-to-ligature, 6 mark-to-mark)
// as well as contextual/extension types (7, 8, 9) are intentionally NOT
// implemented: subtables of an unsupported type are parsed as no-ops.

// gpos is a decoded GPOS table.
type gpos struct {
	layoutHeader
	lookups []gposLookup
}

// gposLookup is one decoded GPOS Lookup: its (supported) pair-adjustment
// subtables.
type gposLookup struct {
	subtables []pairPos
}

// pairPos reports the first glyph's X-advance adjustment for a (left, right)
// pair; ok is false when the subtable has no entry for the pair.
type pairPos interface {
	kern(left, right GlyphIndex) (int, bool)
}

// valueRecordMask bits, from the GPOS ValueFormat definition.
const valueXAdvance = 0x0004

// readValueXAdvance consumes a ValueRecord whose shape is given by format,
// returning its X advance (0 when that field is absent). Each present field is
// a 2-byte value; device fields are read and discarded like the rest.
func readValueXAdvance(r *reader, format uint16) int {
	x := 0
	for bit := 0; bit < 8; bit++ {
		mask := uint16(1) << uint(bit)
		if format&mask == 0 {
			continue
		}
		v := r.i16()
		if mask == valueXAdvance {
			x = int(v)
		}
	}
	return x
}

// parseGPOS decodes a GPOS table.
func parseGPOS(b []byte) (*gpos, error) {
	hdr, raw, err := parseLayoutCommon(b)
	if err != nil {
		return nil, err
	}
	lookups := make([]gposLookup, len(raw))
	for i, lk := range raw {
		gl, err := parseGPOSLookup(lk)
		if err != nil {
			return nil, err
		}
		lookups[i] = gl
	}
	return &gpos{layoutHeader: hdr, lookups: lookups}, nil
}

// parseGPOSLookup decodes one Lookup table, keeping only pair-adjustment
// subtables (lookup type 2).
func parseGPOSLookup(b []byte) (gposLookup, error) {
	r := reader{b: b}
	lookupType := r.u16()
	r.skip(2) // lookupFlag
	subCount := int(r.u16())
	offs := make([]int, subCount)
	for i := 0; i < subCount; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return gposLookup{}, fmt.Errorf("opentype: gpos lookup: %w", r.err)
	}
	var subs []pairPos
	for _, off := range offs {
		st, err := parseGPOSSubtable(lookupType, subslice(b, off))
		if err != nil {
			return gposLookup{}, err
		}
		if st != nil {
			subs = append(subs, st)
		}
	}
	return gposLookup{subtables: subs}, nil
}

// parseGPOSSubtable decodes one subtable of the given lookup type; unsupported
// types yield a nil subtable the caller drops.
func parseGPOSSubtable(lookupType uint16, b []byte) (pairPos, error) {
	if lookupType != 2 {
		return nil, nil // only pair adjustment is supported (documented)
	}
	r := reader{b: b}
	format := r.u16()
	switch format {
	case 1:
		return parsePairPos1(b)
	case 2:
		return parsePairPos2(b)
	default:
		return nil, fmt.Errorf("opentype: pairpos format %d", format)
	}
}

// pairPos1 is a pair-adjustment format 1 subtable: covered first glyphs index
// into per-glyph maps from second glyph to X-advance adjustment.
type pairPos1 struct {
	cov  map[GlyphIndex]int
	sets []map[GlyphIndex]int
}

// parsePairPos1 decodes a PairPos format 1 subtable.
func parsePairPos1(b []byte) (pairPos, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	vf1 := r.u16()
	vf2 := r.u16()
	setCount := int(r.u16())
	setOffs := make([]int, setCount)
	for i := 0; i < setCount; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: pairpos1: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	sets := make([]map[GlyphIndex]int, setCount)
	for i, so := range setOffs {
		m, err := parsePairSet(subslice(b, so), vf1, vf2)
		if err != nil {
			return nil, err
		}
		sets[i] = m
	}
	return &pairPos1{cov: cov, sets: sets}, nil
}

// parsePairSet decodes a PairSet table into a second-glyph -> X-advance map.
func parsePairSet(b []byte, vf1, vf2 uint16) (map[GlyphIndex]int, error) {
	r := reader{b: b}
	count := int(r.u16())
	m := map[GlyphIndex]int{}
	for i := 0; i < count; i++ {
		second := GlyphIndex(r.u16())
		x1 := readValueXAdvance(&r, vf1)
		readValueXAdvance(&r, vf2) // value2 (applies to the right glyph) is skipped
		m[second] = x1
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: pairSet: %w", r.err)
	}
	return m, nil
}

func (p *pairPos1) kern(left, right GlyphIndex) (int, bool) {
	ci, ok := p.cov[left]
	if !ok {
		return 0, false
	}
	if v, ok := p.sets[ci][right]; ok {
		return v, true
	}
	return 0, false
}

// pairPos2 is a pair-adjustment format 2 subtable: the first glyph must be
// covered, then both glyphs are classified and the class pair indexes a grid of
// X-advance adjustments.
type pairPos2 struct {
	cov         map[GlyphIndex]int
	cd1, cd2    map[GlyphIndex]int
	class2Count int
	grid        []int
}

// parsePairPos2 decodes a PairPos format 2 subtable.
func parsePairPos2(b []byte) (pairPos, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	vf1 := r.u16()
	vf2 := r.u16()
	cd1Off := int(r.u16())
	cd2Off := int(r.u16())
	class1Count := int(r.u16())
	class2Count := int(r.u16())
	grid := make([]int, class1Count*class2Count)
	for i := range grid {
		x1 := readValueXAdvance(&r, vf1)
		readValueXAdvance(&r, vf2)
		grid[i] = x1
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: pairpos2: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	cd1, err := parseClassDef(subslice(b, cd1Off))
	if err != nil {
		return nil, err
	}
	cd2, err := parseClassDef(subslice(b, cd2Off))
	if err != nil {
		return nil, err
	}
	return &pairPos2{cov: cov, cd1: cd1, cd2: cd2, class2Count: class2Count, grid: grid}, nil
}

func (p *pairPos2) kern(left, right GlyphIndex) (int, bool) {
	if _, ok := p.cov[left]; !ok {
		return 0, false
	}
	idx := p.cd1[left]*p.class2Count + p.cd2[right]
	if idx >= len(p.grid) {
		return 0, false
	}
	return p.grid[idx], true
}

// Kern returns the X-advance adjustment GPOS applies between left and right via
// the "kern" feature, or 0 when no pair-adjustment lookup covers the pair.
func (p *gpos) Kern(left, right GlyphIndex) int {
	idxs := p.selectLookups("DFLT", "", map[string]bool{"kern": true})
	for _, li := range idxs {
		if li >= len(p.lookups) {
			continue
		}
		for _, st := range p.lookups[li].subtables {
			if v, ok := st.kern(left, right); ok {
				return v
			}
		}
	}
	return 0
}

// Kerner combines GPOS and the legacy kern table: GPOS is preferred and the
// kern table is a fallback used when GPOS yields no adjustment. Either source
// may be nil.
type Kerner struct {
	gpos *gpos
	kern *kern
}

// Kerning returns the X-advance adjustment between left and right, preferring a
// non-zero GPOS value and otherwise falling back to the legacy kern table.
func (kn *Kerner) Kerning(left, right GlyphIndex) int {
	if kn.gpos != nil {
		if v := kn.gpos.Kern(left, right); v != 0 {
			return v
		}
	}
	if kn.kern != nil {
		return kn.kern.Pair(left, right)
	}
	return 0
}
