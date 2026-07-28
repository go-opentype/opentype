// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// parseHead reads unitsPerEm and indexToLocFormat from the head table.
// The full head table is 54 bytes; indexToLocFormat sits at offset 50.
func (f *Font) parseHead(b []byte) error {
	if len(b) < 54 {
		return fmt.Errorf("opentype: head table: %w", errTruncated)
	}
	f.unitsPerEm = int(be16(b[18:]))
	if f.unitsPerEm == 0 {
		return fmt.Errorf("opentype: head table: unitsPerEm is zero")
	}
	f.macStyle = be16(b[44:])
	f.xMin = int(sbe16(b[36:]))
	f.yMin = int(sbe16(b[38:]))
	f.xMax = int(sbe16(b[40:]))
	f.yMax = int(sbe16(b[42:]))
	f.indexToLocFormat = int(sbe16(b[50:]))
	if f.indexToLocFormat != 0 && f.indexToLocFormat != 1 {
		return fmt.Errorf("opentype: head table: bad indexToLocFormat %d", f.indexToLocFormat)
	}
	return nil
}

// parseMaxp reads numGlyphs from the maxp table (offset 4).
func (f *Font) parseMaxp(b []byte) error {
	if len(b) < 6 {
		return fmt.Errorf("opentype: maxp table: %w", errTruncated)
	}
	f.numGlyphs = int(be16(b[4:]))
	if f.numGlyphs == 0 {
		return fmt.Errorf("opentype: maxp table: zero glyphs")
	}
	// Version-1.0 maxp additionally carries the interpreter limits used by the
	// hinting VM. A shorter (version-0.5) table leaves conservative defaults.
	f.maxTwilightPts = 16
	f.maxStorage = 64
	f.maxFunctionDefs = 64
	f.maxStackElements = 256
	if len(b) >= 26 {
		f.maxTwilightPts = int(be16(b[16:]))
		f.maxStorage = int(be16(b[18:]))
		f.maxFunctionDefs = int(be16(b[20:]))
		f.maxStackElements = int(be16(b[24:]))
	}
	return nil
}

// parseHhea reads the vertical metrics and numberOfHMetrics from hhea.
// ascender/descender/lineGap are int16 at offsets 4/6/8; numberOfHMetrics is
// the uint16 at offset 34.
func (f *Font) parseHhea(b []byte) error {
	if len(b) < 36 {
		return fmt.Errorf("opentype: hhea table: %w", errTruncated)
	}
	f.ascender = int(sbe16(b[4:]))
	f.descender = int(sbe16(b[6:]))
	f.lineGap = int(sbe16(b[8:]))
	f.numberOfHMetrics = int(be16(b[34:]))
	if f.numberOfHMetrics == 0 || f.numberOfHMetrics > f.numGlyphs {
		return fmt.Errorf("opentype: hhea table: bad numberOfHMetrics %d", f.numberOfHMetrics)
	}
	return nil
}

// numberOfHMetrics is kept only for the span of parseHhea→parseHmtx.
// It is declared on Font to pass the value between the two parsers.

// parseHmtx expands the hmtx table into per-glyph advanceWidth and left side
// bearing slices. The first numberOfHMetrics entries are (advance, lsb) pairs;
// the trailing glyphs share the last entry's advance and carry their own lsb.
func (f *Font) parseHmtx(b []byte) error {
	n := f.numberOfHMetrics
	need := 4*n + 2*(f.numGlyphs-n)
	if len(b) < need {
		return fmt.Errorf("opentype: hmtx table: %w", errTruncated)
	}
	f.advances = make([]int, f.numGlyphs)
	f.lsbs = make([]int, f.numGlyphs)
	lastAdvance := 0
	for i := 0; i < f.numGlyphs; i++ {
		if i < n {
			lastAdvance = int(be16(b[i*4:]))
			f.advances[i] = lastAdvance
			f.lsbs[i] = int(sbe16(b[i*4+2:]))
		} else {
			f.advances[i] = lastAdvance
			f.lsbs[i] = int(sbe16(b[4*n+2*(i-n):]))
		}
	}
	return nil
}

// parseLoca decodes the loca table into numGlyphs+1 glyph offsets into glyf.
// Short format stores uint16 half-offsets (doubled); long format stores
// uint32 offsets directly, selected by head.indexToLocFormat.
func (f *Font) parseLoca(b []byte) error {
	count := f.numGlyphs + 1
	f.loca = make([]uint32, count)
	if f.indexToLocFormat == 0 {
		if len(b) < count*2 {
			return fmt.Errorf("opentype: loca table (short): %w", errTruncated)
		}
		for i := 0; i < count; i++ {
			f.loca[i] = uint32(be16(b[i*2:])) * 2
		}
		return nil
	}
	if len(b) < count*4 {
		return fmt.Errorf("opentype: loca table (long): %w", errTruncated)
	}
	for i := 0; i < count; i++ {
		f.loca[i] = be32(b[i*4:])
	}
	return nil
}
