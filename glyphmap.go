// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"sort"
)

// glyphMap is a decoded Coverage or ClassDef table: a function from glyph to
// an int (the coverage index, or the class) that is defined on some glyphs.
//
// Memory and decode work are proportional to the bytes read, never to the
// glyphs a range covers (issue #75). A format 2 RangeRecord is six bytes and may
// span 65536 glyphs; expanding it into a map built 3 MB from six bytes, and ten
// thousand of them 655 million map writes. HarfBuzz never expands them: its
// CoverageFormat2::get_coverage and ClassDefFormat2::get_class binary-search the
// RangeRecords in place (src/OT/Layout/Common/CoverageFormat2.hh and
// src/hb-ot-layout-common.hh), and so does this type. A well-formed table (the
// OpenType Common Table Formats require the glyph array and the RangeRecords to
// be sorted and disjoint) is searched directly in the font's bytes, with no
// allocation at all.
//
// The map this replaces had "last write wins" semantics for the duplicated or
// overlapping entries of a malformed table, and the answers stay exactly those:
// a table that is not sorted and disjoint is normalised once, at decode time,
// into sorted disjoint segments by a sweep that lets the later record win. That
// copy holds at most two segments per record, so it too is proportional to the
// bytes read.
//
// A nil *glyphMap is the empty map.
type glyphMap struct {
	kind  gmKind
	b     []byte // the table, for the kinds that are searched in place
	n     int    // record or glyph count
	start int    // classdef format 1: startGlyph
	segs  []gseg // gmSegs: sorted, disjoint
	lin   bool   // gmSegs: value = base + glyph (coverage) rather than base (class)
}

type gmKind uint8

const (
	gmCov1   gmKind = iota // Coverage format 1: n sorted glyph IDs at b[4:]
	gmCov2                 // Coverage format 2: n sorted disjoint ranges at b[4:]
	gmClass1               // ClassDef format 1: n class values for start.. at b[6:]
	gmClass2               // ClassDef format 2: n sorted disjoint ranges at b[4:]
	gmSegs                 // normalised segments
)

// gseg is one normalised segment: every glyph in [lo, hi] maps to base (a
// class) or to base + glyph (a coverage index).
type gseg struct {
	lo, hi uint16
	base   int32
}

func gm16(b []byte, i int) int { return int(b[i])<<8 | int(b[i+1]) }

// get returns the value of g and whether g is in the map.
func (m *glyphMap) get(g GlyphIndex) (int, bool) {
	if m == nil {
		return 0, false
	}
	x := int(g)
	switch m.kind {
	case gmCov1:
		i := sort.Search(m.n, func(i int) bool { return gm16(m.b, 4+2*i) >= x })
		if i < m.n && gm16(m.b, 4+2*i) == x {
			return i, true
		}
	case gmCov2, gmClass2:
		// the first record whose end is >= x; the records are disjoint and sorted
		i := sort.Search(m.n, func(i int) bool { return gm16(m.b, 4+6*i+2) >= x })
		if i < m.n {
			o := 4 + 6*i
			if s := gm16(m.b, o); s <= x {
				if m.kind == gmCov2 {
					return gm16(m.b, o+4) + x - s, true
				}
				return gm16(m.b, o+4), true
			}
		}
	case gmClass1:
		// The glyph of entry i was GlyphIndex(start+i): it wraps at 65536, and the
		// later entry won when two met.
		if i := x - m.start + 0x10000; i >= 0 && i < m.n {
			return gm16(m.b, 6+2*i), true
		}
		if i := x - m.start; i >= 0 && i < m.n {
			return gm16(m.b, 6+2*i), true
		}
	case gmSegs:
		i := sort.Search(len(m.segs), func(i int) bool { return int(m.segs[i].hi) >= x })
		if i < len(m.segs) && int(m.segs[i].lo) <= x {
			if m.lin {
				return int(m.segs[i].base) + x, true
			}
			return int(m.segs[i].base), true
		}
	}
	return 0, false
}

// has reports whether g is in the map.
func (m *glyphMap) has(g GlyphIndex) bool {
	_, ok := m.get(g)
	return ok
}

// class returns the value of g, 0 when g is not in the map: the ClassDef rule.
func (m *glyphMap) class(g GlyphIndex) int {
	v, _ := m.get(g)
	return v
}

// rec is a record of a table being normalised: [lo, hi] -> base (+ glyph).
type rec struct {
	lo, hi int
	base   int32
}

