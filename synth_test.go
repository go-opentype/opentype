// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"math"
	"sort"
)

// This file synthesises minimal-but-valid TrueType fonts in memory so the
// tests never depend on an external font file. Every table is built by hand,
// which lets the tests deterministically exercise each parse and raster branch
// (including the error paths) by tweaking individual tables.

// bw is a tiny big-endian byte writer.
type bw struct{ b []byte }

func (w *bw) u8(v uint8)    { w.b = append(w.b, v) }
func (w *bw) u16(v uint16)  { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *bw) u32(v uint32)  { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *bw) i16(v int16)   { w.u16(uint16(v)) }
func (w *bw) bytes() []byte { return w.b }

// bbox writes a zeroed 4-int16 bounding box (the parser skips it).
func (w *bw) bbox() { w.i16(0); w.i16(0); w.i16(0); w.i16(0) }

// f2dot14 encodes a float as an F2Dot14 int16.
func f2dot14bytes(v float64) int16 { return int16(math.Round(v * 16384.0)) }

// assemble builds a full sfnt blob from a set of tables and an sfnt version.
func assemble(version uint32, tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	numTables := len(tags)
	headerLen := 12 + 16*numTables
	// Lay out table data 4-byte aligned after the directory.
	body := &bw{}
	type rec struct {
		tag            string
		offset, length int
	}
	recs := make([]rec, 0, numTables)
	off := headerLen
	for _, t := range tags {
		data := tables[t]
		recs = append(recs, rec{tag: t, offset: off, length: len(data)})
		body.b = append(body.b, data...)
		for len(body.b)%4 != 0 { // pad to 4 bytes
			body.b = append(body.b, 0)
		}
		off = headerLen + len(body.b)
	}

	h := &bw{}
	h.u32(version)
	h.u16(uint16(numTables))
	h.u16(0) // searchRange
	h.u16(0) // entrySelector
	h.u16(0) // rangeShift
	for _, r := range recs {
		h.b = append(h.b, []byte(r.tag)...)
		h.u32(0) // checksum (ignored by the parser)
		h.u32(uint32(r.offset))
		h.u32(uint32(r.length))
	}
	return append(h.b, body.b...)
}

// headTable builds a 54-byte head table.
func headTable(unitsPerEm int, indexToLocFormat int16) []byte {
	b := make([]byte, 54)
	binary.BigEndian.PutUint16(b[18:], uint16(unitsPerEm))
	binary.BigEndian.PutUint16(b[50:], uint16(indexToLocFormat))
	return b
}

// maxpTable builds a version-0.5+ maxp table carrying numGlyphs.
func maxpTable(numGlyphs int) []byte {
	w := &bw{}
	w.u32(0x00010000)
	w.u16(uint16(numGlyphs))
	return w.bytes()
}

// hheaTable builds a 36-byte hhea table.
func hheaTable(ascender, descender, lineGap, numberOfHMetrics int) []byte {
	b := make([]byte, 36)
	binary.BigEndian.PutUint16(b[4:], uint16(int16(ascender)))
	binary.BigEndian.PutUint16(b[6:], uint16(int16(descender)))
	binary.BigEndian.PutUint16(b[8:], uint16(int16(lineGap)))
	binary.BigEndian.PutUint16(b[34:], uint16(numberOfHMetrics))
	return b
}

// hmtxTable builds an hmtx table: the first numberOfHMetrics glyphs carry
// (advance, lsb) pairs and the rest carry only lsb (sharing the last advance).
func hmtxTable(advances, lsbs []int, numberOfHMetrics int) []byte {
	w := &bw{}
	for i := range advances {
		if i < numberOfHMetrics {
			w.u16(uint16(advances[i]))
			w.i16(int16(lsbs[i]))
		} else {
			w.i16(int16(lsbs[i]))
		}
	}
	return w.bytes()
}

// point is one contour point for the glyph encoder.
type point struct {
	x, y int
	on   bool
}

