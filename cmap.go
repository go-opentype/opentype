// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// cmapLookup maps a rune to a glyph index. ok is false when the rune is not
// mapped by the selected subtable (including a mapping to the .notdef glyph).
//
// reverse answers the other question: which code reaches a given glyph. A
// subtable is a many-to-one map — a space and a no-break space commonly share
// one glyph — so the inverse keeps the lowest code that reaches each glyph,
// and is built once for the whole subtable rather than searched per glyph.
//
// budget bounds how many codes the walk may visit. Most formats describe no
// more codes than their own bytes can hold and ignore it; formats 4, 8 and 12
// do not — one group of a format-12 subtable can claim the whole Unicode
// space, and a damaged one can claim more — so those three spend it.
type cmapLookup interface {
	lookup(r rune) (GlyphIndex, bool)
	reverse(budget int) map[GlyphIndex]rune
}

// maxReverseCodes is the budget Font.RuneOfGlyphInMap gives a walk. A subtable
// that describes more than a million codes is describing far more than any
// inverse of it will be asked about, and the bound keeps a malformed font from
// costing more than a well-formed one.
const maxReverseCodes = 1 << 20

// reverseRange records the codes [first, first+count) as reaching the glyphs
// starting at gid, one glyph per code, into inv. It keeps the lowest code for
// each glyph and reports how many codes it consumed of the budget left.
func reverseRange(inv map[GlyphIndex]rune, first, count, gid uint32, budget int) int {
	if uint32(budget) < count {
		count = uint32(budget)
	}
	for i := uint32(0); i < count; i++ {
		g := GlyphIndex(gid + i)
		if g == 0 {
			continue
		}
		if _, seen := inv[g]; !seen {
			inv[g] = rune(first + i)
		}
	}
	return int(count)
}

// cmapSubtable is one decoded cmap subtable together with the platform and
// encoding it was written for. Which of them to address a font through is the
// caller's decision and cannot be made here: a font embedded in a PDF is
// addressed the way the document says, and only the document knows.
type cmapSubtable struct {
	platform uint16
	encoding uint16
	format   uint16
	lookup   cmapLookup
	inverse  map[GlyphIndex]rune // built on first use by Font.RuneOfGlyphInMap
}

// parseCmap selects and decodes a Unicode cmap subtable. It understands
// formats 0, 2, 4, 6, 8, 10, 12 and 13 for the base rune->glyph mapping,
// preferring whichever can represent the most codepoints:
// 13 = 12 > 10 > 8 > 4 > 6 > 2 > 0.
// Format 14 (Unicode Variation Sequences) is a separate, always-parsed side
// table exposed through Font.GlyphIndexVariation; it never competes for f.cmap
// and its presence does not change ordinary GlyphIndex behaviour.
func (f *Font) parseCmap(b []byte) error {
	r := reader{b: b}
	r.skip(2) // version
	numTables := int(r.u16())
	if r.err != nil {
		return fmt.Errorf("opentype: cmap header: %w", r.err)
	}

	var best cmapLookup
	bestScore := 0
	for i := 0; i < numTables; i++ {
		platform := r.u16()
		encoding := r.u16()
		offset := int(r.u32())
		if r.err != nil {
			return fmt.Errorf("opentype: cmap record: %w", r.err)
		}
		if offset+2 > len(b) {
			return fmt.Errorf("opentype: cmap subtable offset: %w", errTruncated)
		}
		sub := b[offset:]
		format := be16(sub)
		var (
			lk    cmapLookup
			score int
			err   error
		)
		switch format {
		case 0:
			lk, err = parseCmap0(sub)
			score = 1
		case 2:
			lk, err = parseCmap2(sub)
			score = 2
		case 6:
			lk, err = parseCmap6(sub)
			score = 3
		case 4:
			lk, err = parseCmap4(sub)
			score = 4
		case 8:
			lk, err = parseCmap8(sub)
			score = 5
		case 10:
			lk, err = parseCmap10(sub)
			score = 6
		case 12:
			lk, err = parseCmap12(sub)
			score = 7
		case 13:
			lk, err = parseCmap13(sub)
			score = 7
		case 14:
			vs, verr := parseCmap14(sub)
			if verr != nil {
				return verr
			}
			f.cmapVS = vs
			continue
		default:
			continue
		}
		if err != nil {
			return err
		}
		f.cmaps = append(f.cmaps, cmapSubtable{
			platform: platform,
			encoding: encoding,
			format:   format,
			lookup:   lk,
		})
		if score > bestScore {
			best = lk
			bestScore = score
		}
	}
	if best == nil {
		return fmt.Errorf("opentype: no supported cmap subtable")
	}
	f.cmap = best
	return nil
}

