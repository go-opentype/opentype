// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"testing"
)

// This file synthesises minimal-but-valid CFF2 tables, ItemVariationStores and
// CFF2 charstrings in memory, so the CFF2 tests never depend on an external font
// file. Every parse and interpreter branch (including the error paths) is
// exercised deterministically by crafting the bytes directly, mirroring the
// approach of synth_test.go (glyf) and cff_test.go (CFF1). The Type2 charstring
// builder csb from cff_test.go is reused; its op(15)/op(16) emit vsindex/blend.

// --- CFF2 structural builders ----------------------------------------------

// dictEntry is one operator and its operands for the DICT builder.
type dictEntry struct {
	op   int
	args []int
}

// buildDict encodes a CFF DICT, using the fixed 5-byte int32 operand form (29)
// so a DICT's length is invariant to its operand values — which lets the CFF2
// assembler compute section offsets without an extra sizing pass.
func buildDict(entries []dictEntry) []byte {
	w := &bw{}
	for _, e := range entries {
		for _, a := range e.args {
			w.u8(29)
			w.u32(uint32(int32(a)))
		}
		if e.op >= 1200 {
			w.u8(12)
			w.u8(byte(e.op - 1200))
		} else {
			w.u8(byte(e.op))
		}
	}
	return w.bytes()
}

// buildIndex2 encodes a CFF2 INDEX (32-bit count).
func buildIndex2(items [][]byte) []byte {
	w := &bw{}
	if len(items) == 0 {
		w.u32(0)
		return w.bytes()
	}
	total := 1
	offs := []int{1}
	for _, it := range items {
		total += len(it)
		offs = append(offs, total)
	}
	offSize := 1
	for total > (1<<(8*offSize))-1 {
		offSize++
	}
	w.u32(uint32(len(items)))
	w.u8(uint8(offSize))
	for _, o := range offs {
		for k := offSize - 1; k >= 0; k-- {
			w.u8(byte(o >> (8 * k)))
		}
	}
	for _, it := range items {
		w.b = append(w.b, it...)
	}
	return w.bytes()
}

// cff2Head builds a 5-byte CFF2 header for a Top DICT of the given length.
func cff2Head(topLen int) []byte {
	h := &bw{}
	h.u8(2)
	h.u8(0)
	h.u8(5)
	h.u16(uint16(topLen))
	return h.bytes()
}

// buildVstore builds a length-prefixed ItemVariationStore (format 1) from a
// region list and a set of ItemVariationData region-index lists.
func buildVstore(axisCount int, regions [][]regionAxis, ivds [][]int) []byte {
	headerLen := 2 + 4 + 2 + 4*len(ivds)

	rl := &bw{}
	rl.u16(uint16(axisCount))
	rl.u16(uint16(len(regions)))
	for _, reg := range regions {
		for _, ax := range reg {
			rl.i16(f2dot14bytes(ax.start))
			rl.i16(f2dot14bytes(ax.peak))
			rl.i16(f2dot14bytes(ax.end))
		}
	}
	regionListOff := headerLen
	ivdStart := regionListOff + len(rl.bytes())

	var ivdBlobs [][]byte
	for _, ri := range ivds {
		d := &bw{}
		d.u16(0) // itemCount
		d.u16(0) // wordDeltaCount
		d.u16(uint16(len(ri)))
		for _, r := range ri {
			d.u16(uint16(r))
		}
		ivdBlobs = append(ivdBlobs, d.bytes())
	}
	ivdOffs := make([]int, len(ivds))
	cur := ivdStart
	for i, b := range ivdBlobs {
		ivdOffs[i] = cur
		cur += len(b)
	}

	ivs := &bw{}
	ivs.u16(1)
	ivs.u32(uint32(regionListOff))
	ivs.u16(uint16(len(ivds)))
	for _, o := range ivdOffs {
		ivs.u32(uint32(o))
	}
	ivs.b = append(ivs.b, rl.bytes()...)
	for _, b := range ivdBlobs {
		ivs.b = append(ivs.b, b...)
	}
	body := ivs.bytes()

	out := &bw{}
	out.u16(uint16(len(body)))
	out.b = append(out.b, body...)
	return out.bytes()
}

// fdSpec describes one Font DICT for the FDArray builder.
type fdSpec struct {
	localSubrs  [][]byte
	vsindex     int // -1 omits the Private DICT vsindex operator
	withPrivate bool
	withSubrs   bool
}

