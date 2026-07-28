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

	// varCoords, when non-nil, instances a variable font's glyf outlines at the
	// given user-space axis coordinates before rasterising (see SetVariation).
	varCoords map[string]float64

	// hinting enables the TrueType instruction interpreter for glyf glyphs that
	// carry instructions (see SetHinting). interp is built lazily on first use
	// and reused across glyphs; interpReady guards that one-time build.
	hinting     bool
	interp      *interp
	interpReady bool
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

// Font returns the Font this Face renders. It lets a shaper reach the font's
// layout tables ([Font.GSUB], [Font.GPOS]) and per-glyph metrics
// ([Font.GlyphAdvance]) while keeping the Face for pixel sizing.
func (fc *Face) Font() *Font { return fc.font }

// Scale returns the face's font-unit-to-pixel scale factor (the render size in
// pixels divided by the font's unitsPerEm). A shaper multiplies a font-unit
// advance or offset by it to obtain whole pixels at the face's size.
func (fc *Face) Scale() float64 { return fc.scale }

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
	contours, err := fc.outline(gid)
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

// GlyphMaskIndex rasterises the glyph with index gid directly — bypassing the
// cmap — and positions it with (x, y) as the pen origin on the baseline. It is
// the by-glyph-index counterpart of [Face.GlyphMask]: a complex-text shaper
// (see github.com/go-opentype/shape) emits glyph indices after applying GSUB
// substitutions and GPOS positioning, so the runes have already been resolved
// to glyphs and a caller must render each glyph by its index rather than by a
// rune. Callers add the shaper's per-glyph pixel offset to (x, y) before the
// call and advance the pen by the shaper's advance afterwards.
//
// It returns the destination bounds, an *image.Alpha coverage mask, the offset
// into that mask corresponding to bounds.Min (always the origin), the glyph's
// own horizontal advance width in pixels, and ok.
//
// ok is false when gid is out of range or its outline is corrupt; callers
// should render nothing in that case. A valid-but-empty glyph (for example a
// space) returns ok true with a nil mask, an empty bounds, and its advance.
func (fc *Face) GlyphMaskIndex(gid GlyphIndex, x, y int) (bounds image.Rectangle, mask *image.Alpha, maskp image.Point, advance int, ok bool) {
	cg := fc.glyph(gid)
	if !cg.ok {
		return image.Rectangle{}, nil, image.Point{}, 0, false
	}
	advance = roundInt(float64(fc.font.GlyphAdvance(gid)) * fc.scale)
	bounds = cg.bounds.Add(image.Point{X: x, Y: y})
	return bounds, cg.mask, image.Point{}, advance, true
}

// outline returns glyph gid's outline as contours in font units, selecting the
// outline source (CFF2 vs CFF vs glyf), applying variable-font instancing when a
// variation is set, and applying hinting when it is enabled.
//
// A CFF2 font is inherently variable: its outlines are instanced at the face's
// variation coordinates (the default master when none is set), converted to the
// normalized F2Dot14 form cff2.outline expects via NormalizeCoords. CFF1
// outlines are returned as-is (CFF1 has no variation and CFF hinting is
// deferred, see cff.go), so SetHinting affects glyf fonts only.
func (fc *Face) outline(gid GlyphIndex) ([]contour, error) {
	if fc.font.cff2 != nil {
		return fc.font.cff2.outline(int(gid), fc.font.NormalizeCoords(fc.varCoords))
	}
	if fc.font.cff != nil {
		return fc.font.cff.outline(int(gid))
	}
	var (
		contours []contour
		err      error
	)
	if fc.varCoords != nil {
		contours, err = fc.font.InstancePoints(int(gid), fc.varCoords)
	} else {
		contours, err = fc.font.glyphContours(gid)
	}
	if err != nil {
		return nil, err
	}
	if fc.hinting {
		contours = fc.hintedOutline(gid, contours)
	}
	return contours, nil
}

// SetVariation instances the font at the user-space axis coordinates coords
// (keyed by axis tag, e.g. {"wght": 700}); axes absent from coords take their
// default. Subsequent GlyphMask calls reflect the instanced outlines, for both
// glyf (via gvar) and CFF2 (via its blend/vsindex operators) variable fonts.
// Pass nil to return to the default master. A non-variable font (or a CFF1 font,
// which has no variation) is unaffected. The glyph cache is invalidated so the
// change takes effect at once.
func (fc *Face) SetVariation(coords map[string]float64) {
	fc.varCoords = coords
	fc.cache = map[GlyphIndex]cachedGlyph{}
}

// SetHinting enables or disables the TrueType instruction interpreter. When
// enabled, a glyf glyph that carries instructions is grid-fitted at the face's
// pixel size before rasterising; glyphs without instructions, composite glyphs
// and CFF fonts are unaffected. Hinting is off by default (the default output is
// unhinted anti-aliased coverage). The glyph cache is invalidated.
func (fc *Face) SetHinting(on bool) {
	fc.hinting = on
	fc.cache = map[GlyphIndex]cachedGlyph{}
}

// hintInterp lazily builds the interpreter for this face (running the font
// program and control-value program once) and reuses it across glyphs. It
// returns nil when the font has no interpreter or its programs fail to run, in
// which case the caller renders the glyph unhinted.
func (fc *Face) hintInterp() *interp {
	if !fc.interpReady {
		fc.interpReady = true
		if it, err := newInterp(fc.font, fc.sizePx); err == nil {
			fc.interp = it
		}
	}
	return fc.interp
}

// hintedOutline grid-fits glyph gid's contours through the TrueType interpreter
// and returns the hinted contours in font units. It falls back to the input
// contours when the glyph carries no instructions, the interpreter is
// unavailable, or the program fails. The whole outline is treated as one closed
// contour for IUP, matching the interpreter's model (see hint.go).
func (fc *Face) hintedOutline(gid GlyphIndex, contours []contour) []contour {
	instr := fc.font.glyphInstructions(gid)
	if len(instr) == 0 {
		return contours
	}
	it := fc.hintInterp()
	if it == nil {
		return contours
	}
	// Scale to pixel space (what the interpreter expects), recording contour
	// boundaries so the hinted points can be re-split into contours.
	var pts []outlinePoint
	ends := make([]int, 0, len(contours))
	for _, c := range contours {
		for _, p := range c {
			pts = append(pts, outlinePoint{x: p.x * fc.scale, y: p.y * fc.scale, on: p.on})
		}
		ends = append(ends, len(pts)-1)
	}
	out, err := it.run(pts, instr)
	if err != nil {
		return contours
	}
	// Back to font units so the shared rasteriser (which re-applies scale)
	// reproduces the hinted pixel positions.
	result := make([]contour, len(contours))
	start := 0
	for i, e := range ends {
		cc := make(contour, e-start+1)
		for j := range cc {
			p := out[start+j]
			cc[j] = outlinePoint{x: p.x / fc.scale, y: p.y / fc.scale, on: p.on}
		}
		result[i] = cc
		start = e + 1
	}
	return result
}
