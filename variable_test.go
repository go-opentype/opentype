// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

// This file synthesises 'fvar', 'avar' and 'gvar' tables in memory (mirroring
// synth_test.go's approach for the base tables) so the variation code is
// exercised deterministically, including every parse and delta-application
// branch. No external font file is used.

// --- table builders ---------------------------------------------------------

// fixedBytes encodes a float as a 16.16 signed fixed-point uint32.
func fixedBytes(v float64) uint32 { return uint32(int32(v * 65536)) }

type axisSpec struct {
	tag           string
	min, def, max float64
	flags, nameID uint16
}

type instSpec struct {
	sub, flags uint16
	coords     []float64
	psID       uint16
}

// buildFvar encodes an 'fvar' table. withPS controls whether instance records
// carry a trailing postScriptNameID (which changes instanceSize).
func buildFvar(axes []axisSpec, insts []instSpec, withPS bool) []byte {
	axisCount := len(axes)
	axisSize := 20
	instSize := 4 + axisCount*4
	if withPS {
		instSize += 2
	}
	w := &bw{}
	w.u16(1) // majorVersion
	w.u16(0) // minorVersion
	w.u16(16)
	w.u16(2) // reserved
	w.u16(uint16(axisCount))
	w.u16(uint16(axisSize))
	w.u16(uint16(len(insts)))
	w.u16(uint16(instSize))
	for _, a := range axes {
		w.b = append(w.b, []byte(a.tag)...)
		w.u32(fixedBytes(a.min))
		w.u32(fixedBytes(a.def))
		w.u32(fixedBytes(a.max))
		w.u16(a.flags)
		w.u16(a.nameID)
	}
	for _, in := range insts {
		w.u16(in.sub)
		w.u16(in.flags)
		for _, c := range in.coords {
			w.u32(fixedBytes(c))
		}
		if withPS {
			w.u16(in.psID)
		}
	}
	return w.bytes()
}

// buildAvar encodes an 'avar' table from per-axis (from, to) control points.
func buildAvar(segs [][][2]float64) []byte {
	w := &bw{}
	w.u16(1) // majorVersion
	w.u16(0) // minorVersion
	w.u16(0) // reserved
	w.u16(uint16(len(segs)))
	for _, s := range segs {
		w.u16(uint16(len(s)))
		for _, m := range s {
			w.i16(f2dot14bytes(m[0]))
			w.i16(f2dot14bytes(m[1]))
		}
	}
	return w.bytes()
}

// packPointNumbers encodes a packed point-number set (nil => "all points").
func packPointNumbers(points []int) []byte {
	w := &bw{}
	if len(points) == 0 {
		w.u8(0)
		return w.bytes()
	}
	if len(points) < 128 {
		w.u8(uint8(len(points)))
	} else {
		w.u8(uint8(0x80 | len(points)>>8))
		w.u8(uint8(len(points)))
	}
	prev := 0
	i := 0
	for i < len(points) {
		n := len(points) - i
		if n > 64 {
			n = 64
		}
		w.u8(uint8(n - 1)) // byte-mode run
		for j := 0; j < n; j++ {
			w.u8(uint8(points[i+j] - prev))
			prev = points[i+j]
		}
		i += n
	}
	return w.bytes()
}

// packDeltas encodes run-length-packed deltas (zero-runs and byte-runs).
func packDeltas(deltas []int) []byte {
	w := &bw{}
	i := 0
	for i < len(deltas) {
		if deltas[i] == 0 {
			j := i
			for j < len(deltas) && deltas[j] == 0 {
				j++
			}
			for n := j - i; n > 0; {
				run := n
				if run > 64 {
					run = 64
				}
				w.u8(0x80 | uint8(run-1))
				n -= run
			}
			i = j
		} else {
			j := i
			for j < len(deltas) && deltas[j] != 0 {
				j++
			}
			for k := i; k < j; {
				run := j - k
				if run > 64 {
					run = 64
				}
				w.u8(uint8(run - 1)) // byte-mode value run
				for x := 0; x < run; x++ {
					w.u8(uint8(int8(deltas[k+x])))
				}
				k += run
			}
			i = j
		}
	}
	return w.bytes()
}

