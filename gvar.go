// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file implements the optional 'gvar' (Glyph Variations) table for 'glyf'
// outlines: the per-glyph store of point deltas that move a glyph's outline
// from the default master to a requested position in the variation space.
//
// A glyph's variation is a set of "tuple variations". Each tuple has a peak
// (and optionally an intermediate region) in normalized axis space, a set of
// point numbers it touches and a delta (dx, dy) per touched point. At a given
// normalized coordinate every tuple gets a scalar (how strongly it applies);
// the scaled deltas of all tuples are summed. Deltas for points a tuple does
// not touch are recovered by IUP (Interpolation of Untouched Points), done per
// contour against the default-master coordinates.
//
// Deferred (not implemented here): variation of composite-glyph component
// offsets, and metric variation via HVAR/VVAR/MVAR. The four TrueType phantom
// points are carried through delta application (so "all points" and explicit
// point sets index correctly) but are dropped from the result.

// gvarTable is the parsed 'gvar' table.
type gvarTable struct {
	axisCount    int
	sharedTuples [][]float64 // sharedTupleCount peak tuples, each axisCount F2Dot14
	dataOffsets  []uint32    // glyphCount+1 offsets into the data array
	dataArrayOff int         // offset of the glyph-variation-data array within data
	data         []byte      // the whole 'gvar' table
}

// Tuple index / header flag bits (gvar TupleVariationHeader.tupleIndex).
const (
	gvEmbeddedPeak  = 0x8000 // an embedded peak tuple follows the header
	gvIntermediate  = 0x4000 // embedded intermediate-region tuples follow
	gvPrivatePoints = 0x2000 // this tuple has private point numbers
	gvTupleIndexMsk = 0x0FFF // shared-tuple index (when no embedded peak)
)

// gvSharedPointNumbers is the bit in GlyphVariationData.tupleVariationCount
// flagging a shared point-number set at the head of the serialized data.
const gvSharedPointNumbers = 0x8000

// parseGvar decodes the 'gvar' header, the per-glyph offset table and the
// shared-tuple array. Per-glyph variation data is decoded lazily in
// applyVariation, so a glyph is only touched when it is actually instanced.
func (f *Font) parseGvar(b []byte) error {
	r := reader{b: b}
	r.u16() // majorVersion
	r.u16() // minorVersion
	axisCount := int(r.u16())
	sharedTupleCount := int(r.u16())
	sharedTuplesOffset := int(r.u32())
	glyphCount := int(r.u16())
	flags := r.u16()
	dataArrayOffset := int(r.u32())
	if r.err != nil {
		return fmt.Errorf("opentype: gvar table: %w", r.err)
	}

	longOffsets := flags&1 != 0
	offsets := make([]uint32, glyphCount+1)
	for i := range offsets {
		if longOffsets {
			offsets[i] = r.u32()
		} else {
			offsets[i] = uint32(r.u16()) * 2
		}
	}
	if r.err != nil {
		return fmt.Errorf("opentype: gvar offsets: %w", r.err)
	}

	shared := make([][]float64, sharedTupleCount)
	sr := reader{b: b, pos: sharedTuplesOffset}
	for i := range shared {
		t := make([]float64, axisCount)
		for a := range t {
			t[a] = sr.f2dot14()
		}
		shared[i] = t
	}
	if sr.err != nil {
		return fmt.Errorf("opentype: gvar shared tuples: %w", sr.err)
	}

	f.gvar = &gvarTable{
		axisCount:    axisCount,
		sharedTuples: shared,
		dataOffsets:  offsets,
		dataArrayOff: dataArrayOffset,
		data:         b,
	}
	return nil
}

// applyVariation returns pts with glyph gid's 'gvar' deltas applied at the
// normalized coordinate norm (as produced by NormalizeCoords). pts are the
// glyph's default-master outline points in point order; ends holds the last
// point index of each contour (as in glyf's endPtsOfContours). Points untouched
// by a tuple are filled by per-contour IUP against pts. A font without 'gvar',
// a glyph with no variation data, or malformed data yields an unchanged copy.
func (f *Font) applyVariation(gid int, pts []outlinePoint, ends []int, norm []int16) []outlinePoint {
	out := make([]outlinePoint, len(pts))
	copy(out, pts)

	gv := f.gvar
	if gv == nil || gid < 0 || gid+1 >= len(gv.dataOffsets) {
		return out
	}
	start := gv.dataArrayOff + int(gv.dataOffsets[gid])
	end := gv.dataArrayOff + int(gv.dataOffsets[gid+1])
	if start >= end || end > len(gv.data) {
		return out
	}

	coords := make([]float64, gv.axisCount)
	for i := range coords {
		if i < len(norm) {
			coords[i] = float64(norm[i]) / 16384.0
		}
	}

	numReal := len(pts)
	accX := make([]float64, numReal+4) // + four phantom points
	accY := make([]float64, numReal+4)
	if err := gv.applyGlyph(gv.data[start:end], coords, ends, pts, accX, accY); err != nil {
		return out
	}
	for i := 0; i < numReal; i++ {
		out[i].x += accX[i]
		out[i].y += accY[i]
	}
	return out
}