// cmap4 is a decoded format-4 (segmented, BMP) cmap subtable.
type cmap4 struct {
	segCount      int
	endCode       []uint16
	startCode     []uint16
	idDelta       []int16
	idRangeOffset []uint16
	glyphIDArray  []uint16
}

// parseCmap4 decodes a format-4 subtable from sub (which starts at the format
// field).
func parseCmap4(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(6) // format, length, language
	segX2 := int(r.u16())
	segCount := segX2 / 2
	r.skip(6) // searchRange, entrySelector, rangeShift
	c := &cmap4{
		segCount:      segCount,
		endCode:       make([]uint16, segCount),
		startCode:     make([]uint16, segCount),
		idDelta:       make([]int16, segCount),
		idRangeOffset: make([]uint16, segCount),
	}
	for i := 0; i < segCount; i++ {
		c.endCode[i] = r.u16()
	}
	r.skip(2) // reservedPad
	for i := 0; i < segCount; i++ {
		c.startCode[i] = r.u16()
	}
	for i := 0; i < segCount; i++ {
		c.idDelta[i] = r.i16()
	}
	for i := 0; i < segCount; i++ {
		c.idRangeOffset[i] = r.u16()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 4: %w", r.err)
	}
	// The remaining bytes are the glyphIdArray (uint16 each).
	rest := (len(sub) - r.pos) / 2
	c.glyphIDArray = make([]uint16, rest)
	for i := 0; i < rest; i++ {
		c.glyphIDArray[i] = r.u16()
	}
	return c, nil
}

// lookup implements the format-4 segment search with idDelta/idRangeOffset.
func (c *cmap4) lookup(r rune) (GlyphIndex, bool) {
	if uint32(r) > 0xFFFF {
		return 0, false
	}
	cc := uint16(r)
	for i := 0; i < c.segCount; i++ {
		if c.endCode[i] < cc {
			continue
		}
		if c.startCode[i] > cc {
			return 0, false
		}
		var g uint16
		if c.idRangeOffset[i] == 0 {
			g = cc + uint16(c.idDelta[i])
		} else {
			idx := int(c.idRangeOffset[i]/2) + int(cc-c.startCode[i]) - (c.segCount - i)
			if idx < 0 || idx >= len(c.glyphIDArray) {
				return 0, false
			}
			g = c.glyphIDArray[idx]
			if g != 0 {
				g += uint16(c.idDelta[i])
			}
		}
		if g == 0 {
			return 0, false
		}
		return GlyphIndex(g), true
	}
	return 0, false
}

// reverse walks the segments in code order, resolving each code the same way
// lookup does, and keeps the lowest code that reaches each glyph.
func (c *cmap4) reverse(budget int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for i := 0; i < c.segCount; i++ {
		if c.startCode[i] > c.endCode[i] {
			continue
		}
		for cc := uint32(c.startCode[i]); cc <= uint32(c.endCode[i]); cc++ {
			if budget == 0 {
				return inv
			}
			budget--
			g, ok := c.lookup(rune(cc))
			if !ok {
				continue
			}
			if _, seen := inv[g]; !seen {
				inv[g] = rune(cc)
			}
		}
	}
	return inv
}

// cmap12group is one sequential-map group of a format-12 subtable.
type cmap12group struct {
	start    uint32
	end      uint32
	startGID uint32
}

// cmap12 is a decoded format-12 (segmented coverage, full Unicode) subtable.
type cmap12 struct {
	groups []cmap12group
}