// buildFDArray returns a builder that, given the FDArray's absolute offset,
// emits the Font DICT INDEX followed by each Font DICT's Private DICT and Local
// Subr INDEX, with all cross-references resolved.
func buildFDArray(fds []fdSpec) func(int) []byte {
	return func(off int) []byte {
		n := len(fds)
		priv := make([][]byte, n)
		lsEnc := make([][]byte, n)
		contentLen := make([]int, n)
		for i, fd := range fds {
			if !fd.withPrivate {
				continue
			}
			pe := []dictEntry{}
			if fd.vsindex >= 0 {
				pe = append(pe, dictEntry{22, []int{fd.vsindex}})
			}
			if fd.withSubrs {
				pe = append(pe, dictEntry{19, []int{0}})
			}
			psize := len(buildDict(pe))
			pe2 := []dictEntry{}
			if fd.vsindex >= 0 {
				pe2 = append(pe2, dictEntry{22, []int{fd.vsindex}})
			}
			if fd.withSubrs {
				pe2 = append(pe2, dictEntry{19, []int{psize}}) // Subrs right after Private DICT
				lsEnc[i] = buildIndex2(fd.localSubrs)
			}
			priv[i] = buildDict(pe2)
			contentLen[i] = 11 // Private op: 2 int32 operands + operator
		}

		placeholders := make([][]byte, n)
		for i := range placeholders {
			placeholders[i] = make([]byte, contentLen[i])
		}
		fdiLen := len(buildIndex2(placeholders))

		cursor := off + fdiLen
		privOff := make([]int, n)
		for i := range fds {
			privOff[i] = cursor
			cursor += len(priv[i]) + len(lsEnc[i])
		}

		fdBytes := make([][]byte, n)
		for i, fd := range fds {
			if fd.withPrivate {
				fdBytes[i] = buildDict([]dictEntry{{18, []int{len(priv[i]), privOff[i]}}})
			}
		}
		out := buildIndex2(fdBytes)
		for i := range fds {
			out = append(out, priv[i]...)
			out = append(out, lsEnc[i]...)
		}
		return out
	}
}

// buildFDSelect0 builds an FDSelect format-0 subtable from a per-glyph list.
func buildFDSelect0(fds []int) []byte {
	w := &bw{}
	w.u8(0)
	for _, f := range fds {
		w.u8(uint8(f))
	}
	return w.bytes()
}

// buildFDSelect3 builds an FDSelect format-3 subtable from {first, fd} ranges.
func buildFDSelect3(ranges [][2]int, sentinel int) []byte {
	w := &bw{}
	w.u8(3)
	w.u16(uint16(len(ranges)))
	for _, r := range ranges {
		w.u16(uint16(r[0]))
		w.u8(uint8(r[1]))
	}
	w.u16(uint16(sentinel))
	return w.bytes()
}

// buildCFF2 assembles a complete CFF2 table. gsubrs, vstore, fdarrayFn and
// fdselect are optional (nil to omit). Layout: header, Top DICT, Global Subr
// INDEX, CharStrings INDEX, then any vstore/FDArray/FDSelect sections; all Top
// DICT offsets are absolute.
func buildCFF2(charStrings, gsubrs [][]byte, vstore []byte, fdarrayFn func(int) []byte, fdselect []byte) []byte {
	gEnc := buildIndex2(gsubrs)
	csEnc := buildIndex2(charStrings)
	hasVstore := vstore != nil
	hasFDArray := fdarrayFn != nil
	hasFDSelect := fdselect != nil

	mk := func(csOff, vsOff, fdaOff, fdsOff int) []byte {
		entries := []dictEntry{{17, []int{csOff}}}
		if hasVstore {
			entries = append(entries, dictEntry{24, []int{vsOff}})
		}
		if hasFDArray {
			entries = append(entries, dictEntry{1236, []int{fdaOff}})
		}
		if hasFDSelect {
			entries = append(entries, dictEntry{1237, []int{fdsOff}})
		}
		return buildDict(entries)
	}
	l := len(mk(0, 0, 0, 0))
	cursor := 5 + l
	cursor += len(gEnc)
	csOff := cursor
	cursor += len(csEnc)
	vsOff := cursor
	if hasVstore {
		cursor += len(vstore)
	}
	fdaOff := cursor
	var fdarray []byte
	if hasFDArray {
		fdarray = fdarrayFn(fdaOff)
		cursor += len(fdarray)
	}
	fdsOff := cursor

	top := mk(csOff, vsOff, fdaOff, fdsOff)
	out := append(cff2Head(len(top)), top...)
	out = append(out, gEnc...)
	out = append(out, csEnc...)
	if hasVstore {
		out = append(out, vstore...)
	}
	if hasFDArray {
		out = append(out, fdarray...)
	}
	if hasFDSelect {
		out = append(out, fdselect...)
	}
	return out
}

