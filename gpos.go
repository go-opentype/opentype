// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"sort"
)

// This file decodes and applies the GPOS (Glyph Positioning) table. It supports
// the full set of positioning lookup types complex scripts rely on:
//
//   - Type 1: single adjustment (formats 1 and 2).
//   - Type 2: pair adjustment / kerning (formats 1 and 2).
//   - Type 3: cursive attachment (entry/exit anchors).
//   - Type 4: mark-to-base attachment (diacritics on a base glyph).
//   - Type 5: mark-to-ligature attachment.
//   - Type 6: mark-to-mark attachment (stacked diacritics).
//   - Type 7: contextual positioning (formats 1, 2 and 3).
//   - Type 8: chaining contextual positioning (formats 1, 2 and 3).
//   - Type 9: extension positioning (indirection to another lookup type).
//
// Anchor tables in formats 1, 2 and 3 are decoded; the anchor-point index of
// format 2 and the Device/VariationIndex offsets of format 3 are tolerated and
// ignored (their contribution is a hinting/variation refinement).
//
// The class-based contextual sub-format (format 2 of types 7 and 8) classifies
// the glyph run through ClassDef tables and matches rules by class sequence,
// mirroring the class-based GSUB contextual/chaining subtables. Every
// sub-format is implemented.
//
// Two entry points are exposed for the package's Face API to call: Kern (and
// Kerner.Kerning) for horizontal pair kerning, and position, which applies the
// full set of lookups over a glyph run and returns per-glyph placement and
// advance adjustments.

// gpos is a decoded GPOS table.
type gpos struct {
	layoutHeader
	lookups []gposLookup
	gdef    *gdefTable // font GDEF (nil when absent); drives glyph skipping
}

// skipperFor builds the glyph skipper for lookup lk from the table's GDEF and
// the lookup's flag and mark-filtering set.
func (p *gpos) skipperFor(lk gposLookup) skipper {
	return skipper{gdef: p.gdef, flag: lk.flag, markFilterSet: lk.markSet}
}

// gposLookup is one decoded GPOS Lookup: its supported positioning subtables.
// flag is the lookup's lookupFlag and markSet its mark-filtering-set index;
// with the font's GDEF table they drive which glyphs the lookup skips.
type gposLookup struct {
	subtables []posSubtable
	flag      uint16
	markSet   uint16
}

// posSubtable applies a positioning adjustment at index i of a glyph run whose
// current per-glyph adjustments live in the context. It reports whether it
// applied, so lookup iteration knows to stop trying further subtables.
type posSubtable interface {
	apply(ctx *posContext, i int) bool
}

// pairPos is a positioning subtable that can also answer a standalone
// (left, right) X-advance kern query; ok is false when it has no entry for the
// pair. Only the pair-adjustment subtables (type 2) implement it.
type pairPos interface {
	posSubtable
	kern(left, right GlyphIndex) (int, bool)
}

// GlyphPosition is the positioning adjustment GPOS computes for one glyph in a
// run: pen placement offsets (XOffset, YOffset) and advance adjustments
// (XAdvance, YAdvance), all in font units.
type GlyphPosition struct {
	XOffset  int
	YOffset  int
	XAdvance int
	YAdvance int
}

// valueRecord holds the four positioning fields of a GPOS ValueRecord; Device
// and VariationIndex offsets are consumed during parsing but not retained.
type valueRecord struct {
	xPlacement int
	yPlacement int
	xAdvance   int
	yAdvance   int
}

// addValue accumulates a ValueRecord into a glyph's adjustment.
func (p *GlyphPosition) addValue(v valueRecord) {
	p.XOffset += v.xPlacement
	p.YOffset += v.yPlacement
	p.XAdvance += v.xAdvance
	p.YAdvance += v.yAdvance
}

// readValueRecord consumes a ValueRecord whose shape is given by format,
// returning its four positioning fields. Present fields are 2 bytes each and
// appear in ValueFormat bit order; the four Device/VariationIndex offset fields
// are read and discarded.
func readValueRecord(r *reader, format uint16) valueRecord {
	var v valueRecord
	if format&0x0001 != 0 {
		v.xPlacement = int(r.i16())
	}
	if format&0x0002 != 0 {
		v.yPlacement = int(r.i16())
	}
	if format&0x0004 != 0 {
		v.xAdvance = int(r.i16())
	}
	if format&0x0008 != 0 {
		v.yAdvance = int(r.i16())
	}
	if format&0x0010 != 0 {
		r.u16() // X placement device
	}
	if format&0x0020 != 0 {
		r.u16() // Y placement device
	}
	if format&0x0040 != 0 {
		r.u16() // X advance device
	}
	if format&0x0080 != 0 {
		r.u16() // Y advance device
	}
	return v
}

// parseGPOS decodes a GPOS table.
func parseGPOS(b []byte) (*gpos, error) {
	hdr, raw, err := parseLayoutCommon(b)
	if err != nil {
		return nil, err
	}
	lookups := make([]gposLookup, len(raw))
	for i, lk := range raw {
		gl, err := parseGPOSLookup(lk)
		if err != nil {
			return nil, err
		}
		lookups[i] = gl
	}
	return &gpos{layoutHeader: hdr, lookups: lookups}, nil
}

// parseGPOSLookup decodes one Lookup table into its supported subtables.
func parseGPOSLookup(b []byte) (gposLookup, error) {
	r := reader{b: b}
	lookupType := r.u16()
	flag := r.u16()
	subCount := int(r.u16())
	offs := make([]int, subCount)
	for i := 0; i < subCount; i++ {
		offs[i] = int(r.u16())
	}
	var markSet uint16
	if flag&flagUseMarkFilteringSet != 0 {
		markSet = r.u16() // markFilteringSet follows the subtable offsets
	}
	if r.err != nil {
		return gposLookup{}, fmt.Errorf("opentype: gpos lookup: %w", r.err)
	}
	var subs []posSubtable
	for _, off := range offs {
		st, err := parseGPOSSubtable(lookupType, subslice(b, off))
		if err != nil {
			return gposLookup{}, err
		}
		if st != nil {
			subs = append(subs, st)
		}
	}
	return gposLookup{subtables: subs, flag: flag, markSet: markSet}, nil
}