type rawTuple struct {
	embedPeak    bool
	peak         []float64
	sharedIdx    int
	intermediate bool
	start, end   []float64
	private      bool
	privateAll   bool
	privatePts   []int
	dX, dY       []int
}

// buildGlyphVarData encodes one glyph's GlyphVariationData.
func buildGlyphVarData(sharedPts []int, hasShared, sharedAll bool, tuples []rawTuple) []byte {
	var shared []byte
	if hasShared {
		if sharedAll {
			shared = packPointNumbers(nil)
		} else {
			shared = packPointNumbers(sharedPts)
		}
	}
	type built struct{ hdr, body []byte }
	var bs []built
	for _, t := range tuples {
		var body []byte
		if t.private {
			if t.privateAll {
				body = append(body, packPointNumbers(nil)...)
			} else {
				body = append(body, packPointNumbers(t.privatePts)...)
			}
		}
		body = append(body, packDeltas(t.dX)...)
		body = append(body, packDeltas(t.dY)...)
		h := &bw{}
		h.u16(uint16(len(body)))
		var idx uint16
		if t.embedPeak {
			idx |= 0x8000
		} else {
			idx = uint16(t.sharedIdx) & 0x0FFF
		}
		if t.intermediate {
			idx |= 0x4000
		}
		if t.private {
			idx |= 0x2000
		}
		h.u16(idx)
		if t.embedPeak {
			for _, v := range t.peak {
				h.i16(f2dot14bytes(v))
			}
		}
		if t.intermediate {
			for _, v := range t.start {
				h.i16(f2dot14bytes(v))
			}
			for _, v := range t.end {
				h.i16(f2dot14bytes(v))
			}
		}
		bs = append(bs, built{hdr: h.bytes(), body: body})
	}
	headerBytes := 0
	for _, b := range bs {
		headerBytes += len(b.hdr)
	}
	w := &bw{}
	tvc := uint16(len(tuples)) & 0x0FFF
	if hasShared {
		tvc |= 0x8000
	}
	w.u16(tvc)
	w.u16(uint16(4 + headerBytes))
	for _, b := range bs {
		w.b = append(w.b, b.hdr...)
	}
	w.b = append(w.b, shared...)
	for _, b := range bs {
		w.b = append(w.b, b.body...)
	}
	return w.bytes()
}

// buildGvar encodes a 'gvar' table from shared tuples and per-glyph data.
func buildGvar(axisCount int, sharedTuples [][]float64, glyphData [][]byte, longOffsets bool) []byte {
	glyphCount := len(glyphData)
	offW := 2
	if longOffsets {
		offW = 4
	}
	sharedOff := 20 + (glyphCount+1)*offW
	dataArrayOff := sharedOff + len(sharedTuples)*axisCount*2
	dataArr := &bw{}
	offs := []int{0}
	for _, gd := range glyphData {
		dataArr.b = append(dataArr.b, gd...)
		for len(dataArr.b)%2 != 0 {
			dataArr.b = append(dataArr.b, 0)
		}
		offs = append(offs, len(dataArr.b))
	}
	w := &bw{}
	w.u16(1) // majorVersion
	w.u16(0) // minorVersion
	w.u16(uint16(axisCount))
	w.u16(uint16(len(sharedTuples)))
	w.u32(uint32(sharedOff))
	w.u16(uint16(glyphCount))
	var flags uint16
	if longOffsets {
		flags = 1
	}
	w.u16(flags)
	w.u32(uint32(dataArrayOff))
	for _, o := range offs {
		if longOffsets {
			w.u32(uint32(o))
		} else {
			w.u16(uint16(o / 2))
		}
	}
	for _, st := range sharedTuples {
		for _, v := range st {
			w.i16(f2dot14bytes(v))
		}
	}
	w.b = append(w.b, dataArr.bytes()...)
	return w.bytes()
}

// squareGlyph is a 4-point on-curve square with the given corners.
func squareGlyph() []byte {
	return simpleGlyphBytes([][]point{{
		{0, 0, true}, {100, 0, true}, {100, 100, true}, {0, 100, true},
	}})
}