// parseCmap12 decodes a format-12 subtable from sub (starting at the format
// field).
func parseCmap12(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(12) // format, reserved, length, language
	numGroups := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 12 header: %w", r.err)
	}
	c := &cmap12{groups: make([]cmap12group, numGroups)}
	for i := 0; i < numGroups; i++ {
		c.groups[i].start = r.u32()
		c.groups[i].end = r.u32()
		c.groups[i].startGID = r.u32()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 12 groups: %w", r.err)
	}
	return c, nil
}

// lookup implements the format-12 binary search over sorted groups.
func (c *cmap12) lookup(r rune) (GlyphIndex, bool) {
	cc := uint32(r)
	lo, hi := 0, len(c.groups)
	for lo < hi {
		mid := (lo + hi) / 2
		g := c.groups[mid]
		if cc < g.start {
			hi = mid
		} else if cc > g.end {
			lo = mid + 1
		} else {
			return GlyphIndex(g.startGID + (cc - g.start)), true
		}
	}
	return 0, false
}

// reverse expands each group: a group advances one glyph per code point, so
// the codes it covers reach a contiguous run of glyphs.
func (c *cmap12) reverse(budget int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for _, g := range c.groups {
		if g.end < g.start {
			continue
		}
		budget -= reverseRange(inv, g.start, g.end-g.start+1, g.startGID, budget)
		if budget == 0 {
			break
		}
	}
	return inv
}

// cmap13group is one constant-map group of a format-13 subtable: every code
// point in [start, end] maps to the single glyph glyphID.
type cmap13group struct {
	start   uint32
	end     uint32
	glyphID uint32
}

// cmap13 is a decoded format-13 (many-to-one range mappings) subtable. Its
// on-disk layout is identical to format 12, but the third group field is read
// with different semantics: format 12's startGlyphID advances one glyph per
// code point across a group, whereas format 13's glyphID is a constant so the
// whole range collapses onto a single glyph. This is what LastResort and other
// fallback fonts use to point every code point in a block at one "missing
// glyph" placeholder.
type cmap13 struct {
	groups []cmap13group
}

// parseCmap13 decodes a format-13 subtable from sub (starting at the format
// field).
func parseCmap13(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(12) // format, reserved, length, language
	numGroups := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 13 header: %w", r.err)
	}
	c := &cmap13{groups: make([]cmap13group, numGroups)}
	for i := 0; i < numGroups; i++ {
		c.groups[i].start = r.u32()
		c.groups[i].end = r.u32()
		c.groups[i].glyphID = r.u32()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 13 groups: %w", r.err)
	}
	return c, nil
}

// lookup implements the format-13 binary search over sorted groups, returning
// the group's constant glyph for any code point it covers.
func (c *cmap13) lookup(r rune) (GlyphIndex, bool) {
	cc := uint32(r)
	lo, hi := 0, len(c.groups)
	for lo < hi {
		mid := (lo + hi) / 2
		g := c.groups[mid]
		if cc < g.start {
			hi = mid
		} else if cc > g.end {
			lo = mid + 1
		} else {
			return GlyphIndex(g.glyphID), true
		}
	}
	return 0, false
}

// reverse keeps one code per group: every code point a format-13 group covers
// reaches the same glyph, so only the lowest of them is worth recording, and a
// group covering a whole Unicode block costs one entry rather than a million.
// The budget goes unspent: a group costs one entry however many codes it
// covers, so the walk is only as long as the subtable itself.
func (c *cmap13) reverse(int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for _, g := range c.groups {
		if g.end < g.start || g.glyphID == 0 {
			continue
		}
		gid := GlyphIndex(g.glyphID)
		if _, seen := inv[gid]; !seen {
			inv[gid] = rune(g.start)
		}
	}
	return inv
}

// cmap8Group is one sequential-map group of a format-8 subtable; identical in
// shape and semantics to a format-12 group (a range advancing one glyph per
// code point from startGID).
type cmap8Group struct {
	start    uint32
	end      uint32
	startGID uint32
}