// cff2Custom lays out header + Top DICT + empty Global Subr INDEX + CharStrings
// INDEX + a caller-built trailing blob. extraFn adds Top DICT operators (e.g. a
// vstore/FDArray/FDSelect offset) as a function of the trailing blob's absolute
// base; trailingFn builds that blob. It is the low-level escape hatch for the
// parser's error-path tests.
func cff2Custom(csItems [][]byte, extraFn func(int) []dictEntry, trailingFn func(int) []byte) []byte {
	csEnc := buildIndex2(csItems)
	gsubrs := buildIndex2(nil)
	l := len(buildDict(append([]dictEntry{{17, []int{0}}}, extraFn(0)...)))
	csOff := 5 + l + len(gsubrs)
	trailBase := csOff + len(csEnc)
	top := buildDict(append([]dictEntry{{17, []int{csOff}}}, extraFn(trailBase)...))
	out := append(cff2Head(len(top)), top...)
	out = append(out, gsubrs...)
	out = append(out, csEnc...)
	if trailingFn != nil {
		out = append(out, trailingFn(trailBase)...)
	}
	return out
}

// --- test drivers -----------------------------------------------------------

func cff2Single(cs []byte) []byte { return buildCFF2([][]byte{cs}, nil, nil, nil, nil) }

func parseGlyph(t *testing.T, data []byte, gid int, coords []int16) []contour {
	t.Helper()
	c, err := parseCFF2(data)
	if err != nil {
		t.Fatalf("parseCFF2: unexpected error: %v", err)
	}
	got, err := c.outline(gid, coords)
	if err != nil {
		t.Fatalf("outline: unexpected error: %v", err)
	}
	return got
}

func wantParseErr(t *testing.T, data []byte) {
	t.Helper()
	if _, err := parseCFF2(data); err == nil {
		t.Fatalf("parseCFF2: expected error, got nil")
	}
}

func wantOutlineErr(t *testing.T, cs []byte) {
	t.Helper()
	c, err := parseCFF2(cff2Single(cs))
	if err != nil {
		t.Fatalf("parseCFF2: unexpected error: %v", err)
	}
	if _, err := c.outline(0, nil); err == nil {
		t.Fatalf("outline: expected error, got nil")
	}
}

// --- functional: blend actually moves points --------------------------------

func TestCFF2BlendMovesPoints(t *testing.T) {
	// One axis, one region peaking at the axis maximum, one ItemVariationData
	// referencing that region.
	vstore := buildVstore(1, [][]regionAxis{{{start: 0, peak: 1, end: 1}}}, [][]int{{0}})

	// blend two values (dx=100, dy=0) with region deltas (0, 200); the result
	// feeds rmoveto, so the start point's y moves from 0 (default) to 200 (max).
	c := &csb{}
	c.num(100).num(0).num(0).num(200).num(2).op(16) // blend -> [dx', dy']
	c.op(21)                                        // rmoveto
	c.num(50).num(0).op(5)                          // rlineto
	data := buildCFF2([][]byte{c.b}, nil, vstore, nil, nil)

	def := parseGlyph(t, data, 0, nil)           // default instance
	mx := parseGlyph(t, data, 0, []int16{16384}) // axis at +1.0
	if def[0][0].x != 100 || def[0][0].y != 0 {
		t.Fatalf("default start = (%v,%v), want (100,0)", def[0][0].x, def[0][0].y)
	}
	if mx[0][0].x != 100 || mx[0][0].y != 200 {
		t.Fatalf("instanced start = (%v,%v), want (100,200)", mx[0][0].x, mx[0][0].y)
	}
	// Half-way along the axis the point should be half-way moved.
	half := parseGlyph(t, data, 0, []int16{8192})
	if half[0][0].y != 100 {
		t.Fatalf("half start y = %v, want 100", half[0][0].y)
	}
}

