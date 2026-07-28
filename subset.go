// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// This file implements TrueType ('glyf') glyph subsetting: rebuilding a minimal,
// self-contained sfnt that carries only a requested set of glyphs (and every
// glyph a composite among them references). It is the primitive a PDF or EPUB
// embedder needs so it can ship a subset font program instead of the whole face,
// and so it does not have to re-parse and re-serialise the sfnt container itself.
// CFF ('CFF ') charstring subsetting lives in subsetcff.go.

// componentRef locates one component-glyph reference inside a composite glyph's
// raw bytes: the byte offset of its two-byte glyph-id field and the glyph id
// stored there. Subsetting reads the ids to compute the closure and rewrites the
// field at the offset to the component's new id.
type componentRef struct {
	offset int
	gid    GlyphIndex
}

// componentRefs returns the component-glyph references of a raw glyph. It returns
// nil for an empty or simple glyph (numberOfContours >= 0) and, for a composite,
// one entry per component in stream order. The walk is bounds-checked: a record
// that would read past the end of data ends the scan, so a truncated composite
// yields the components decodable so far rather than a panic.
func componentRefs(data []byte) []componentRef {
	if len(data) < 10 || sbe16(data) >= 0 {
		return nil
	}
	var refs []componentRef
	p := 10
	for p+4 <= len(data) {
		flags := be16(data[p:])
		refs = append(refs, componentRef{offset: p + 2, gid: GlyphIndex(be16(data[p+2:]))})
		p += 4
		if flags&compArgsAreWords != 0 {
			p += 4
		} else {
			p += 2
		}
		switch {
		case flags&compWeHaveScale != 0:
			p += 2
		case flags&compXAndYScale != 0:
			p += 4
		case flags&compTwoByTwo != 0:
			p += 8
		}
		if flags&compMoreComponents == 0 {
			break
		}
	}
	return refs
}

// rawGlyph returns glyph gid's raw bytes from the glyf table (the loca-delimited
// slice), or nil for an empty glyph. The caller must not mutate the result; it
// aliases the font's backing data.
func (f *Font) rawGlyph(gid GlyphIndex) []byte {
	start, end := f.loca[gid], f.loca[gid+1]
	if start >= end || int(end) > len(f.glyf) {
		return nil
	}
	return f.glyf[start:end]
}

