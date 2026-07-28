// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file implements the optional 'avar' (Axis Variations) table. It refines
// the linear normalization performed by NormalizeCoords: for each axis it maps
// the normalized coordinate through a piecewise-linear segment map, letting a
// font make, say, the visual midpoint of a weight axis land somewhere other
// than the arithmetic midpoint. A font without 'avar' normalizes linearly.

// avarMap is one (from, to) control point of an axis segment map, both in the
// normalized F2Dot14 domain decoded to float64.
type avarMap struct {
	from, to float64
}

// avarTable holds one segment map per axis, in axis order.
type avarTable struct {
	segments [][]avarMap
}

// parseAvar decodes the 'avar' table: a header followed by one SegmentMaps
// record per axis, each a count and that many (fromCoordinate, toCoordinate)
// F2Dot14 pairs.
func (f *Font) parseAvar(b []byte) error {
	r := reader{b: b}
	r.u16() // majorVersion
	r.u16() // minorVersion
	r.u16() // reserved
	axisCount := int(r.u16())
	segments := make([][]avarMap, axisCount)
	for i := range segments {
		count := int(r.u16())
		maps := make([]avarMap, count)
		for j := range maps {
			maps[j].from = r.f2dot14()
			maps[j].to = r.f2dot14()
		}
		segments[i] = maps
	}
	if r.err != nil {
		return fmt.Errorf("opentype: avar table: %w", r.err)
	}
	f.avar = &avarTable{segments: segments}
	return nil
}

// apply maps a normalized coordinate v for the given axis through its segment
// map. Control points are assumed sorted by ascending fromCoordinate (as the
// specification requires). An axis with fewer than two control points, or an
// index past the table, is treated as identity.
func (a *avarTable) apply(axis int, v float64) float64 {
	if axis >= len(a.segments) {
		return v
	}
	seg := a.segments[axis]
	if len(seg) < 2 {
		return v
	}
	if v <= seg[0].from {
		return seg[0].to
	}
	for i := 1; i < len(seg); i++ {
		if v < seg[i].from {
			lo, hi := seg[i-1], seg[i]
			return lo.to + (hi.to-lo.to)*(v-lo.from)/(hi.from-lo.from)
		}
		if v == seg[i].from {
			return seg[i].to
		}
	}
	return seg[len(seg)-1].to
}
