// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

// This file synthesises HVAR, VVAR and MVAR tables (mirroring variable_test.go)
// so metric variation is exercised deterministically, including every parse and
// delta branch and the gvar phantom-point advance fallback. No external font is
// used.

// --- ItemVariationStore / DeltaSetIndexMap builders -------------------------

// ivDataSpec describes one ItemVariationData subtable: its region ordering, the
// LONG_WORDS flag, the count of wide leading columns, and the item delta rows.
type ivDataSpec struct {
	regionIndices []int
	longWords     bool
	wordCount     int
	rows          [][]int
}

// buildItemVarStore encodes a format-1 ItemVariationStore. regions[i][a] is the
// {start, peak, end} of region i on axis a.
func buildItemVarStore(axisCount int, regions [][][3]float64, datas []ivDataSpec) []byte {
	rl := &bw{}
	rl.u16(uint16(axisCount))
	rl.u16(uint16(len(regions)))
	for _, reg := range regions {
		for a := 0; a < axisCount; a++ {
			rl.i16(f2dot14bytes(reg[a][0]))
			rl.i16(f2dot14bytes(reg[a][1]))
			rl.i16(f2dot14bytes(reg[a][2]))
		}
	}

	ivdBlobs := make([][]byte, len(datas))
	for i, d := range datas {
		w := &bw{}
		w.u16(uint16(len(d.rows)))
		wdc := uint16(d.wordCount)
		if d.longWords {
			wdc |= 0x8000
		}
		w.u16(wdc)
		w.u16(uint16(len(d.regionIndices)))
		for _, ri := range d.regionIndices {
			w.u16(uint16(ri))
		}
		for _, row := range d.rows {
			for j, v := range row {
				wide := j < d.wordCount
				switch {
				case d.longWords && wide:
					w.u32(uint32(int32(v)))
				case d.longWords || wide:
					w.i16(int16(v))
				default:
					w.u8(uint8(int8(v)))
				}
			}
		}
		ivdBlobs[i] = w.bytes()
	}

	headerLen := 2 + 4 + 2 + 4*len(datas)
	body := &bw{}
	body.b = append(body.b, rl.bytes()...)
	ivdOffs := make([]int, len(datas))
	for i, blob := range ivdBlobs {
		ivdOffs[i] = headerLen + len(body.b)
		body.b = append(body.b, blob...)
	}
	h := &bw{}
	h.u16(1)
	h.u32(uint32(headerLen)) // region list first in body
	h.u16(uint16(len(datas)))
	for _, o := range ivdOffs {
		h.u32(uint32(o))
	}
	return append(h.bytes(), body.bytes()...)
}

// buildDeltaSetMap encodes a DeltaSetIndexMap. entries[i] is {outer, inner}.
func buildDeltaSetMap(format, innerBits, entrySize int, entries [][2]int) []byte {
	w := &bw{}
	w.u8(uint8(format))
	w.u8(uint8((innerBits-1)&0x0F) | uint8(((entrySize-1)&0x03)<<4))
	if format == 0 {
		w.u16(uint16(len(entries)))
	} else {
		w.u32(uint32(len(entries)))
	}
	for _, e := range entries {
		entry := uint32(e[0])<<uint(innerBits) | uint32(e[1])
		for k := entrySize - 1; k >= 0; k-- {
			w.u8(uint8(entry >> (8 * k)))
		}
	}
	return w.bytes()
}

// buildHVAR encodes an HVAR table from a store and an optional advance-width
// map (lsb/rsb maps omitted).
func buildHVAR(store, advMap []byte) []byte {
	const headerLen = 20
	w := &bw{}
	w.u16(1)
	w.u16(0)
	w.u32(headerLen) // itemVariationStore offset
	if advMap != nil {
		w.u32(uint32(headerLen + len(store)))
	} else {
		w.u32(0)
	}
	w.u32(0) // lsbMapping
	w.u32(0) // rsbMapping
	w.b = append(w.b, store...)
	w.b = append(w.b, advMap...)
	return w.bytes()
}

