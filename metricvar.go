// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file implements the optional metric-variation tables HVAR, VVAR and
// MVAR, so a variable font's advance widths, advance heights and global font
// metrics track the axis coordinate rather than staying at the default master.
//
//   - HVAR (Horizontal Metrics Variations): per-glyph horizontal-advance deltas
//     (plus optional left/right side-bearing maps), applied by Face.Advance,
//     Face.Measure and the GlyphMask family.
//   - VVAR (Vertical Metrics Variations): the vertical analogue, per-glyph
//     advance-height deltas, applied by Face.VerticalAdvance.
//   - MVAR (Metrics Variations): global font-metric deltas keyed by four-byte
//     value tags (hasc/hdsc/hlgp/...), folded into Face.Metrics.
//
// All three share one machinery: an ItemVariationStore (the same format 1 store
// CFF2 references, but here the delta sets are fully decoded because the deltas
// come from the store rather than from a charstring) plus, per metric, an
// optional DeltaSetIndexMap translating a glyph index into an (outer, inner)
// pair addressing the store. The tent-function region evaluator supportScalar
// (gvar.go) is reused unchanged.
//
// When a variable glyf font ships no HVAR table, horizontal advances fall back
// to the per-glyph gvar phantom-point deltas the specification prescribes (see
// gvarAdvanceWidthDelta); this fallback is implemented for simple glyphs.
// Composite-glyph advance fallback is out of scope (it returns no delta).

// metricVarData is one ItemVariationData subtable: the ordered region indices
// its columns correspond to, and one fully-decoded delta row per item.
type metricVarData struct {
	regionIndices []int
	deltaSets     [][]int32 // itemCount rows, each len(regionIndices) deltas
}

// metricVarStore is a format-1 ItemVariationStore with its delta sets decoded.
// Each region is kept as parallel per-axis lower/peak/upper coordinate slices so
// the shared supportScalar evaluator can be called without repacking.
type metricVarStore struct {
	axisCount int
	regLower  [][]float64
	regPeak   [][]float64
	regUpper  [][]float64
	data      []metricVarData
}

// delta returns the interpolated delta for item (outer, inner) at the normalized
// coordinate norm (F2Dot14 int16 per axis, as produced by NormalizeCoords): the
// sum over the subtable's regions of the region's tent scalar times the stored
// delta. An out-of-range outer or inner index yields zero.
func (s *metricVarStore) delta(outer, inner int, norm []int16) float64 {
	if outer < 0 || outer >= len(s.data) {
		return 0
	}
	d := s.data[outer]
	if inner < 0 || inner >= len(d.deltaSets) {
		return 0
	}
	coords := make([]float64, s.axisCount)
	for i := range coords {
		if i < len(norm) {
			coords[i] = float64(norm[i]) / 16384.0
		}
	}
	row := d.deltaSets[inner]
	sum := 0.0
	for j, ri := range d.regionIndices {
		sum += supportScalar(coords, s.regLower[ri], s.regPeak[ri], s.regUpper[ri]) * float64(row[j])
	}
	return sum
}

// parseMetricVarStore decodes the ItemVariationStore at offset off in b, whose
// internal region-list and subtable offsets are relative to off. Only format 1
// is supported (the sole format defined for OpenType variations).
func parseMetricVarStore(b []byte, off int) (*metricVarStore, error) {
	r := reader{b: b, pos: off}
	if format := r.u16(); format != 1 {
		return nil, fmt.Errorf("opentype: ItemVariationStore: unsupported format %d", format)
	}
	regionListOff := off + int(r.u32())
	ivdCount := int(r.u16())
	ivdOffsets := make([]int, ivdCount)
	for i := range ivdOffsets {
		ivdOffsets[i] = off + int(r.u32())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: ItemVariationStore header: %w", r.err)
	}

	rr := reader{b: b, pos: regionListOff}
	axisCount := int(rr.u16())
	regionCount := int(rr.u16())
	lower := make([][]float64, regionCount)
	peak := make([][]float64, regionCount)
	upper := make([][]float64, regionCount)
	for i := 0; i < regionCount; i++ {
		lo := make([]float64, axisCount)
		pk := make([]float64, axisCount)
		up := make([]float64, axisCount)
		for a := 0; a < axisCount; a++ {
			lo[a] = rr.f2dot14()
			pk[a] = rr.f2dot14()
			up[a] = rr.f2dot14()
		}
		lower[i], peak[i], upper[i] = lo, pk, up
	}
	if rr.err != nil {
		return nil, fmt.Errorf("opentype: ItemVariationStore regions: %w", rr.err)
	}

	data := make([]metricVarData, ivdCount)
	for i, o := range ivdOffsets {
		d, err := parseItemVarData(b, o, regionCount)
		if err != nil {
			return nil, err
		}
		data[i] = d
	}
	return &metricVarStore{axisCount: axisCount, regLower: lower, regPeak: peak, regUpper: upper, data: data}, nil
}

