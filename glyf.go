// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"fmt"
)

// outlinePoint is one point of a glyph contour in font units. on reports
// whether it lies on the curve (true) or is an off-curve quadratic control
// point (false).
type outlinePoint struct {
	x, y float64
	on   bool
}

// contour is one closed sequence of outline points.
type contour []outlinePoint

// Simple-glyph flag bits (TrueType spec).
const (
	flagOnCurve   = 0x01
	flagXShort    = 0x02
	flagYShort    = 0x04
	flagRepeat    = 0x08
	flagXSamePosX = 0x10 // X is same (long form) or positive (short form)
	flagYSamePosY = 0x20 // Y is same (long form) or positive (short form)
)

// Composite-glyph flag bits (TrueType spec).
const (
	compArgsAreWords   = 0x0001
	compArgsAreXY      = 0x0002
	compWeHaveScale    = 0x0008
	compMoreComponents = 0x0020
	compXAndYScale     = 0x0040
	compTwoByTwo       = 0x0080
)

// maxCompositeDepth bounds composite-glyph recursion; anything deeper is
// rejected as malformed (in addition to explicit cycle detection).
const maxCompositeDepth = 8

// glyphContours returns the outline of glyph gid, resolving composites, with
// a fresh recursion-tracking state. It returns the default-master outline; a
// nil norm disables variation (see loadGlyph).
func (f *Font) glyphContours(gid GlyphIndex) ([]contour, error) {
	return f.loadGlyph(gid, 0, map[GlyphIndex]bool{}, nil)
}

// glyphInstructions returns the TrueType instruction bytecode attached to a
// simple glyph gid, or nil for an empty glyph, a composite glyph, or a glyph
// with no instructions. It is only called after glyphContours has successfully
// decoded gid, so the loca range and the simple-glyph header are known valid.
func (f *Font) glyphInstructions(gid GlyphIndex) []byte {
	start, end := f.loca[gid], f.loca[gid+1]
	if start == end {
		return nil // empty glyph (e.g. space)
	}
	r := reader{b: f.glyf[start:end]}
	n := int(r.i16())
	if n < 0 {
		return nil // composite glyph: not hinted here
	}
	r.skip(8) // xMin, yMin, xMax, yMax
	for i := 0; i < n; i++ {
		r.u16() // endPtsOfContours[i]
	}
	instrLen := int(r.u16())
	if instrLen == 0 {
		return nil
	}
	return r.b[r.pos : r.pos+instrLen]
}

// loadGlyph decodes glyph gid's contours in font units. depth and visited
// guard composite recursion against over-deep nesting and cycles. norm, when
// non-nil, is the normalized variation coordinate at which the glyph is
// instanced: simple-glyph points get their 'gvar' deltas (with IUP) and
// composite-component offsets get theirs (see gvar.go). A nil norm yields the
// unvaried default-master outline.
func (f *Font) loadGlyph(gid GlyphIndex, depth int, visited map[GlyphIndex]bool, norm []int16) ([]contour, error) {
	if depth > maxCompositeDepth {
		return nil, errors.New("opentype: composite glyph nesting too deep")
	}
	if int(gid) >= f.numGlyphs {
		return nil, fmt.Errorf("opentype: glyph index %d out of range", int(gid))
	}
	if visited[gid] {
		return nil, fmt.Errorf("opentype: cyclic composite glyph %d", int(gid))
	}
	start, end := f.loca[gid], f.loca[gid+1]
	if start > end {
		return nil, fmt.Errorf("opentype: glyph %d: loca not monotonic", int(gid))
	}
	if int(end) > len(f.glyf) {
		return nil, fmt.Errorf("opentype: glyph %d: %w", int(gid), errTruncated)
	}
	if start == end {
		return nil, nil // empty glyph (e.g. space)
	}
	visited[gid] = true
	defer delete(visited, gid)

	r := reader{b: f.glyf[start:end]}
	numberOfContours := int(r.i16())
	r.skip(8) // xMin, yMin, xMax, yMax
	if numberOfContours >= 0 {
		contours, err := f.simpleGlyph(&r, numberOfContours)
		if err != nil {
			return nil, err
		}
		return f.varySimple(int(gid), contours, norm), nil
	}
	if depth == 0 {
		if err := f.checkGlyphBudget(gid); err != nil {
			return nil, err
		}
	}
	return f.compositeGlyph(int(gid), &r, depth, visited, norm)
}