// cmap8 is a decoded format-8 (mixed 16/32-bit coverage) subtable. is32 is
// the on-disk 65536-bit bitmap (8192 bytes) flagging which 16-bit lead code
// units begin a 32-bit (surrogate-pair) character rather than standing alone
// as a BMP character; it is retained for spec fidelity but, like other
// implementations, is not consulted by lookup, since lookup already receives
// a fully decoded rune rather than raw UTF-16 code units — the groups alone
// are sufficient to resolve it, exactly as in format 12.
type cmap8 struct {
	is32   [8192]byte
	groups []cmap8Group
}

// parseCmap8 decodes a format-8 subtable from sub (starting at the format
// field).
func parseCmap8(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(12) // format, reserved, length, language
	var c cmap8
	for i := range c.is32 {
		c.is32[i] = r.u8()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 8 is32: %w", r.err)
	}
	numGroups := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 8 header: %w", r.err)
	}
	c.groups = make([]cmap8Group, numGroups)
	for i := range c.groups {
		c.groups[i].start = r.u32()
		c.groups[i].end = r.u32()
		c.groups[i].startGID = r.u32()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 8 groups: %w", r.err)
	}
	return &c, nil
}

// lookup implements the format-8 binary search over sorted groups, the same
// algorithm as format 12's lookup (the is32 bitmap plays no role once the
// input is already a decoded rune).
func (c *cmap8) lookup(r rune) (GlyphIndex, bool) {
	cc := uint32(r)
	lo, hi := 0, len(c.groups)
	for lo < hi {
		mid := (lo + hi) / 2
		g := c.groups[mid]
		if cc < g.start {
			hi = mid
		} else if cc > g.end {
			lo = mid + 1
		} else {
			return GlyphIndex(g.startGID + (cc - g.start)), true
		}
	}
	return 0, false
}

// reverse expands each group, exactly as format 12's does: the two formats
// describe their groups the same way.
func (c *cmap8) reverse(budget int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for _, g := range c.groups {
		if g.end < g.start {
			continue
		}
		budget -= reverseRange(inv, g.start, g.end-g.start+1, g.startGID, budget)
		if budget == 0 {
			break
		}
	}
	return inv
}

// cmap0 is a decoded format-0 (byte encoding table) subtable: a flat 256-byte
// glyph-id array covering U+0000-U+00FF.
type cmap0 struct {
	glyphIDArray [256]byte
}

// parseCmap0 decodes a format-0 subtable from sub (starting at the format
// field).
func parseCmap0(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(6) // format, length, language
	var c cmap0
	for i := range c.glyphIDArray {
		c.glyphIDArray[i] = r.u8()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 0: %w", r.err)
	}
	return &c, nil
}

// lookup returns the direct array entry for r, or false when r is outside
// U+0000-U+00FF or maps to the .notdef glyph.
func (c *cmap0) lookup(r rune) (GlyphIndex, bool) {
	if uint32(r) > 0xFF {
		return 0, false
	}
	g := c.glyphIDArray[r]
	if g == 0 {
		return 0, false
	}
	return GlyphIndex(g), true
}

// reverse walks the 256-entry array, keeping the lowest code per glyph.
// The budget goes unspent: the array is 256 entries.
func (c *cmap0) reverse(int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for code, g := range c.glyphIDArray {
		if g == 0 {
			continue
		}
		if _, seen := inv[GlyphIndex(g)]; !seen {
			inv[GlyphIndex(g)] = rune(code)
		}
	}
	return inv
}

// cmap6 is a decoded format-6 (trimmed table mapping) subtable: a
// contiguous, zero-based glyph-id array starting at firstCode.
type cmap6 struct {
	firstCode    uint16
	glyphIDArray []uint16
}

// parseCmap6 decodes a format-6 subtable from sub (starting at the format
// field).
func parseCmap6(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(6) // format, length, language
	firstCode := r.u16()
	entryCount := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 6 header: %w", r.err)
	}
	c := &cmap6{firstCode: firstCode, glyphIDArray: make([]uint16, entryCount)}
	for i := range c.glyphIDArray {
		c.glyphIDArray[i] = r.u16()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 6: %w", r.err)
	}
	return c, nil
}