// buildVVAR encodes a VVAR table from a store and an optional advance-height
// map (tsb/bsb/vorg maps omitted).
func buildVVAR(store, advMap []byte) []byte {
	const headerLen = 24
	w := &bw{}
	w.u16(1)
	w.u16(0)
	w.u32(headerLen) // itemVariationStore offset
	if advMap != nil {
		w.u32(uint32(headerLen + len(store)))
	} else {
		w.u32(0)
	}
	w.u32(0) // tsbMapping
	w.u32(0) // bsbMapping
	w.u32(0) // vOrgMapping
	w.b = append(w.b, store...)
	w.b = append(w.b, advMap...)
	return w.bytes()
}

type mvarRec struct {
	tag          string
	outer, inner int
}

// buildMVAR encodes an MVAR table from an optional store and value records.
func buildMVAR(store []byte, recs []mvarRec) []byte {
	const headerLen = 12
	const valueRecordSize = 8
	storeOff := 0
	if store != nil {
		storeOff = headerLen + valueRecordSize*len(recs)
	}
	w := &bw{}
	w.u16(1)
	w.u16(0)
	w.u16(0) // reserved
	w.u16(valueRecordSize)
	w.u16(uint16(len(recs)))
	w.u16(uint16(storeOff))
	for _, r := range recs {
		w.b = append(w.b, []byte(r.tag)...)
		w.u16(uint16(r.outer))
		w.u16(uint16(r.inner))
	}
	w.b = append(w.b, store...)
	return w.bytes()
}

// makeMetricFont assembles a variable glyf font (weight axis) with the given
// optional variation tables. vhea/vmtx are always present so vertical advances
// (and their VVAR deltas) have a base.
func makeMetricFont(t *testing.T, glyphs [][]byte, fvarB, gvarB, hvarB, vvarB, mvarB []byte) *Font {
	t.Helper()
	n := len(glyphs)
	loca, glyf := glyfAndLoca(glyphs, false)
	m := map[rune]uint16{}
	adv := make([]int, n)
	lsb := make([]int, n)
	vadv := make([]int, n)
	tsb := make([]int, n)
	for i := 0; i < n; i++ {
		m[rune('A'+i)] = uint16(i)
		adv[i] = 500
		vadv[i] = 700
	}
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 100, n),
		"hmtx": hmtxTable(adv, lsb, n),
		"vhea": vheaTable(880, -120, 90, n),
		"vmtx": vmtxTable(vadv, tsb, n),
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
	if hvarB != nil {
		tables["HVAR"] = hvarB
	}
	if vvarB != nil {
		tables["VVAR"] = vvarB
	}
	if mvarB != nil {
		tables["MVAR"] = mvarB
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

// wghtPeakRegion is a single region peaking at wght = +1 (max).
func wghtPeakRegion() [][][3]float64 { return [][][3]float64{{{0, 1, 1}}} }

// --- HVAR -------------------------------------------------------------------

func TestHVARAdvanceVaries(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// Region peaks at wght max; glyph 1 gains +200 advance there.
	store := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
		regionIndices: []int{0}, wordCount: 1, // 16-bit deltas (200 > int8)
		rows: [][]int{{0}, {200}}, // inner 0 -> 0, inner 1 -> 200
	}})
	advMap := buildDeltaSetMap(0, 1, 2, [][2]int{{0, 0}, {0, 1}}) // glyph1 -> (0,1)
	hv := buildHVAR(store, advMap)
	f := makeMetricFont(t, [][]byte{nil, squareGlyph()}, fv, nil, hv, nil, nil)

	fc := f.NewFace(1000) // scale 1: font units == pixels
	if got := fc.Advance('B'); got != 500 {
		t.Fatalf("default advance = %d, want 500", got)
	}
	fc.SetVariation(map[string]float64{"wght": 900})
	if got := fc.Advance('B'); got != 700 {
		t.Fatalf("varied advance = %d, want 700", got)
	}
	// Half weight -> half the delta.
	fc.SetVariation(map[string]float64{"wght": 650})
	if got := fc.Advance('B'); got != 600 {
		t.Fatalf("half-varied advance = %d, want 600", got)
	}
	// Measure sums varied advances.
	if got := fc.Measure("BB"); got != 1200 {
		t.Fatalf("Measure = %d, want 1200", got)
	}
	// Back to default: byte-identical to the base advance.
	fc.SetVariation(nil)
	if got := fc.Advance('B'); got != 500 {
		t.Fatalf("reset advance = %d, want 500", got)
	}
}