// makeVarFont assembles a font from glyph blobs plus optional variation tables.
func makeVarFont(t *testing.T, glyphs [][]byte, fvarB, gvarB, avarB []byte) *Font {
	t.Helper()
	n := len(glyphs)
	loca, glyf := glyfAndLoca(glyphs, false)
	m := map[rune]uint16{}
	adv := make([]int, n)
	lsb := make([]int, n)
	for i := 0; i < n; i++ {
		m[rune('A'+i)] = uint16(i)
		adv[i] = 500
		lsb[i] = 0
	}
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 100, n),
		"hmtx": hmtxTable(adv, lsb, n),
		"cmap": cmapTable([][]byte{cmap4FromMap(m)}),
		"loca": loca,
		"glyf": glyf,
	}
	if fvarB != nil {
		tables["fvar"] = fvarB
	}
	if gvarB != nil {
		tables["gvar"] = gvarB
	}
	if avarB != nil {
		tables["avar"] = avarB
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

// wghtAxis is the standard test weight axis: min 100, default 400, max 900.
func wghtAxis() []axisSpec {
	return []axisSpec{{tag: "wght", min: 100, def: 400, max: 900, flags: 1, nameID: 256}}
}

// flat concatenates contour points into one slice for comparison.
func flat(cs []contour) []outlinePoint {
	var out []outlinePoint
	for _, c := range cs {
		out = append(out, c...)
	}
	return out
}

// eqPts asserts a instanced outline equals want (x,y pairs).
func eqPts(t *testing.T, got []outlinePoint, want [][2]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("point count = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].x != w[0] || got[i].y != w[1] {
			t.Fatalf("point %d = (%g,%g), want (%g,%g)", i, got[i].x, got[i].y, w[0], w[1])
		}
	}
}

// --- fvar / NormalizeCoords -------------------------------------------------

func TestFvarAxesAndInstances(t *testing.T) {
	insts := []instSpec{{sub: 2, flags: 0, coords: []float64{900}, psID: 5}}
	fv := buildFvar(wghtAxis(), insts, true)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil)

	axes := f.Axes()
	if len(axes) != 1 || axes[0].Tag != "wght" || axes[0].Min != 100 ||
		axes[0].Default != 400 || axes[0].Max != 900 || axes[0].Flags != 1 || axes[0].NameID != 256 {
		t.Fatalf("Axes = %+v", axes)
	}
	ni := f.NamedInstances()
	if len(ni) != 1 || ni[0].SubfamilyNameID != 2 || ni[0].Coordinates["wght"] != 900 || ni[0].PostScriptNameID != 5 {
		t.Fatalf("NamedInstances = %+v", ni)
	}
}

func TestFvarInstanceNoPostScript(t *testing.T) {
	insts := []instSpec{{sub: 3, coords: []float64{700}}}
	fv := buildFvar(wghtAxis(), insts, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil)
	ni := f.NamedInstances()
	if len(ni) != 1 || ni[0].PostScriptNameID != 0 {
		t.Fatalf("NamedInstances = %+v", ni)
	}
}

func TestNonVariableFontHasNoAxes(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	if f.Axes() != nil || f.NamedInstances() != nil || f.NormalizeCoords(nil) != nil {
		t.Fatal("static font reported variation data")
	}
}

func TestNormalizeCoords(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil)
	cases := []struct {
		w    float64
		want int16
	}{
		{400, 0},      // default
		{900, 16384},  // max -> +1
		{100, -16384}, // min -> -1
		{250, -8192},  // below default
		{650, 8192},   // above default
		{50, -16384},  // clamped to min
		{1000, 16384}, // clamped to max
	}
	for _, c := range cases {
		got := f.NormalizeCoords(map[string]float64{"wght": c.w})
		if len(got) != 1 || got[0] != c.want {
			t.Fatalf("NormalizeCoords(%g) = %v, want [%d]", c.w, got, c.want)
		}
	}
	// Axis absent from the map takes its default (0).
	if got := f.NormalizeCoords(nil); got[0] != 0 {
		t.Fatalf("NormalizeCoords(nil) = %v", got)
	}
}

// --- avar -------------------------------------------------------------------