// parseItemVarData decodes one ItemVariationData subtable at offset off, reading
// its region ordering and the full grid of item deltas. regionCount bounds the
// region indices. The wordDeltaCount field's high bit (LONG_WORDS) and its low
// 15 bits (the count of wide leading columns) select each delta's byte width.
func parseItemVarData(b []byte, off, regionCount int) (metricVarData, error) {
	r := reader{b: b, pos: off}
	itemCount := int(r.u16())
	wordDeltaCount := int(r.u16())
	regionIndexCount := int(r.u16())
	idx := make([]int, regionIndexCount)
	for k := range idx {
		ri := int(r.u16())
		if ri >= regionCount {
			return metricVarData{}, fmt.Errorf("opentype: ItemVariationData: region index %d out of range", ri)
		}
		idx[k] = ri
	}
	if r.err != nil {
		return metricVarData{}, fmt.Errorf("opentype: ItemVariationData header: %w", r.err)
	}

	longWords := wordDeltaCount&0x8000 != 0
	wordCount := wordDeltaCount & 0x7FFF
	if wordCount > regionIndexCount {
		return metricVarData{}, fmt.Errorf("opentype: ItemVariationData: word count %d exceeds region count %d", wordCount, regionIndexCount)
	}
	sets := make([][]int32, itemCount)
	for i := 0; i < itemCount; i++ {
		row := make([]int32, regionIndexCount)
		for j := 0; j < regionIndexCount; j++ {
			row[j] = readVarDelta(&r, longWords, j < wordCount)
		}
		sets[i] = row
	}
	if r.err != nil {
		return metricVarData{}, fmt.Errorf("opentype: ItemVariationData deltas: %w", r.err)
	}
	return metricVarData{regionIndices: idx, deltaSets: sets}, nil
}

// readVarDelta reads one ItemVariationData delta. With LONG_WORDS the wide
// columns are 32-bit and the narrow ones 16-bit; without it the wide columns are
// 16-bit and the narrow ones 8-bit. wide selects the wide width for this column.
func readVarDelta(r *reader, longWords, wide bool) int32 {
	if longWords && wide {
		return int32(r.u32())
	}
	if longWords || wide {
		return int32(r.i16())
	}
	return int32(int8(r.u8()))
}

// deltaSetMap is a decoded DeltaSetIndexMap: per glyph index, the (outer, inner)
// pair addressing the ItemVariationStore. A nil or empty map maps a glyph index
// to itself (outer 0, inner = glyph index).
type deltaSetMap struct {
	outer []uint32
	inner []uint32
}

// lookup returns the (outer, inner) store indices for glyph gid. A nil or empty
// map is the identity mapping (outer 0, inner gid); a gid past the map's last
// entry clamps to that entry (as the specification prescribes).
func (m *deltaSetMap) lookup(gid int) (int, int) {
	if m == nil || len(m.outer) == 0 {
		return 0, gid
	}
	if gid >= len(m.outer) {
		gid = len(m.outer) - 1
	}
	return int(m.outer[gid]), int(m.inner[gid])
}

// parseDeltaSetMap decodes the DeltaSetIndexMap at offset off in b (format 0 or
// 1). A zero offset means the map is absent and yields a nil map (the identity
// mapping). entryFormat's low nibble gives the inner-index bit count minus one
// and bits 4-5 give the per-entry byte size minus one.
func parseDeltaSetMap(b []byte, off int) (*deltaSetMap, error) {
	if off == 0 {
		return nil, nil
	}
	r := reader{b: b, pos: off}
	format := r.u8()
	entryFormat := r.u8()
	var count int
	switch format {
	case 0:
		count = int(r.u16())
	case 1:
		count = int(r.u32())
	default:
		return nil, fmt.Errorf("opentype: DeltaSetIndexMap: unsupported format %d", format)
	}
	innerBits := uint(entryFormat&0x0F) + 1
	entrySize := int((entryFormat&0x30)>>4) + 1
	innerMask := uint32(1)<<innerBits - 1

	m := &deltaSetMap{outer: make([]uint32, count), inner: make([]uint32, count)}
	for i := 0; i < count; i++ {
		var entry uint32
		for k := 0; k < entrySize; k++ {
			entry = entry<<8 | uint32(r.u8())
		}
		m.inner[i] = entry & innerMask
		m.outer[i] = entry >> innerBits
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: DeltaSetIndexMap: %w", r.err)
	}
	return m, nil
}