func TestCFF2VsindexSelectsData(t *testing.T) {
	// Two regions, two ItemVariationData: index 0 -> region 0 (peak +1),
	// index 1 -> region 1 (peak -1). A vsindex operator switches the source.
	regions := [][]regionAxis{
		{{start: 0, peak: 1, end: 1}},
		{{start: -1, peak: -1, end: 0}},
	}
	vstore := buildVstore(1, regions, [][]int{{0}, {1}})

	c := &csb{}
	c.num(1).op(15)                               // vsindex -> data 1 (region 1, peak -1)
	c.num(0).num(0).num(0).num(300).num(2).op(16) // blend dy with delta 300
	c.op(21)                                      // rmoveto
	c.num(10).num(0).op(5)                        // rlineto
	data := buildCFF2([][]byte{c.b}, nil, vstore, nil, nil)

	// At axis -1.0 region 1 is fully active: dy = 0 + 1*300.
	neg := parseGlyph(t, data, 0, []int16{-16384})
	if neg[0][0].y != 300 {
		t.Fatalf("neg start y = %v, want 300", neg[0][0].y)
	}
	// At axis +1.0 region 1 is inactive: dy stays 0.
	pos := parseGlyph(t, data, 0, []int16{16384})
	if pos[0][0].y != 0 {
		t.Fatalf("pos start y = %v, want 0", pos[0][0].y)
	}
}

func TestCFF2PrivateDefaultVsindex(t *testing.T) {
	// The Private DICT sets a default vsindex of 1; a charstring with no vsindex
	// operator must blend from ItemVariationData 1.
	regions := [][]regionAxis{
		{{start: 0, peak: 1, end: 1}},
		{{start: 0, peak: 1, end: 1}},
	}
	// data 0 has no regions, data 1 has region 0.
	vstore := buildVstore(1, regions, [][]int{{}, {0}})

	c := &csb{}
	c.num(0).num(0).num(0).num(150).num(2).op(16) // blend using default vsindex (=1)
	c.op(21)
	c.num(5).num(0).op(5)
	fda := buildFDArray([]fdSpec{{vsindex: 1, withPrivate: true}})
	data := buildCFF2([][]byte{c.b}, nil, vstore, fda, buildFDSelect0([]int{0}))

	mx := parseGlyph(t, data, 0, []int16{16384})
	if mx[0][0].y != 150 {
		t.Fatalf("start y = %v, want 150 (default vsindex 1)", mx[0][0].y)
	}
}

func TestCFF2BlendWithoutVstore(t *testing.T) {
	// blend with no variation store is a no-op leaving its default operands.
	c := &csb{}
	c.num(70).num(0).num(2).op(16) // numBlends=2, k=0: leaves [70,0]
	c.op(21)
	c.num(5).num(0).op(5)
	got := parseGlyph(t, cff2Single(c.b), 0, nil)
	if got[0][0].x != 70 || got[0][0].y != 0 {
		t.Fatalf("start = (%v,%v), want (70,0)", got[0][0].x, got[0][0].y)
	}
}

// --- functional: subroutines and Font DICT selection ------------------------

func TestCFF2SubrsAndFDSelect(t *testing.T) {
	// Two Font DICTs, each with a distinct local subr; FDSelect (format 3) maps
	// glyph 0 -> fd 0 and glyph 1 -> fd 1. A global subr is shared.
	subA := &csb{}
	subA.num(40).num(0).op(5).op(11) // rlineto dx=40, return
	subB := &csb{}
	subB.num(0).num(60).op(5).op(11) // rlineto dy=60, return
	gsub := &csb{}
	gsub.num(5).num(5).op(5).op(11) // rlineto (5,5), return

	cs0 := &csb{}
	cs0.num(0).num(0).op(21) // rmoveto
	cs0.num(-107).op(10)     // callsubr local 0 (bias 107)
	cs0.num(-107).op(29)     // callgsubr 0
	cs1 := &csb{}
	cs1.num(0).num(0).op(21)
	cs1.num(-107).op(10)
	cs1.num(-107).op(29)

	fda := buildFDArray([]fdSpec{
		{localSubrs: [][]byte{subA.b}, vsindex: -1, withPrivate: true, withSubrs: true},
		{localSubrs: [][]byte{subB.b}, vsindex: -1, withPrivate: true, withSubrs: true},
	})
	fdsel := buildFDSelect3([][2]int{{0, 0}, {1, 1}}, 2)
	data := buildCFF2([][]byte{cs0.b, cs1.b}, [][]byte{gsub.b}, nil, fda, fdsel)

	g0 := parseGlyph(t, data, 0, nil)
	g1 := parseGlyph(t, data, 1, nil)
	// glyph 0's subr moved x by 40; glyph 1's moved y by 60.
	if g0[0][1].x != 40 {
		t.Fatalf("glyph0 second point x = %v, want 40", g0[0][1].x)
	}
	if g1[0][1].y != 60 {
		t.Fatalf("glyph1 second point y = %v, want 60", g1[0][1].y)
	}
}