func TestAvarApply(t *testing.T) {
	a := &avarTable{segments: [][]avarMap{
		{{-1, -1}, {0, 0}, {0.5, 0.8}}, // axis 0
		{{0, 0}},                       // axis 1: fewer than 2 maps -> identity
	}}
	cases := []struct {
		axis int
		v, w float64
	}{
		{5, 0.3, 0.3},  // axis index past table -> identity
		{1, 0.3, 0.3},  // <2 control points -> identity
		{0, -1.0, -1},  // at/below first control point
		{0, 0.25, 0.4}, // interpolated between (0,0) and (0.5,0.8)
		{0, 0.5, 0.8},  // exact control point
		{0, 1.0, 0.8},  // past last control point -> last value
	}
	for _, c := range cases {
		if got := a.apply(c.axis, c.v); got != c.w {
			t.Fatalf("apply(%d,%g) = %g, want %g", c.axis, c.v, got, c.w)
		}
	}
}

func TestNormalizeCoordsWithAvar(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	av := buildAvar([][][2]float64{{{-1, -1}, {0, 0}, {0.5, 0.8}, {1, 1}}})
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, av)
	// wght 650 -> linear 0.5 -> avar 0.8 (0.8*16384 rounds to 13107).
	if got := f.NormalizeCoords(map[string]float64{"wght": 650}); got[0] != 13107 {
		t.Fatalf("avar remap = %v", got)
	}
	// wght 525 -> linear 0.25 -> avar 0.4 (0.4*16384 rounds to 6554).
	if got := f.NormalizeCoords(map[string]float64{"wght": 525}); got[0] != 6554 {
		t.Fatalf("avar interp = %v", got)
	}
}

func TestNormalizeCoordsAvarClamp(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// Deliberately out-of-range avar to exercise the [-1,1] clamp.
	av := buildAvar([][][2]float64{{{-1, -1.5}, {0, 0}, {1, 1.5}}})
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, av)
	if got := f.NormalizeCoords(map[string]float64{"wght": 900}); got[0] != 16384 {
		t.Fatalf("high clamp = %v", got)
	}
	if got := f.NormalizeCoords(map[string]float64{"wght": 100}); got[0] != -16384 {
		t.Fatalf("low clamp = %v", got)
	}
}

// --- gvar end-to-end --------------------------------------------------------

