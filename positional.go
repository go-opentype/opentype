// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

// This file exposes the parsed GSUB and GPOS tables to external callers and
// adds positional GSUB application: applying a feature's lookups only at
// selected glyph positions. That per-glyph masking is the mechanism a
// complex-text shaper needs for Arabic cursive joining, where the isol, init,
// medi and fina single-substitution features must each be applied only at the
// glyphs that take that joining form. Whole-run application (as done by Apply)
// remains available by passing a nil mask.

// GSUB is the exported handle to a font's parsed GSUB (glyph substitution)
// table, obtained via [Font.GSUB]. It exposes [GSUB.Apply] (whole-run) and
// [GSUB.ApplyMasked] (positional) so a shaper outside this package can drive
// substitution lookups directly.
type GSUB = gsub

// GPOS is the exported handle to a font's parsed GPOS (glyph positioning)
// table, obtained via [Font.GPOS]. Its [GPOS.Position] method runs positioning
// lookups (pair kerning, mark attachment, cursive attachment, ...) over a glyph
// run a caller has already substituted.
type GPOS = gpos

// GSUB returns the font's parsed GSUB table, or nil when the font carries no
// GSUB table (in which case no substitution is available).
func (f *Font) GSUB() *GSUB { return f.gsub }

// GPOS returns the font's parsed GPOS table, or nil when the font carries no
// GPOS table (in which case no positioning is available).
func (f *Font) GPOS() *GPOS { return f.gpos }

// GlyphAdvance returns the horizontal advance of glyph g in font units, or 0
// when g is out of range. A shaper multiplies it by [Face.Scale] to obtain
// pixels, and feeds the per-glyph advances to [GPOS.Position] so mark
// attachment can pull a diacritic back onto its base.
func (f *Font) GlyphAdvance(g GlyphIndex) int {
	if int(g) < len(f.advances) {
		return f.advances[g]
	}
	return 0
}

// FeatureApp requests one GSUB feature applied over a subset of a glyph run.
// Tag is the 4-byte feature tag (for example "init" or "liga"). Positions
// selects, by run index, the glyphs the feature's lookups may start at: a
// lookup is attempted at glyph i only when Positions[i] is true. A nil
// Positions applies the feature over the whole run (identical to Apply); a
// non-nil Positions restricts it, with indices at or past its end treated as
// false.
//
// Masking is intended for the length-preserving single substitutions OpenType
// files the Arabic positional-form features (isol/init/medi/fina) as, so the
// mask indices stay aligned to the run as it is rewritten.
type FeatureApp struct {
	Tag       string
	Positions []bool
}

// ApplyMasked applies each feature in feats, in order, to the glyph run and
// returns the substituted run; the input slice is not modified. For a feature
// with a nil Positions mask the feature's lookups run over the whole run (as
// [GSUB.Apply] does); with a non-nil mask they are attempted only at the glyphs
// the mask selects — the mechanism Arabic cursive shaping needs to apply
// init/medi/fina/isol each at just the glyphs in that joining position.
//
// Features are resolved with the script-agnostic default fallback (the DFLT
// script, then the union across every script's default LangSys), so features
// filed only under a real script such as "arab" or "latn" still resolve. An
// empty feature list or an empty run leaves the input unchanged.
func (g *gsub) ApplyMasked(glyphs []GlyphIndex, feats []FeatureApp) []GlyphIndex {
	if len(feats) == 0 || len(glyphs) == 0 {
		return glyphs
	}
	out := append([]GlyphIndex(nil), glyphs...)
	a := &gsubApplier{lookups: g.lookups}
	for _, fa := range feats {
		idxs := g.selectLookupsDefault(map[string]bool{fa.Tag: true})
		for _, li := range idxs {
			if li >= len(g.lookups) {
				continue
			}
			out = a.applyLookupMasked(g.lookups[li], out, fa.Positions)
		}
	}
	return out
}

// applyLookupMasked applies one lookup like applyLookup, but restricted to the
// positions the mask selects. A nil mask means the whole run (delegating to
// applyLookup). With a mask, a substitution is attempted at glyph i only when
// mask[i] is true (indices past the mask are treated as false); this is meant
// for length-preserving single substitutions so the mask stays aligned.
func (a *gsubApplier) applyLookupMasked(lk gsubLookup, glyphs []GlyphIndex, mask []bool) []GlyphIndex {
	if mask == nil {
		return a.applyLookup(lk, glyphs)
	}
	masked := func(i int) bool { return i < len(mask) && mask[i] }
	if lk.reverse {
		for i := len(glyphs) - 1; i >= 0; i-- {
			if !masked(i) {
				continue
			}
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
		if !masked(i) {
			i++
			continue
		}
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

// Position runs the GPOS positioning lookups activated by the given feature
// tags over the glyph run and returns one adjustment per glyph, in font units.
// advances gives each glyph's base horizontal advance (used to pull marks back
// onto their base) and may be nil (treated as all-zero). Features are resolved
// with the same script-agnostic default fallback as kerning, so kern/mark/mkmk
// filed under a real script resolve. It is the exported entry point a shaper
// uses to position a run it substituted itself.
func (p *gpos) Position(glyphs []GlyphIndex, advances []int, features ...string) []GlyphPosition {
	return p.position(glyphs, advances, features...)
}