// parseGPOSSubtable decodes one subtable of the given lookup type. A deferred
// sub-format yields a nil subtable the caller drops.
func parseGPOSSubtable(lookupType uint16, b []byte) (posSubtable, error) {
	switch lookupType {
	case 1:
		return parseSinglePos(b)
	case 2:
		return parsePairPosAny(b)
	case 3:
		return parseCursivePos(b)
	case 4:
		return parseMarkBasePos(b)
	case 5:
		return parseMarkLigPos(b)
	case 6:
		return parseMarkMarkPos(b)
	case 7:
		return parseContextPos(b)
	case 8:
		return parseChainPos(b)
	case 9:
		return parseExtensionPos(b)
	default:
		return nil, fmt.Errorf("opentype: gpos lookup type %d", lookupType)
	}
}

// --- Anchor tables ----------------------------------------------------------

// anchor is a decoded Anchor table's design-space coordinate.
type anchor struct {
	x int
	y int
}

// parseAnchor decodes an Anchor table (format 1, 2 or 3). Format 2's anchor
// point and format 3's Device/VariationIndex offsets are read and ignored.
func parseAnchor(b []byte) (anchor, error) {
	r := reader{b: b}
	format := r.u16()
	a := anchor{x: int(r.i16()), y: int(r.i16())}
	switch format {
	case 1:
	case 2:
		r.u16() // anchorPoint (ignored)
	case 3:
		r.u16() // xDeviceOffset (ignored)
		r.u16() // yDeviceOffset (ignored)
	default:
		return anchor{}, fmt.Errorf("opentype: anchor format %d", format)
	}
	if r.err != nil {
		return anchor{}, fmt.Errorf("opentype: anchor: %w", r.err)
	}
	return a, nil
}

