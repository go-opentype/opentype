// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// GlyphIndex is a glyph identifier within a font: an index into the font's
// glyph store, as produced by the cmap. Zero is the ".notdef" glyph.
type GlyphIndex uint16

// errTruncated is the sentinel a bounded reader records when a read runs past
// the end of its slice. Callers wrap it with table context.
var errTruncated = errors.New("unexpected end of data")

// Font is a parsed TrueType/OpenType font. It is immutable after Parse and
// safe for concurrent use by multiple goroutines. Build a Face from it with
// NewFace to obtain sized metrics and rasterised glyphs.
type Font struct {
	unitsPerEm       int
	indexToLocFormat int
	numGlyphs        int
	ascender         int
	descender        int
	lineGap          int
	numberOfHMetrics int
	advances         []int // advanceWidth per glyph, in font units
	lsbs             []int // left side bearing per glyph, in font units
	loca             []uint32
	glyf             []byte
	cmap             cmapLookup
}

// be16 reads a big-endian uint16 from b[0:2]; the caller guarantees len(b)>=2.
func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }

// be32 reads a big-endian uint32 from b[0:4]; the caller guarantees len(b)>=4.
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

// sbe16 reads a big-endian int16 from b[0:2]; the caller guarantees len(b)>=2.
func sbe16(b []byte) int16 { return int16(binary.BigEndian.Uint16(b)) }

// Recognised sfnt version tags.
const (
	versionTrueType = 0x00010000 // 'glyf' outlines
	versionTrue     = 0x74727565 // "true", Apple TrueType
	versionOTTO     = 0x4F54544F // "OTTO", CFF/OpenType outlines (phase 2)
)

// Parse decodes a TrueType/OpenType font from b and returns a Font. The byte
// slice is retained (not copied) and must not be mutated by the caller.
//
// It fails on a corrupt or unsupported container: a bad sfnt magic, truncated
// data, a missing required table, or CFF/OpenType ("OTTO") outlines, which are
// not yet supported.
func Parse(b []byte) (*Font, error) {
	if len(b) < 12 {
		return nil, fmt.Errorf("opentype: short header: %w", errTruncated)
	}
	version := be32(b)
	switch version {
	case versionTrueType, versionTrue:
		// supported: TrueType glyf outlines.
	case versionOTTO:
		return nil, errors.New("opentype: unsupported: CFF/OpenType outlines")
	default:
		return nil, fmt.Errorf("opentype: bad sfnt version 0x%08x", version)
	}

	numTables := int(be16(b[4:]))
	tables := make(map[string][]byte, numTables)
	// The table directory is numTables records of 16 bytes starting at 12.
	if 12+numTables*16 > len(b) {
		return nil, fmt.Errorf("opentype: table directory: %w", errTruncated)
	}
	for i := 0; i < numTables; i++ {
		rec := b[12+i*16:]
		tag := string(rec[0:4])
		off := int(be32(rec[8:]))
		length := int(be32(rec[12:]))
		if off+length > len(b) {
			return nil, fmt.Errorf("opentype: table %q out of range", tag)
		}
		tables[tag] = b[off : off+length]
	}

	for _, name := range []string{"head", "maxp", "hhea", "hmtx", "cmap", "loca", "glyf"} {
		if _, ok := tables[name]; !ok {
			return nil, fmt.Errorf("opentype: missing required table %q", name)
		}
	}

	f := &Font{}
	if err := f.parseHead(tables["head"]); err != nil {
		return nil, err
	}
	if err := f.parseMaxp(tables["maxp"]); err != nil {
		return nil, err
	}
	if err := f.parseHhea(tables["hhea"]); err != nil {
		return nil, err
	}
	if err := f.parseHmtx(tables["hmtx"]); err != nil {
		return nil, err
	}
	if err := f.parseLoca(tables["loca"]); err != nil {
		return nil, err
	}
	f.glyf = tables["glyf"]
	if err := f.parseCmap(tables["cmap"]); err != nil {
		return nil, err
	}
	return f, nil
}

// NumGlyphs returns the number of glyphs in the font (from the maxp table).
func (f *Font) NumGlyphs() int { return f.numGlyphs }

// GlyphIndex maps a rune to its glyph index via the selected cmap subtable.
// ok is false when the rune has no glyph in that subtable.
func (f *Font) GlyphIndex(r rune) (GlyphIndex, bool) {
	return f.cmap.lookup(r)
}