// encodeAxis returns the two flag bits and the coordinate bytes for one axis
// delta, choosing the same/short/long encoding.
func encodeAxis(delta int, shortBit, samePosBit uint8) (flag uint8, data []byte) {
	switch {
	case delta == 0:
		return samePosBit, nil // "same" (no bytes)
	case delta > 0 && delta <= 255:
		return shortBit | samePosBit, []byte{byte(delta)}
	case delta < 0 && delta >= -255:
		return shortBit, []byte{byte(-delta)}
	default:
		bb := make([]byte, 2)
		binary.BigEndian.PutUint16(bb, uint16(int16(delta)))
		return 0, bb
	}
}

// simpleGlyphBytes encodes a simple glyph from its contours.
func simpleGlyphBytes(contours [][]point) []byte {
	w := &bw{}
	w.i16(int16(len(contours)))
	w.i16(0) // xMin
	w.i16(0) // yMin
	w.i16(0) // xMax
	w.i16(0) // yMax

	var all []point
	end := -1
	for _, c := range contours {
		end += len(c)
		w.u16(uint16(end))
		all = append(all, c...)
	}
	w.u16(0) // instructionLength

	var flags, xdata, ydata []byte
	px, py := 0, 0
	for _, p := range all {
		var fl uint8
		if p.on {
			fl |= flagOnCurve
		}
		fx, xb := encodeAxis(p.x-px, flagXShort, flagXSamePosX)
		fy, yb := encodeAxis(p.y-py, flagYShort, flagYSamePosY)
		fl |= fx | fy
		flags = append(flags, fl)
		xdata = append(xdata, xb...)
		ydata = append(ydata, yb...)
		px, py = p.x, p.y
	}
	w.b = append(w.b, flags...)
	w.b = append(w.b, xdata...)
	w.b = append(w.b, ydata...)
	return w.bytes()
}

// component describes one composite-glyph component for the builder.
type component struct {
	glyphIndex uint16
	arg1, arg2 int
	useWords   bool
	argsAreXY  bool
	hasScale   bool
	scale      float64
	hasXY      bool
	sx, sy     float64
	has2x2     bool
	a, b, c, d float64
	more       bool // MORE_COMPONENTS follows
}

// compositeGlyphBytes encodes a composite glyph from its components.
func compositeGlyphBytes(comps []component) []byte {
	w := &bw{}
	w.i16(-1) // numberOfContours < 0 marks a composite
	w.i16(0)  // xMin
	w.i16(0)  // yMin
	w.i16(0)  // xMax
	w.i16(0)  // yMax
	for _, comp := range comps {
		var flags uint16
		if comp.useWords {
			flags |= compArgsAreWords
		}
		if comp.argsAreXY {
			flags |= compArgsAreXY
		}
		if comp.hasScale {
			flags |= compWeHaveScale
		}
		if comp.hasXY {
			flags |= compXAndYScale
		}
		if comp.has2x2 {
			flags |= compTwoByTwo
		}
		if comp.more {
			flags |= compMoreComponents
		}
		w.u16(flags)
		w.u16(comp.glyphIndex)
		if comp.useWords {
			w.i16(int16(comp.arg1))
			w.i16(int16(comp.arg2))
		} else {
			w.u8(uint8(int8(comp.arg1)))
			w.u8(uint8(int8(comp.arg2)))
		}
		switch {
		case comp.hasScale:
			w.i16(f2dot14bytes(comp.scale))
		case comp.hasXY:
			w.i16(f2dot14bytes(comp.sx))
			w.i16(f2dot14bytes(comp.sy))
		case comp.has2x2:
			w.i16(f2dot14bytes(comp.a))
			w.i16(f2dot14bytes(comp.b))
			w.i16(f2dot14bytes(comp.c))
			w.i16(f2dot14bytes(comp.d))
		}
	}
	return w.bytes()
}

// glyfAndLoca packs glyph blobs into a glyf table and its matching loca table.
func glyfAndLoca(glyphs [][]byte, longLoca bool) (loca, glyf []byte) {
	g := &bw{}
	offsets := []int{0}
	for _, blob := range glyphs {
		g.b = append(g.b, blob...)
		for len(g.b)%2 != 0 { // even-align for short loca
			g.b = append(g.b, 0)
		}
		offsets = append(offsets, len(g.b))
	}
	l := &bw{}
	for _, off := range offsets {
		if longLoca {
			l.u32(uint32(off))
		} else {
			l.u16(uint16(off / 2))
		}
	}
	return l.bytes(), g.bytes()
}