// hvarTable is the parsed HVAR table: the shared store plus the advance-width
// and optional left/right side-bearing DeltaSetIndexMaps. Only the advance-width
// map drives a metric here; the side-bearing maps are decoded for completeness.
type hvarTable struct {
	store  *metricVarStore
	advMap *deltaSetMap
	lsbMap *deltaSetMap
	rsbMap *deltaSetMap
}

// vvarTable is the parsed VVAR table: the shared store plus the advance-height
// and optional top/bottom side-bearing and vertical-origin maps.
type vvarTable struct {
	store   *metricVarStore
	advMap  *deltaSetMap
	tsbMap  *deltaSetMap
	bsbMap  *deltaSetMap
	vorgMap *deltaSetMap
}

// mvarTable is the parsed MVAR table: the shared store plus a map from each
// four-byte value tag to its (outer, inner) store indices.
type mvarTable struct {
	store  *metricVarStore
	values map[string][2]int
}

// delta returns the MVAR delta for value tag at norm, or zero when the tag is
// absent or the table carries no store.
func (m *mvarTable) delta(tag string, norm []int16) float64 {
	v, ok := m.values[tag]
	if !ok || m.store == nil {
		return 0
	}
	return m.store.delta(v[0], v[1], norm)
}

// parseMetricVarTable decodes the header common to HVAR and VVAR: a version, a
// required ItemVariationStore offset, and mapCount DeltaSetIndexMap offsets (3
// for HVAR, 4 for VVAR), all uint32 and relative to the table start.
func parseMetricVarTable(b []byte, mapCount int) (*metricVarStore, []*deltaSetMap, error) {
	r := reader{b: b}
	r.u16() // majorVersion
	r.u16() // minorVersion
	storeOff := int(r.u32())
	offs := make([]int, mapCount)
	for i := range offs {
		offs[i] = int(r.u32())
	}
	if r.err != nil {
		return nil, nil, fmt.Errorf("opentype: metric variations header: %w", r.err)
	}
	store, err := parseMetricVarStore(b, storeOff)
	if err != nil {
		return nil, nil, err
	}
	maps := make([]*deltaSetMap, mapCount)
	for i, o := range offs {
		m, err := parseDeltaSetMap(b, o)
		if err != nil {
			return nil, nil, err
		}
		maps[i] = m
	}
	return store, maps, nil
}

// parseHVAR decodes the HVAR table into f.hvar.
func (f *Font) parseHVAR(b []byte) error {
	store, maps, err := parseMetricVarTable(b, 3)
	if err != nil {
		return err
	}
	f.hvar = &hvarTable{store: store, advMap: maps[0], lsbMap: maps[1], rsbMap: maps[2]}
	return nil
}

// parseVVAR decodes the VVAR table into f.vvar.
func (f *Font) parseVVAR(b []byte) error {
	store, maps, err := parseMetricVarTable(b, 4)
	if err != nil {
		return err
	}
	f.vvar = &vvarTable{store: store, advMap: maps[0], tsbMap: maps[1], bsbMap: maps[2], vorgMap: maps[3]}
	return nil
}

// parseMVAR decodes the MVAR table into f.mvar: a header, a run of value records
// (each a four-byte tag and an (outer, inner) index pair) and, unless there are
// none, a shared ItemVariationStore. The store offset here is a uint16.
func (f *Font) parseMVAR(b []byte) error {
	r := reader{b: b}
	r.u16() // majorVersion
	r.u16() // minorVersion
	r.u16() // reserved
	valueRecordSize := int(r.u16())
	valueRecordCount := int(r.u16())
	storeOff := int(r.u16())
	if r.err != nil {
		return fmt.Errorf("opentype: MVAR header: %w", r.err)
	}
	base := r.pos
	values := make(map[string][2]int, valueRecordCount)
	for i := 0; i < valueRecordCount; i++ {
		vr := reader{b: b, pos: base + i*valueRecordSize}
		tag := readTag(&vr)
		outer := int(vr.u16())
		inner := int(vr.u16())
		if vr.err != nil {
			return fmt.Errorf("opentype: MVAR value record %d: %w", i, vr.err)
		}
		values[tag] = [2]int{outer, inner}
	}
	var store *metricVarStore
	if storeOff != 0 {
		s, err := parseMetricVarStore(b, storeOff)
		if err != nil {
			return err
		}
		store = s
	}
	f.mvar = &mvarTable{store: store, values: values}
	return nil
}