// parseAnchorAt decodes the Anchor table at offset off from the start of b,
// returning nil for a NULL (zero) offset.
func parseAnchorAt(b []byte, off int) (*anchor, error) {
	if off == 0 {
		return nil, nil
	}
	a, err := parseAnchor(subslice(b, off))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// parseAnchorMatrix decodes a rows*cols grid of Anchor offsets (a BaseArray,
// Mark2Array or LigatureAttach body): a rowCount uint16 followed by
// rowCount*cols anchor offsets relative to the start of b. NULL offsets yield a
// nil anchor.
func parseAnchorMatrix(b []byte, cols int) ([][]*anchor, error) {
	r := reader{b: b}
	rows := int(r.u16())
	offs := make([][]int, rows)
	for i := 0; i < rows; i++ {
		offs[i] = make([]int, cols)
		for j := 0; j < cols; j++ {
			offs[i][j] = int(r.u16())
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: anchor matrix: %w", r.err)
	}
	out := make([][]*anchor, rows)
	for i := 0; i < rows; i++ {
		out[i] = make([]*anchor, cols)
		for j := 0; j < cols; j++ {
			a, err := parseAnchorAt(b, offs[i][j])
			if err != nil {
				return nil, err
			}
			out[i][j] = a
		}
	}
	return out, nil
}

// markRecord binds a mark glyph's class to its attachment anchor.
type markRecord struct {
	class uint16
	anch  anchor
}

// parseMarkArray decodes a MarkArray table (markCount records, each a class and
// an Anchor offset).
func parseMarkArray(b []byte) ([]markRecord, error) {
	r := reader{b: b}
	n := int(r.u16())
	type mrec struct {
		class uint16
		off   int
	}
	recs := make([]mrec, n)
	for i := 0; i < n; i++ {
		recs[i].class = r.u16()
		recs[i].off = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: markArray: %w", r.err)
	}
	out := make([]markRecord, n)
	for i, rc := range recs {
		a, err := parseAnchor(subslice(b, rc.off))
		if err != nil {
			return nil, err
		}
		out[i] = markRecord{class: rc.class, anch: a}
	}
	return out, nil
}

// --- Type 1: single adjustment ----------------------------------------------

// singlePos1 applies one ValueRecord to every covered glyph.
type singlePos1 struct {
	cov map[GlyphIndex]int
	val valueRecord
}

// singlePos2 applies a per-glyph ValueRecord indexed by coverage index.
type singlePos2 struct {
	cov  map[GlyphIndex]int
	vals []valueRecord
}

// parseSinglePos decodes a SinglePos subtable (format 1 or 2).
func parseSinglePos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	covOff := int(r.u16())
	valueFormat := r.u16()
	switch format {
	case 1:
		v := readValueRecord(&r, valueFormat)
		if r.err != nil {
			return nil, fmt.Errorf("opentype: singlepos1: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		return &singlePos1{cov: cov, val: v}, nil
	case 2:
		n := int(r.u16())
		vals := make([]valueRecord, n)
		for i := 0; i < n; i++ {
			vals[i] = readValueRecord(&r, valueFormat)
		}
		if r.err != nil {
			return nil, fmt.Errorf("opentype: singlepos2: %w", r.err)
		}
		cov, err := parseCoverage(subslice(b, covOff))
		if err != nil {
			return nil, err
		}
		return &singlePos2{cov: cov, vals: vals}, nil
	default:
		return nil, fmt.Errorf("opentype: singlepos format %d", format)
	}
}

func (s *singlePos1) apply(ctx *posContext, i int) bool {
	if _, ok := s.cov[ctx.glyphs[i]]; !ok {
		return false
	}
	ctx.positions[i].addValue(s.val)
	return true
}

func (s *singlePos2) apply(ctx *posContext, i int) bool {
	ci, ok := s.cov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	if ci >= len(s.vals) {
		return false
	}
	ctx.positions[i].addValue(s.vals[ci])
	return true
}

// --- Type 2: pair adjustment (kerning) --------------------------------------

// parsePairPosAny decodes a PairPos subtable (format 1 or 2).
func parsePairPosAny(b []byte) (posSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	switch format {
	case 1:
		return parsePairPos1(b)
	case 2:
		return parsePairPos2(b)
	default:
		return nil, fmt.Errorf("opentype: pairpos format %d", format)
	}
}

// pairPos1 is a pair-adjustment format 1 subtable: covered first glyphs index
// into per-glyph maps from second glyph to X-advance adjustment.
type pairPos1 struct {
	cov  map[GlyphIndex]int
	sets []map[GlyphIndex]int
}

// parsePairPos1 decodes a PairPos format 1 subtable.
func parsePairPos1(b []byte) (pairPos, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	vf1 := r.u16()
	vf2 := r.u16()
	setCount := int(r.u16())
	setOffs := make([]int, setCount)
	for i := 0; i < setCount; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: pairpos1: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	sets := make([]map[GlyphIndex]int, setCount)
	for i, so := range setOffs {
		m, err := parsePairSet(subslice(b, so), vf1, vf2)
		if err != nil {
			return nil, err
		}
		sets[i] = m
	}
	return &pairPos1{cov: cov, sets: sets}, nil
}

// parsePairSet decodes a PairSet table into a second-glyph -> X-advance map.
func parsePairSet(b []byte, vf1, vf2 uint16) (map[GlyphIndex]int, error) {
	r := reader{b: b}
	count := int(r.u16())
	m := map[GlyphIndex]int{}
	for i := 0; i < count; i++ {
		second := GlyphIndex(r.u16())
		v1 := readValueRecord(&r, vf1)
		readValueRecord(&r, vf2) // value2 (applies to the right glyph) is skipped
		m[second] = v1.xAdvance
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: pairSet: %w", r.err)
	}
	return m, nil
}

func (p *pairPos1) kern(left, right GlyphIndex) (int, bool) {
	ci, ok := p.cov[left]
	if !ok {
		return 0, false
	}
	if v, ok := p.sets[ci][right]; ok {
		return v, true
	}
	return 0, false
}

func (p *pairPos1) apply(ctx *posContext, i int) bool {
	j := ctx.skip.next(ctx.glyphs, i+1)
	if j >= len(ctx.glyphs) {
		return false
	}
	v, ok := p.kern(ctx.glyphs[i], ctx.glyphs[j])
	if !ok {
		return false
	}
	ctx.positions[i].XAdvance += v
	return true
}

// pairPos2 is a pair-adjustment format 2 subtable: the first glyph must be
// covered, then both glyphs are classified and the class pair indexes a grid of
// X-advance adjustments.
type pairPos2 struct {
	cov         map[GlyphIndex]int
	cd1, cd2    map[GlyphIndex]int
	class2Count int
	grid        []int
}

// parsePairPos2 decodes a PairPos format 2 subtable.
func parsePairPos2(b []byte) (pairPos, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	vf1 := r.u16()
	vf2 := r.u16()
	cd1Off := int(r.u16())
	cd2Off := int(r.u16())
	class1Count := int(r.u16())
	class2Count := int(r.u16())
	grid := make([]int, class1Count*class2Count)
	for i := range grid {
		v1 := readValueRecord(&r, vf1)
		readValueRecord(&r, vf2)
		grid[i] = v1.xAdvance
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: pairpos2: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	cd1, err := parseClassDef(subslice(b, cd1Off))
	if err != nil {
		return nil, err
	}
	cd2, err := parseClassDef(subslice(b, cd2Off))
	if err != nil {
		return nil, err
	}
	return &pairPos2{cov: cov, cd1: cd1, cd2: cd2, class2Count: class2Count, grid: grid}, nil
}

func (p *pairPos2) kern(left, right GlyphIndex) (int, bool) {
	if _, ok := p.cov[left]; !ok {
		return 0, false
	}
	idx := p.cd1[left]*p.class2Count + p.cd2[right]
	if idx >= len(p.grid) {
		return 0, false
	}
	return p.grid[idx], true
}

func (p *pairPos2) apply(ctx *posContext, i int) bool {
	j := ctx.skip.next(ctx.glyphs, i+1)
	if j >= len(ctx.glyphs) {
		return false
	}
	v, ok := p.kern(ctx.glyphs[i], ctx.glyphs[j])
	if !ok {
		return false
	}
	ctx.positions[i].XAdvance += v
	return true
}

// --- Type 3: cursive attachment ---------------------------------------------

// cursivePos joins covered glyphs by aligning one glyph's exit anchor to the
// next glyph's entry anchor. entry[i] and exit[i] are indexed by coverage index
// and may be nil (NULL anchor).
type cursivePos struct {
	cov   map[GlyphIndex]int
	entry []*anchor
	exit  []*anchor
}

// parseCursivePos decodes a CursivePos format 1 subtable.
func parseCursivePos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	n := int(r.u16())
	entryOffs := make([]int, n)
	exitOffs := make([]int, n)
	for i := 0; i < n; i++ {
		entryOffs[i] = int(r.u16())
		exitOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cursivepos: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	entry := make([]*anchor, n)
	exit := make([]*anchor, n)
	for i := 0; i < n; i++ {
		if entry[i], err = parseAnchorAt(b, entryOffs[i]); err != nil {
			return nil, err
		}
		if exit[i], err = parseAnchorAt(b, exitOffs[i]); err != nil {
			return nil, err
		}
	}
	return &cursivePos{cov: cov, entry: entry, exit: exit}, nil
}

func (c *cursivePos) apply(ctx *posContext, i int) bool {
	ci, ok := c.cov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	j := ctx.skip.next(ctx.glyphs, i+1)
	if j >= len(ctx.glyphs) {
		return false
	}
	ni, ok := c.cov[ctx.glyphs[j]]
	if !ok {
		return false
	}
	if ci >= len(c.exit) || ni >= len(c.entry) {
		return false
	}
	ex, en := c.exit[ci], c.entry[ni]
	if ex == nil || en == nil {
		return false
	}
	// Join the exit anchor of the current glyph to the entry anchor of the next.
	// Left-to-right, the following glyph is moved so its entry meets the current
	// exit; right-to-left, the current glyph is moved so its exit meets the next
	// glyph's entry.
	if ctx.skip.rtl() {
		ctx.positions[i].XOffset += en.x - ex.x
		ctx.positions[i].YOffset += en.y - ex.y
	} else {
		ctx.positions[j].XOffset += ex.x - en.x
		ctx.positions[j].YOffset += ex.y - en.y
	}
	return true
}

// --- Types 4/5/6: mark attachment -------------------------------------------

// attachMark positions a mark glyph so its anchor coincides with the base's
// anchor, discounting the advances between the base and the mark so the mark
// (whose pen sits after the base) is pulled back onto it.
func (ctx *posContext) attachMark(base, mark int, baseAnchor, markAnchor anchor) {
	dx := baseAnchor.x - markAnchor.x
	dy := baseAnchor.y - markAnchor.y
	for k := base; k < mark; k++ {
		dx -= ctx.advance(k)
	}
	ctx.positions[mark].XOffset = ctx.positions[base].XOffset + dx
	ctx.positions[mark].YOffset = ctx.positions[base].YOffset + dy
	ctx.positions[mark].XAdvance = 0
	ctx.positions[mark].YAdvance = 0
}

// markBasePos is a mark-to-base (type 4) subtable: base[baseIndex][markClass]
// gives the base attachment anchor for each mark class.
type markBasePos struct {
	markCov map[GlyphIndex]int
	baseCov map[GlyphIndex]int
	marks   []markRecord
	base    [][]*anchor
}

// parseMarkBasePos decodes a MarkBasePos format 1 subtable.
func parseMarkBasePos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	markCovOff := int(r.u16())
	baseCovOff := int(r.u16())
	classCount := int(r.u16())
	markArrOff := int(r.u16())
	baseArrOff := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: markbasepos: %w", r.err)
	}
	markCov, err := parseCoverage(subslice(b, markCovOff))
	if err != nil {
		return nil, err
	}
	baseCov, err := parseCoverage(subslice(b, baseCovOff))
	if err != nil {
		return nil, err
	}
	marks, err := parseMarkArray(subslice(b, markArrOff))
	if err != nil {
		return nil, err
	}
	base, err := parseAnchorMatrix(subslice(b, baseArrOff), classCount)
	if err != nil {
		return nil, err
	}
	return &markBasePos{markCov: markCov, baseCov: baseCov, marks: marks, base: base}, nil
}

func (m *markBasePos) apply(ctx *posContext, i int) bool {
	mi, ok := m.markCov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	if mi >= len(m.marks) {
		return false
	}
	mr := m.marks[mi]
	for b := i - 1; b >= 0; b-- {
		// A base glyph is the nearest preceding non-skipped, non-mark glyph: skip
		// glyphs the lookup ignores and any GDEF mark (a mark cannot be a base).
		if ctx.skip.skip(ctx.glyphs[b]) || ctx.skip.isMark(ctx.glyphs[b]) {
			continue
		}
		bi, ok := m.baseCov[ctx.glyphs[b]]
		if !ok {
			continue
		}
		if bi >= len(m.base) || int(mr.class) >= len(m.base[bi]) {
			return false
		}
		ba := m.base[bi][mr.class]
		if ba == nil {
			return false
		}
		ctx.attachMark(b, i, *ba, mr.anch)
		return true
	}
	return false
}

// markLigPos is a mark-to-ligature (type 5) subtable: ligs[ligIndex][component]
// [markClass] gives the attachment anchor. Component selection (which needs the
// GSUB ligature-component tracking the caller does not provide) is simplified
// to the first component.
type markLigPos struct {
	markCov map[GlyphIndex]int
	ligCov  map[GlyphIndex]int
	marks   []markRecord
	ligs    [][][]*anchor
}

// parseMarkLigPos decodes a MarkLigPos format 1 subtable.
func parseMarkLigPos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	markCovOff := int(r.u16())
	ligCovOff := int(r.u16())
	classCount := int(r.u16())
	markArrOff := int(r.u16())
	ligArrOff := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: markligpos: %w", r.err)
	}
	markCov, err := parseCoverage(subslice(b, markCovOff))
	if err != nil {
		return nil, err
	}
	ligCov, err := parseCoverage(subslice(b, ligCovOff))
	if err != nil {
		return nil, err
	}
	marks, err := parseMarkArray(subslice(b, markArrOff))
	if err != nil {
		return nil, err
	}
	ligs, err := parseLigatureArray(subslice(b, ligArrOff), classCount)
	if err != nil {
		return nil, err
	}
	return &markLigPos{markCov: markCov, ligCov: ligCov, marks: marks, ligs: ligs}, nil
}

