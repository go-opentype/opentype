// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"fmt"
)

// This file bakes a variable ('glyf'/'gvar') font at a chosen axis position into
// a plain static font, so an embedder that cannot ship a variable font (a PDF
// writer, say) can ship the exact instance a document uses. Every glyph's outline
// is instanced (gvar deltas applied, composites flattened) and re-encoded as a
// simple glyph, every advance width is instanced (HVAR, or the gvar phantom-point
// fallback), and the variation tables (fvar, avar, gvar, HVAR/VVAR/MVAR, STAT) are
// dropped so the result advertises — and behaves as — a single static master.
//
// Scope (documented): the CFF2 (variable CFF) outline path is not baked; a CFF2
// font returns an error. TrueType instructions and the shared hinting programs
// (cvt/fpgm/prep) are dropped, since flattening composites into simple glyphs
// renumbers the points the instructions address; the instance renders unhinted.
// Global metric variations (MVAR) are not folded into hhea/OS-2; the instance
// keeps the default master's global metrics.

// glyfWriter is a minimal big-endian byte writer for re-encoding glyf/loca and
// patching the cloned metric tables.
type glyfWriter struct{ b []byte }

func (w *glyfWriter) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *glyfWriter) i16(v int16)  { w.u16(uint16(v)) }

// Instance bakes the font at the user-space axis coordinates coords into a new,
// static Font: a font with the requested variation permanently applied and no
// variation axes. Axes absent from coords take their default. It is the parsed
// counterpart of InstanceBytes.
//
// It returns an error for a non-variable font, a CFF2 (variable CFF) font, or when
// coords names an axis the font does not have.
func (f *Font) Instance(coords map[string]float64) (*Font, error) {
	b, err := f.InstanceBytes(coords)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// InstanceBytes bakes the font at coords into the bytes of a new static TrueType
// sfnt (see Instance for the semantics). The returned font carries instanced glyf
// outlines and advance widths and no variation tables; Instance parses these bytes.
func (f *Font) InstanceBytes(coords map[string]float64) ([]byte, error) {
	if f.cff2 != nil {
		return nil, fmt.Errorf("opentype: Instance: CFF2 (variable CFF) fonts are not supported")
	}
	if f.fvar == nil {
		return nil, fmt.Errorf("opentype: Instance: font is not variable (no fvar table)")
	}
	if f.glyf == nil {
		return nil, fmt.Errorf("opentype: Instance: font has no glyf outlines to instance")
	}
	known := make(map[string]bool, len(f.fvar.axes))
	for _, a := range f.fvar.axes {
		known[a.Tag] = true
	}
	for tag := range coords {
		if !known[tag] {
			return nil, fmt.Errorf("opentype: Instance: unknown axis %q", tag)
		}
	}
	norm := f.NormalizeCoords(coords)

	glyphs := make([][]byte, f.numGlyphs)
	advances := make([]int, f.numGlyphs)
	lsbs := make([]int, f.numGlyphs)
	haveBox := false
	var xMin, yMin, xMax, yMax int
	for gid := 0; gid < f.numGlyphs; gid++ {
		contours, err := f.loadGlyph(GlyphIndex(gid), 0, map[GlyphIndex]bool{}, norm)
		if err != nil {
			return nil, err
		}
		glyphs[gid] = encodeSimpleGlyph(contours)
		advances[gid] = roundInt(float64(f.advances[gid]) + f.horizontalAdvanceDelta(gid, norm))
		gx0, gy0, gx1, gy1, ok := contourBounds(contours)
		if ok {
			lsbs[gid] = gx0
			if !haveBox {
				xMin, yMin, xMax, yMax, haveBox = gx0, gy0, gx1, gy1, true
			} else {
				xMin, yMin = min(xMin, gx0), min(yMin, gy0)
				xMax, yMax = max(xMax, gx1), max(yMax, gy1)
			}
		}
	}

	loca, glyf := packGlyf(glyphs)
	tables := map[string][]byte{
		"glyf": glyf,
		"loca": loca,
		"head": f.instanceHead(xMin, yMin, xMax, yMax),
		"hhea": f.subsetHhea(f.numGlyphs),
		"maxp": instanceMaxp(f.tables["maxp"], glyphs),
		"hmtx": buildHmtx(advances, lsbs),
	}
	// Carry over every non-variation table verbatim. The variation tables and the
	// hinting tables are dropped (see the file comment); the six rebuilt tables
	// above are already set and must not be overwritten by the originals.
	drop := map[string]bool{
		"fvar": true, "avar": true, "gvar": true,
		"HVAR": true, "VVAR": true, "MVAR": true, "STAT": true,
		"cvt ": true, "fpgm": true, "prep": true,
		"glyf": true, "loca": true, "head": true,
		"hhea": true, "maxp": true, "hmtx": true,
	}
	for tag, b := range f.tables {
		if drop[tag] {
			continue
		}
		tables[tag] = append([]byte(nil), b...)
	}
	return buildSFNT(versionTrueType, tables), nil
}

// packGlyf concatenates the per-glyph blobs into a glyf table and its matching
// long-format loca (numGlyphs+1 offsets), 2-byte-aligning each glyph.
func packGlyf(glyphs [][]byte) (loca, glyf []byte) {
	offsets := make([]uint32, len(glyphs)+1)
	for i, g := range glyphs {
		offsets[i] = uint32(len(glyf))
		glyf = append(glyf, g...)
		for len(glyf)%2 != 0 {
			glyf = append(glyf, 0)
		}
	}
	offsets[len(glyphs)] = uint32(len(glyf))
	loca = make([]byte, len(offsets)*4)
	for i, o := range offsets {
		binary.BigEndian.PutUint32(loca[i*4:], o)
	}
	return loca, glyf
}

// buildHmtx builds a full hmtx table: one (advanceWidth, leftSideBearing) pair for
// every glyph (numberOfHMetrics equals the glyph count).
func buildHmtx(advances, lsbs []int) []byte {
	w := &glyfWriter{}
	for i := range advances {
		w.u16(uint16(advances[i]))
		w.i16(int16(lsbs[i]))
	}
	return w.b
}

// instanceHead clones the head table with checkSumAdjustment zeroed (buildSFNT
// recomputes it), indexToLocFormat set to long, and the recomputed instanced glyph
// bounding box.
func (f *Font) instanceHead(xMin, yMin, xMax, yMax int) []byte {
	head := append([]byte(nil), f.tables["head"]...)
	binary.BigEndian.PutUint32(head[8:], 0)
	binary.BigEndian.PutUint16(head[36:], uint16(int16(xMin)))
	binary.BigEndian.PutUint16(head[38:], uint16(int16(yMin)))
	binary.BigEndian.PutUint16(head[40:], uint16(int16(xMax)))
	binary.BigEndian.PutUint16(head[42:], uint16(int16(yMax)))
	binary.BigEndian.PutUint16(head[50:], 1)
	return head
}

// instanceMaxp clones maxp with the outline statistics of the instanced glyphs:
// the max simple-glyph point and contour counts are recomputed, and the composite
// statistics are zeroed since the instance carries only simple glyphs. A
// version-0.5 maxp (too short to hold these fields) is returned unchanged.
func instanceMaxp(orig []byte, glyphs [][]byte) []byte {
	maxp := append([]byte(nil), orig...)
	if len(maxp) < 32 {
		return maxp
	}
	maxPoints, maxContours := 0, 0
	for _, g := range glyphs {
		p, c := simpleGlyphStats(g)
		maxPoints = max(maxPoints, p)
		maxContours = max(maxContours, c)
	}
	binary.BigEndian.PutUint16(maxp[6:], uint16(maxPoints))
	binary.BigEndian.PutUint16(maxp[8:], uint16(maxContours))
	binary.BigEndian.PutUint16(maxp[10:], 0) // maxCompositePoints
	binary.BigEndian.PutUint16(maxp[12:], 0) // maxCompositeContours
	return maxp
}

// simpleGlyphStats returns the point and contour counts of a re-encoded simple
// glyph blob (as encodeSimpleGlyph produced it), or (0,0) for an empty glyph.
func simpleGlyphStats(g []byte) (points, contours int) {
	if len(g) < 10 {
		return 0, 0
	}
	n := int(int16(binary.BigEndian.Uint16(g)))
	if n <= 0 {
		return 0, 0
	}
	// endPtsOfContours[n-1] + 1 is the point count; the last end point sits at
	// offset 10 + 2*(n-1).
	last := 10 + 2*(n-1)
	if last+2 > len(g) {
		return 0, 0
	}
	return int(binary.BigEndian.Uint16(g[last:])) + 1, n
}

// contourBounds returns the integer bounding box of the (rounded) contour points
// and whether any point was present.
func contourBounds(contours []contour) (xMin, yMin, xMax, yMax int, ok bool) {
	for _, c := range contours {
		for _, p := range c {
			x, y := roundInt(p.x), roundInt(p.y)
			if !ok {
				xMin, yMin, xMax, yMax, ok = x, y, x, y, true
				continue
			}
			xMin, yMin = min(xMin, x), min(yMin, y)
			xMax, yMax = max(xMax, x), max(yMax, y)
		}
	}
	return xMin, yMin, xMax, yMax, ok
}

// encodeSimpleGlyph re-encodes instanced contours as a TrueType simple glyph, with
// coordinates rounded to integers and no instructions. It returns nil (an empty
// glyph) when the contours hold no points. Off-curve (quadratic control) points
// are preserved, so the re-encoded glyph re-parses to the same outline.
func encodeSimpleGlyph(contours []contour) []byte {
	var pts []outlinePoint
	var ends []int
	for _, c := range contours {
		if len(c) == 0 {
			continue
		}
		pts = append(pts, c...)
		ends = append(ends, len(pts)-1)
	}
	if len(pts) == 0 {
		return nil
	}
	xMin, yMin, xMax, yMax, _ := contourBounds(contours)

	w := &glyfWriter{}
	w.i16(int16(len(ends)))
	w.i16(int16(xMin))
	w.i16(int16(yMin))
	w.i16(int16(xMax))
	w.i16(int16(yMax))
	for _, e := range ends {
		w.u16(uint16(e))
	}
	w.u16(0) // instructionLength

	var flags, xs, ys []byte
	px, py := 0, 0
	for _, p := range pts {
		x, y := roundInt(p.x), roundInt(p.y)
		var fl byte
		if p.on {
			fl |= flagOnCurve
		}
		fx, xb := encodeGlyfDelta(x-px, flagXShort, flagXSamePosX)
		fy, yb := encodeGlyfDelta(y-py, flagYShort, flagYSamePosY)
		fl |= fx | fy
		flags = append(flags, fl)
		xs = append(xs, xb...)
		ys = append(ys, yb...)
		px, py = x, y
	}
	w.b = append(w.b, flags...)
	w.b = append(w.b, xs...)
	w.b = append(w.b, ys...)
	return w.b
}

// encodeGlyfDelta encodes one coordinate delta with the short/same/long flag
// scheme readCoords decodes: zero deltas set the "same" bit and emit no bytes,
// deltas in [-255,255] use one byte with a sign carried by the short/positive
// bits, and larger deltas fall back to a signed 16-bit value.
func encodeGlyfDelta(delta int, shortBit, samePosBit byte) (flag byte, data []byte) {
	switch {
	case delta == 0:
		return samePosBit, nil
	case delta > 0 && delta <= 255:
		return shortBit | samePosBit, []byte{byte(delta)}
	case delta < 0 && delta >= -255:
		return shortBit, []byte{byte(-delta)}
	default:
		d := int16(delta)
		return 0, []byte{byte(uint16(d) >> 8), byte(d)}
	}
}
