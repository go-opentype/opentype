// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

// ctrlBox is a running control box: the bounds of every point of an outline,
// on-curve and off-curve alike, as FreeType's FT_Outline_Get_CBox computes it.
//
// The CFF and Type 1 interpreters keep their outlines flattened, and a flattened
// cubic has lost its control points. A control point can lie beyond the curve —
// on any glyph not drawn with points at its extrema, and on every slanted one —
// so a box taken from the flattened outline is the TIGHT box, and is smaller
// than the one a TeX engine reads: XeTeX's italic correction is
// max(0, cbox.xMax − advance) (XeTeXFontInst::getGlyphItalCorr, with
// FT_Glyph_Get_CBox(…, FT_GLYPH_BBOX_UNSCALED, …)). The interpreters therefore
// record the box as they draw, control points included.
type ctrlBox struct {
	xMin, yMin, xMax, yMax float64
	set                    bool
}

// add grows the box to hold (x, y).
func (b *ctrlBox) add(x, y float64) {
	if !b.set {
		*b = ctrlBox{x, y, x, y, true}
		return
	}
	b.xMin = min(b.xMin, x)
	b.yMin = min(b.yMin, y)
	b.xMax = max(b.xMax, x)
	b.yMax = max(b.yMax, y)
}

// union grows the box to hold o shifted by (dx, dy): a seac accent's box,
// placed where the accent is drawn.
func (b *ctrlBox) union(o ctrlBox, dx, dy float64) {
	if !o.set {
		return
	}
	b.add(o.xMin+dx, o.yMin+dy)
	b.add(o.xMax+dx, o.yMax+dy)
}

// startSegment adds a contour's starting point the first time something is
// drawn from it. FreeType's CFF and Type 1 builders add a moveto's point only
// when a segment follows it, so a moveto that draws nothing widens no box.
func (b *ctrlBox) startSegment(cur contour) {
	if len(cur) == 1 {
		b.add(cur[0].x, cur[0].y)
	}
}

// contoursBox is the control box of decoded contours: for TrueType, whose
// off-curve points are kept, every point is on the outline FreeType bounds.
func contoursBox(cs []contour) ctrlBox {
	var b ctrlBox
	for _, c := range cs {
		for _, p := range c {
			b.add(p.x, p.y)
		}
	}
	return b
}

// GlyphControlBox returns glyph gid's control box in font design units, Y up:
// the bounds of all its outline points, the curves' control points included,
// unhinted and at the face's current variation. It is FreeType's
// FT_Glyph_Get_CBox with FT_GLYPH_BBOX_UNSCALED — the box XeTeX takes a glyph's
// italic correction from — and it can be wider than the ink, never narrower.
//
// For the ink's own bounds use [Face.GlyphOutline]; CFF outlines come back
// flattened there, so their extent is the curves' and not their control
// points'.
//
// ok is false when gid is out of range or its outline is corrupt. An empty
// glyph (a space) has a zero box and ok true, as FreeType gives it.
func (fc *Face) GlyphControlBox(gid GlyphIndex) (xMin, yMin, xMax, yMax float64, ok bool) {
	var b ctrlBox
	switch f := fc.font; {
	case f.t1 != nil:
		m, err := f.t1.runGlyph(int(gid))
		if err != nil {
			return 0, 0, 0, 0, false
		}
		b = m.box
	case f.cff2 != nil:
		m, err := f.cff2.runGlyph(int(gid), f.NormalizeCoords(fc.varCoords))
		if err != nil {
			return 0, 0, 0, 0, false
		}
		b = m.tm.box
	case f.cff != nil:
		m, err := f.cff.runGlyph(int(gid), 0)
		if err != nil {
			return 0, 0, 0, 0, false
		}
		b = m.box
	default:
		var cs []contour
		var err error
		if fc.varCoords != nil {
			cs, err = f.InstancePoints(int(gid), fc.varCoords)
		} else {
			cs, err = f.glyphContours(gid)
		}
		if err != nil {
			return 0, 0, 0, 0, false
		}
		b = contoursBox(cs)
	}
	return b.xMin, b.yMin, b.xMax, b.yMax, true
}
