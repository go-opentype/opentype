// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"image"
	"math"
)

// Metrics holds a Face's vertical metrics in whole pixels.
type Metrics struct {
	Ascent  int // baseline-to-top distance (a positive height above the baseline)
	Descent int // baseline-to-bottom distance (a positive depth below the baseline)
	Height  int // line height: ascent + descent + line gap
}

// Face renders a Font at a fixed pixel size. It caches rasterised glyphs and
// is not safe for concurrent use; build one Face per goroutine if needed.
type Face struct {
	font   *Font
	sizePx int
	scale  float64
	cache  map[GlyphIndex]cachedGlyph
}

// cachedGlyph is a rasterised glyph held in the Face cache. ok is false when
// the glyph could not be decoded (a corrupt outline), in which case it renders
// as nothing.
type cachedGlyph struct {
	bounds image.Rectangle
	mask   *image.Alpha
	ok     bool
}

// roundInt rounds a float to the nearest integer (halves away from zero).
func roundInt(v float64) int { return int(math.Round(v)) }

// NewFace returns a Face that renders f at sizePx pixels per em. The scale
// factor is sizePx / unitsPerEm; sizePx should be positive.
func (f *Font) NewFace(sizePx int) *Face {
	return &Face{
		font:   f,
		sizePx: sizePx,
		scale:  float64(sizePx) / float64(f.unitsPerEm),
		cache:  map[GlyphIndex]cachedGlyph{},
	}
}

// Metrics returns the face's vertical metrics in pixels.
func (fc *Face) Metrics() Metrics {
	s := fc.scale
	return Metrics{
		Ascent:  roundInt(float64(fc.font.ascender) * s),
		Descent: roundInt(float64(-fc.font.descender) * s),
		Height:  roundInt(float64(fc.font.ascender-fc.font.descender+fc.font.lineGap) * s),
	}
}

// Advance returns the horizontal advance of r in pixels, or 0 if the rune is
// not mapped by the font's cmap.
func (fc *Face) Advance(r rune) int {
	gid, ok := fc.font.GlyphIndex(r)
	if !ok {
		return 0
	}
	return roundInt(float64(fc.font.advances[gid]) * fc.scale)
}

// Measure returns the total advance width of s in pixels (the sum of each
// rune's Advance).
func (fc *Face) Measure(s string) int {
	total := 0
	for _, r := range s {
		total += fc.Advance(r)
	}
	return total
}

// glyph returns the cached rasterisation of glyph gid, rendering it on first
// use.
func (fc *Face) glyph(gid GlyphIndex) cachedGlyph {
	if cg, ok := fc.cache[gid]; ok {
		return cg
	}
	cg := fc.render(gid)
	fc.cache[gid] = cg
	return cg
}

// render decodes and rasterises glyph gid at the face's size.
func (fc *Face) render(gid GlyphIndex) cachedGlyph {
	contours, err := fc.font.glyphContours(gid)
	if err != nil {
		return cachedGlyph{ok: false}
	}
	polys := make([][]fpoint, 0, len(contours))
	for _, c := range contours {
		p := flattenContour(c, fc.scale)
		if len(p) >= 3 {
			polys = append(polys, p)
		}
	}
	bounds, mask := rasterize(polys)
	return cachedGlyph{bounds: bounds, mask: mask, ok: true}
}

// GlyphMask rasterises r and positions it with (x, y) as the pen origin on the
// baseline. It returns the destination bounds, an *image.Alpha coverage mask,
// the offset into that mask corresponding to bounds.Min (always the origin),
// the advance width in pixels, and ok.
//
// ok is false when the rune is not mapped by the cmap or its glyph outline is
// corrupt; callers should render nothing in that case. A mapped-but-empty
// glyph (for example a space) returns ok true with a nil mask, an empty
// bounds, and its advance.
func (fc *Face) GlyphMask(r rune, x, y int) (bounds image.Rectangle, mask *image.Alpha, maskp image.Point, advance int, ok bool) {
	gid, mapped := fc.font.GlyphIndex(r)
	if !mapped {
		return image.Rectangle{}, nil, image.Point{}, 0, false
	}
	cg := fc.glyph(gid)
	if !cg.ok {
		return image.Rectangle{}, nil, image.Point{}, 0, false
	}
	advance = roundInt(float64(fc.font.advances[gid]) * fc.scale)
	bounds = cg.bounds.Add(image.Point{X: x, Y: y})
	return bounds, cg.mask, image.Point{}, advance, true
}