// parseMetricVariations parses the optional HVAR, VVAR and MVAR tables. Each is
// independent; absence is not an error. A present-but-malformed table is
// reported so a corrupt font fails cleanly.
func (f *Font) parseMetricVariations(tables map[string][]byte) error {
	if b, ok := tables["HVAR"]; ok {
		if err := f.parseHVAR(b); err != nil {
			return err
		}
	}
	if b, ok := tables["VVAR"]; ok {
		if err := f.parseVVAR(b); err != nil {
			return err
		}
	}
	if b, ok := tables["MVAR"]; ok {
		if err := f.parseMVAR(b); err != nil {
			return err
		}
	}
	return nil
}

// horizontalAdvanceDelta returns glyph gid's horizontal-advance delta in font
// units at normalized coordinate norm: the HVAR advance-width delta when the
// font carries HVAR, otherwise the gvar phantom-point fallback for a variable
// glyf font. It is zero for a static font (nil norm) or when nothing applies.
func (f *Font) horizontalAdvanceDelta(gid int, norm []int16) float64 {
	if norm == nil {
		return 0
	}
	if f.hvar != nil {
		outer, inner := f.hvar.advMap.lookup(gid)
		return f.hvar.store.delta(outer, inner, norm)
	}
	return f.gvarAdvanceWidthDelta(gid, norm)
}

// verticalAdvanceDelta returns glyph gid's advance-height delta in font units at
// normalized coordinate norm from the VVAR table, or zero when the font has no
// VVAR table or norm is nil.
func (f *Font) verticalAdvanceDelta(gid int, norm []int16) float64 {
	if norm == nil || f.vvar == nil {
		return 0
	}
	outer, inner := f.vvar.advMap.lookup(gid)
	return f.vvar.store.delta(outer, inner, norm)
}

// gvarAdvanceWidthDelta computes glyph gid's horizontal-advance delta from its
// 'gvar' phantom points — the fallback the specification prescribes when a
// variable font varies advances through gvar but ships no HVAR table. It decodes
// the simple glyph's default points, runs the same gvar delta accumulation used
// for outlines (gvar.go) over a buffer that includes the four TrueType phantom
// points, and returns the advance-width phantom's x delta (pp2) minus the left
// phantom's (pp1). It is zero for an empty glyph, a composite glyph, a
// missing/short gvar, or malformed data.
func (f *Font) gvarAdvanceWidthDelta(gid int, norm []int16) float64 {
	gv := f.gvar
	if gv == nil || gid < 0 || gid+1 >= len(gv.dataOffsets) {
		return 0
	}
	start, end := f.loca[gid], f.loca[gid+1]
	if start >= end || int(end) > len(f.glyf) {
		return 0 // empty or out-of-range glyph
	}
	r := reader{b: f.glyf[start:end]}
	n := int(r.i16())
	r.skip(8) // xMin, yMin, xMax, yMax
	if n < 0 {
		return 0 // composite-glyph advance fallback is out of scope
	}
	contours, err := f.simpleGlyph(&r, n)
	if err != nil {
		return 0
	}
	var pts []outlinePoint
	var ends []int
	for _, c := range contours {
		pts = append(pts, c...)
		ends = append(ends, len(pts)-1)
	}
	numReal := len(pts)

	gstart := gv.dataArrayOff + int(gv.dataOffsets[gid])
	gend := gv.dataArrayOff + int(gv.dataOffsets[gid+1])
	if gstart >= gend || gend > len(gv.data) {
		return 0
	}
	coords := make([]float64, gv.axisCount)
	for i := range coords {
		if i < len(norm) {
			coords[i] = float64(norm[i]) / 16384.0
		}
	}
	accX := make([]float64, numReal+4)
	accY := make([]float64, numReal+4)
	if err := gv.applyGlyph(gv.data[gstart:gend], coords, ends, pts, accX, accY, false); err != nil {
		return 0
	}
	return accX[numReal+1] - accX[numReal]
}
