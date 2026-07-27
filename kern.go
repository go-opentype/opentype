// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file parses the legacy 'kern' table in its Microsoft/OpenType form
// (table version 0), decoding only format-0 subtables: a flat list of
// (left, right) glyph pairs each with a signed kerning value in font units.
// Non-format-0 subtables are skipped by their declared length. The Apple
// version-1 ('kern' with a 32-bit version) header is not handled.

// kern is a decoded legacy kern table: kerning values keyed by the packed
// (left, right) glyph pair.
type kern struct {
	pairs map[uint32]int
}

// kernKey packs a glyph pair into the map key.
func kernKey(left, right GlyphIndex) uint32 {
	return uint32(left)<<16 | uint32(right)
}

// parseKern decodes a legacy kern table (version 0).
func parseKern(b []byte) (*kern, error) {
	r := reader{b: b}
	r.skip(2) // version (0)
	nTables := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: kern header: %w", r.err)
	}
	k := &kern{pairs: map[uint32]int{}}
	for t := 0; t < nTables; t++ {
		stStart := r.pos
		r.skip(2) // subtable version
		length := int(r.u16())
		coverage := r.u16()
		if r.err != nil {
			return nil, fmt.Errorf("opentype: kern subtable header: %w", r.err)
		}
		if coverage>>8 == 0 { // format is the high byte of the coverage field
			if err := k.parseFormat0(&r); err != nil {
				return nil, err
			}
		}
		// Advance to the next subtable using its declared length. For format 0
		// this matches where the sequential read left off; for skipped formats
		// it steps over the unread body.
		next := stStart + length
		if next > len(r.b) {
			return nil, fmt.Errorf("opentype: kern subtable length: %w", errTruncated)
		}
		r.pos = next
	}
	return k, nil
}

// parseFormat0 reads a format-0 subtable body (positioned just after the 6-byte
// subtable header) into the pair map.
func (k *kern) parseFormat0(r *reader) error {
	nPairs := int(r.u16())
	r.skip(6) // searchRange, entrySelector, rangeShift
	for i := 0; i < nPairs; i++ {
		left := GlyphIndex(r.u16())
		right := GlyphIndex(r.u16())
		value := int(r.i16())
		k.pairs[kernKey(left, right)] = value
	}
	if r.err != nil {
		return fmt.Errorf("opentype: kern format 0: %w", r.err)
	}
	return nil
}

// Pair returns the legacy kerning adjustment between left and right in font
// units, or 0 when the pair is absent.
func (k *kern) Pair(left, right GlyphIndex) int {
	return k.pairs[kernKey(left, right)]
}