// parseLigatureArray decodes a LigatureArray table: a ligatureCount uint16 then
// that many LigatureAttach offsets, each an anchor matrix of componentCount
// rows and cols (markClassCount) columns.
func parseLigatureArray(b []byte, cols int) ([][][]*anchor, error) {
	r := reader{b: b}
	n := int(r.u16())
	offs := make([]int, n)
	for i := 0; i < n; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: ligatureArray: %w", r.err)
	}
	out := make([][][]*anchor, n)
	for i, off := range offs {
		la, err := parseAnchorMatrix(subslice(b, off), cols)
		if err != nil {
			return nil, err
		}
		out[i] = la
	}
	return out, nil
}

func (m *markLigPos) apply(ctx *posContext, i int) bool {
	mi, ok := m.markCov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	if mi >= len(m.marks) {
		return false
	}
	mr := m.marks[mi]
	for b := i - 1; b >= 0; b-- {
		// The base ligature is the nearest preceding non-skipped, non-mark glyph.
		if ctx.skip.skip(ctx.glyphs[b]) || ctx.skip.isMark(ctx.glyphs[b]) {
			continue
		}
		li, ok := m.ligCov[ctx.glyphs[b]]
		if !ok {
			continue
		}
		if li >= len(m.ligs) || len(m.ligs[li]) == 0 {
			return false
		}
		comp := m.ligs[li][0] // first component (documented simplification)
		if int(mr.class) >= len(comp) {
			return false
		}
		la := comp[mr.class]
		if la == nil {
			return false
		}
		ctx.attachMark(b, i, *la, mr.anch)
		return true
	}
	return false
}

// markMarkPos is a mark-to-mark (type 6) subtable: mark2[mark2Index][markClass]
// gives the base-mark attachment anchor.
type markMarkPos struct {
	mark1Cov map[GlyphIndex]int
	mark2Cov map[GlyphIndex]int
	marks    []markRecord
	mark2    [][]*anchor
}