func TestCFF2FontDictWithoutPrivate(t *testing.T) {
	// A Font DICT with no Private DICT: the glyph still renders (no local subrs).
	c := &csb{}
	c.num(0).num(0).op(21).num(30).num(0).op(5)
	fda := buildFDArray([]fdSpec{{withPrivate: false}})
	data := buildCFF2([][]byte{c.b}, nil, nil, fda, buildFDSelect0([]int{0}))
	got := parseGlyph(t, data, 0, nil)
	if len(got) != 1 {
		t.Fatalf("contours = %d, want 1", len(got))
	}
}

func TestCFF2PrivateWithoutSubrs(t *testing.T) {
	// A Private DICT present but declaring no Local Subrs.
	c := &csb{}
	c.num(0).num(0).op(21).num(30).num(0).op(5)
	fda := buildFDArray([]fdSpec{{vsindex: -1, withPrivate: true, withSubrs: false}})
	data := buildCFF2([][]byte{c.b}, nil, nil, fda, buildFDSelect0([]int{0}))
	parseGlyph(t, data, 0, nil)
}

// --- functional: every path operator ----------------------------------------

func TestCFF2AllPathOperators(t *testing.T) {
	c := &csb{}
	c.num(0).num(10).num(20).op(1)                                                                        // hstem  (1 pair)
	c.num(30).num(40).op(3)                                                                               // vstem  (1 pair)
	c.num(0).num(5).op(18)                                                                                // hstemhm(1 pair)
	c.num(0).num(5).op(23)                                                                                // vstemhm(1 pair) -> 4 stems total
	c.op(19).raw(0x00)                                                                                    // hintmask (1 mask byte)
	c.op(20).raw(0x00)                                                                                    // cntrmask (1 mask byte)
	c.num(0).num(0).op(21)                                                                                // rmoveto
	c.num(20).op(22)                                                                                      // hmoveto (new contour)
	c.num(20).op(4)                                                                                       // vmoveto (new contour)
	c.num(10).num(0).op(5)                                                                                // rlineto
	c.num(10).op(6)                                                                                       // hlineto
	c.num(10).op(7)                                                                                       // vlineto
	c.num(5).num(5).num(5).num(5).num(5).num(5).op(8)                                                     // rrcurveto
	c.num(5).num(5).num(5).num(5).num(5).num(5).num(10).num(0).op(24)                                     // rcurveline
	c.num(10).num(0).num(5).num(5).num(5).num(5).num(5).num(5).op(25)                                     // rlinecurve
	c.num(5).num(5).num(5).num(5).op(26)                                                                  // vvcurveto
	c.num(5).num(5).num(5).num(5).op(27)                                                                  // hhcurveto
	c.num(5).num(5).num(5).num(5).op(30)                                                                  // vhcurveto
	c.num(5).num(5).num(5).num(5).op(31)                                                                  // hvcurveto
	c.num(1).num(1).num(1).num(1).num(1).num(1).num(1).num(1).num(1).num(1).num(1).num(1).num(1).op(1235) // flex
	got := parseGlyph(t, cff2Single(c.b), 0, nil)
	if len(got) != 3 || len(got[len(got)-1]) < 5 {
		t.Fatalf("unexpected outline: %d contours", len(got))
	}
}

func TestCFF2CntrmaskAndReturn(t *testing.T) {
	// A subr that ends with return; the caller continues afterwards.
	sub := &csb{}
	sub.num(20).num(0).op(5).op(11)
	c := &csb{}
	c.num(0).num(0).op(21)
	c.num(-107).op(10) // callsubr -> draws, returns
	c.num(0).num(15).op(5)
	fda := buildFDArray([]fdSpec{{localSubrs: [][]byte{sub.b}, vsindex: -1, withPrivate: true, withSubrs: true}})
	data := buildCFF2([][]byte{c.b}, nil, nil, fda, buildFDSelect0([]int{0}))
	got := parseGlyph(t, data, 0, nil)
	if len(got[0]) != 3 {
		t.Fatalf("points = %d, want 3", len(got[0]))
	}
}

// --- charstring interpreter error paths -------------------------------------