// maxGlyphPoints is the per-glyph budget of a composite: the number of points
// it expands to, every reference counted, each contour counting at least one
// point. TrueType itself cannot name more than 65535 points in one glyph (a
// simple glyph's endPtsOfContours entries are uint16), so a composite that
// needs more is not one the format can express. Measured on every TrueType
// font of this machine plus the repository's testdata (209 TrueType fonts),
// the largest expansion is 4103 points (Kokonor, glyph 214), 16 times under
// it; the largest maxp maxCompositePoints declared is 3886. maxp is not used
// as the bound: a font states it and an attacker states it too, and FreeType
// repairs the sibling field maxComponentDepth when a font misdeclares it
// (src/truetype/ttgload.c, load_truetype_glyph). FreeType's own composite
// guards there are a recursion limit of 100 and a cycle check; it has no
// point-count bound, so this is the stricter one the issue asks for. A reference counts however many times it is made, which
// is what bounds the k^8 expansion of a composite that names the glyph below
// it k times: the memoized count is computed before any contour is built.
const maxGlyphPoints = 65535

// errGlyphBudget is wrapped into the error of a glyph that exceeds
// maxGlyphPoints.
var errGlyphBudget = errors.New("glyph exceeds its per-glyph point budget")

// checkGlyphBudget counts, without building anything, the points glyph root
// will expand to and refuses it past maxGlyphPoints. It only judges the budget:
// a structural fault (bad loca, cycle, nesting, truncation) costs zero here and
// is reported by the expansion that follows, with its own message.
func (f *Font) checkGlyphBudget(root GlyphIndex) error {
	_, err := f.glyphCost(root, root, 0, map[GlyphIndex]bool{}, map[GlyphIndex]int{})
	return err
}

// glyphCost is the point count of glyph gid, memoized per glyph ID so that a
// glyph referenced many times is counted once.
func (f *Font) glyphCost(root, gid GlyphIndex, depth int, visited map[GlyphIndex]bool, memo map[GlyphIndex]int) (int, error) {
	if c, ok := memo[gid]; ok {
		return c, nil
	}
	if depth > maxCompositeDepth || int(gid) >= f.numGlyphs || visited[gid] {
		return 0, nil
	}
	start, end := f.loca[gid], f.loca[gid+1]
	if start >= end || int(end) > len(f.glyf) {
		return 0, nil
	}
	r := reader{b: f.glyf[start:end]}
	n := int(r.i16())
	r.skip(8) // xMin, yMin, xMax, yMax
	cost := 0
	if n >= 0 {
		for i := 0; i < n; i++ {
			cost = int(r.u16()) + 1 // the last endPtsOfContours entry wins
		}
		cost = max(cost, n)
	} else {
		comps, err := parseComponents(&r)
		if err != nil {
			return 0, nil
		}
		visited[gid] = true
		for _, comp := range comps {
			sub, err := f.glyphCost(root, comp.gid, depth+1, visited, memo)
			if err != nil {
				return 0, err
			}
			cost += sub
		}
		delete(visited, gid)
	}
	if r.err != nil {
		return 0, nil
	}
	if cost > maxGlyphPoints {
		return 0, fmt.Errorf("opentype: glyph %d: %w: more than %d points", int(root), errGlyphBudget, maxGlyphPoints)
	}
	memo[gid] = cost
	return cost, nil
}

// simpleGlyph decodes a simple (non-composite) glyph's n contours.
func (f *Font) simpleGlyph(r *reader, n int) ([]contour, error) {
	endPts := make([]uint16, n)
	for i := range endPts {
		endPts[i] = r.u16()
		// The format requires endPtsOfContours to be non-decreasing; one that is
		// not would index past the points of the last contour (found by
		// FuzzGlyphContours).
		if i > 0 && endPts[i] < endPts[i-1] {
			return nil, errors.New("opentype: simple glyph: endPtsOfContours decreases")
		}
	}
	numPoints := 0
	if n > 0 {
		numPoints = int(endPts[n-1]) + 1
	}
	instrLen := int(r.u16())
	r.skip(instrLen)

	flags := make([]uint8, numPoints)
	for i := 0; i < numPoints; {
		fl := r.u8()
		flags[i] = fl
		i++
		if fl&flagRepeat != 0 {
			rep := int(r.u8())
			for j := 0; j < rep && i < numPoints; j++ {
				flags[i] = fl
				i++
			}
		}
	}

	xs := readCoords(r, flags, numPoints, flagXShort, flagXSamePosX)
	ys := readCoords(r, flags, numPoints, flagYShort, flagYSamePosY)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: simple glyph: %w", r.err)
	}

	contours := make([]contour, n)
	p := 0
	for ci := 0; ci < n; ci++ {
		endp := int(endPts[ci])
		var cnt contour
		for ; p <= endp; p++ {
			cnt = append(cnt, outlinePoint{
				x:  float64(xs[p]),
				y:  float64(ys[p]),
				on: flags[p]&flagOnCurve != 0,
			})
		}
		contours[ci] = cnt
	}
	return contours, nil
}

