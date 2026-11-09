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
	font *Font
	// sizePx is the RASTERISER's ppem, rounded to a whole pixel: hinting is
	// defined on a whole-pixel grid (head flags bit 3). size is the em size as the
	// caller asked for it, and scale — size/unitsPerEm — is what every metric is
	// multiplied by. The two are kept apart so a caller working in a finer unit
	// than the pixel can have exact metrics and a hinted raster at once.
	sizePx int
	size   float64
	scale  float64
	cache  map[GlyphIndex]cachedGlyph
	// phased holds glyphs rasterised at a sub-pixel phase (see GlyphMaskIndexAt).
	phased map[phasedKey]cachedGlyph
	// outlines holds each glyph's flattened outline, decoded once (see flat).
	outlines map[GlyphIndex]flatGlyph

	// varCoords, when non-nil, instances a variable font's glyf outlines at the
	// given user-space axis coordinates before rasterising (see SetVariation).
	// varNorm caches those coordinates normalized (NormalizeCoords) so the
	// metric-variation tables (HVAR/VVAR/MVAR) can be evaluated per call.
	varCoords map[string]float64
	varNorm   []int16

	// hinting enables the TrueType instruction interpreter for glyf glyphs that
	// carry instructions (see SetHinting). interp is built lazily on first use
	// and reused across glyphs; interpReady guards that one-time build.
	hinting     bool
	interp      *interp
	interpReady bool

	// stemDarken enables CFF/CFF2 stem darkening (weight compensation) when
	// hinting is on: stems are embiggened by a small ppem-dependent amount to
	// improve contrast on the anti-aliased rasteriser at small sizes (see
	// SetStemDarkening). Off by default, so the default hinted output is
	// byte-identical to the undarkened result.
	stemDarken bool
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
//
// For a size that is not a whole number of pixels, use [Font.NewFaceAt].
func (f *Font) NewFace(sizePx int) *Face { return f.NewFaceAt(float64(sizePx)) }

// NewFaceAt returns a Face that renders f at size pixels per em, where size may
// be FRACTIONAL. The metric scale is size / unitsPerEm exactly.
//
// The OpenType specification allows this. head.flags bit 3 reads "Force ppem to
// integer values for all internal scaler math; may use fractional ppem sizes if
// this bit is clear. It is strongly recommended that this be set in hinted fonts."
// — so the integer constraint belongs to the SCALER, not to the metrics, and this
// Face keeps the two apart: fc.scale is exact, while the whole-pixel ppem the
// TrueType interpreter and CFF stem darkening need is the rounded value.
//
// ⛔⛔ WHY THIS EXISTS. A caller that works in a unit finer than the pixel cannot
// express its sizes through an int. LaTeX's size ladder is a table of fractional
// points — \Large is 14.4pt, \LARGE 17.28, \huge 20.74 — and go-tex, which
// measures in scaled points (1/65536 pt), was obliged to round every step to a
// whole pixel before asking for a face: \Large came out at 14, 2.8% narrow, on
// every section heading of every document (go-opentype/opentype#61).
//
// Such a caller already reads metrics in FONT UNITS and scales them itself —
// AdvanceIndexUnits, KernUnits — precisely to avoid this library's own rounding.
// That arithmetic was exact in every term but one: Scale() itself was anchored on
// an integer. This is the term.
func (f *Font) NewFaceAt(size float64) *Face {
	return &Face{
		font: f,
		// The rasteriser's ppem. Hinting is defined on a whole-pixel grid (head
		// flags bit 3), and the interpreter is handed this, not the exact size.
		sizePx: roundInt(size),
		size:   size,
		scale:  size / float64(f.unitsPerEm),
		cache:  map[GlyphIndex]cachedGlyph{},
	}
}

// Size returns the face's em size in pixels, as it was asked for — fractional
// when the face was built with [Font.NewFaceAt]. [Face.Scale] is this divided by
// the font's unitsPerEm.
func (fc *Face) Size() float64 { return fc.size }