func TestHVARImplicitGidMap(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// No advance map: glyph index is used directly as the inner index.
	store := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
		regionIndices: []int{0},
		rows:          [][]int{{0}, {120}},
	}})
	hv := buildHVAR(store, nil)
	f := makeMetricFont(t, [][]byte{nil, squareGlyph()}, fv, nil, hv, nil, nil)

	fc := f.NewFace(1000)
	fc.SetVariation(map[string]float64{"wght": 900})
	if got := fc.Advance('B'); got != 620 { // glyph 1 -> inner 1 -> +120
		t.Fatalf("implicit-map advance = %d, want 620", got)
	}
}

func TestHVARGlyphMaskAdvances(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	store := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
		regionIndices: []int{0}, wordCount: 1,
		rows: [][]int{{0}, {200}},
	}})
	advMap := buildDeltaSetMap(0, 1, 2, [][2]int{{0, 0}, {0, 1}})
	hv := buildHVAR(store, advMap)
	f := makeMetricFont(t, [][]byte{nil, squareGlyph()}, fv, nil, hv, nil, nil)

	fc := f.NewFace(1000)
	fc.SetVariation(map[string]float64{"wght": 900})
	if _, _, _, adv, ok := fc.GlyphMask('B', 0, 0); !ok || adv != 700 {
		t.Fatalf("GlyphMask advance = %d ok=%v, want 700", adv, ok)
	}
	if _, _, _, adv, ok := fc.GlyphMaskIndex(1, 0, 0); !ok || adv != 700 {
		t.Fatalf("GlyphMaskIndex advance = %d ok=%v, want 700", adv, ok)
	}
}

// --- VVAR -------------------------------------------------------------------

func TestVVARAdvanceVaries(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	store := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
		regionIndices: []int{0},
		rows:          [][]int{{0}, {80}},
	}})
	advMap := buildDeltaSetMap(1, 1, 2, [][2]int{{0, 0}, {0, 1}}) // format 1
	vv := buildVVAR(store, advMap)
	f := makeMetricFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil, vv, nil)

	fc := f.NewFace(1000)
	if got := fc.VerticalAdvance('B'); got != 700 {
		t.Fatalf("default vadvance = %d, want 700", got)
	}
	fc.SetVariation(map[string]float64{"wght": 900})
	if got := fc.VerticalAdvance('B'); got != 780 {
		t.Fatalf("varied vadvance = %d, want 780", got)
	}
}

// --- MVAR -------------------------------------------------------------------

func TestMVARMetrics(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	store := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
		regionIndices: []int{0},
		rows:          [][]int{{40}, {-30}}, // inner 0 -> ascender, inner 1 -> descender
	}})
	// hasc uses (0,0)=+40, hdsc uses (0,1)=-30; hlgp absent -> a lookup miss.
	mv := buildMVAR(store, []mvarRec{{"hasc", 0, 0}, {"hdsc", 0, 1}})
	f := makeMetricFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil, nil, mv)

	fc := f.NewFace(1000)
	base := fc.Metrics()
	if base.Ascent != 800 || base.Descent != 200 {
		t.Fatalf("base metrics = %+v", base)
	}
	fc.SetVariation(map[string]float64{"wght": 900})
	got := fc.Metrics()
	// ascender 800+40=840; descender -200-30=-230 -> Descent 230; lineGap 100.
	if got.Ascent != 840 || got.Descent != 230 {
		t.Fatalf("varied metrics = %+v", got)
	}
	// Height = asc - desc + gap = 840 - (-230) + 100 = 1170.
	if got.Height != 1170 {
		t.Fatalf("varied height = %d, want 1170", got.Height)
	}
}

// --- gvar phantom-point advance fallback ------------------------------------