func TestInstanceAllPoints(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{10, 20, 30, 40, 0, 0, 0, 0},
		dY: []int{5, 6, 7, 8, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)

	// At the maximum weight the outline is fully displaced.
	cs, err := f.InstancePoints(1, map[string]float64{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	eqPts(t, flat(cs), [][2]float64{{10, 5}, {120, 6}, {130, 107}, {40, 108}})

	// At the default weight the tuple's scalar is 0: the outline is unchanged.
	cs, _ = f.InstancePoints(1, map[string]float64{"wght": 400})
	eqPts(t, flat(cs), [][2]float64{{0, 0}, {100, 0}, {100, 100}, {0, 100}})

	// The empty glyph 0 instances to an empty outline (start == end path).
	if cs, _ := f.InstancePoints(0, map[string]float64{"wght": 900}); len(cs) != 0 {
		t.Fatalf("empty glyph produced %d contours", len(cs))
	}
}

func TestInstanceExplicitPointsIUP(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// Touch only points 0 and 2; points 1 and 3 are recovered by IUP.
	gd := buildGlyphVarData(nil, false, false, []rawTuple{{
		sharedIdx: 0,
		private:   true, privatePts: []int{0, 2},
		dX: []int{10, 30}, dY: []int{0, 0},
	}})
	gv := buildGvar(1, [][]float64{{1.0}}, [][]byte{nil, gd}, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)

	cs, err := f.InstancePoints(1, map[string]float64{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	// p1 and p3 inherit the interpolated x deltas (30 and 10 respectively).
	eqPts(t, flat(cs), [][2]float64{{10, 0}, {130, 0}, {130, 100}, {10, 100}})
}

func TestInstanceNegativePeak(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{-1.0}, // negative peak: lower bound path
		dX: []int{-5, -5, -5, -5, 0, 0, 0, 0},
		dY: []int{0, 0, 0, 0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)
	// wght 100 normalizes to -1 == peak: full application (-5 in x).
	cs, err := f.InstancePoints(1, map[string]float64{"wght": 100})
	if err != nil {
		t.Fatal(err)
	}
	eqPts(t, flat(cs), [][2]float64{{-5, 0}, {95, 0}, {95, 100}, {-5, 100}})
}

// --- composite-glyph variation ----------------------------------------------

func TestInstanceCompositeVariation(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// Glyph 1: a simple square base whose point 0 is displaced by (5,5) at max.
	gd1 := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{5, 0, 0, 0, 0, 0, 0, 0},
		dY: []int{5, 0, 0, 0, 0, 0, 0, 0},
	}})
	// Glyph 2: a composite placing glyph 1 at offset (200,300); the single
	// component offset (gvar point 0) is displaced by (50,-20) at max, ahead of
	// the four phantom points.
	gd2 := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{50, 0, 0, 0, 0},
		dY: []int{-20, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd1, gd2}, false)

	comp := compositeGlyphBytes([]component{{
		glyphIndex: 1, arg1: 200, arg2: 300, useWords: true, argsAreXY: true,
	}})
	f := makeVarFont(t, [][]byte{nil, squareGlyph(), comp}, fv, gv, nil)

	// At maximum weight both the component offset and the base point move.
	cs, err := f.InstancePoints(2, map[string]float64{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	eqPts(t, flat(cs), [][2]float64{{255, 285}, {350, 280}, {350, 380}, {250, 380}})

	// At the default weight nothing moves: the plain composite placement.
	def := [][2]float64{{200, 300}, {300, 300}, {300, 400}, {200, 400}}
	cs, _ = f.InstancePoints(2, map[string]float64{"wght": 400})
	eqPts(t, flat(cs), def)

	// glyphContours takes the default path (nil norm): no variation at all.
	dc, err := f.glyphContours(2)
	if err != nil {
		t.Fatal(err)
	}
	eqPts(t, flat(dc), def)
}

func TestVaryComponentOffsetsEdges(t *testing.T) {
	comps := []glyphComponent{{arg1: 10, arg2: 20, argsAreXY: true}}
	reset := func() ([]float64, []float64) { return []float64{10}, []float64{20} }
	unchanged := func(dxs, dys []float64) {
		t.Helper()
		if dxs[0] != 10 || dys[0] != 20 {
			t.Fatalf("component offsets changed: %v %v", dxs, dys)
		}
	}
	norm := []int16{16384}

	// nil norm: default path, no variation.
	dxs, dys := reset()
	(&Font{gvar: &gvarTable{}}).varyComponentOffsets(0, comps, dxs, dys, nil)
	unchanged(dxs, dys)

	// No gvar table.
	dxs, dys = reset()
	(&Font{}).varyComponentOffsets(0, comps, dxs, dys, norm)
	unchanged(dxs, dys)

	// Glyph id out of range.
	dxs, dys = reset()
	f := &Font{gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 4}, data: make([]byte, 8)}}
	f.varyComponentOffsets(9, comps, dxs, dys, norm)
	unchanged(dxs, dys)

	// Empty data range (start >= end).
	dxs, dys = reset()
	g := &Font{gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 0}, data: make([]byte, 8)}}
	g.varyComponentOffsets(0, comps, dxs, dys, norm)
	unchanged(dxs, dys)

	// Offset past the data (end > len).
	dxs, dys = reset()
	h := &Font{gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 100}, data: make([]byte, 10)}}
	h.varyComponentOffsets(0, comps, dxs, dys, norm)
	unchanged(dxs, dys)

	// Malformed glyph variation data (shared tuple index out of range).
	dxs, dys = reset()
	blob := buildGlyphVarData(nil, false, false, []rawTuple{{
		sharedIdx: 5, private: true, privateAll: true,
		dX: make([]int, 5), dY: make([]int, 5),
	}})
	e := &Font{gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, uint32(len(blob))}, data: blob}}
	e.varyComponentOffsets(0, comps, dxs, dys, norm)
	unchanged(dxs, dys)
}