// parseMarkMarkPos decodes a MarkMarkPos format 1 subtable.
func parseMarkMarkPos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	mark1CovOff := int(r.u16())
	mark2CovOff := int(r.u16())
	classCount := int(r.u16())
	mark1ArrOff := int(r.u16())
	mark2ArrOff := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: markmarkpos: %w", r.err)
	}
	mark1Cov, err := parseCoverage(subslice(b, mark1CovOff))
	if err != nil {
		return nil, err
	}
	mark2Cov, err := parseCoverage(subslice(b, mark2CovOff))
	if err != nil {
		return nil, err
	}
	marks, err := parseMarkArray(subslice(b, mark1ArrOff))
	if err != nil {
		return nil, err
	}
	mark2, err := parseAnchorMatrix(subslice(b, mark2ArrOff), classCount)
	if err != nil {
		return nil, err
	}
	return &markMarkPos{mark1Cov: mark1Cov, mark2Cov: mark2Cov, marks: marks, mark2: mark2}, nil
}

func (m *markMarkPos) apply(ctx *posContext, i int) bool {
	mi, ok := m.mark1Cov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	if mi >= len(m.marks) {
		return false
	}
	mr := m.marks[mi]
	for b := i - 1; b >= 0; b-- {
		// The base mark is the nearest preceding non-skipped glyph (it is itself a
		// mark, so marks are not skipped here — only the lookup's own ignores).
		if ctx.skip.skip(ctx.glyphs[b]) {
			continue
		}
		bi, ok := m.mark2Cov[ctx.glyphs[b]]
		if !ok {
			continue
		}
		if bi >= len(m.mark2) || int(mr.class) >= len(m.mark2[bi]) {
			return false
		}
		ba := m.mark2[bi][mr.class]
		if ba == nil {
			return false
		}
		ctx.attachMark(b, i, *ba, mr.anch)
		return true
	}
	return false
}

// --- Types 7/8: contextual and chaining positioning -------------------------

// posLookupRecord applies the lookup at lookupIndex to the input glyph at
// seqIndex within a matched context.
type posLookupRecord struct {
	seqIndex    uint16
	lookupIndex uint16
}

// readPosLookupRecords reads n PosLookupRecords.
func readPosLookupRecords(r *reader, n int) []posLookupRecord {
	recs := make([]posLookupRecord, n)
	for i := range recs {
		recs[i].seqIndex = r.u16()
		recs[i].lookupIndex = r.u16()
	}
	return recs
}

// glyphPredsG builds glyph-id-equality predicates from a GlyphIndex sequence
// (the in-memory form of GPOS input, backtrack and lookahead sequences).
func glyphPredsG(seq []GlyphIndex) []glyphPred {
	preds := make([]glyphPred, len(seq))
	for k := range seq {
		v := seq[k]
		preds[k] = func(g GlyphIndex) bool { return g == v }
	}
	return preds
}

// classPredsG builds class-equality predicates from a GlyphIndex class sequence
// (stored with the on-disk class values of a Pos(Chain)ClassRule) and a
// ClassDef map; glyphs absent from cd fall in class 0.
func classPredsG(seq []GlyphIndex, cd map[GlyphIndex]int) []glyphPred {
	preds := make([]glyphPred, len(seq))
	for k := range seq {
		v := int(seq[k])
		preds[k] = func(g GlyphIndex) bool { return classOf(cd, g) == v }
	}
	return preds
}

// posRule is one contextual (type 7 format 1) rule: an input sequence (from the
// second glyph) and the lookup records to run when it matches.
type posRule struct {
	input []GlyphIndex
	recs  []posLookupRecord
}

// contextPos1 is a contextual positioning format 1 subtable.
type contextPos1 struct {
	cov  map[GlyphIndex]int
	sets [][]posRule
}

// contextPos2 is a contextual positioning format 2 subtable (class based): the
// first glyph must be covered, then the input run is matched by glyph class.
// sets is indexed by the first glyph's class; a nil set is empty (a NULL
// PosClassSet offset, common for class 0).
type contextPos2 struct {
	cov      map[GlyphIndex]int
	classDef map[GlyphIndex]int
	sets     [][]posRule
}

// contextPos3 is a contextual positioning format 3 subtable: a per-position
// coverage sequence and the lookup records to run on a full match.
type contextPos3 struct {
	covs []map[GlyphIndex]int
	recs []posLookupRecord
}

// parseContextPos decodes a contextual positioning subtable (format 1, 2 or 3).
func parseContextPos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	if r.err != nil {
		return nil, fmt.Errorf("opentype: contextpos: %w", r.err)
	}
	switch format {
	case 1:
		return parseContextPos1(b)
	case 2:
		return parseContextPos2(b)
	case 3:
		return parseContextPos3(b)
	default:
		return nil, fmt.Errorf("opentype: contextpos format %d", format)
	}
}

// parseContextPos2 decodes a contextual positioning format 2 subtable.
func parseContextPos2(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	classOff := int(r.u16())
	n := int(r.u16())
	setOffs := make([]int, n)
	for i := 0; i < n; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: contextpos2: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	cd, err := parseClassDef(subslice(b, classOff))
	if err != nil {
		return nil, err
	}
	sets := make([][]posRule, n)
	for i, so := range setOffs {
		if so == 0 {
			continue // NULL PosClassSet: this class triggers no rule
		}
		rs, err := parsePosRuleSet(subslice(b, so))
		if err != nil {
			return nil, err
		}
		sets[i] = rs
	}
	return &contextPos2{cov: cov, classDef: cd, sets: sets}, nil
}

func (c *contextPos2) apply(ctx *posContext, i int) bool {
	if _, ok := c.cov[ctx.glyphs[i]]; !ok {
		return false
	}
	cls := classOf(c.classDef, ctx.glyphs[i])
	if cls >= len(c.sets) {
		return false
	}
	for _, rule := range c.sets[cls] {
		matched, _, ok := ctx.skip.matchForward(ctx.glyphs, i+1, classPredsG(rule.input, c.classDef))
		if !ok {
			continue
		}
		ctx.applyRecords(rule.recs, append([]int{i}, matched...))
		return true
	}
	return false
}