func TestGvarAdvanceFallback(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// Glyph 1: a square with 4 real points + 4 phantom points. The advance-width
	// phantom (index 5 = pp2) gains +60 in x at max weight; the left phantom
	// (index 4 = pp1) stays. No HVAR table -> advance falls back to gvar.
	gd := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{0, 0, 0, 0, 0, 60, 0, 0}, // pp2.x delta = 60
		dY: []int{0, 0, 0, 0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd}, false)
	f := makeMetricFont(t, [][]byte{nil, squareGlyph()}, fv, gv, nil, nil, nil)

	fc := f.NewFace(1000)
	if got := fc.Advance('B'); got != 500 {
		t.Fatalf("default fallback advance = %d, want 500", got)
	}
	fc.SetVariation(map[string]float64{"wght": 900})
	if got := fc.Advance('B'); got != 560 {
		t.Fatalf("fallback advance = %d, want 560", got)
	}
	// Empty glyph 0: no fallback delta.
	if got := f.gvarAdvanceWidthDelta(0, f.NormalizeCoords(map[string]float64{"wght": 900})); got != 0 {
		t.Fatalf("empty glyph fallback = %g, want 0", got)
	}
}

func TestGvarAdvanceFallbackComposite(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	// Glyph 2 is composite; its advance fallback is out of scope -> 0.
	gd1 := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{0, 0, 0, 0, 0, 10, 0, 0}, dY: []int{0, 0, 0, 0, 0, 0, 0, 0},
	}})
	gd2 := buildGlyphVarData(nil, true, true, []rawTuple{{
		embedPeak: true, peak: []float64{1.0},
		dX: []int{0, 0, 0, 0, 0}, dY: []int{0, 0, 0, 0, 0},
	}})
	gv := buildGvar(1, nil, [][]byte{nil, gd1, gd2}, false)
	comp := compositeGlyphBytes([]component{{
		glyphIndex: 1, arg1: 0, arg2: 0, useWords: true, argsAreXY: true,
	}})
	f := makeMetricFont(t, [][]byte{nil, squareGlyph(), comp}, fv, gv, nil, nil, nil)
	norm := f.NormalizeCoords(map[string]float64{"wght": 900})
	if got := f.gvarAdvanceWidthDelta(2, norm); got != 0 {
		t.Fatalf("composite fallback = %g, want 0", got)
	}
}

func TestGvarAdvanceFallbackEdges(t *testing.T) {
	norm := []int16{16384}
	// No gvar table.
	if got := (&Font{}).gvarAdvanceWidthDelta(0, norm); got != 0 {
		t.Fatal("no-gvar fallback nonzero")
	}
	// Glyph id out of range.
	f := &Font{gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 4}}}
	if got := f.gvarAdvanceWidthDelta(9, norm); got != 0 {
		t.Fatal("oob gid fallback nonzero")
	}
	// glyf end past the table.
	g := &Font{
		gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 4}},
		loca: []uint32{0, 100},
		glyf: make([]byte, 10),
	}
	if got := g.gvarAdvanceWidthDelta(0, norm); got != 0 {
		t.Fatal("glyf-overrun fallback nonzero")
	}
	// A simple-glyph header whose body is truncated (simpleGlyph fails).
	badGlyf := &bw{}
	badGlyf.i16(1) // one contour
	badGlyf.bbox() // 8 bytes
	badGlyf.u16(0) // endPtsOfContours[0] = 0 -> 1 point
	badGlyf.u16(0) // instructionLength
	// no flags/coords follow -> truncated
	h := &Font{
		gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 4}},
		loca: []uint32{0, uint32(len(badGlyf.bytes()))},
		glyf: badGlyf.bytes(),
	}
	if got := h.gvarAdvanceWidthDelta(0, norm); got != 0 {
		t.Fatal("truncated simple glyph fallback nonzero")
	}
	// Valid simple glyph but the gvar data range is empty (start >= end).
	okGlyf := squareGlyph()
	e := &Font{
		gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, 0}, data: make([]byte, 8)},
		loca: []uint32{0, uint32(len(okGlyf))},
		glyf: okGlyf,
	}
	if got := e.gvarAdvanceWidthDelta(0, norm); got != 0 {
		t.Fatal("empty gvar range fallback nonzero")
	}
	// Valid simple glyph but malformed gvar data (applyGlyph errors).
	blob := buildGlyphVarData(nil, false, false, []rawTuple{{
		sharedIdx: 5, private: true, privateAll: true,
		dX: make([]int, 8), dY: make([]int, 8),
	}})
	m := &Font{
		gvar: &gvarTable{axisCount: 1, dataOffsets: []uint32{0, uint32(len(blob))}, data: blob},
		loca: []uint32{0, uint32(len(okGlyf))},
		glyf: okGlyf,
	}
	if got := m.gvarAdvanceWidthDelta(0, []int16{}); got != 0 { // short norm -> coords stay default
		t.Fatal("malformed gvar fallback nonzero")
	}
}