// cmap4FromMap builds a format-4 cmap subtable (idRangeOffset==0 segments)
// from a rune→glyph map, appending the mandatory 0xFFFF sentinel segment.
func cmap4FromMap(m map[rune]uint16) []byte {
	runes := make([]int, 0, len(m))
	for r := range m {
		runes = append(runes, int(r))
	}
	sort.Ints(runes)

	var ends, starts []uint16
	var deltas []int16
	i := 0
	for i < len(runes) {
		j := i
		for j+1 < len(runes) && runes[j+1] == runes[j]+1 &&
			m[rune(runes[j+1])] == m[rune(runes[j])]+uint16(runes[j+1]-runes[j]) {
			j++
		}
		start := runes[i]
		starts = append(starts, uint16(start))
		ends = append(ends, uint16(runes[j]))
		deltas = append(deltas, int16(int(m[rune(start)])-start))
		i = j + 1
	}
	// sentinel
	starts = append(starts, 0xFFFF)
	ends = append(ends, 0xFFFF)
	deltas = append(deltas, 1)

	return rawCmap4(ends, starts, deltas, make([]uint16, len(ends)), nil)
}

// rawCmap4 builds a format-4 subtable from explicit segment arrays, allowing
// idRangeOffset and glyphIdArray to be crafted directly.
func rawCmap4(endCode, startCode []uint16, idDelta []int16, idRangeOffset, glyphIDArray []uint16) []byte {
	segCount := len(endCode)
	w := &bw{}
	w.u16(4)                    // format
	w.u16(0)                    // length (unused by parser)
	w.u16(0)                    // language
	w.u16(uint16(segCount * 2)) // segCountX2
	w.u16(0)                    // searchRange
	w.u16(0)                    // entrySelector
	w.u16(0)                    // rangeShift
	for _, v := range endCode {
		w.u16(v)
	}
	w.u16(0) // reservedPad
	for _, v := range startCode {
		w.u16(v)
	}
	for _, v := range idDelta {
		w.i16(v)
	}
	for _, v := range idRangeOffset {
		w.u16(v)
	}
	for _, v := range glyphIDArray {
		w.u16(v)
	}
	return w.bytes()
}

// cmap12FromMap builds a format-12 subtable from a rune→glyph map.
func cmap12FromMap(m map[rune]uint16) []byte {
	runes := make([]int, 0, len(m))
	for r := range m {
		runes = append(runes, int(r))
	}
	sort.Ints(runes)

	type group struct{ start, end, gid int }
	var groups []group
	i := 0
	for i < len(runes) {
		j := i
		for j+1 < len(runes) && runes[j+1] == runes[j]+1 &&
			int(m[rune(runes[j+1])]) == int(m[rune(runes[j])])+(runes[j+1]-runes[j]) {
			j++
		}
		groups = append(groups, group{start: runes[i], end: runes[j], gid: int(m[rune(runes[i])])})
		i = j + 1
	}

	w := &bw{}
	w.u16(12) // format
	w.u16(0)  // reserved
	w.u32(0)  // length (unused)
	w.u32(0)  // language
	w.u32(uint32(len(groups)))
	for _, g := range groups {
		w.u32(uint32(g.start))
		w.u32(uint32(g.end))
		w.u32(uint32(g.gid))
	}
	return w.bytes()
}

// cmapTable wraps one or more subtables into a cmap table with encoding
// records. Each subtable gets a (platformID, encodingID) record.
func cmapTable(subtables [][]byte) []byte {
	w := &bw{}
	w.u16(0) // version
	w.u16(uint16(len(subtables)))
	headerLen := 4 + 8*len(subtables)
	off := headerLen
	body := &bw{}
	for i, sub := range subtables {
		w.u16(uint16(3))     // platformID (Windows)
		w.u16(uint16(i + 1)) // encodingID (arbitrary, distinct)
		w.u32(uint32(off))
		body.b = append(body.b, sub...)
		off += len(sub)
	}
	return append(w.bytes(), body.b...)
}
