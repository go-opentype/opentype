// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file adds vertical writing-mode metrics: the data needed to lay glyphs
// out top-to-bottom for CJK tategaki and other vertical text. Three optional
// tables carry it:
//
//   - vhea: the vertical header (ascender/descender/lineGap for vertical layout
//     plus numOfLongVerMetrics, the horizontal analogue of hhea);
//   - vmtx: per-glyph advanceHeight and topSideBearing (analogous to hmtx);
//   - VORG: the Vertical Origin table, a default and per-glyph y coordinate of
//     each glyph's vertical origin.
//
// Absence of these tables is not an error; it simply means vertical metrics are
// unavailable (HasVerticalMetrics reports false) and the caller falls back to
// the em square.
//
// Selecting vertical GLYPH FORMS (the OpenType 'vert'/'vrt2' features that swap
// horizontal glyphs for their upright vertical variants) is a shaping concern
// handled through GSUB (see gsub.go and Face.Shape). This file supplies the
// vertical METRICS and origin those laid-out glyphs are positioned with; it does
// not itself perform substitution.

// parseVertical decodes the optional vhea, vmtx and VORG tables. Each is
// independent, but vmtx is meaningful only alongside vhea (which supplies
// numOfLongVerMetrics), so a vmtx without a vhea is ignored. A malformed table
// that is present is reported as an error so a corrupt font fails cleanly.
func (f *Font) parseVertical(tables map[string][]byte) error {
	if b, ok := tables["vhea"]; ok {
		if err := f.parseVhea(b); err != nil {
			return err
		}
	}
	if b, ok := tables["vmtx"]; ok && f.hasVhea {
		if err := f.parseVmtx(b); err != nil {
			return err
		}
	}
	if b, ok := tables["VORG"]; ok {
		if err := f.parseVORG(b); err != nil {
			return err
		}
	}
	return nil
}

// parseVhea reads the vertical metrics and numOfLongVerMetrics from vhea. Its
// layout mirrors hhea: vertTypoAscender/Descender/LineGap are int16 at offsets
// 4/6/8 and numOfLongVerMetrics is the uint16 at offset 34.
func (f *Font) parseVhea(b []byte) error {
	if len(b) < 36 {
		return fmt.Errorf("opentype: vhea table: %w", errTruncated)
	}
	f.vertAscender = int(sbe16(b[4:]))
	f.vertDescender = int(sbe16(b[6:]))
	f.vertLineGap = int(sbe16(b[8:]))
	f.numOfLongVerMetrics = int(be16(b[34:]))
	if f.numOfLongVerMetrics == 0 || f.numOfLongVerMetrics > f.numGlyphs {
		return fmt.Errorf("opentype: vhea table: bad numOfLongVerMetrics %d", f.numOfLongVerMetrics)
	}
	f.hasVhea = true
	return nil
}

// parseVmtx expands the vmtx table into per-glyph advanceHeight and top side
// bearing slices, mirroring parseHmtx. The first numOfLongVerMetrics entries are
// (advanceHeight, topSideBearing) pairs; the trailing glyphs share the last
// entry's advance and carry their own top side bearing.
func (f *Font) parseVmtx(b []byte) error {
	n := f.numOfLongVerMetrics
	need := 4*n + 2*(f.numGlyphs-n)
	if len(b) < need {
		return fmt.Errorf("opentype: vmtx table: %w", errTruncated)
	}
	f.vertAdvances = make([]int, f.numGlyphs)
	f.tsbs = make([]int, f.numGlyphs)
	lastAdvance := 0
	for i := 0; i < f.numGlyphs; i++ {
		if i < n {
			lastAdvance = int(be16(b[i*4:]))
			f.vertAdvances[i] = lastAdvance
			f.tsbs[i] = int(sbe16(b[i*4+2:]))
		} else {
			f.vertAdvances[i] = lastAdvance
			f.tsbs[i] = int(sbe16(b[4*n+2*(i-n):]))
		}
	}
	f.hasVmtx = true
	return nil
}