func TestCFF2CharstringErrors(t *testing.T) {
	tests := map[string]func(*csb){
		"rmoveto args":   func(c *csb) { c.num(5).op(21) },
		"hmoveto args":   func(c *csb) { c.op(22) },
		"vmoveto args":   func(c *csb) { c.op(4) },
		"callsubr args":  func(c *csb) { c.op(10) },
		"callsubr range": func(c *csb) { c.num(100).op(10) },
		"callgsubr":      func(c *csb) { c.num(100).op(29) },
		"vsindex args":   func(c *csb) { c.op(15) },
		"blend count":    func(c *csb) { c.op(16) },
		"blend negative": func(c *csb) { c.num(-1).op(16) },
		"blend operands": func(c *csb) { c.num(100).num(3).op(16) },
		"unknown op":     func(c *csb) { c.raw(13) },
		"escape trunc":   func(c *csb) { c.raw(12) },
		"escape unknown": func(c *csb) { c.raw(12, 99) },
		"operand trunc":  func(c *csb) { c.raw(28) },
		"hintmask trunc": func(c *csb) { c.num(0).num(0).raw(19) },
	}
	for name, build := range tests {
		c := &csb{}
		build(c)
		t.Run(name, func(t *testing.T) { wantOutlineErr(t, c.b) })
	}
}

func TestCFF2StackOverflow(t *testing.T) {
	c := &csb{}
	for k := 0; k <= maxCFF2Stack; k++ { // one past the limit
		c.num(0)
	}
	c.op(21)
	wantOutlineErr(t, c.b)
}

func TestCFF2RecursionTooDeep(t *testing.T) {
	// A local subr that calls itself forever.
	self := &csb{}
	self.num(-107).op(10) // callsubr 0 (itself)
	c := &csb{}
	c.num(-107).op(10)
	fda := buildFDArray([]fdSpec{{localSubrs: [][]byte{self.b}, vsindex: -1, withPrivate: true, withSubrs: true}})
	data := buildCFF2([][]byte{c.b}, nil, nil, fda, buildFDSelect0([]int{0}))
	cc, err := parseCFF2(data)
	if err != nil {
		t.Fatalf("parseCFF2: %v", err)
	}
	if _, err := cc.outline(0, nil); err == nil {
		t.Fatalf("expected recursion error")
	}
}

func TestCFF2OutlineGidRange(t *testing.T) {
	c, err := parseCFF2(cff2Single([]byte{14}))
	if err != nil {
		t.Fatalf("parseCFF2: %v", err)
	}
	if _, err := c.outline(-1, nil); err == nil {
		t.Fatalf("gid -1: expected error")
	}
	if _, err := c.outline(5, nil); err == nil {
		t.Fatalf("gid 5: expected error")
	}
}

func TestCFF2OutlineBadVsindex(t *testing.T) {
	// A charstring selecting an out-of-range vsindex fails at interpretation.
	vstore := buildVstore(1, [][]regionAxis{{{0, 1, 1}}}, [][]int{{0}})
	c := &csb{}
	c.num(5).op(15) // vsindex 5, but only 1 data subtable
	data := buildCFF2([][]byte{c.b}, nil, vstore, nil, nil)
	cc, err := parseCFF2(data)
	if err != nil {
		t.Fatalf("parseCFF2: %v", err)
	}
	if _, err := cc.outline(0, nil); err == nil {
		t.Fatalf("expected vsindex error")
	}
}

// --- table parser error paths -----------------------------------------------

func TestCFF2OutlineBadDefaultVsindex(t *testing.T) {
	// The Private DICT default vsindex is out of range for the store, so the
	// blend vector cannot be computed before the charstring even runs.
	vstore := buildVstore(1, [][]regionAxis{{{0, 1, 1}}}, [][]int{{0}})
	c := &csb{}
	c.num(0).num(0).op(21)
	fda := buildFDArray([]fdSpec{{vsindex: 7, withPrivate: true}})
	data := buildCFF2([][]byte{c.b}, nil, vstore, fda, buildFDSelect0([]int{0}))
	cc, err := parseCFF2(data)
	if err != nil {
		t.Fatalf("parseCFF2: %v", err)
	}
	if _, err := cc.outline(0, []int16{16384}); err == nil {
		t.Fatalf("expected default-vsindex error")
	}
}

func TestCFF2ParseHeaderErrors(t *testing.T) {
	wantParseErr(t, []byte{2, 0, 5}) // too short

	badMajor := cff2Single([]byte{14})
	badMajor[0] = 3
	wantParseErr(t, badMajor)

	badHdr := cff2Single([]byte{14})
	badHdr[2] = 2 // headerSize < 5
	wantParseErr(t, badHdr)

	// Top DICT length past the end of the data.
	top := buildDict([]dictEntry{{17, []int{0}}})
	shortTop := append(cff2Head(len(top)+100), top...)
	wantParseErr(t, shortTop)

	// Truncated errTruncated is wrapped so callers can match it.
	if _, err := parseCFF2([]byte{2, 0}); !errors.Is(err, errTruncated) {
		t.Fatalf("err = %v, want errTruncated", err)
	}
}