// tupleHeader is a decoded TupleVariationHeader: the region it applies to and
// the size and point-set kind of its serialized data.
type tupleHeader struct {
	size         int       // serialized data size in bytes (private points + deltas)
	peak         []float64 // per-axis peak coordinate
	lower        []float64 // per-axis region lower bound
	upper        []float64 // per-axis region upper bound
	privatePoint bool      // has private point numbers
}

// applyGlyph decodes one glyph's GlyphVariationData and accumulates every
// applicable tuple's scaled deltas into accX/accY (length numReal+4).
func (gv *gvarTable) applyGlyph(gd []byte, coords []float64, ends []int, pts []outlinePoint, accX, accY []float64) error {
	numPts := len(accX)
	r := reader{b: gd}
	countAndFlags := r.u16()
	dataOffset := int(r.u16())
	tupleCount := int(countAndFlags & gvTupleIndexMsk)
	hasShared := countAndFlags&gvSharedPointNumbers != 0

	headers := make([]tupleHeader, tupleCount)
	for i := range headers {
		h := tupleHeader{
			size:  int(r.u16()),
			lower: make([]float64, gv.axisCount),
			upper: make([]float64, gv.axisCount),
		}
		idx := r.u16()
		if idx&gvEmbeddedPeak != 0 {
			h.peak = make([]float64, gv.axisCount)
			for a := range h.peak {
				h.peak[a] = r.f2dot14()
			}
		} else {
			si := int(idx & gvTupleIndexMsk)
			if si >= len(gv.sharedTuples) {
				return fmt.Errorf("opentype: gvar: shared tuple index %d out of range", si)
			}
			h.peak = gv.sharedTuples[si]
		}
		if idx&gvIntermediate != 0 {
			for a := 0; a < gv.axisCount; a++ {
				h.lower[a] = r.f2dot14()
			}
			for a := 0; a < gv.axisCount; a++ {
				h.upper[a] = r.f2dot14()
			}
		} else {
			for a := 0; a < gv.axisCount; a++ {
				if h.peak[a] < 0 {
					h.lower[a] = h.peak[a]
				}
				if h.peak[a] > 0 {
					h.upper[a] = h.peak[a]
				}
			}
		}
		h.privatePoint = idx&gvPrivatePoints != 0
		headers[i] = h
	}
	if r.err != nil {
		return fmt.Errorf("opentype: gvar tuple headers: %w", r.err)
	}

	sd := reader{b: gd, pos: dataOffset}
	var sharedPoints []int
	sharedAll := false
	if hasShared {
		sharedPoints, sharedAll = readPackedPoints(&sd)
	}
	if sd.err != nil {
		return fmt.Errorf("opentype: gvar shared points: %w", sd.err)
	}

	pos := sd.pos
	for i := range headers {
		h := headers[i]
		tr := reader{b: gd, pos: pos}
		points, all := sharedPoints, sharedAll
		if h.privatePoint {
			points, all = readPackedPoints(&tr)
		}
		n := len(points)
		if all {
			n = numPts
		}
		dx := readPackedDeltas(&tr, n)
		dy := readPackedDeltas(&tr, n)
		if tr.err != nil {
			return fmt.Errorf("opentype: gvar tuple %d data: %w", i, tr.err)
		}

		scalar := supportScalar(coords, h.lower, h.peak, h.upper)
		if scalar != 0 {
			accumulate(scalar, all, points, dx, dy, ends, pts, accX, accY)
		}
		pos += h.size
	}
	return nil
}

// readPackedPoints decodes a packed point-number set. all is true for the
// "all points" encoding (a leading count of zero), in which case pts is nil.
// Numbers are stored as run-length-encoded deltas from the previous number.
func readPackedPoints(r *reader) (pts []int, all bool) {
	first := r.u8()
	count := int(first)
	if first&0x80 != 0 {
		count = int(first&0x7f)<<8 | int(r.u8())
	}
	if count == 0 {
		return nil, true
	}
	pts = make([]int, 0, count)
	val := 0
	for len(pts) < count {
		control := r.u8()
		run := int(control&0x7f) + 1
		words := control&0x80 != 0
		for j := 0; j < run && len(pts) < count; j++ {
			if words {
				val += int(r.u16())
			} else {
				val += int(r.u8())
			}
			pts = append(pts, val)
		}
		if r.err != nil {
			return pts, false
		}
	}
	return pts, false
}

// readPackedDeltas decodes count run-length-encoded deltas (one axis's worth).
func readPackedDeltas(r *reader, count int) []float64 {
	out := make([]float64, 0, count)
	for len(out) < count {
		control := r.u8()
		run := int(control&0x3f) + 1
		switch {
		case control&0x80 != 0: // DELTAS_ARE_ZERO
			for j := 0; j < run && len(out) < count; j++ {
				out = append(out, 0)
			}
		case control&0x40 != 0: // DELTAS_ARE_WORDS
			for j := 0; j < run && len(out) < count; j++ {
				out = append(out, float64(int16(r.u16())))
			}
		default:
			for j := 0; j < run && len(out) < count; j++ {
				out = append(out, float64(int8(r.u8())))
			}
		}
		if r.err != nil {
			return out
		}
	}
	return out
}