// parseVORG decodes the Vertical Origin table: an 8-byte header carrying the
// defaultVertOriginY (int16 at offset 4) and numVertOriginYMetrics (uint16 at
// offset 6), followed by that many 4-byte {glyphIndex uint16, vertOriginY int16}
// records overriding the default for specific glyphs.
func (f *Font) parseVORG(b []byte) error {
	if len(b) < 8 {
		return fmt.Errorf("opentype: VORG table: %w", errTruncated)
	}
	f.vorgDefault = int(sbe16(b[4:]))
	count := int(be16(b[6:]))
	if len(b) < 8+4*count {
		return fmt.Errorf("opentype: VORG table: %w", errTruncated)
	}
	f.vorg = make(map[GlyphIndex]int, count)
	for i := 0; i < count; i++ {
		rec := b[8+4*i:]
		f.vorg[GlyphIndex(be16(rec))] = int(sbe16(rec[2:]))
	}
	f.hasVORG = true
	return nil
}

// HasVerticalMetrics reports whether the font carries the vertical header and
// per-glyph vertical advances (vhea and vmtx) required to lay text out
// top-to-bottom. When it returns false, VerticalAdvance falls back to the em
// square and VerticalMetrics returns zeroes.
func (f *Font) HasVerticalMetrics() bool {
	return f.hasVhea && f.hasVmtx
}

// verticalAdvance returns glyph gid's advance height in font units. When the
// font has no vmtx table it falls back to one em (unitsPerEm), the conventional
// full-width advance for vertical CJK layout.
func (f *Font) verticalAdvance(gid GlyphIndex) int {
	if !f.hasVmtx {
		return f.unitsPerEm
	}
	return f.vertAdvances[gid]
}

// verticalOriginY returns glyph gid's vertical origin y in font units and
// whether the font supplies one (a VORG table). A glyph without its own record
// takes the table's default vertical origin.
func (f *Font) verticalOriginY(gid GlyphIndex) (int, bool) {
	if !f.hasVORG {
		return 0, false
	}
	if y, ok := f.vorg[gid]; ok {
		return y, true
	}
	return f.vorgDefault, true
}

// VerticalAdvance returns the vertical advance (advance height) of r in pixels,
// or 0 if the rune is not mapped by the font's cmap. When the font has no vmtx
// table the advance is one em, scaled to the face's size.
//
// This is the top-to-bottom analogue of Advance. Choosing the upright vertical
// glyph form for r (the 'vert'/'vrt2' features) is a separate shaping step done
// through GSUB; VerticalAdvance measures whichever glyph the cmap maps.
func (fc *Face) VerticalAdvance(r rune) int {
	gid, ok := fc.font.GlyphIndex(r)
	if !ok {
		return 0
	}
	return roundInt(float64(fc.font.verticalAdvance(gid)) * fc.scale)
}

// VerticalMetrics returns the face's vertical-layout line metrics in pixels: the
// vhea vertTypoAscender, vertTypoDescender and vertTypoLineGap scaled to the
// face's size. All three are zero when the font has no vhea table.
func (fc *Face) VerticalMetrics() (ascent, descent, lineGap int) {
	s := fc.scale
	return roundInt(float64(fc.font.vertAscender) * s),
		roundInt(float64(fc.font.vertDescender) * s),
		roundInt(float64(fc.font.vertLineGap) * s)
}

// VerticalOrigin returns the y coordinate of r's vertical origin in pixels and
// whether the font supplies one (a VORG table). The origin locates the glyph
// relative to the pen when advancing top-to-bottom; ok is false when r is
// unmapped or the font has no VORG table.
func (fc *Face) VerticalOrigin(r rune) (int, bool) {
	gid, mapped := fc.font.GlyphIndex(r)
	if !mapped {
		return 0, false
	}
	y, ok := fc.font.verticalOriginY(gid)
	if !ok {
		return 0, false
	}
	return roundInt(float64(y) * fc.scale), true
}