func TestCFF2ParseTopDictErrors(t *testing.T) {
	// Malformed Top DICT (reserved operand byte 31 with no operator).
	top := []byte{31}
	data := append(cff2Head(len(top)), top...)
	data = append(data, buildIndex2(nil)...)
	wantParseErr(t, data)

	// Truncated Global Subr INDEX (count runs past the end).
	top2 := buildDict([]dictEntry{{17, []int{0}}})
	data2 := append(cff2Head(len(top2)), top2...)
	data2 = append(data2, 0, 0) // only 2 of the 4 count bytes
	wantParseErr(t, data2)

	// Truncated two-byte (escape) operator at the end of the Top DICT.
	topEsc := []byte{12}
	dataEsc := append(cff2Head(len(topEsc)), topEsc...)
	dataEsc = append(dataEsc, buildIndex2(nil)...)
	wantParseErr(t, dataEsc)

	// Missing CharStrings operator.
	empty := buildDict(nil)
	data3 := append(cff2Head(len(empty)), empty...)
	data3 = append(data3, buildIndex2(nil)...)
	wantParseErr(t, data3)

	// CharStrings offset out of range.
	top4 := buildDict([]dictEntry{{17, []int{1 << 20}}})
	data4 := append(cff2Head(len(top4)), top4...)
	data4 = append(data4, buildIndex2(nil)...)
	wantParseErr(t, data4)
}

func TestCFF2ParseIndex2Empty(t *testing.T) {
	// A zero-count Global Subr INDEX must decode to no subrs (covered by the
	// happy path already, but assert the parser accepts count==0 explicitly).
	c := &csb{}
	c.num(0).num(0).op(21)
	if got := parseGlyph(t, cff2Single(c.b), 0, nil); len(got) != 1 {
		t.Fatalf("contours = %d, want 1", len(got))
	}
}

func TestCFF2ParseVstoreErrors(t *testing.T) {
	mk := func(trailing []byte) []byte {
		return cff2Custom([][]byte{{0}}, func(b int) []dictEntry {
			return []dictEntry{{24, []int{b}}}
		}, func(int) []byte { return trailing })
	}
	// length prefix truncated.
	wantParseErr(t, mk([]byte{0}))
	// declared length past the end.
	wantParseErr(t, mk([]byte{0, 40, 0, 1}))
	// unsupported format.
	wantParseErr(t, mk([]byte{0, 4, 0, 9, 0, 0}))
	// header truncated within the declared length (regionListOffset).
	wantParseErr(t, mk([]byte{0, 2, 0, 1}))
	// region list truncated.
	badRegions := &bw{}
	badRegions.u16(1)  // format
	badRegions.u32(10) // regionListOffset
	badRegions.u16(0)  // ivdCount
	badRegions.u16(1)  // axisCount at offset 10
	badRegions.u16(1)  // regionCount, but no region axes follow
	wrapped := &bw{}
	wrapped.u16(uint16(len(badRegions.bytes())))
	wrapped.b = append(wrapped.b, badRegions.bytes()...)
	wantParseErr(t, mk(wrapped.bytes()))
	// region index out of range.
	wantParseErr(t, mk(buildVstore(1, [][]regionAxis{{{0, 1, 1}}}, [][]int{{5}})))
	// ItemVariationData offset points past the store (truncated data read).
	badIvd := &bw{}
	badIvd.u16(1)   // format
	badIvd.u32(12)  // regionListOffset
	badIvd.u16(1)   // ivdCount
	badIvd.u32(999) // ivd offset out of range
	badIvd.u16(1)   // axisCount at 12
	badIvd.u16(0)   // regionCount
	wrapIvd := &bw{}
	wrapIvd.u16(uint16(len(badIvd.bytes())))
	wrapIvd.b = append(wrapIvd.b, badIvd.bytes()...)
	wantParseErr(t, mk(wrapIvd.bytes()))
}