// subsetClosure returns the sorted set of glyph ids to keep: the requested gids,
// glyph 0 (.notdef, always retained so the subset has a valid missing glyph), and
// every glyph transitively referenced by a composite among them. A requested id
// outside the font's glyph range is an error. Cycles are terminated by the
// membership set, so a self- or mutually-referential composite is handled.
func (f *Font) subsetClosure(gids []GlyphIndex) ([]GlyphIndex, error) {
	needed := map[GlyphIndex]bool{}
	var walk func(gid GlyphIndex) error
	walk = func(gid GlyphIndex) error {
		if int(gid) >= f.numGlyphs {
			return fmt.Errorf("opentype: subset: glyph index %d out of range", int(gid))
		}
		if needed[gid] {
			return nil
		}
		needed[gid] = true
		for _, ref := range componentRefs(f.rawGlyph(gid)) {
			if err := walk(ref.gid); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(0); err != nil {
		return nil, err
	}
	for _, g := range gids {
		if err := walk(g); err != nil {
			return nil, err
		}
	}
	order := make([]GlyphIndex, 0, len(needed))
	for g := range needed {
		order = append(order, g)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	return order, nil
}

// SubsetTrueType builds a minimal, valid TrueType sfnt carrying only the glyphs
// in gids plus glyph 0 and every component a composite among them references. The
// kept glyphs are renumbered compactly (glyph 0 stays 0); the returned oldToNew
// map gives each retained original glyph id its id in the subset, which a PDF
// CIDFontType2 embedder turns into a /CIDToGIDMap (CID = original id -> subset id)
// and a content-stream writer uses to re-address glyphs.
//
// The rebuilt font contains glyf, loca (long form), head, hhea, maxp, hmtx and a
// minimal cmap (enough to re-parse; a PDF addresses the subset by glyph id, not
// through the cmap), plus the TrueType instruction tables (cvt/fpgm/prep) when the
// original font carried them so hinted glyphs still render as designed. Composite
// component references are rewritten to the new glyph numbering.
//
// It returns an error for a font with no glyf table (a CFF/OpenType font — use
// SubsetCFF) or when a requested glyph id is out of range.
func (f *Font) SubsetTrueType(gids []GlyphIndex) (data []byte, oldToNew map[GlyphIndex]GlyphIndex, err error) {
	if f.glyf == nil {
		return nil, nil, errors.New("opentype: SubsetTrueType: font has no glyf table (not a TrueType font)")
	}
	kept, err := f.subsetClosure(gids)
	if err != nil {
		return nil, nil, err
	}

	oldToNew = make(map[GlyphIndex]GlyphIndex, len(kept))
	for newGID, oldGID := range kept {
		oldToNew[oldGID] = GlyphIndex(newGID)
	}

	// Rebuild glyf and a long-format loca in the new, compact glyph order,
	// rewriting each composite's component ids to their subset ids.
	blobs := make([][]byte, len(kept))
	for newGID, oldGID := range kept {
		blobs[newGID] = remapGlyph(f.rawGlyph(oldGID), oldToNew)
	}
	locaBytes, glyf := packGlyf(blobs)

	tables := map[string][]byte{
		"glyf": glyf,
		"loca": locaBytes,
		"head": f.subsetHead(),
		"hhea": f.subsetHhea(len(kept)),
		"maxp": f.subsetMaxp(len(kept)),
		"hmtx": f.subsetHmtx(kept),
		"cmap": minimalCmap(),
	}
	for _, opt := range []string{"cvt ", "fpgm", "prep"} {
		if b, ok := f.tables[opt]; ok {
			tables[opt] = append([]byte(nil), b...)
		}
	}
	return buildSFNT(versionTrueType, tables), oldToNew, nil
}

// remapGlyph returns a copy of a raw glyph with every composite component id
// rewritten to its subset id via oldToNew. A simple or empty glyph is copied
// unchanged. A component id absent from oldToNew (which cannot happen for a glyph
// produced by subsetClosure, whose closure includes every component) is left as
// is, so the function never panics on unexpected input.
func remapGlyph(data []byte, oldToNew map[GlyphIndex]GlyphIndex) []byte {
	refs := componentRefs(data)
	out := append([]byte(nil), data...)
	for _, ref := range refs {
		if nb, ok := oldToNew[ref.gid]; ok {
			binary.BigEndian.PutUint16(out[ref.offset:], uint16(nb))
		}
	}
	return out
}

// subsetHead clones the head table with checkSumAdjustment zeroed (buildSFNT
// recomputes it) and indexToLocFormat set to long, matching the long-format loca
// the subsetter always emits.
func (f *Font) subsetHead() []byte {
	head := append([]byte(nil), f.tables["head"]...)
	binary.BigEndian.PutUint32(head[8:], 0)  // checkSumAdjustment: recomputed by buildSFNT
	binary.BigEndian.PutUint16(head[50:], 1) // indexToLocFormat: long
	return head
}

// subsetHhea clones the hhea table with numberOfHMetrics set to the subset glyph
// count, matching the full-width hmtx subsetHmtx emits.
func (f *Font) subsetHhea(n int) []byte {
	hhea := append([]byte(nil), f.tables["hhea"]...)
	binary.BigEndian.PutUint16(hhea[34:], uint16(n))
	return hhea
}

// subsetMaxp clones the maxp table with numGlyphs set to the subset glyph count.
// The other maxp limits (max points, max component depth, ...) remain valid upper
// bounds for a subset of the original glyphs, so they are carried over unchanged.
func (f *Font) subsetMaxp(n int) []byte {
	maxp := append([]byte(nil), f.tables["maxp"]...)
	binary.BigEndian.PutUint16(maxp[4:], uint16(n))
	return maxp
}

// subsetHmtx builds an hmtx table for the subset glyph order: one (advanceWidth,
// leftSideBearing) pair per kept glyph, taken from the original per-glyph metrics.
// Emitting a full entry for every glyph (numberOfHMetrics == glyph count, set in
// subsetHhea) keeps the table trivially correct without the trailing shared-advance
// optimisation.
func (f *Font) subsetHmtx(kept []GlyphIndex) []byte {
	out := make([]byte, 4*len(kept))
	for i, oldGID := range kept {
		binary.BigEndian.PutUint16(out[i*4:], uint16(f.advances[oldGID]))
		binary.BigEndian.PutUint16(out[i*4+2:], uint16(int16(f.lsbs[oldGID])))
	}
	return out
}