// normalise builds the segments of recs, later records overriding earlier ones.
func normalise(recs []rec, lin bool) *glyphMap {
	order := make([]int, 0, len(recs)) // records by lo, skipping empty ranges
	for i, r := range recs {
		if r.lo <= r.hi {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return recs[order[a]].lo < recs[order[b]].lo })
	m := &glyphMap{kind: gmSegs, lin: lin}
	var heap []int // max-heap of record indices, the latest on top
	push := func(v int) {
		heap = append(heap, v)
		for c := len(heap) - 1; c > 0; {
			p := (c - 1) / 2
			if heap[p] >= heap[c] {
				break
			}
			heap[p], heap[c] = heap[c], heap[p]
			c = p
		}
	}
	pop := func() {
		last := len(heap) - 1
		heap[0] = heap[last]
		heap = heap[:last]
		for p := 0; ; {
			c := 2*p + 1
			if c >= len(heap) {
				break
			}
			if c+1 < len(heap) && heap[c+1] > heap[c] {
				c++
			}
			if heap[p] >= heap[c] {
				break
			}
			heap[p], heap[c] = heap[c], heap[p]
			p = c
		}
	}
	next, pos := 0, 0 // next index into order; first glyph not yet assigned
	for next < len(order) || len(heap) > 0 {
		if len(heap) == 0 && pos < recs[order[next]].lo {
			pos = recs[order[next]].lo
		}
		for next < len(order) && recs[order[next]].lo <= pos {
			push(order[next])
			next++
		}
		for len(heap) > 0 && recs[heap[0]].hi < pos {
			pop()
		}
		if len(heap) == 0 {
			continue
		}
		// The winner holds until its own end or the start of the next record.
		w := recs[heap[0]]
		end := w.hi
		if next < len(order) && recs[order[next]].lo-1 < end {
			end = recs[order[next]].lo - 1
		}
		m.segs = append(m.segs, gseg{lo: uint16(pos), hi: uint16(end), base: w.base})
		pos = end + 1
	}
	return m
}

// glyphMapFromMap returns the glyphMap of m, as classes. It is how tests build a
// table without encoding it.
func glyphMapFromMap(m map[GlyphIndex]int) *glyphMap {
	recs := make([]rec, 0, len(m))
	for g, v := range m {
		recs = append(recs, rec{lo: int(g), hi: int(g), base: int32(v)})
	}
	return normalise(recs, false)
}

// parseCoverage decodes a Coverage table (format 1 or 2): glyph to coverage index.
func parseCoverage(b []byte) (*glyphMap, error) {
	r := reader{b: b}
	format := r.u16()
	switch format {
	case 1, 2:
		n := int(r.u16())
		size := 2
		if format == 2 {
			size = 6
		}
		if r.err != nil || n > (len(b)-4)/size {
			return nil, fmt.Errorf("opentype: coverage: %w", errTruncated)
		}
		if format == 1 {
			if sortedGlyphs(b, n) {
				return &glyphMap{kind: gmCov1, b: b, n: n}, nil
			}
			recs := make([]rec, n)
			for i := range recs {
				g := gm16(b, 4+2*i)
				recs[i] = rec{lo: g, hi: g, base: int32(i - g)}
			}
			return normalise(recs, true), nil
		}
		if sortedRanges(b, n) {
			return &glyphMap{kind: gmCov2, b: b, n: n}, nil
		}
		recs := make([]rec, n)
		for i := range recs {
			o := 4 + 6*i
			recs[i] = rec{lo: gm16(b, o), hi: gm16(b, o+2), base: int32(gm16(b, o+4) - gm16(b, o))}
		}
		return normalise(recs, true), nil
	}
	return nil, fmt.Errorf("opentype: coverage format %d", format)
}

// parseClassDef decodes a ClassDef table (format 1 or 2): glyph to class. Glyphs
// absent from the map belong to class 0.
func parseClassDef(b []byte) (*glyphMap, error) {
	r := reader{b: b}
	format := r.u16()
	switch format {
	case 1:
		start := int(r.u16())
		n := int(r.u16())
		if r.err != nil || n > (len(b)-6)/2 {
			return nil, fmt.Errorf("opentype: classdef: %w", errTruncated)
		}
		return &glyphMap{kind: gmClass1, b: b, n: n, start: start}, nil
	case 2:
		n := int(r.u16())
		if r.err != nil || n > (len(b)-4)/6 {
			return nil, fmt.Errorf("opentype: classdef: %w", errTruncated)
		}
		if sortedRanges(b, n) {
			return &glyphMap{kind: gmClass2, b: b, n: n}, nil
		}
		recs := make([]rec, n)
		for i := range recs {
			o := 4 + 6*i
			recs[i] = rec{lo: gm16(b, o), hi: gm16(b, o+2), base: int32(gm16(b, o+4))}
		}
		return normalise(recs, false), nil
	}
	return nil, fmt.Errorf("opentype: classdef format %d", format)
}

// sortedGlyphs reports whether the n glyph IDs at b[4:] are strictly increasing.
func sortedGlyphs(b []byte, n int) bool {
	for i := 1; i < n; i++ {
		if gm16(b, 4+2*i) <= gm16(b, 4+2*(i-1)) {
			return false
		}
	}
	return true
}

// sortedRanges reports whether the n RangeRecords at b[4:] are non-empty, sorted
// and disjoint.
func sortedRanges(b []byte, n int) bool {
	for i := 0; i < n; i++ {
		o := 4 + 6*i
		if gm16(b, o) > gm16(b, o+2) || i > 0 && gm16(b, o) <= gm16(b, o-4) {
			return false
		}
	}
	return true
}

// covValues is a Coverage joined with a parallel value slice: the value of glyph
// g is vals[coverage index of g], when that index is in range. It replaces the
// glyph-to-value map the MATH decoders used to build by iterating every covered
// glyph.
type covValues[T any] struct {
	cov  *glyphMap
	vals []T
}

// get returns the value of g and whether g has one.
func (c covValues[T]) get(g GlyphIndex) (T, bool) {
	if i, ok := c.cov.get(g); ok && i < len(c.vals) {
		return c.vals[i], true
	}
	var zero T
	return zero, false
}

// val returns the value of g, the zero value when g has none.
func (c covValues[T]) val(g GlyphIndex) T {
	v, _ := c.get(g)
	return v
}