// lookup returns the trimmed-table entry for r, or false when r falls
// outside [firstCode, firstCode+len(glyphIDArray)) or maps to .notdef.
func (c *cmap6) lookup(r rune) (GlyphIndex, bool) {
	if uint32(r) < uint32(c.firstCode) {
		return 0, false
	}
	idx := uint32(r) - uint32(c.firstCode)
	if idx >= uint32(len(c.glyphIDArray)) {
		return 0, false
	}
	g := c.glyphIDArray[idx]
	if g == 0 {
		return 0, false
	}
	return GlyphIndex(g), true
}

// reverse walks the trimmed array, keeping the lowest code per glyph.
// The budget goes unspent: the array is no longer than the subtable said.
func (c *cmap6) reverse(int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for i, g := range c.glyphIDArray {
		if g == 0 {
			continue
		}
		if _, seen := inv[GlyphIndex(g)]; !seen {
			inv[GlyphIndex(g)] = rune(uint32(c.firstCode) + uint32(i))
		}
	}
	return inv
}

// cmap10 is a decoded format-10 (trimmed array) subtable: the 32-bit
// analogue of format 6 — a contiguous, zero-based glyph-id array starting at
// startCharCode, wide enough to cover astral code points.
type cmap10 struct {
	startCharCode uint32
	glyphIDArray  []uint16
}

// parseCmap10 decodes a format-10 subtable from sub (starting at the format
// field).
func parseCmap10(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(12) // format, reserved, length, language
	startCharCode := r.u32()
	numChars := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 10 header: %w", r.err)
	}
	c := &cmap10{startCharCode: startCharCode, glyphIDArray: make([]uint16, numChars)}
	for i := range c.glyphIDArray {
		c.glyphIDArray[i] = r.u16()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 10: %w", r.err)
	}
	return c, nil
}

// lookup returns the trimmed-table entry for r, or false when r falls
// outside [startCharCode, startCharCode+len(glyphIDArray)) or maps to
// .notdef.
func (c *cmap10) lookup(r rune) (GlyphIndex, bool) {
	if uint32(r) < c.startCharCode {
		return 0, false
	}
	idx := uint32(r) - c.startCharCode
	if idx >= uint32(len(c.glyphIDArray)) {
		return 0, false
	}
	g := c.glyphIDArray[idx]
	if g == 0 {
		return 0, false
	}
	return GlyphIndex(g), true
}

// reverse walks the trimmed array, keeping the lowest code per glyph.
// The budget goes unspent: the array is no longer than the subtable said.
func (c *cmap10) reverse(int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for i, g := range c.glyphIDArray {
		if g == 0 {
			continue
		}
		if _, seen := inv[GlyphIndex(g)]; !seen {
			inv[GlyphIndex(g)] = rune(c.startCharCode + uint32(i))
		}
	}
	return inv
}

// cmap2SubHeader is one decoded subHeader record of a format-2 subtable.
// fieldOffset is the byte offset of this subHeader's idRangeOffset field
// relative to the start of glyphIndexArray, precomputed so lookup can turn
// (fieldOffset + idRangeOffset + 2*index) into a glyphIndexArray index the
// same way format 4 turns its own idRangeOffset into an array index.
type cmap2SubHeader struct {
	firstCode     uint16
	entryCount    uint16
	idDelta       int16
	idRangeOffset uint16
	fieldOffset   int
}

// cmap2 is a decoded format-2 (high-byte mapping through table) subtable,
// the legacy encoding for mixed single/double-byte CJK charsets. The code
// passed to lookup is treated as a raw 1- or 2-byte legacy code (its low 16
// bits), exactly like the other cmap formats treat their input as a raw
// code rather than interpreting charset semantics.
type cmap2 struct {
	subHeaderKeys   [256]uint16 // per high-byte value, subHeaders' byte offset (index*8)
	subHeaders      []cmap2SubHeader
	glyphIndexArray []uint16
}