func TestParseRejectsMalformedVariationTables(t *testing.T) {
	base := func() map[string][]byte {
		loca, glyf := glyfAndLoca([][]byte{nil, squareGlyph()}, false)
		return map[string][]byte{
			"head": headTable(1000, 0),
			"maxp": maxpTable(2),
			"hhea": hheaTable(800, -200, 100, 2),
			"hmtx": hmtxTable([]int{500, 500}, []int{0, 0}, 2),
			"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1})}),
			"loca": loca,
			"glyf": glyf,
		}
	}
	for _, tab := range []string{"fvar", "avar", "gvar"} {
		tb := base()
		tb[tab] = make([]byte, 3) // too short: the parser must reject the font
		if _, err := Parse(assemble(versionTrueType, tb)); err == nil {
			t.Fatalf("%s: expected parse error", tab)
		}
	}
}

func TestInstanceIntermediateTuple(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{0.5},
		intermediate: true, start: []float64{0.0}, end: []float64{1.0},
		dX: []int{10, 10, 10, 10, 0, 0, 0, 0},
		dY: []int{0, 0, 0, 0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)

	// wght 650 normalizes to 0.5 == peak: full application (+10 in x).
	cs, err := f.InstancePoints(1, map[string]float64{"wght": 650})
	if err != nil {
		t.Fatal(err)
	}
	eqPts(t, flat(cs), [][2]float64{{10, 0}, {110, 0}, {110, 100}, {10, 100}})
}

func TestInstanceLongOffsets(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{1, 2, 3, 4, 0, 0, 0, 0},
		dY: []int{0, 0, 0, 0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, true) // long offsets
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)
	cs, err := f.InstancePoints(1, map[string]float64{"wght": 900})
	if err != nil {
		t.Fatal(err)
	}
	eqPts(t, flat(cs), [][2]float64{{1, 0}, {102, 0}, {103, 100}, {4, 100}})
}

// --- applyVariation edge cases ----------------------------------------------

func TestApplyVariationEdges(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{10, 0, 0, 0, 0, 0, 0, 0},
		dY: []int{0, 0, 0, 0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)

	pts := []outlinePoint{{0, 0, true}, {100, 0, true}, {100, 100, true}, {0, 100, true}}
	ends := []int{3}

	// A norm shorter than the axis count leaves the missing axis at 0.
	if got := f.applyVariation(1, pts, ends, []int16{}); got[0].x != 0 {
		t.Fatalf("short norm applied a delta: %+v", got[0])
	}
	// Negative and out-of-range glyph ids are no-ops.
	if got := f.applyVariation(-1, pts, ends, []int16{16384}); got[0].x != 0 {
		t.Fatal("negative gid varied")
	}
	if got := f.applyVariation(999, pts, ends, []int16{16384}); got[0].x != 0 {
		t.Fatal("out-of-range gid varied")
	}

	// A gvar whose offsets point past the data is ignored.
	bad := &Font{gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 100}, data: make([]byte, 10)}}
	if got := bad.applyVariation(0, pts, ends, []int16{16384}); got[0].x != 0 {
		t.Fatal("overflowing offset varied")
	}

	// A font without gvar returns the outline unchanged.
	static := mustParse(t, stdBytes(false, false))
	if got := static.applyVariation(1, pts, ends, nil); got[0].x != 0 {
		t.Fatal("static font varied")
	}
}

func TestInstancePointsBadGlyph(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil)
	if _, err := f.InstancePoints(999, nil); err == nil {
		t.Fatal("expected error for out-of-range glyph")
	}
}

// --- malformed glyph variation data -----------------------------------------

func TestGvarMalformedGlyphData(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	def := [][2]float64{{0, 0}, {100, 0}, {100, 100}, {0, 100}}
	cases := []struct {
		name string
		blob []byte
	}{
		{"header", []byte{0x00, 0x01, 0x00, 0x04}},
		{"shared", []byte{0x80, 0x01, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00}},
		{"tuple", []byte{0x00, 0x01, 0x00, 0x08, 0x00, 0x01, 0x20, 0x00, 0x00}},
		{"sharedIndex", buildGlyphVarData(nil, false, false, []rawTuple{{
			sharedIdx: 5, private: true, privateAll: true,
			dX: make([]int, 8), dY: make([]int, 8),
		}})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gv := buildGvar(1, [][]float64{{1.0}}, [][]byte{nil, c.blob}, false)
			f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil)
			cs, err := f.InstancePoints(1, map[string]float64{"wght": 900})
			if err != nil {
				t.Fatal(err)
			}
			eqPts(t, flat(cs), def) // malformed data -> unchanged outline
		})
	}
}