// readCoords decodes one axis (x or y) of point coordinates, accumulating the
// signed deltas per the short/same flag encoding.
func readCoords(r *reader, flags []uint8, numPoints int, shortBit, samePosBit uint8) []int {
	coords := make([]int, numPoints)
	v := 0
	for i := 0; i < numPoints; i++ {
		fl := flags[i]
		if fl&shortBit != 0 {
			d := int(r.u8())
			if fl&samePosBit == 0 {
				d = -d
			}
			v += d
		} else if fl&samePosBit == 0 {
			v += int(r.i16())
		}
		coords[i] = v
	}
	return coords
}

// glyphComponent is one decoded component record of a composite glyph: the
// referenced glyph, its placement offset (arg1/arg2, when argsAreXY) and its
// 2x2 transform (a,b,c,d).
type glyphComponent struct {
	flags      uint16
	gid        GlyphIndex
	arg1, arg2 int
	a, b, c, d float64
	argsAreXY  bool
}

// parseComponents decodes a composite glyph's component records; r must be
// positioned just past the glyph header. Components are returned in order.
func parseComponents(r *reader) ([]glyphComponent, error) {
	var comps []glyphComponent
	for {
		var comp glyphComponent
		comp.flags = r.u16()
		comp.gid = GlyphIndex(r.u16())
		if comp.flags&compArgsAreWords != 0 {
			comp.arg1 = int(r.i16())
			comp.arg2 = int(r.i16())
		} else {
			comp.arg1 = int(int8(r.u8()))
			comp.arg2 = int(int8(r.u8()))
		}
		comp.argsAreXY = comp.flags&compArgsAreXY != 0
		comp.a, comp.b, comp.c, comp.d = 1.0, 0.0, 0.0, 1.0
		switch {
		case comp.flags&compWeHaveScale != 0:
			comp.a = r.f2dot14()
			comp.d = comp.a
		case comp.flags&compXAndYScale != 0:
			comp.a = r.f2dot14()
			comp.d = r.f2dot14()
		case comp.flags&compTwoByTwo != 0:
			comp.a = r.f2dot14()
			comp.b = r.f2dot14()
			comp.c = r.f2dot14()
			comp.d = r.f2dot14()
		}
		if r.err != nil {
			return nil, fmt.Errorf("opentype: composite glyph: %w", r.err)
		}
		comps = append(comps, comp)
		if comp.flags&compMoreComponents == 0 {
			return comps, nil
		}
	}
}

// compositeGlyph assembles a composite glyph from its components, recursively
// decoding and transforming each referenced glyph. When norm is non-nil the
// component offsets are first varied by 'gvar' (varyComponentOffsets) and each
// component is instanced recursively at the same coordinate.
func (f *Font) compositeGlyph(gid int, r *reader, depth int, visited map[GlyphIndex]bool, norm []int16) ([]contour, error) {
	comps, err := parseComponents(r)
	if err != nil {
		return nil, err
	}
	dxs := make([]float64, len(comps))
	dys := make([]float64, len(comps))
	for i := range comps {
		dxs[i] = float64(comps[i].arg1)
		dys[i] = float64(comps[i].arg2)
	}
	f.varyComponentOffsets(gid, comps, dxs, dys, norm)

	var out []contour
	for i := range comps {
		comp := comps[i]
		if !comp.argsAreXY {
			return nil, errors.New("opentype: unsupported: composite point matching")
		}
		sub, err := f.loadGlyph(comp.gid, depth+1, visited, norm)
		if err != nil {
			return nil, err
		}
		out = append(out, transformContours(sub, comp.a, comp.b, comp.c, comp.d, dxs[i], dys[i])...)
	}
	return out, nil
}

// transformContours applies the affine transform [a b c d] with translation
// (dx, dy) to every point of cs, returning fresh contours.
func transformContours(cs []contour, a, b, c, d, dx, dy float64) []contour {
	out := make([]contour, len(cs))
	for ci, cont := range cs {
		nc := make(contour, len(cont))
		for i, pt := range cont {
			nc[i] = outlinePoint{
				x:  a*pt.x + c*pt.y + dx,
				y:  b*pt.x + d*pt.y + dy,
				on: pt.on,
			}
		}
		out[ci] = nc
	}
	return out
}