// parseContextPos1 decodes a contextual positioning format 1 subtable.
func parseContextPos1(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	n := int(r.u16())
	setOffs := make([]int, n)
	for i := 0; i < n; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: contextpos1: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	sets := make([][]posRule, n)
	for i, so := range setOffs {
		rs, err := parsePosRuleSet(subslice(b, so))
		if err != nil {
			return nil, err
		}
		sets[i] = rs
	}
	return &contextPos1{cov: cov, sets: sets}, nil
}

// parsePosRuleSet decodes a PosRuleSet table (a list of PosRule offsets).
func parsePosRuleSet(b []byte) ([]posRule, error) {
	r := reader{b: b}
	n := int(r.u16())
	offs := make([]int, n)
	for i := 0; i < n; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: posRuleSet: %w", r.err)
	}
	rules := make([]posRule, n)
	for i, off := range offs {
		rule, err := parsePosRule(subslice(b, off))
		if err != nil {
			return nil, err
		}
		rules[i] = rule
	}
	return rules, nil
}

// parsePosRule decodes one PosRule table.
func parsePosRule(b []byte) (posRule, error) {
	r := reader{b: b}
	glyphCount := int(r.u16())
	lookupCount := int(r.u16())
	input := make([]GlyphIndex, 0, glyphCount)
	for k := 1; k < glyphCount; k++ {
		input = append(input, GlyphIndex(r.u16()))
	}
	recs := readPosLookupRecords(&r, lookupCount)
	if r.err != nil {
		return posRule{}, fmt.Errorf("opentype: posRule: %w", r.err)
	}
	return posRule{input: input, recs: recs}, nil
}

func (c *contextPos1) apply(ctx *posContext, i int) bool {
	ci, ok := c.cov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	if ci >= len(c.sets) {
		return false
	}
	for _, rule := range c.sets[ci] {
		matched, _, ok := ctx.skip.matchForward(ctx.glyphs, i+1, glyphPredsG(rule.input))
		if !ok {
			continue
		}
		ctx.applyRecords(rule.recs, append([]int{i}, matched...))
		return true
	}
	return false
}

// parseContextPos3 decodes a contextual positioning format 3 subtable.
func parseContextPos3(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	glyphCount := int(r.u16())
	lookupCount := int(r.u16())
	covOffs := make([]int, glyphCount)
	for i := 0; i < glyphCount; i++ {
		covOffs[i] = int(r.u16())
	}
	recs := readPosLookupRecords(&r, lookupCount)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: contextpos3: %w", r.err)
	}
	covs, err := parsePosCoverageList(b, covOffs)
	if err != nil {
		return nil, err
	}
	return &contextPos3{covs: covs, recs: recs}, nil
}

func (c *contextPos3) apply(ctx *posContext, i int) bool {
	matched, _, ok := ctx.skip.matchContext(ctx.glyphs, i, c.covs)
	if !ok {
		return false
	}
	ctx.applyRecords(c.recs, matched)
	return true
}

// parsePosCoverageList decodes a slice of Coverage tables at the given offsets from
// the start of b.
func parsePosCoverageList(b []byte, offs []int) ([]map[GlyphIndex]int, error) {
	covs := make([]map[GlyphIndex]int, len(offs))
	for i, off := range offs {
		cov, err := parseCoverage(subslice(b, off))
		if err != nil {
			return nil, err
		}
		covs[i] = cov
	}
	return covs, nil
}

// posChainRule is one chaining contextual (type 8 format 1) rule.
type posChainRule struct {
	back  []GlyphIndex
	input []GlyphIndex
	ahead []GlyphIndex
	recs  []posLookupRecord
}

// chainPos1 is a chaining contextual positioning format 1 subtable.
type chainPos1 struct {
	cov  map[GlyphIndex]int
	sets [][]posChainRule
}

// chainPos2 is a chaining contextual positioning format 2 subtable (class
// based): three ClassDefs classify the backtrack, input and lookahead runs, and
// rules match by class sequence. sets is indexed by the first glyph's input
// class; a nil set is empty (a NULL ChainPosClassSet offset).
type chainPos2 struct {
	cov       map[GlyphIndex]int
	backtrack map[GlyphIndex]int
	input     map[GlyphIndex]int
	lookahead map[GlyphIndex]int
	sets      [][]posChainRule
}

// chainPos3 is a chaining contextual positioning format 3 subtable.
type chainPos3 struct {
	back  []map[GlyphIndex]int
	input []map[GlyphIndex]int
	ahead []map[GlyphIndex]int
	recs  []posLookupRecord
}

// parseChainPos decodes a chaining contextual positioning subtable (format 1, 2
// or 3).
func parseChainPos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	format := r.u16()
	if r.err != nil {
		return nil, fmt.Errorf("opentype: chainpos: %w", r.err)
	}
	switch format {
	case 1:
		return parseChainPos1(b)
	case 2:
		return parseChainPos2(b)
	case 3:
		return parseChainPos3(b)
	default:
		return nil, fmt.Errorf("opentype: chainpos format %d", format)
	}
}

// parseChainPos2 decodes a chaining contextual positioning format 2 subtable.
func parseChainPos2(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	btOff := int(r.u16())
	inOff := int(r.u16())
	laOff := int(r.u16())
	n := int(r.u16())
	setOffs := make([]int, n)
	for i := 0; i < n; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: chainpos2: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	btCD, err := parseClassDef(subslice(b, btOff))
	if err != nil {
		return nil, err
	}
	inCD, err := parseClassDef(subslice(b, inOff))
	if err != nil {
		return nil, err
	}
	laCD, err := parseClassDef(subslice(b, laOff))
	if err != nil {
		return nil, err
	}
	sets := make([][]posChainRule, n)
	for i, so := range setOffs {
		if so == 0 {
			continue // NULL ChainPosClassSet: this class triggers no rule
		}
		rs, err := parsePosChainRuleSet(subslice(b, so))
		if err != nil {
			return nil, err
		}
		sets[i] = rs
	}
	return &chainPos2{cov: cov, backtrack: btCD, input: inCD, lookahead: laCD, sets: sets}, nil
}

