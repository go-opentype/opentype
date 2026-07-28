// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

// This file wires the OpenType Layout tables (GSUB, GPOS and the legacy kern
// table, parsed in gsub.go/gpos.go/kern.go) into the public Face API: Shape
// runs glyph substitution over a text run, while Kern and MeasureKerned apply
// pair-adjustment kerning. A font without the relevant tables behaves as a
// plain unshaped, unkerned font.

// Shape maps text to its glyph run and applies the GSUB lookups activated by
// the given feature tags (for example "liga" for standard ligatures, "smcp"
// for small caps). Each rune maps through the font's cmap; an unmapped rune
// becomes glyph 0 (.notdef). With no GSUB table, no features, or features that
// match no lookups, the run is the plain cmap mapping.
func (fc *Face) Shape(text string, features ...string) []GlyphIndex {
	var glyphs []GlyphIndex
	for _, r := range text {
		gid, _ := fc.font.GlyphIndex(r) // unmapped -> 0 (.notdef)
		glyphs = append(glyphs, gid)
	}
	if fc.font.gsub != nil {
		glyphs = fc.font.gsub.Apply(glyphs, features...)
	}
	return glyphs
}

// Kern returns the horizontal kerning adjustment between the consecutive runes
// prev and r, in whole pixels at the face's size. GPOS pair positioning is
// preferred; the legacy kern table is used as a fallback. It is zero when either
// rune is unmapped or the font carries no kerning for the pair.
func (fc *Face) Kern(prev, r rune) int {
	lg, ok := fc.font.GlyphIndex(prev)
	if !ok {
		return 0
	}
	rg, ok := fc.font.GlyphIndex(r)
	if !ok {
		return 0
	}
	kn := Kerner{gpos: fc.font.gpos, kern: fc.font.kern}
	return roundInt(float64(kn.Kerning(lg, rg)) * fc.scale)
}

// MeasureKerned returns the total advance width of s in pixels like Measure,
// but additionally applies the font's kerning between each pair of consecutive
// runes. For a font with no kerning it equals Measure.
func (fc *Face) MeasureKerned(s string) int {
	total := 0
	var prev rune
	havePrev := false
	for _, r := range s {
		if havePrev {
			total += fc.Kern(prev, r)
		}
		total += fc.Advance(r)
		prev = r
		havePrev = true
	}
	return total
}