// Font returns the Font this Face renders. It lets a shaper reach the font's
// layout tables ([Font.GSUB], [Font.GPOS]) and per-glyph metrics
// ([Font.GlyphAdvance]) while keeping the Face for pixel sizing.
func (fc *Face) Font() *Font { return fc.font }

// Scale returns the face's font-unit-to-pixel scale factor (the render size in
// pixels divided by the font's unitsPerEm). A shaper multiplies a font-unit
// advance or offset by it to obtain whole pixels at the face's size.
func (fc *Face) Scale() float64 { return fc.scale }

// Metrics returns the face's vertical metrics in pixels. When a variation is
// set and the font carries an MVAR table, the global metric deltas for the
// ascender ("hasc"), descender ("hdsc") and line gap ("hlgp") are folded in.
func (fc *Face) Metrics() Metrics {
	s := fc.scale
	asc := float64(fc.font.ascender)
	desc := float64(fc.font.descender)
	gap := float64(fc.font.lineGap)
	if fc.varCoords != nil && fc.font.mvar != nil {
		asc += fc.font.mvar.delta("hasc", fc.varNorm)
		desc += fc.font.mvar.delta("hdsc", fc.varNorm)
		gap += fc.font.mvar.delta("hlgp", fc.varNorm)
	}
	return Metrics{
		Ascent:  roundInt(asc * s),
		Descent: roundInt(-desc * s),
		Height:  roundInt((asc - desc + gap) * s),
	}
}

// hAdvanceUnits returns glyph gid's horizontal advance in font units at the
// face's current variation: the base advance plus, when a variation is set, the
// HVAR delta (or the gvar phantom-point fallback). With no variation set the
// result is exactly the base advance.
func (fc *Face) hAdvanceUnits(gid GlyphIndex) float64 {
	adv := float64(fc.font.GlyphAdvance(gid))
	if fc.varCoords != nil {
		adv += fc.font.horizontalAdvanceDelta(int(gid), fc.varNorm)
	}
	return adv
}

// Advance returns the horizontal advance of r in WHOLE pixels, or 0 if the rune
// is not mapped by the font's cmap. When a variation is set the HVAR delta (or
// the gvar phantom-point fallback) is included.
//
// ⛔ IT ROUNDS, and at a text size that is coarse: a consumer laying out text
// wants AdvanceUnits. On STIXTwoMath at 10pt — where one pixel is one point —
// the space advance is 2.35 and this returns 2. A typesetter reading it puts
// every interword space 0.35pt narrow, which is ~3.9pt per 66-character line.
// go-tex did exactly that until it was measured.
func (fc *Face) Advance(r rune) int {
	gid, ok := fc.font.GlyphIndex(r)
	if !ok {
		return 0
	}
	return roundInt(fc.hAdvanceUnits(gid) * fc.scale)
}

// AdvanceUnits returns the horizontal advance of r in pixels WITHOUT rounding,
// or 0 if the rune is not mapped by the font's cmap.
//
// It is Advance's exact counterpart, as AdvanceIndexUnits is AdvanceIndex's.
// A layout engine that accumulates advances must use this one: rounding each
// glyph to a whole pixel accumulates along the line, and the error does not
// cancel — it is a bias toward whatever side the font's widths happen to fall.
func (fc *Face) AdvanceUnits(r rune) float64 {
	gid, ok := fc.font.GlyphIndex(r)
	if !ok {
		return 0
	}
	return fc.hAdvanceUnits(gid) * fc.scale
}

// ⛔ Measure sums ROUNDED advances, so its error compounds with the length of s
// rather than staying under half a pixel: it is the sum of each rune's Advance,
// not the rounded sum of the exact advances. For a measurement a layout decision
// depends on, accumulate AdvanceUnits and round once at the end.
//
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
	if len(fc.cache) >= maxCachedGlyphs {
		fc.cache = map[GlyphIndex]cachedGlyph{}
	}
	fc.cache[gid] = cg
	return cg
}