// --- direct parse-error coverage --------------------------------------------

func TestParseFvarErrors(t *testing.T) {
	cases := map[string][]byte{
		"header":   make([]byte, 4),
		"axis":     buildFvarHdr(1000, 1, 0, 20, 4),
		"instance": buildFvarHdr(16, 0, 1, 20, 4),
	}
	for name, b := range cases {
		if err := (&Font{}).parseFvar(b); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

// buildFvarHdr writes just an fvar header (no axis/instance bodies) for the
// error tests.
func buildFvarHdr(axesOffset, axisCount, instCount, axisSize, instSize int) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(0)
	w.u16(uint16(axesOffset))
	w.u16(2)
	w.u16(uint16(axisCount))
	w.u16(uint16(axisSize))
	w.u16(uint16(instCount))
	w.u16(uint16(instSize))
	return w.bytes()
}

func TestParseAvarError(t *testing.T) {
	b := &bw{}
	b.u16(1)
	b.u16(0)
	b.u16(0)
	b.u16(1) // axisCount 1 but no segment data follows
	if err := (&Font{}).parseAvar(b.bytes()); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseGvarErrors(t *testing.T) {
	// Short header.
	if err := (&Font{}).parseGvar(make([]byte, 8)); err == nil {
		t.Fatal("header: expected error")
	}
	// Valid header, glyphCount 1 but no offset bytes.
	h := gvarHdr(1, 0, 20, 1, 0, 20)
	if err := (&Font{}).parseGvar(h); err == nil {
		t.Fatal("offsets: expected error")
	}
	// Valid header + offsets, but sharedTuplesOffset points past the data.
	h2 := gvarHdr(1, 1, 9999, 0, 0, 20)
	h2 = append(h2, 0, 0) // one uint16 offset for glyphCount 0 (+1 entry)
	if err := (&Font{}).parseGvar(h2); err == nil {
		t.Fatal("shared tuples: expected error")
	}
}

// gvarHdr writes a 20-byte gvar header.
func gvarHdr(axisCount, sharedCount, sharedOff, glyphCount, flags, dataOff int) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(0)
	w.u16(uint16(axisCount))
	w.u16(uint16(sharedCount))
	w.u32(uint32(sharedOff))
	w.u16(uint16(glyphCount))
	w.u16(uint16(flags))
	w.u32(uint32(dataOff))
	return w.bytes()
}

// --- direct helper coverage -------------------------------------------------

func TestSupportScalar(t *testing.T) {
	cases := []struct {
		coords, lo, pk, up []float64
		want               float64
	}{
		{[]float64{0.5}, []float64{0}, []float64{0}, []float64{0}, 1},      // peak 0 -> axis ignored
		{[]float64{1}, []float64{0}, []float64{1}, []float64{1}, 1},        // at peak
		{[]float64{0.5}, []float64{0}, []float64{1}, []float64{1}, 0.5},    // below peak
		{[]float64{-0.5}, []float64{0}, []float64{1}, []float64{1}, 0},     // outside (<= lower)
		{[]float64{1.0}, []float64{0}, []float64{0.5}, []float64{1}, 0},    // outside (>= upper)
		{[]float64{0.75}, []float64{0}, []float64{0.5}, []float64{1}, 0.5}, // above peak (intermediate)
		{[]float64{0.3}, []float64{0.8}, []float64{0.5}, []float64{1}, 1},  // lower > peak -> ignored
		{[]float64{0.3}, []float64{-1}, []float64{0.5}, []float64{1}, 1},   // region spans zero -> ignored
	}
	for i, c := range cases {
		if got := supportScalar(c.coords, c.lo, c.pk, c.up); got != c.want {
			t.Fatalf("case %d: supportScalar = %g, want %g", i, got, c.want)
		}
	}
}

func TestIupContour(t *testing.T) {
	// No touched point: contour stays put.
	dx := make([]float64, 4)
	dy := make([]float64, 4)
	pts := []outlinePoint{{0, 0, true}, {100, 0, true}, {100, 100, true}, {0, 100, true}}
	iupContour(pts, dx, dy, []bool{false, false, false, false})
	for i := range dx {
		if dx[i] != 0 || dy[i] != 0 {
			t.Fatalf("untouched contour moved at %d", i)
		}
	}

	// One touched point: the whole contour shifts by it.
	dx = []float64{0, 7, 0, 0}
	dy = []float64{0, 9, 0, 0}
	iupContour(pts, dx, dy, []bool{false, true, false, false})
	for i := range dx {
		if dx[i] != 7 || dy[i] != 9 {
			t.Fatalf("single-touch broadcast wrong at %d: (%g,%g)", i, dx[i], dy[i])
		}
	}

	// Two touched points with a strictly-interior untouched point (interp) and
	// a wrap-around untouched point.
	mid := []outlinePoint{{0, 0, true}, {50, 0, true}, {100, 0, true}, {50, 0, true}}
	dx = []float64{10, 0, 30, 0}
	dy = []float64{0, 0, 0, 0}
	iupContour(mid, dx, dy, []bool{true, false, true, false})
	if dx[1] != 20 || dx[3] != 20 {
		t.Fatalf("iup interpolation wrong: %v", dx)
	}
}

func TestIupInterpBounds(t *testing.T) {
	// target below the lower coordinate.
	if got := iupInterp(-5, 0, 100, 10, 30); got != 10 {
		t.Fatalf("below = %g", got)
	}
	// target above the upper coordinate, with swapped bounds.
	if got := iupInterp(200, 100, 0, 30, 10); got != 30 {
		t.Fatalf("above (swapped) = %g", got)
	}
}

func TestAccumulateOutOfRangePoint(t *testing.T) {
	accX := make([]float64, 8)
	accY := make([]float64, 8)
	pts := []outlinePoint{{0, 0, true}, {100, 0, true}, {100, 100, true}, {0, 100, true}}
	// Point index 1000 is out of range and must be skipped.
	accumulate(1, false, []int{0, 1000}, []float64{5, 99}, []float64{0, 0},
		[]int{3}, pts, accX, accY, true)
	if accX[0] != 5 {
		t.Fatalf("valid point not accumulated: %v", accX)
	}
}

func TestReadPackedPoints(t *testing.T) {
	// Two-byte count with a word-mode run.
	r := reader{b: []byte{0x80, 0x03, 0x82, 0x00, 0x01, 0x00, 0x01, 0x00, 0x01}}
	pts, all := readPackedPoints(&r)
	if all || len(pts) != 3 || pts[0] != 1 || pts[1] != 2 || pts[2] != 3 {
		t.Fatalf("word points = %v all=%v", pts, all)
	}
	// "All points" encoding.
	r = reader{b: []byte{0x00}}
	if _, all := readPackedPoints(&r); !all {
		t.Fatal("expected all-points")
	}
	// Truncated mid-run.
	r = reader{b: []byte{0x03, 0x02, 0x05}}
	if _, _ = readPackedPoints(&r); r.err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestReadPackedDeltas(t *testing.T) {
	// Zero run.
	r := reader{b: []byte{0x82}}
	if d := readPackedDeltas(&r, 3); len(d) != 3 || d[0] != 0 || d[2] != 0 {
		t.Fatalf("zeros = %v", d)
	}
	// Word run: +10, -10.
	r = reader{b: []byte{0x41, 0x00, 0x0A, 0xFF, 0xF6}}
	if d := readPackedDeltas(&r, 2); d[0] != 10 || d[1] != -10 {
		t.Fatalf("words = %v", d)
	}
	// Byte run: +5, -5.
	r = reader{b: []byte{0x01, 0x05, 0xFB}}
	if d := readPackedDeltas(&r, 2); d[0] != 5 || d[1] != -5 {
		t.Fatalf("bytes = %v", d)
	}
	// Truncated.
	r = reader{b: []byte{0x01, 0x05}}
	if _ = readPackedDeltas(&r, 2); r.err == nil {
		t.Fatal("expected truncation error")
	}
}