// --- unit coverage: store / map / delta -------------------------------------

func TestMetricVarStoreDelta(t *testing.T) {
	// Two regions on one axis; a subtable mixing 8/16/32-bit deltas.
	store := &metricVarStore{
		axisCount: 1,
		regLower:  [][]float64{{0}, {-1}},
		regPeak:   [][]float64{{1}, {-1}},
		regUpper:  [][]float64{{1}, {0}},
		data: []metricVarData{{
			regionIndices: []int{0, 1},
			deltaSets:     [][]int32{{10, 20}},
		}},
	}
	// At wght +1 only region 0 applies (scalar 1): delta = 10.
	if got := store.delta(0, 0, []int16{16384}); got != 10 {
		t.Fatalf("delta at +1 = %g, want 10", got)
	}
	// At wght -1 only region 1 applies: delta = 20.
	if got := store.delta(0, 0, []int16{-16384}); got != 20 {
		t.Fatalf("delta at -1 = %g, want 20", got)
	}
	// Out-of-range outer and inner both yield 0.
	if store.delta(5, 0, nil) != 0 || store.delta(0, 5, nil) != 0 {
		t.Fatal("out-of-range indices returned nonzero")
	}
}

func TestReadVarDeltaWidths(t *testing.T) {
	// LONG_WORDS, wide column: 32-bit.
	r := reader{b: []byte{0x00, 0x01, 0x00, 0x00}}
	if got := readVarDelta(&r, true, true); got != 65536 {
		t.Fatalf("long wide = %d, want 65536", got)
	}
	// LONG_WORDS, narrow column: 16-bit.
	r = reader{b: []byte{0xFF, 0xFF}}
	if got := readVarDelta(&r, true, false); got != -1 {
		t.Fatalf("long narrow = %d, want -1", got)
	}
	// short mode, wide column: 16-bit.
	r = reader{b: []byte{0x00, 0x05}}
	if got := readVarDelta(&r, false, true); got != 5 {
		t.Fatalf("short wide = %d, want 5", got)
	}
	// short mode, narrow column: 8-bit.
	r = reader{b: []byte{0xFB}}
	if got := readVarDelta(&r, false, false); got != -5 {
		t.Fatalf("short narrow = %d, want -5", got)
	}
}

func TestDeltaSetMapLookup(t *testing.T) {
	// nil map -> identity.
	var nilMap *deltaSetMap
	if o, i := nilMap.lookup(7); o != 0 || i != 7 {
		t.Fatalf("nil map lookup = (%d,%d), want (0,7)", o, i)
	}
	// empty map -> identity.
	empty := &deltaSetMap{}
	if o, i := empty.lookup(3); o != 0 || i != 3 {
		t.Fatalf("empty map lookup = (%d,%d), want (0,3)", o, i)
	}
	// populated map, in range and clamped past the end.
	m := &deltaSetMap{outer: []uint32{1, 2}, inner: []uint32{3, 4}}
	if o, i := m.lookup(1); o != 2 || i != 4 {
		t.Fatalf("lookup(1) = (%d,%d), want (2,4)", o, i)
	}
	if o, i := m.lookup(9); o != 2 || i != 4 {
		t.Fatalf("clamped lookup = (%d,%d), want (2,4)", o, i)
	}
}

func TestMVARDeltaBranches(t *testing.T) {
	// Tag miss.
	m := &mvarTable{values: map[string][2]int{}}
	if got := m.delta("hasc", nil); got != 0 {
		t.Fatalf("tag miss = %g", got)
	}
	// Tag present but no store.
	m = &mvarTable{values: map[string][2]int{"hasc": {0, 0}}}
	if got := m.delta("hasc", nil); got != 0 {
		t.Fatalf("no-store = %g", got)
	}
}