// phasedKey names one glyph at one horizontal sub-pixel phase.
type phasedKey struct {
	gid   GlyphIndex
	phase int
}

// SubpixelPhases is the number of horizontal positions within a pixel that
// [Face.GlyphMaskIndexAt] tells apart: a glyph origin is rounded to the nearest
// 1/SubpixelPhases of a pixel, an error of at most 1/(2*SubpixelPhases) px.
const SubpixelPhases = 4

// GlyphMaskIndexAt is [Face.GlyphMaskIndex] with a FRACTIONAL pen position: the
// glyph is rasterised with its origin at the nearest of [SubpixelPhases]
// horizontal positions in the pixel, so a caller that keeps its pen in a unit
// finer than the pixel (the exact advances [Face.AdvanceIndexUnits] times
// [Face.Scale]) lays a line out as a browser does, instead of rounding every
// advance to a whole pixel. Combined with [Font.NewFaceAt] the outlines are drawn
// at the exact size as well.
//
// x is the pen origin on the baseline in pixels (fractional), y the baseline row.
// bounds is the destination rectangle of mask, mask a coverage mask, advance the
// glyph's own horizontal advance in pixels, UNROUNDED. ok is false when gid is
// out of range or its outline is corrupt. A valid-but-empty glyph (a space)
// returns ok true with a nil mask and its advance. Masks are cached per glyph
// and phase and must not be modified.
func (fc *Face) GlyphMaskIndexAt(gid GlyphIndex, x float64, y int) (bounds image.Rectangle, mask *image.Alpha, advance float64, ok bool) {
	ix := int(math.Floor(x))
	phase := int((x-float64(ix))*SubpixelPhases + 0.5)
	if phase == SubpixelPhases {
		ix++
		phase = 0
	}
	key := phasedKey{gid, phase}
	cg, hit := fc.phased[key]
	if !hit {
		cg = fc.renderAt(gid, float64(phase)/SubpixelPhases)
		if fc.phased == nil || len(fc.phased) >= maxPhasedGlyphs {
			fc.phased = map[phasedKey]cachedGlyph{}
		}
		fc.phased[key] = cg
	}
	if !cg.ok {
		return image.Rectangle{}, nil, 0, false
	}
	return cg.bounds.Add(image.Point{X: ix, Y: y}), cg.mask, fc.hAdvanceUnits(gid) * fc.scale, true
}

// render decodes and rasterises glyph gid at the face's size.
func (fc *Face) render(gid GlyphIndex) cachedGlyph { return fc.renderAt(gid, 0) }

// Bounds on the per-face caches, in entries: a font may hold 65535 glyphs and a
// page may ask for any of them, so a cache that only grows is a memory bomb. A
// cache that reaches its bound is emptied (a page's working set is small and
// refills at once; an adversarial one only costs time, never memory).
var (
	maxCachedGlyphs = 4096                             // masks at phase 0, and flattened outlines
	maxPhasedGlyphs = maxCachedGlyphs * SubpixelPhases // masks at any phase (variables so a test can lower them)
)

// flatGlyph is one glyph's outline, decoded and flattened to polygons once.
type flatGlyph struct {
	polys [][]fpoint
	ok    bool
}

// flat returns glyph gid's flattened outline at the face's size, decoding it on
// first use: decoding (the glyf program, gvar deltas) and flattening are most of
// what rasterising a glyph costs, and every sub-pixel phase of it needs the same
// polygons, only shifted. Reset with the face's other caches when the face
// changes (SetVariation, SetHinting, SetStemDarkening).
func (fc *Face) flat(gid GlyphIndex) flatGlyph {
	if g, ok := fc.outlines[gid]; ok {
		return g
	}
	g := flatGlyph{}
	if contours, err := fc.outline(gid); err == nil {
		g.ok = true
		g.polys = make([][]fpoint, 0, len(contours))
		for _, c := range contours {
			if p := flattenContour(c, fc.scale); len(p) >= 3 {
				g.polys = append(g.polys, p)
			}
		}
	}
	if fc.outlines == nil || len(fc.outlines) >= maxCachedGlyphs {
		fc.outlines = make(map[GlyphIndex]flatGlyph)
	}
	fc.outlines[gid] = g
	return g
}