func (c *chainPos2) apply(ctx *posContext, i int) bool {
	if _, ok := c.cov[ctx.glyphs[i]]; !ok {
		return false
	}
	cls := classOf(c.input, ctx.glyphs[i])
	if cls >= len(c.sets) {
		return false
	}
	for _, rule := range c.sets[cls] {
		matched, end, ok := ctx.skip.matchForward(ctx.glyphs, i+1, classPredsG(rule.input, c.input))
		if !ok ||
			!ctx.skip.matchBackward(ctx.glyphs, i-1, classPredsG(rule.back, c.backtrack)) {
			continue
		}
		if _, _, ok := ctx.skip.matchForward(ctx.glyphs, end, classPredsG(rule.ahead, c.lookahead)); !ok {
			continue
		}
		ctx.applyRecords(rule.recs, append([]int{i}, matched...))
		return true
	}
	return false
}

// parseChainPos1 decodes a chaining contextual positioning format 1 subtable.
func parseChainPos1(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	covOff := int(r.u16())
	n := int(r.u16())
	setOffs := make([]int, n)
	for i := 0; i < n; i++ {
		setOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: chainpos1: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	sets := make([][]posChainRule, n)
	for i, so := range setOffs {
		rs, err := parsePosChainRuleSet(subslice(b, so))
		if err != nil {
			return nil, err
		}
		sets[i] = rs
	}
	return &chainPos1{cov: cov, sets: sets}, nil
}

// parsePosChainRuleSet decodes a ChainPosRuleSet table.
func parsePosChainRuleSet(b []byte) ([]posChainRule, error) {
	r := reader{b: b}
	n := int(r.u16())
	offs := make([]int, n)
	for i := 0; i < n; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: chainRuleSet: %w", r.err)
	}
	rules := make([]posChainRule, n)
	for i, off := range offs {
		rule, err := parsePosChainRule(subslice(b, off))
		if err != nil {
			return nil, err
		}
		rules[i] = rule
	}
	return rules, nil
}

// parsePosChainRule decodes one ChainPosRule table.
func parsePosChainRule(b []byte) (posChainRule, error) {
	r := reader{b: b}
	bn := int(r.u16())
	back := make([]GlyphIndex, bn)
	for k := 0; k < bn; k++ {
		back[k] = GlyphIndex(r.u16())
	}
	in := int(r.u16())
	input := make([]GlyphIndex, 0, in)
	for k := 1; k < in; k++ {
		input = append(input, GlyphIndex(r.u16()))
	}
	an := int(r.u16())
	ahead := make([]GlyphIndex, an)
	for k := 0; k < an; k++ {
		ahead[k] = GlyphIndex(r.u16())
	}
	ln := int(r.u16())
	recs := readPosLookupRecords(&r, ln)
	if r.err != nil {
		return posChainRule{}, fmt.Errorf("opentype: posChainRule: %w", r.err)
	}
	return posChainRule{back: back, input: input, ahead: ahead, recs: recs}, nil
}

func (c *chainPos1) apply(ctx *posContext, i int) bool {
	ci, ok := c.cov[ctx.glyphs[i]]
	if !ok {
		return false
	}
	if ci >= len(c.sets) {
		return false
	}
	for _, rule := range c.sets[ci] {
		matched, end, ok := ctx.skip.matchForward(ctx.glyphs, i+1, glyphPredsG(rule.input))
		if !ok ||
			!ctx.skip.matchBackward(ctx.glyphs, i-1, glyphPredsG(rule.back)) {
			continue
		}
		if _, _, ok := ctx.skip.matchForward(ctx.glyphs, end, glyphPredsG(rule.ahead)); !ok {
			continue
		}
		ctx.applyRecords(rule.recs, append([]int{i}, matched...))
		return true
	}
	return false
}

// parseChainPos3 decodes a chaining contextual positioning format 3 subtable.
func parseChainPos3(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat
	bn := int(r.u16())
	backOffs := readOffsets(&r, bn)
	in := int(r.u16())
	inputOffs := readOffsets(&r, in)
	an := int(r.u16())
	aheadOffs := readOffsets(&r, an)
	ln := int(r.u16())
	recs := readPosLookupRecords(&r, ln)
	if r.err != nil {
		return nil, fmt.Errorf("opentype: chainpos3: %w", r.err)
	}
	back, err := parsePosCoverageList(b, backOffs)
	if err != nil {
		return nil, err
	}
	input, err := parsePosCoverageList(b, inputOffs)
	if err != nil {
		return nil, err
	}
	ahead, err := parsePosCoverageList(b, aheadOffs)
	if err != nil {
		return nil, err
	}
	return &chainPos3{back: back, input: input, ahead: ahead, recs: recs}, nil
}

// readOffsets reads n uint16 offsets.
func readOffsets(r *reader, n int) []int {
	offs := make([]int, n)
	for i := range offs {
		offs[i] = int(r.u16())
	}
	return offs
}

func (c *chainPos3) apply(ctx *posContext, i int) bool {
	matched, end, ok := ctx.skip.matchContext(ctx.glyphs, i, c.input)
	if !ok ||
		!ctx.skip.matchBackward(ctx.glyphs, i-1, covPreds(c.back)) {
		return false
	}
	if _, _, ok := ctx.skip.matchForward(ctx.glyphs, end, covPreds(c.ahead)); !ok {
		return false
	}
	ctx.applyRecords(c.recs, matched)
	return true
}

// --- Type 9: extension positioning ------------------------------------------

// parseExtensionPos decodes an ExtensionPos format 1 subtable, following the
// indirection to the real subtable of extensionLookupType.
func parseExtensionPos(b []byte) (posSubtable, error) {
	r := reader{b: b}
	r.skip(2) // posFormat (1)
	extType := r.u16()
	off := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: extensionpos: %w", r.err)
	}
	if extType == 9 {
		return nil, fmt.Errorf("opentype: nested extension positioning")
	}
	return parseGPOSSubtable(extType, subslice(b, off))
}

// --- Application over a glyph run -------------------------------------------

// posContext carries the run being positioned and the accumulating adjustments
// while a lookup (and any lookups it nests) is applied.
type posContext struct {
	g         *gpos
	glyphs    []GlyphIndex
	positions []GlyphPosition
	advances  []int
	skip      skipper // skipper of the lookup currently being applied
}