// parseCmap2 decodes a format-2 subtable from sub (starting at the format
// field). The subtable has no explicit subHeader count; it is derived from
// the highest offset referenced by subHeaderKeys, following the same
// convention used by other OpenType/TrueType cmap format-2 parsers.
func parseCmap2(sub []byte) (cmapLookup, error) {
	r := reader{b: sub}
	r.skip(6) // format, length, language
	var c cmap2
	for i := range c.subHeaderKeys {
		c.subHeaderKeys[i] = r.u16()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 2 keys: %w", r.err)
	}

	var maxKey uint16
	for _, k := range c.subHeaderKeys {
		if k > maxKey {
			maxKey = k
		}
	}
	numSubHeaders := int(maxKey)/8 + 1
	glyphIndexArrayStart := r.pos + numSubHeaders*8

	c.subHeaders = make([]cmap2SubHeader, numSubHeaders)
	for i := range c.subHeaders {
		fieldPos := r.pos + 6 // firstCode(2)+entryCount(2)+idDelta(2) precede idRangeOffset
		c.subHeaders[i] = cmap2SubHeader{
			firstCode:     r.u16(),
			entryCount:    r.u16(),
			idDelta:       r.i16(),
			idRangeOffset: r.u16(),
			fieldOffset:   fieldPos - glyphIndexArrayStart,
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 2 subheaders: %w", r.err)
	}

	// The remaining bytes are the glyphIndexArray (uint16 each); rest is sized
	// to whatever fits, so this run of reads never overruns.
	rest := (len(sub) - r.pos) / 2
	c.glyphIndexArray = make([]uint16, rest)
	for i := range c.glyphIndexArray {
		c.glyphIndexArray[i] = r.u16()
	}
	return &c, nil
}

// lookup implements the format-2 high-byte dispatch: the high byte of r
// selects a subHeader via subHeaderKeys (a value of 0 selects subHeader 0,
// which also serves single-byte codes, since a single-byte code has an
// implicit high byte of 0), then the low byte is checked against that
// subHeader's [firstCode, firstCode+entryCount) range and translated through
// glyphIndexArray and idDelta, mirroring format 4's idRangeOffset scheme.
func (c *cmap2) lookup(r rune) (GlyphIndex, bool) {
	if uint32(r) > 0xFFFF {
		return 0, false
	}
	code := uint32(r)
	hi := (code >> 8) & 0xFF
	lo := code & 0xFF

	sh := c.subHeaders[int(c.subHeaderKeys[hi])/8]
	rel := lo - uint32(sh.firstCode)
	if rel >= uint32(sh.entryCount) || sh.idRangeOffset == 0 {
		return 0, false
	}
	gidx := (sh.fieldOffset + int(sh.idRangeOffset) + 2*int(rel)) / 2
	if gidx < 0 || gidx >= len(c.glyphIndexArray) {
		return 0, false
	}
	g := c.glyphIndexArray[gidx]
	if g == 0 {
		return 0, false
	}
	return GlyphIndex(uint16(int32(g) + int32(sh.idDelta))), true
}

// reverse walks every code the high-byte dispatch can reach, resolving each
// the same way lookup does. A single-byte code has an implicit high byte of
// zero, so the walk covers the whole 16-bit space rather than only the codes
// some subHeader claims.
// The budget goes unspent: the walk is the 16-bit code space, once.
func (c *cmap2) reverse(int) map[GlyphIndex]rune {
	inv := make(map[GlyphIndex]rune)
	for cc := uint32(0); cc <= 0xFFFF; cc++ {
		g, ok := c.lookup(rune(cc))
		if !ok {
			continue
		}
		if _, seen := inv[g]; !seen {
			inv[g] = rune(cc)
		}
	}
	return inv
}

// cmap14DefaultRange is one entry of a format-14 default-UVS table: base
// runes in [start, start+count) pair with this variation selector to
// resolve through the font's ordinary cmap rather than an explicit glyph.
type cmap14DefaultRange struct {
	start uint32
	count uint32
}

// cmap14NonDefault is one entry of a format-14 non-default-UVS table: base
// rune unicode paired with this variation selector resolves to glyph
// directly, bypassing the ordinary cmap.
type cmap14NonDefault struct {
	unicode uint32
	glyph   GlyphIndex
}

// cmap14Record is one variation-selector record of a format-14 subtable.
type cmap14Record struct {
	varSelector   uint32
	defaultRanges []cmap14DefaultRange
	nonDefault    []cmap14NonDefault
}

// cmap14 is a decoded format-14 (Unicode Variation Sequences) subtable. It is
// never installed as Font.cmap: it is a side table consulted only through
// GlyphIndexVariation.
type cmap14 struct {
	records []cmap14Record
}

// parseCmap14 decodes a format-14 subtable from sub (starting at the format
// field).
func parseCmap14(sub []byte) (*cmap14, error) {
	r := reader{b: sub}
	r.skip(6) // format, length
	numRecords := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 14 header: %w", r.err)
	}

	type rawRecord struct {
		varSelector               uint32
		defaultOff, nonDefaultOff uint32
	}
	raws := make([]rawRecord, numRecords)
	for i := range raws {
		raws[i] = rawRecord{
			varSelector:   r.u24(),
			defaultOff:    r.u32(),
			nonDefaultOff: r.u32(),
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 14 records: %w", r.err)
	}

	c := &cmap14{records: make([]cmap14Record, numRecords)}
	for i, raw := range raws {
		c.records[i].varSelector = raw.varSelector
		if raw.defaultOff != 0 {
			ranges, err := parseUVSDefaultTable(sub, int(raw.defaultOff))
			if err != nil {
				return nil, err
			}
			c.records[i].defaultRanges = ranges
		}
		if raw.nonDefaultOff != 0 {
			maps, err := parseUVSNonDefaultTable(sub, int(raw.nonDefaultOff))
			if err != nil {
				return nil, err
			}
			c.records[i].nonDefault = maps
		}
	}
	return c, nil
}

// parseUVSDefaultTable decodes a Default UVS table at byte offset off within
// sub (the format-14 subtable, offsets are subtable-relative). The offset
// itself is only checked against sub's length here; a short read of the
// table's own fields is reported separately by the reader below.
func parseUVSDefaultTable(sub []byte, off int) ([]cmap14DefaultRange, error) {
	if off > len(sub) {
		return nil, fmt.Errorf("opentype: cmap format 14 default UVS offset: %w", errTruncated)
	}
	r := reader{b: sub[off:]}
	numRanges := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 14 default UVS header: %w", r.err)
	}
	ranges := make([]cmap14DefaultRange, numRanges)
	for i := range ranges {
		start := r.u24()
		additionalCount := r.u8()
		ranges[i] = cmap14DefaultRange{start: start, count: uint32(additionalCount) + 1}
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 14 default UVS ranges: %w", r.err)
	}
	return ranges, nil
}

