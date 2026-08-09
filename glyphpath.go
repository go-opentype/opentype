// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"strconv"
	"strings"
)

// Point is a 2-D coordinate in a glyph outline.
type Point struct{ X, Y float64 }

// SegmentOp identifies a glyph-outline path command.
type SegmentOp uint8

const (
	// SegMoveTo starts a new contour at P[0].
	SegMoveTo SegmentOp = iota
	// SegLineTo draws a straight line to P[0].
	SegLineTo
	// SegQuadTo draws a quadratic Bézier with control point P[0] to endpoint P[1].
	SegQuadTo
	// SegClose closes the current contour; it carries no points.
	SegClose
)

// Segment is one command of a glyph outline. SegMoveTo and SegLineTo use P[0];
// SegQuadTo uses P[0] as the quadratic control point and P[1] as the on-curve
// endpoint; SegClose uses neither.
type Segment struct {
	Op SegmentOp
	P  [2]Point
}

// GlyphOutline returns glyph gid's outline as path segments in font design
// units, in the font's native Y-up orientation with the pen origin at (0, 0).
//
// TrueType off-curve points are emitted as [SegQuadTo] control points, with the
// implied on-curve midpoint synthesised between two consecutive off-curve
// points. CFF/CFF2 outlines are stored by this package pre-flattened to
// polylines, so they yield [SegLineTo] segments only.
//
// ok is false when gid is out of range or its outline is corrupt; a valid but
// empty glyph (for example a space) returns nil, true.
func (fc *Face) GlyphOutline(gid GlyphIndex) (segs []Segment, ok bool) {
	cs, err := fc.outline(gid)
	if err != nil {
		return nil, false
	}
	for _, c := range cs {
		segs = appendContour(segs, c)
	}
	return segs, true
}

// mid returns the midpoint of a and b.
func mid(a, b outlinePoint) Point {
	return Point{X: (a.x + b.x) / 2, Y: (a.y + b.y) / 2}
}

// appendContour converts one closed contour of on/off-curve points into path
// segments (a MoveTo, the body, and a Close), following the TrueType rule that
// consecutive off-curve points imply an on-curve midpoint between them.
func appendContour(dst []Segment, c contour) []Segment {
	n := len(c)
	if n == 0 {
		return dst
	}

	// Choose the starting on-curve point; an all-off-curve contour starts at the
	// synthesised midpoint of its last and first points.
	var start Point
	var body contour
	switch {
	case c[0].on:
		start, body = Point{c[0].x, c[0].y}, c[1:]
	case c[n-1].on:
		start, body = Point{c[n-1].x, c[n-1].y}, c[:n-1]
	default:
		start, body = mid(c[n-1], c[0]), c
	}
	dst = append(dst, Segment{Op: SegMoveTo, P: [2]Point{start}})

	var ctrl Point
	haveCtrl := false
	for _, p := range body {
		cur := Point{p.x, p.y}
		if p.on {
			if haveCtrl {
				dst = append(dst, Segment{Op: SegQuadTo, P: [2]Point{ctrl, cur}})
				haveCtrl = false
			} else {
				dst = append(dst, Segment{Op: SegLineTo, P: [2]Point{cur}})
			}
			continue
		}
		if haveCtrl {
			// Two consecutive off-curve points: emit the implied midpoint.
			m := Point{(ctrl.X + cur.X) / 2, (ctrl.Y + cur.Y) / 2}
			dst = append(dst, Segment{Op: SegQuadTo, P: [2]Point{ctrl, m}})
		}
		ctrl, haveCtrl = cur, true
	}
	if haveCtrl {
		dst = append(dst, Segment{Op: SegQuadTo, P: [2]Point{ctrl, start}})
	}
	return append(dst, Segment{Op: SegClose})
}

// GlyphSVGPath returns glyph gid's outline as an SVG path "d" string at the
// face's pixel size, in SVG's Y-down coordinate space with the pen origin at
// (0, 0) — so the baseline is y = 0 and ascenders have negative y. Callers place
// the glyph by wrapping the path in a translate transform.
//
// ok is false when gid is out of range or its outline is corrupt; a valid but
// empty glyph returns "", true.
func (fc *Face) GlyphSVGPath(gid GlyphIndex) (d string, ok bool) {
	segs, ok := fc.GlyphOutline(gid)
	if !ok {
		return "", false
	}
	return segmentsToSVG(segs, fc.scale), true
}

// segmentsToSVG renders outline segments (font units, Y-up) into an SVG path
// "d" string scaled by scale and flipped to SVG's Y-down space.
func segmentsToSVG(segs []Segment, scale float64) string {
	var b strings.Builder
	writePoint := func(p Point) {
		b.WriteString(ftoa(p.X * scale))
		b.WriteByte(' ')
		b.WriteString(ftoa(-p.Y * scale))
	}
	for _, s := range segs {
		switch s.Op {
		case SegMoveTo:
			b.WriteByte('M')
			writePoint(s.P[0])
		case SegLineTo:
			b.WriteByte('L')
			writePoint(s.P[0])
		case SegQuadTo:
			b.WriteByte('Q')
			writePoint(s.P[0])
			b.WriteByte(' ')
			writePoint(s.P[1])
		case SegClose:
			b.WriteByte('Z')
		}
	}
	return b.String()
}

// ftoa formats v with up to three decimals and no trailing zeros (so "12.000"
// becomes "12" and "1.250" becomes "1.25"), keeping SVG paths compact. The
// fixed-precision form always contains a decimal point, so trimming is safe.
func ftoa(v float64) string {
	if v == 0 { // collapse both +0 and the -0 produced by the Y flip
		return "0"
	}
	s := strings.TrimRight(strconv.FormatFloat(v, 'f', 3, 64), "0")
	return strings.TrimSuffix(s, ".")
}
