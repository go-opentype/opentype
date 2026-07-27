// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// cmapLookup maps a rune to a glyph index. ok is false when the rune is not
// mapped by the selected subtable (including a mapping to the .notdef glyph).
type cmapLookup interface {
	lookup(r rune) (GlyphIndex, bool)
}

// parseCmap selects and decodes a Unicode cmap subtable. It understands
// formats 4 (BMP) and 12 (full Unicode), preferring 12 when both are present.
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
		r.skip(4) // platformID, encodingID
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
		case 4:
			lk, err = parseCmap4(sub)
			score = 1
		case 12:
			lk, err = parseCmap12(sub)
			score = 2
		default:
			continue
		}
		if err != nil {
			return err
		}
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