func TestHorizontalAdvanceDeltaNilNorm(t *testing.T) {
	// A static font: nil norm short-circuits to zero.
	if got := (&Font{}).horizontalAdvanceDelta(0, nil); got != 0 {
		t.Fatalf("nil-norm h delta = %g", got)
	}
	if got := (&Font{}).verticalAdvanceDelta(0, nil); got != 0 {
		t.Fatalf("nil-norm v delta = %g", got)
	}
	// varCoords set on a static font: NormalizeCoords is nil, so no delta.
	f := mustParse(t, stdBytes(false, false))
	fc := f.NewFace(1000)
	fc.SetVariation(map[string]float64{"wght": 900})
	if got := fc.Advance('A'); got != f.NewFace(1000).Advance('A') {
		t.Fatal("static font advance changed under SetVariation")
	}
}

func TestMVARStoreOffsetZero(t *testing.T) {
	// An MVAR with no value records and no store parses to an empty table.
	f := &Font{}
	if err := f.parseMVAR(buildMVAR(nil, nil)); err != nil {
		t.Fatal(err)
	}
	if f.mvar == nil || f.mvar.store != nil || len(f.mvar.values) != 0 {
		t.Fatalf("empty MVAR = %+v", f.mvar)
	}
}

// --- parse-error coverage ---------------------------------------------------