// advance returns the effective X advance of glyph k (its base advance, when
// supplied, plus any adjustment applied so far).
func (ctx *posContext) advance(k int) int {
	a := ctx.positions[k].XAdvance
	if k < len(ctx.advances) {
		a += ctx.advances[k]
	}
	return a
}

// applyRecords runs a context's PosLookupRecords. positions holds the absolute
// run indices of the matched input glyphs (skipped glyphs excluded), so a
// record's seqIndex selects the position it acts on; a record whose seqIndex
// lies past the matched input is a no-op.
func (ctx *posContext) applyRecords(recs []posLookupRecord, positions []int) {
	for _, rec := range recs {
		si := int(rec.seqIndex)
		if si >= len(positions) {
			continue
		}
		ctx.g.applyLookupAt(int(rec.lookupIndex), ctx, positions[si])
	}
}

// applyLookup applies one lookup left-to-right across the whole run, taking the
// first subtable that matches at each position. A glyph the lookup's flags
// ignore is not a starting position and is skipped.
func (p *gpos) applyLookup(li int, ctx *posContext) {
	if li < 0 || li >= len(p.lookups) {
		return
	}
	lk := p.lookups[li]
	ctx.skip = p.skipperFor(lk)
	for i := 0; i < len(ctx.glyphs); i++ {
		if ctx.skip.skip(ctx.glyphs[i]) {
			continue
		}
		for _, st := range lk.subtables {
			if st.apply(ctx, i) {
				break
			}
		}
	}
}

// applyLookupAt applies one lookup at a single run position, taking the first
// matching subtable; it reports whether any subtable matched. The nested lookup
// matches under its own lookupFlag, so the context's skipper is swapped for the
// duration and restored afterwards.
func (p *gpos) applyLookupAt(li int, ctx *posContext, i int) bool {
	if li < 0 || li >= len(p.lookups) {
		return false
	}
	if i < 0 || i >= len(ctx.glyphs) {
		return false
	}
	saved := ctx.skip
	ctx.skip = p.skipperFor(p.lookups[li])
	matched := false
	for _, st := range p.lookups[li].subtables {
		if st.apply(ctx, i) {
			matched = true
			break
		}
	}
	ctx.skip = saved
	return matched
}

// position applies the GPOS lookups activated by the given feature tags over a
// glyph run and returns one adjustment per glyph. advances gives each glyph's
// base horizontal advance in font units (used to pull marks back onto their
// base); it may be nil, treated as all-zero. The returned slice has the same
// length as glyphs; an empty run or no matching lookups yields all-zero
// adjustments.
func (p *gpos) position(glyphs []GlyphIndex, advances []int, features ...string) []GlyphPosition {
	positions := make([]GlyphPosition, len(glyphs))
	if len(glyphs) == 0 {
		return positions
	}
	want := make(map[string]bool, len(features))
	for _, f := range features {
		want[f] = true
	}
	ctx := &posContext{g: p, glyphs: glyphs, positions: positions, advances: advances}
	for _, li := range p.selectLookupsDefault(want) {
		p.applyLookup(li, ctx)
	}
	return positions
}

// --- Feature/script selection ------------------------------------------------

// featureLookups accumulates, into out (de-duplicated via seen), the lookup
// indices activated by the wanted feature tags of one LangSys, including its
// required feature.
func (h *layoutHeader) featureLookups(ls *langSys, want map[string]bool, seen map[int]bool, out *[]int) {
	add := func(featIdx uint16) {
		if int(featIdx) >= len(h.features) {
			return
		}
		fr := h.features[featIdx]
		if !want[fr.tag] {
			return
		}
		for _, li := range fr.lookups {
			if !seen[int(li)] {
				seen[int(li)] = true
				*out = append(*out, int(li))
			}
		}
	}
	if ls.required != 0xFFFF {
		add(ls.required)
	}
	for _, fi := range ls.features {
		add(fi)
	}
}

// selectLookupsDefault resolves the lookups for the wanted features using the
// default (script-agnostic) resolution real text runs use: it first tries the
// DFLT script's default LangSys, then falls back to unioning the wanted
// features across every script's default LangSys. The fallback is what makes a
// font that registers "kern"/"mark"/"mkmk" only under a real script (such as
// latn on Source Serif, or arab) resolve when a bare DFLT lookup was empty.
func (h *layoutHeader) selectLookupsDefault(want map[string]bool) []int {
	if out := h.selectLookups("DFLT", "", want); len(out) > 0 {
		return out
	}
	seen := map[int]bool{}
	var out []int
	for i := range h.scripts {
		if ls := h.scripts[i].defaultLang; ls != nil {
			h.featureLookups(ls, want, seen, &out)
		}
	}
	sort.Ints(out)
	return out
}

// Kern returns the X-advance adjustment GPOS applies between left and right via
// the "kern" feature, or 0 when no pair-adjustment lookup covers the pair. The
// kern feature is resolved with the default script-agnostic fallback, so fonts
// that file kerning under a real script (not DFLT) still resolve.
func (p *gpos) Kern(left, right GlyphIndex) int {
	idxs := p.selectLookupsDefault(map[string]bool{"kern": true})
	for _, li := range idxs {
		if li >= len(p.lookups) {
			continue
		}
		for _, st := range p.lookups[li].subtables {
			pp, ok := st.(pairPos)
			if !ok {
				continue
			}
			if v, ok := pp.kern(left, right); ok {
				return v
			}
		}
	}
	return 0
}

// Kerner combines GPOS and the legacy kern table: GPOS is preferred and the
// kern table is a fallback used when GPOS yields no adjustment. Either source
// may be nil.
type Kerner struct {
	gpos *gpos
	kern *kern
}

// Kerning returns the X-advance adjustment between left and right, preferring a
// non-zero GPOS value and otherwise falling back to the legacy kern table.
func (kn *Kerner) Kerning(left, right GlyphIndex) int {
	if kn.gpos != nil {
		if v := kn.gpos.Kern(left, right); v != 0 {
			return v
		}
	}
	if kn.kern != nil {
		return kn.kern.Pair(left, right)
	}
	return 0
}