func TestCFF2ParseFDArrayErrors(t *testing.T) {
	// FDArray offset out of range.
	wantParseErr(t, cff2Custom([][]byte{{0}}, func(int) []dictEntry {
		return []dictEntry{{1236, []int{1 << 20}}}
	}, nil))

	fdAt := func(trailing func(int) []byte) []byte {
		return cff2Custom([][]byte{{0}}, func(b int) []dictEntry {
			return []dictEntry{{1236, []int{b}}}
		}, trailing)
	}
	// Truncated FDArray INDEX.
	wantParseErr(t, fdAt(func(int) []byte { return []byte{0, 0} }))
	// Empty FDArray.
	wantParseErr(t, fdAt(func(int) []byte { return buildIndex2(nil) }))
	// Malformed Font DICT (reserved operand).
	wantParseErr(t, fdAt(func(int) []byte { return buildIndex2([][]byte{{31}}) }))
	// Private DICT entry with too few operands.
	wantParseErr(t, fdAt(func(int) []byte {
		return buildIndex2([][]byte{buildDict([]dictEntry{{18, []int{5}}})})
	}))
	// Private DICT out of range.
	wantParseErr(t, fdAt(func(int) []byte {
		return buildIndex2([][]byte{buildDict([]dictEntry{{18, []int{10, 1 << 20}}})})
	}))
	// Private DICT bytes malformed (a reserved operand byte).
	wantParseErr(t, fdAt(func(base int) []byte {
		priv := []byte{31}
		privOff := base + len(buildIndex2([][]byte{make([]byte, 11)}))
		fd := buildDict([]dictEntry{{18, []int{len(priv), privOff}}})
		out := buildIndex2([][]byte{fd})
		out = append(out, priv...)
		return out
	}))
	// Local Subrs offset out of range and truncated Local Subrs INDEX.
	wantParseErr(t, fdAt(func(base int) []byte {
		fdi := buildIndex2([][]byte{buildDict([]dictEntry{{18, nil}})}) // placeholder, patched below
		priv := buildDict([]dictEntry{{19, []int{1 << 20}}})
		privOff := base + len(buildIndex2([][]byte{make([]byte, 11)}))
		fd := buildDict([]dictEntry{{18, []int{len(priv), privOff}}})
		out := buildIndex2([][]byte{fd})
		_ = fdi
		out = append(out, priv...)
		return out
	}))
	wantParseErr(t, fdAt(func(base int) []byte {
		priv := buildDict([]dictEntry{{19, []int{5}}}) // Subrs at privOff+5
		privOff := base + len(buildIndex2([][]byte{make([]byte, 11)}))
		fd := buildDict([]dictEntry{{18, []int{len(priv), privOff}}})
		out := buildIndex2([][]byte{fd})
		out = append(out, priv...)
		out = append(out, 0, 0) // truncated Local Subrs count
		return out
	}))
}

func TestCFF2ParseFDSelectErrors(t *testing.T) {
	// FDSelect offset out of range (no FDArray, so numFDs defaults to 1).
	wantParseErr(t, cff2Custom([][]byte{{0}}, func(int) []dictEntry {
		return []dictEntry{{1237, []int{1 << 20}}}
	}, nil))

	withSelect := func(sel []byte) []byte {
		c := &csb{}
		c.num(0).num(0).op(21)
		fda := buildFDArray([]fdSpec{{withPrivate: false}})
		return buildCFF2([][]byte{c.b}, nil, nil, fda, sel)
	}
	// Format 0 fd out of range.
	wantParseErr(t, withSelect(buildFDSelect0([]int{5})))
	// Format 0 truncated (too few entries).
	wantParseErr(t, withSelect([]byte{0}))
	// Format 3 fd out of range.
	wantParseErr(t, withSelect(buildFDSelect3([][2]int{{0, 5}}, 1)))
	// Format 3 truncated.
	wantParseErr(t, withSelect([]byte{3, 0, 1}))
	// Unsupported format.
	wantParseErr(t, withSelect([]byte{9}))
}

func TestCFF2Index2Errors(t *testing.T) {
	// Bad offSize in a CharStrings INDEX reached through a valid header.
	mk := func(csBytes []byte) []byte {
		top := buildDict([]dictEntry{{17, []int{0}}})
		l := len(top)
		gsubrs := buildIndex2(nil)
		csOff := 5 + l + len(gsubrs)
		top = buildDict([]dictEntry{{17, []int{csOff}}})
		out := append(cff2Head(len(top)), top...)
		out = append(out, gsubrs...)
		out = append(out, csBytes...)
		return out
	}
	wantParseErr(t, mk([]byte{0, 0, 0, 1, 0}))          // offSize 0
	wantParseErr(t, mk([]byte{0, 0, 0, 1}))             // count read but offSize missing
	wantParseErr(t, mk([]byte{0, 0, 0, 1, 2}))          // offSize ok, offsets truncated
	wantParseErr(t, mk([]byte{0, 0, 0, 1, 1, 5, 0xFF})) // item end past the data
}