func TestParseMetricVarErrors(t *testing.T) {
	goodStore := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
		regionIndices: []int{0}, rows: [][]int{{5}},
	}})

	t.Run("hvar-header", func(t *testing.T) {
		if err := (&Font{}).parseHVAR(make([]byte, 4)); err == nil {
			t.Fatal("expected header error")
		}
	})
	t.Run("hvar-store", func(t *testing.T) {
		// storeOffset points past the data.
		w := &bw{}
		w.u16(1)
		w.u16(0)
		w.u32(9999)
		w.u32(0)
		w.u32(0)
		w.u32(0)
		if err := (&Font{}).parseHVAR(w.bytes()); err == nil {
			t.Fatal("expected store error")
		}
	})
	t.Run("hvar-map", func(t *testing.T) {
		// Valid store but a map offset with an unsupported format byte.
		badMap := []byte{0x09, 0x00, 0x00, 0x00} // format 9
		hv := buildHVAR(goodStore, badMap)
		if err := (&Font{}).parseHVAR(hv); err == nil {
			t.Fatal("expected map error")
		}
	})
	t.Run("vvar-store", func(t *testing.T) {
		w := &bw{}
		w.u16(1)
		w.u16(0)
		w.u32(9999)
		w.u32(0)
		w.u32(0)
		w.u32(0)
		w.u32(0)
		if err := (&Font{}).parseVVAR(w.bytes()); err == nil {
			t.Fatal("expected vvar store error")
		}
	})
	t.Run("mvar-header", func(t *testing.T) {
		if err := (&Font{}).parseMVAR(make([]byte, 4)); err == nil {
			t.Fatal("expected mvar header error")
		}
	})
	t.Run("mvar-record", func(t *testing.T) {
		// Header claims one record but the body is missing.
		w := &bw{}
		w.u16(1)
		w.u16(0)
		w.u16(0)
		w.u16(8) // valueRecordSize
		w.u16(1) // valueRecordCount
		w.u16(0) // storeOffset
		if err := (&Font{}).parseMVAR(w.bytes()); err == nil {
			t.Fatal("expected mvar record error")
		}
	})
	t.Run("mvar-store", func(t *testing.T) {
		mv := buildMVAR(goodStore, []mvarRec{{"hasc", 0, 0}})
		// Corrupt the store format so parseMetricVarStore rejects it.
		mv[12+8] = 0x00 // first store byte (format high byte) -> format 0
		mv[12+8+1] = 0x02
		if err := (&Font{}).parseMVAR(mv); err == nil {
			t.Fatal("expected mvar store error")
		}
	})
	t.Run("ivs-format", func(t *testing.T) {
		bad := []byte{0x00, 0x02} // format 2
		if _, err := parseMetricVarStore(bad, 0); err == nil {
			t.Fatal("expected ivs format error")
		}
	})
	t.Run("ivs-header", func(t *testing.T) {
		bad := []byte{0x00, 0x01} // format 1 then truncated
		if _, err := parseMetricVarStore(bad, 0); err == nil {
			t.Fatal("expected ivs header error")
		}
	})
	t.Run("ivs-regions", func(t *testing.T) {
		// Region list offset points past the data.
		w := &bw{}
		w.u16(1)
		w.u32(9999) // regionListOffset
		w.u16(0)    // ivdCount
		if _, err := parseMetricVarStore(w.bytes(), 0); err == nil {
			t.Fatal("expected ivs regions error")
		}
	})
	t.Run("ivd-region-index", func(t *testing.T) {
		store := buildItemVarStore(1, wghtPeakRegion(), []ivDataSpec{{
			regionIndices: []int{9}, rows: [][]int{{1}}, // region 9 out of range
		}})
		if _, err := parseMetricVarStore(store, 0); err == nil {
			t.Fatal("expected region-index error")
		}
	})
	t.Run("ivd-header", func(t *testing.T) {
		// A store whose single IVD subtable is truncated in its header. The
		// region list (offset 12) is valid so the IVD offset (200, past the data)
		// is what fails.
		w := &bw{}
		w.u16(1)
		w.u32(12)  // regionListOffset (immediately after the 12-byte header)
		w.u16(1)   // ivdCount
		w.u32(200) // ivd offset past the data
		w.u16(1)   // axisCount
		w.u16(0)   // regionCount
		if _, err := parseMetricVarStore(w.bytes(), 0); err == nil {
			t.Fatal("expected ivd header error")
		}
	})
	t.Run("ivd-wordcount", func(t *testing.T) {
		// wordDeltaCount (1) exceeds regionIndexCount (0).
		rl := &bw{}
		rl.u16(1) // axisCount
		rl.u16(0) // regionCount
		ivd := &bw{}
		ivd.u16(1) // itemCount
		ivd.u16(1) // wordDeltaCount = 1
		ivd.u16(0) // regionIndexCount = 0
		headerLen := 2 + 4 + 2 + 4
		body := append(rl.bytes(), ivd.bytes()...)
		w := &bw{}
		w.u16(1)
		w.u32(uint32(headerLen))
		w.u16(1)
		w.u32(uint32(headerLen + len(rl.bytes())))
		w.b = append(w.b, body...)
		if _, err := parseMetricVarStore(w.bytes(), 0); err == nil {
			t.Fatal("expected wordcount error")
		}
	})
	t.Run("ivd-deltas", func(t *testing.T) {
		// A valid header promising deltas the body does not supply.
		rl := &bw{}
		rl.u16(1) // axisCount
		rl.u16(1) // regionCount
		rl.i16(f2dot14bytes(0))
		rl.i16(f2dot14bytes(1))
		rl.i16(f2dot14bytes(1))
		ivd := &bw{}
		ivd.u16(1) // itemCount
		ivd.u16(0) // wordDeltaCount
		ivd.u16(1) // regionIndexCount
		ivd.u16(0) // region index
		// no delta byte for the single item -> truncation
		headerLen := 2 + 4 + 2 + 4
		w := &bw{}
		w.u16(1)
		w.u32(uint32(headerLen))
		w.u16(1)
		w.u32(uint32(headerLen + len(rl.bytes())))
		w.b = append(w.b, rl.bytes()...)
		w.b = append(w.b, ivd.bytes()...)
		if _, err := parseMetricVarStore(w.bytes(), 0); err == nil {
			t.Fatal("expected deltas error")
		}
	})
	t.Run("map-truncated", func(t *testing.T) {
		// format 0 map claiming one entry but no entry bytes (a leading pad byte
		// keeps the offset nonzero, since offset 0 means "map absent").
		bad := []byte{0xFF, 0x00, 0x00, 0x00, 0x01}
		if _, err := parseDeltaSetMap(bad, 1); err == nil {
			t.Fatal("expected map truncation error")
		}
	})
}

func TestParseRejectsMalformedMetricTables(t *testing.T) {
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
	for _, tab := range []string{"HVAR", "VVAR", "MVAR"} {
		tb := base()
		tb[tab] = make([]byte, 3) // too short: the parser must reject the font
		if _, err := Parse(assemble(versionTrueType, tb)); err == nil {
			t.Fatalf("%s: expected parse error", tab)
		}
	}
}
