// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

// This file adds by-glyph-index advance accessors that honour the face's current
// variation instance. Face.Advance and Face.VerticalAdvance take a rune and go
// through the cmap; a text layer that has already resolved glyph indices (a
// shaper, or a PDF writer emitting a /W width array) needs the advance of a glyph
// it names directly, at the same instance the outlines are rendered for. Without
// these it would have to rasterise the glyph or reach past the cmap by hand.

// AdvanceIndex returns the horizontal advance of glyph gid in whole pixels at the
// face's size, honouring the face's current variation (the HVAR delta, or the
// gvar phantom-point fallback, is folded in exactly as Face.Advance does for a
// rune). With no variation set it is the base advance scaled to pixels. It is the
// by-glyph-index counterpart of Face.Advance, for a caller that already holds a
// glyph id rather than a rune. An out-of-range gid advances by zero.
func (fc *Face) AdvanceIndex(gid GlyphIndex) int {
	return roundInt(fc.hAdvanceUnits(gid) * fc.scale)
}

// AdvanceIndexUnits returns the horizontal advance of glyph gid in font units at
// the face's current variation, as a float64 (the value AdvanceIndex rounds and
// scales to pixels). It is the exact width a PDF /W array wants — font-unit widths
// that track the instanced font — without the caller re-deriving the HVAR delta.
// An out-of-range gid advances by zero.
func (fc *Face) AdvanceIndexUnits(gid GlyphIndex) float64 {
	return fc.hAdvanceUnits(gid)
}

// VerticalAdvanceIndex returns the vertical advance (advance height) of glyph gid
// in whole pixels at the face's size, honouring the face's current variation (the
// VVAR delta is folded in). When the font has no vmtx table the advance is one em.
// It is the by-glyph-index counterpart of Face.VerticalAdvance.
func (fc *Face) VerticalAdvanceIndex(gid GlyphIndex) int {
	adv := float64(fc.font.verticalAdvance(gid))
	if fc.varCoords != nil {
		adv += fc.font.verticalAdvanceDelta(int(gid), fc.varNorm)
	}
	return roundInt(adv * fc.scale)
}
