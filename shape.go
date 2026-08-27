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

// PositionedGlyph is one glyph of a positioned run produced by ShapePositioned:
// the glyph to draw, the pen-relative placement offsets (XOffset, YOffset) GPOS
// assigned it (a non-zero YOffset lifts an attached diacritic onto its base),
// and the advance to move the pen by afterwards, all in whole pixels at the
// face's size.
type PositionedGlyph struct {
	Glyph    GlyphIndex
	XOffset  int
	YOffset  int
	XAdvance int
}

// defaultPosFeatures are the GPOS features ShapePositioned always activates in
// addition to any the caller requests: pair/positional kerning ("kern"),
// mark-to-base/ligature attachment ("mark") and mark-to-mark stacking ("mkmk").
var defaultPosFeatures = []string{"kern", "mark", "mkmk"}

// ShapePositioned maps text to a fully positioned glyph run: each rune maps
// through the cmap (an unmapped rune becomes glyph 0), the requested GSUB
// features are applied (ligatures, contextual substitution, ...), then GPOS
// positions the resulting glyphs — pair kerning, single/cursive adjustment and
// mark attachment (types 4/5/6 pull diacritics onto their base). The positioning
// features are the caller's features plus the always-on kern/mark/mkmk set.
//
// The result has one PositionedGlyph per output glyph, with placement and
// advance in whole pixels at the face's size. A font lacking GSUB or GPOS simply
// skips that stage; with neither the run is the plain cmap mapping at its
// unadjusted advances.
func (fc *Face) ShapePositioned(text string, features ...string) []PositionedGlyph {
	var glyphs []GlyphIndex
	for _, r := range text {
		gid, _ := fc.font.GlyphIndex(r) // unmapped -> 0 (.notdef)
		glyphs = append(glyphs, gid)
	}
	if fc.font.gsub != nil {
		glyphs = fc.font.gsub.Apply(glyphs, features...)
	}
	// Base horizontal advances in font units, indexed by glyph, feed both the
	// mark-attachment pull-back and the final per-glyph advance.
	advances := make([]int, len(glyphs))
	for i, g := range glyphs {
		advances[i] = fc.font.advances[g]
	}
	var positions []GlyphPosition
	if fc.font.gpos != nil {
		posFeatures := append(append([]string(nil), features...), defaultPosFeatures...)
		positions = fc.font.gpos.position(glyphs, advances, posFeatures...)
	}
	out := make([]PositionedGlyph, len(glyphs))
	for i, g := range glyphs {
		adv := advances[i]
		var xOff, yOff int
		if positions != nil {
			xOff = positions[i].XOffset
			yOff = positions[i].YOffset
			adv += positions[i].XAdvance
		}
		out[i] = PositionedGlyph{
			Glyph:    g,
			XOffset:  roundInt(float64(xOff) * fc.scale),
			YOffset:  roundInt(float64(yOff) * fc.scale),
			XAdvance: roundInt(float64(adv) * fc.scale),
		}
	}
	return out
}

// Kern returns the horizontal kerning adjustment between the consecutive runes
// prev and r, in whole pixels at the face's size. GPOS pair positioning is
// preferred; the legacy kern table is used as a fallback. It is zero when either
// rune is unmapped or the font carries no kerning for the pair.
//
// The result is rounded to a whole pixel. A text layer that sets type at (or
// near) the font's own em — where one pixel spans a large design quantum and a
// single-pixel kern would quantise away most of the sub-pixel pair adjustment —
// should measure with KernUnits (or KernIndexUnits) instead, which return the
// unrounded font-unit value, exactly as AdvanceIndexUnits complements
// AdvanceIndex.
func (fc *Face) Kern(prev, r rune) int {
	return roundInt(fc.KernUnits(prev, r) * fc.scale)
}

// KernIndexUnits returns the horizontal kerning adjustment between the
// consecutive glyphs left and right in font units (unrounded) — the value Kern
// scales and rounds to pixels. GPOS pair positioning is preferred; the legacy
// kern table is the fallback. It is the by-glyph-index, font-unit counterpart of
// Kern, the exact primitive a shaper or a text engine wants: kerning applies
// between shaped glyphs (which may be ligatures), and a caller typesetting at
// the design em keeps the sub-pixel adjustment that Kern's whole-pixel rounding
// would discard. A caller multiplies the result by Face.Scale to obtain pixels
// (mirroring AdvanceIndexUnits). The adjustment is zero when the font carries no
// kerning for the pair.
func (fc *Face) KernIndexUnits(left, right GlyphIndex) float64 {
	kn := Kerner{gpos: fc.font.gpos, kern: fc.font.kern}
	return float64(kn.Kerning(left, right))
}

// KernUnits returns the horizontal kerning adjustment between the consecutive
// runes prev and r in font units (unrounded) — the rune-keyed, font-unit
// counterpart of Kern. Each rune maps through the cmap; the result is zero when
// either rune is unmapped or the font carries no kerning for the pair. A caller
// multiplies by Face.Scale to obtain pixels; see KernIndexUnits for the
// by-glyph-index form a shaper drives on an already-substituted run.
func (fc *Face) KernUnits(prev, r rune) float64 {
	lg, ok := fc.font.GlyphIndex(prev)
	if !ok {
		return 0
	}
	rg, ok := fc.font.GlyphIndex(r)
	if !ok {
		return 0
	}
	return fc.KernIndexUnits(lg, rg)
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
