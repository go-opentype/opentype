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
// a fresh recursion-tracking state.
func (f *Font) glyphContours(gid GlyphIndex) ([]contour, error) {
	return f.loadGlyph(gid, 0, map[GlyphIndex]bool{})
}

// loadGlyph decodes glyph gid's contours in font units. depth and visited
// guard composite recursion against over-deep nesting and cycles.
func (f *Font) loadGlyph(gid GlyphIndex, depth int, visited map[GlyphIndex]bool) ([]contour, error) {
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
		return f.simpleGlyph(&r, numberOfContours)
	}
	return f.compositeGlyph(&r, depth, visited)
}

// simpleGlyph decodes a simple (non-composite) glyph's n contours.
func (f *Font) simpleGlyph(r *reader, n int) ([]contour, error) {
	endPts := make([]uint16, n)
	for i := range endPts {
		endPts[i] = r.u16()
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

// compositeGlyph assembles a composite glyph from its components, recursively
// decoding and transforming each referenced glyph.
func (f *Font) compositeGlyph(r *reader, depth int, visited map[GlyphIndex]bool) ([]contour, error) {
	var out []contour
	for {
		flags := r.u16()
		compGID := r.u16()
		var arg1, arg2 int
		if flags&compArgsAreWords != 0 {
			arg1 = int(r.i16())
			arg2 = int(r.i16())
		} else {
			arg1 = int(int8(r.u8()))
			arg2 = int(int8(r.u8()))
		}
		if flags&compArgsAreXY == 0 {
			return nil, errors.New("opentype: unsupported: composite point matching")
		}
		a, b, c, d := 1.0, 0.0, 0.0, 1.0
		switch {
		case flags&compWeHaveScale != 0:
			a = r.f2dot14()
			d = a
		case flags&compXAndYScale != 0:
			a = r.f2dot14()
			d = r.f2dot14()
		case flags&compTwoByTwo != 0:
			a = r.f2dot14()
			b = r.f2dot14()
			c = r.f2dot14()
			d = r.f2dot14()
		}
		if r.err != nil {
			return nil, fmt.Errorf("opentype: composite glyph: %w", r.err)
		}
		dx, dy := float64(arg1), float64(arg2)
		sub, err := f.loadGlyph(GlyphIndex(compGID), depth+1, visited)
		if err != nil {
			return nil, err
		}
		for _, cont := range sub {
			nc := make(contour, len(cont))
			for i, pt := range cont {
				nc[i] = outlinePoint{
					x:  a*pt.x + c*pt.y + dx,
					y:  b*pt.x + d*pt.y + dy,
					on: pt.on,
				}
			}
			out = append(out, nc)
		}
		if flags&compMoreComponents == 0 {
			break
		}
	}
	return out, nil
}
