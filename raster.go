// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"image"
	"image/color"
	"math"
)

// fpoint is a point in device (pixel) space.
type fpoint struct{ x, y float64 }

// supersample is the per-axis oversampling factor; the rasteriser takes
// ss*ss coverage samples per output pixel for anti-aliasing.
const supersample = 4

// flattenContour scales a font-unit contour to device space (flipping the
// y axis so font "up" becomes image "down") and flattens its quadratic
// segments into a closed polygon. It returns nil for a degenerate contour of
// fewer than two points.
func flattenContour(c contour, scale float64) []fpoint {
	n := len(c)
	if n < 2 {
		return nil
	}
	type dp struct {
		x, y float64
		on   bool
	}
	pts := make([]dp, n)
	for i, p := range c {
		pts[i] = dp{x: p.x * scale, y: -p.y * scale, on: p.on}
	}

	// Insert an implied on-curve midpoint between any two consecutive
	// off-curve points (walking the contour cyclically).
	q := make([]dp, 0, n*2)
	for i := 0; i < n; i++ {
		a := pts[i]
		q = append(q, a)
		b := pts[(i+1)%n]
		if !a.on && !b.on {
			q = append(q, dp{x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, on: true})
		}
	}

	// Rotate so the walk starts on an on-curve point (one is guaranteed to
	// exist for n >= 2 after midpoint insertion).
	s := 0
	for !q[s].on {
		s++
	}
	ordered := append(append([]dp{}, q[s:]...), q[:s]...)
	m := len(ordered)

	poly := []fpoint{{x: ordered[0].x, y: ordered[0].y}}
	i := 0
	for i < m {
		next := ordered[(i+1)%m]
		if next.on {
			poly = append(poly, fpoint{x: next.x, y: next.y})
			i++
			continue
		}
		cur := ordered[i]
		end := ordered[(i+2)%m]
		flattenQuad(&poly, cur.x, cur.y, next.x, next.y, end.x, end.y)
		i += 2
	}
	return poly
}

// flattenQuad appends a flattened quadratic Bézier (from the start point,
// already the last element of *poly, through control point (cx,cy) to end
// point (x1,y1)) using a step count adapted to the curve's device-space size.
func flattenQuad(poly *[]fpoint, x0, y0, cx, cy, x1, y1 float64) {
	d := math.Hypot(cx-x0, cy-y0) + math.Hypot(x1-cx, y1-cy)
	steps := int(d / 3.0)
	if steps < 1 {
		steps = 1
	}
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		mt := 1 - t
		x := mt*mt*x0 + 2*mt*t*cx + t*t*x1
		y := mt*mt*y0 + 2*mt*t*cy + t*t*y1
		*poly = append(*poly, fpoint{x: x, y: y})
	}
}

// isLeft returns a positive value if point (x,y) is left of the directed edge
// a→b, negative if right, and zero if collinear.
func isLeft(a, b fpoint, x, y float64) float64 {
	return (b.x-a.x)*(y-a.y) - (x-a.x)*(b.y-a.y)
}

// insideNonZero reports whether the point (x,y) is inside the polygon set
// under the non-zero winding rule.
func insideNonZero(polys [][]fpoint, x, y float64) bool {
	wn := 0
	for _, poly := range polys {
		n := len(poly)
		for i := 0; i < n; i++ {
			a := poly[i]
			b := poly[(i+1)%n]
			if a.y <= y {
				if b.y > y && isLeft(a, b, x, y) > 0 {
					wn++
				}
			} else if b.y <= y && isLeft(a, b, x, y) < 0 {
				wn--
			}
		}
	}
	return wn != 0
}

// rasterize scan-converts the device-space polygons into an 8-bit alpha
// coverage mask using supersampled non-zero winding. It returns the mask's
// device-space bounds (relative to the glyph origin) and an *image.Alpha whose
// own rectangle starts at (0,0). An empty polygon set yields an empty rect and
// a nil mask.
func rasterize(polys [][]fpoint) (image.Rectangle, *image.Alpha) {
	if len(polys) == 0 {
		return image.Rectangle{}, nil
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, poly := range polys {
		for _, p := range poly {
			if p.x < minX {
				minX = p.x
			}
			if p.x > maxX {
				maxX = p.x
			}
			if p.y < minY {
				minY = p.y
			}
			if p.y > maxY {
				maxY = p.y
			}
		}
	}
	x0 := int(math.Floor(minX))
	y0 := int(math.Floor(minY))
	x1 := int(math.Ceil(maxX))
	y1 := int(math.Ceil(maxY))
	w, h := x1-x0, y1-y0

	alpha := image.NewAlpha(image.Rect(0, 0, w, h))
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			count := 0
			for sy := 0; sy < supersample; sy++ {
				for sx := 0; sx < supersample; sx++ {
					fx := float64(x0+px) + (float64(sx)+0.5)/supersample
					fy := float64(y0+py) + (float64(sy)+0.5)/supersample
					if insideNonZero(polys, fx, fy) {
						count++
					}
				}
			}
			alpha.SetAlpha(px, py, color.Alpha{A: uint8(count * 255 / (supersample * supersample))})
		}
	}
	return image.Rect(x0, y0, x1, y1), alpha
}