// renderAt rasterises glyph gid with its outline shifted right by dx pixels.
func (fc *Face) renderAt(gid GlyphIndex, dx float64) cachedGlyph {
	g := fc.flat(gid)
	if !g.ok {
		return cachedGlyph{ok: false}
	}
	bounds, mask := rasterizeAt(g.polys, dx)
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
	advance = roundInt(fc.hAdvanceUnits(gid) * fc.scale)
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
	advance = roundInt(fc.hAdvanceUnits(gid) * fc.scale)
	bounds = cg.bounds.Add(image.Point{X: x, Y: y})
	return bounds, cg.mask, image.Point{}, advance, true
}

// outline returns glyph gid's outline as contours in font units, selecting the
// outline source (CFF2 vs CFF vs glyf), applying variable-font instancing when a
// variation is set, and applying hinting when it is enabled.
//
// A CFF2 font is inherently variable: its outlines are instanced at the face's
// variation coordinates (the default master when none is set), converted to the
// normalized F2Dot14 form cff2.outline expects via NormalizeCoords. When hinting
// is enabled both CFF2 and CFF1 outlines are grid-fitted (see cffhint.go), so
// SetHinting affects all three outline flavours; with hinting off they are
// returned unhinted.
func (fc *Face) outline(gid GlyphIndex) ([]contour, error) {
	if fc.font.t1 != nil {
		// A Type 1 program carries hints of its own, in a form nothing here
		// grid-fits; its outlines are drawn as they were written.
		return fc.font.t1.outline(int(gid))
	}
	if fc.font.cff2 != nil {
		contours, gh, err := fc.font.cff2.outlineHints(int(gid), fc.font.NormalizeCoords(fc.varCoords))
		if err != nil {
			return nil, err
		}
		if fc.hinting {
			contours = cffGridFit(contours, gh, fc.scale, float64(fc.sizePx), fc.stemDarken)
		}
		return contours, nil
	}
	if fc.font.cff != nil {
		contours, gh, err := fc.font.cff.outlineHints(int(gid))
		if err != nil {
			return nil, err
		}
		if fc.hinting {
			contours = cffGridFit(contours, gh, fc.scale, float64(fc.sizePx), fc.stemDarken)
		}
		return contours, nil
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
	fc.varNorm = fc.font.NormalizeCoords(coords)
	fc.cache = map[GlyphIndex]cachedGlyph{}
	fc.phased = nil
	fc.outlines = nil
}

// SetHinting enables or disables the TrueType instruction interpreter. When
// enabled, a glyf glyph that carries instructions is grid-fitted at the face's
// pixel size before rasterising; glyphs without instructions, composite glyphs
// and CFF fonts are unaffected. Hinting is off by default (the default output is
// unhinted anti-aliased coverage). The glyph cache is invalidated.
func (fc *Face) SetHinting(on bool) {
	fc.hinting = on
	fc.cache = map[GlyphIndex]cachedGlyph{}
	fc.phased = nil
	fc.outlines = nil
}

// SetStemDarkening enables or disables stem darkening (weight compensation) for
// CFF/CFF2 glyphs. When both hinting and darkening are on, every grid-fitted stem
// is embiggened by a small ppem-dependent amount (strongest at small sizes,
// fading to none at larger sizes) so stems keep enough contrast on the
// anti-aliased rasteriser at small text sizes, as FreeType's CFF engine does by
// default. It has no effect while hinting is off or on glyf (TrueType) outlines.
// Darkening is off by default, so the default hinted output is byte-identical to
// the undarkened result. The glyph cache is invalidated.
func (fc *Face) SetStemDarkening(on bool) {
	fc.stemDarken = on
	fc.cache = map[GlyphIndex]cachedGlyph{}
	fc.phased = nil
	fc.outlines = nil
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