// supportScalar returns how strongly a tuple with the given per-axis region
// (lower <= peak <= upper) applies at coords: 1 at the peak, tapering linearly
// to 0 at the region edges, and 0 outside. An axis whose peak is 0, or whose
// region is degenerate or spans zero, does not constrain the tuple.
func supportScalar(coords, lower, peak, upper []float64) float64 {
	scalar := 1.0
	for a := range peak {
		p := peak[a]
		if p == 0 {
			continue
		}
		lo, up := lower[a], upper[a]
		if lo > p || p > up {
			continue
		}
		if lo < 0 && up > 0 {
			continue
		}
		v := coords[a]
		if v == p {
			continue
		}
		if v <= lo || up <= v {
			return 0
		}
		if v < p {
			scalar *= (v - lo) / (p - lo)
		} else {
			scalar *= (up - v) / (up - p)
		}
	}
	return scalar
}

// accumulate adds one tuple's contribution to accX/accY. For an explicit point
// set it fills untouched points per contour by IUP before scaling; for the
// "all points" set every point is touched and deltas apply directly.
func accumulate(scalar float64, all bool, points []int, dx, dy []float64, ends []int, pts []outlinePoint, accX, accY []float64) {
	numPts := len(accX)
	fullX := make([]float64, numPts)
	fullY := make([]float64, numPts)

	if all {
		for i := 0; i < numPts && i < len(dx); i++ {
			fullX[i] = dx[i]
			fullY[i] = dy[i]
		}
	} else {
		touched := make([]bool, numPts)
		for k, pi := range points {
			if pi >= numPts {
				continue
			}
			fullX[pi] = dx[k]
			fullY[pi] = dy[k]
			touched[pi] = true
		}
		startIdx := 0
		for _, e := range ends {
			iupContour(pts[startIdx:e+1], fullX[startIdx:e+1], fullY[startIdx:e+1], touched[startIdx:e+1])
			startIdx = e + 1
		}
	}

	for i := 0; i < numPts; i++ {
		accX[i] += scalar * fullX[i]
		accY[i] += scalar * fullY[i]
	}
}

// iupContour fills deltas for the untouched points of a single contour in
// place, interpolating each coordinate against the default-master positions in
// pts. With no touched point the contour is left unmoved; with exactly one the
// whole contour is shifted by that delta; otherwise untouched points between
// each pair of consecutive touched points (wrapping around the contour) are
// interpolated.
func iupContour(pts []outlinePoint, dx, dy []float64, touched []bool) {
	n := len(pts)
	var tp []int
	for i := 0; i < n; i++ {
		if touched[i] {
			tp = append(tp, i)
		}
	}
	if len(tp) == 0 {
		return
	}
	if len(tp) == 1 {
		for i := 0; i < n; i++ {
			dx[i] = dx[tp[0]]
			dy[i] = dy[tp[0]]
		}
		return
	}
	for k := 0; k < len(tp); k++ {
		a := tp[k]
		b := tp[(k+1)%len(tp)]
		for i := (a + 1) % n; i != b; i = (i + 1) % n {
			dx[i] = iupInterp(pts[i].x, pts[a].x, pts[b].x, dx[a], dx[b])
			dy[i] = iupInterp(pts[i].y, pts[a].y, pts[b].y, dy[a], dy[b])
		}
	}
}

// iupInterp interpolates the delta of one untouched point at coordinate target,
// bracketed by two touched points at coordinates lo/hi with deltas dlo/dhi.
// Outside the bracket the nearer touched delta is held constant; inside, the
// delta is linearly interpolated by coordinate.
func iupInterp(target, lo, hi, dlo, dhi float64) float64 {
	if lo > hi {
		lo, hi = hi, lo
		dlo, dhi = dhi, dlo
	}
	if target <= lo {
		return dlo
	}
	if target >= hi {
		return dhi
	}
	return dlo + (dhi-dlo)*(target-lo)/(hi-lo)
}

// InstancePoints returns glyph gid's outline, as contours in font units,
// instanced at the user-space axis coordinates coords (keyed by axis tag).
// Axes absent from coords take their default. For a non-variable font, or a
// glyph with no variation data, it returns the default outline. Composite
// glyphs are returned resolved but with component-offset variation not applied
// (see the deferred note in this file).
func (f *Font) InstancePoints(gid int, coords map[string]float64) ([]contour, error) {
	contours, err := f.glyphContours(GlyphIndex(gid))
	if err != nil {
		return nil, err
	}
	var pts []outlinePoint
	var ends []int
	for _, c := range contours {
		pts = append(pts, c...)
		ends = append(ends, len(pts)-1)
	}
	norm := f.NormalizeCoords(coords)
	varied := f.applyVariation(gid, pts, ends, norm)

	result := make([]contour, len(contours))
	startIdx := 0
	for i, e := range ends {
		result[i] = varied[startIdx : e+1]
		startIdx = e + 1
	}
	return result, nil
}