// parseUVSNonDefaultTable decodes a Non-Default UVS table at byte offset off
// within sub (the format-14 subtable, offsets are subtable-relative). The
// offset itself is only checked against sub's length here; a short read of
// the table's own fields is reported separately by the reader below.
func parseUVSNonDefaultTable(sub []byte, off int) ([]cmap14NonDefault, error) {
	if off > len(sub) {
		return nil, fmt.Errorf("opentype: cmap format 14 non-default UVS offset: %w", errTruncated)
	}
	r := reader{b: sub[off:]}
	numMappings := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 14 non-default UVS header: %w", r.err)
	}
	maps := make([]cmap14NonDefault, numMappings)
	for i := range maps {
		maps[i] = cmap14NonDefault{unicode: r.u24(), glyph: GlyphIndex(r.u16())}
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cmap format 14 non-default UVS mappings: %w", r.err)
	}
	return maps, nil
}

// lookupVariation resolves (r, vs) against c's records. A non-default
// mapping (an explicit glyph) takes precedence over a default-range match
// (which falls back to f's ordinary cmap); a vs with no record, or an r
// matched by neither its non-default mappings nor its default ranges,
// reports not-ok.
func (c *cmap14) lookupVariation(f *Font, r, vs rune) (GlyphIndex, bool) {
	target := uint32(vs)
	for _, rec := range c.records {
		if rec.varSelector != target {
			continue
		}
		cc := uint32(r)
		for _, nd := range rec.nonDefault {
			if nd.unicode == cc {
				return nd.glyph, true
			}
		}
		for _, dr := range rec.defaultRanges {
			if cc >= dr.start && cc-dr.start < dr.count {
				return f.GlyphIndex(r)
			}
		}
		return 0, false
	}
	return 0, false
}
