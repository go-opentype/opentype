// Copyright (c) 2026, the go-opentype authors
// All rights reserved.
//
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"math"
)

// ParseCFF decodes a bare CFF font program — a Compact Font Format font that
// is not wrapped in an sfnt container, which is how a PDF carries one as a
// FontFile3 of subtype Type1C. Such a program has no head, no hmtx and no
// character map: the size of its units comes from its font matrix, its
// advances from the charstrings themselves, and the way from a name or a byte
// to a glyph from its own charset and encoding. [Font.HasCharacterMap] is
// false for the result, and [Font.GlyphIndexByName] and
// [Font.GlyphIndexByCode] are how it is addressed.
//
// The byte slice is retained, not copied, and must not be mutated afterwards.
func ParseCFF(b []byte) (*Font, error) {
	cff, err := parseCFF(b)
	if err != nil {
		return nil, err
	}
	n := len(cff.charStrings)
	if n == 0 {
		return nil, fmt.Errorf("opentype: cff: the font has no glyphs")
	}
	f := &Font{
		cff:              cff,
		numGlyphs:        n,
		unitsPerEm:       unitsPerEmOf(cff.fontMatrix),
		numberOfHMetrics: n,
	}
	f.advances, f.lsbs = cff.metrics(n)
	// A bare program says nothing about how the line is spaced; a page of one
	// is laid out by whatever carries it, which for a PDF is the text matrix.
	f.ascender, f.descender = f.unitsPerEm, 0
	f.buildNames()
	return f, nil
}

// unitsPerEmOf turns a font matrix into the number of units to the em, which
// is what the rest of this package counts in. A matrix of a thousandth is the
// usual one and gives a thousand.
func unitsPerEmOf(m [6]float64) int {
	upem := int(math.Round(1 / m[0]))
	if upem < 16 || upem > 16384 {
		return 1000
	}
	return upem
}

// metrics is the advance and left side bearing of every glyph, read from the
// charstrings. A charstring that does not say gets the font's default.
func (c *cffTable) metrics(n int) (advances, lsbs []int) {
	advances = make([]int, n)
	lsbs = make([]int, n)
	def, nominal := 0.0, 0.0
	if c.priv != nil {
		def, nominal = c.priv.defaultWidthX, c.priv.nominalWidthX
	}
	for gid := 0; gid < n; gid++ {
		w := def
		if delta, ok := charstringWidth(c.charStrings[gid]); ok {
			w = nominal + delta
		}
		advances[gid] = int(math.Round(w))
	}
	return advances, lsbs
}

// charstringWidth reads the optional width a Type 2 charstring may carry in
// front of its first stem, move or endchar operator. It reads operands only,
// and gives up at the first operator that could hide one behind a subroutine
// rather than guess.
func charstringWidth(cs []byte) (float64, bool) {
	var nOperands int
	var first float64
	for i := 0; i < len(cs); {
		b := cs[i]
		switch {
		case b >= 32 || b == 28:
			v, next, ok := t2Operand(cs, i)
			if !ok {
				return 0, false
			}
			if nOperands == 0 {
				first = v
			}
			nOperands++
			i = next
		case b == 1 || b == 3 || b == 18 || b == 23: // hstem, vstem, hstemhm, vstemhm
			return first, nOperands%2 == 1
		case b == 19 || b == 20: // hintmask, cntrmask
			return first, nOperands%2 == 1
		case b == 21: // rmoveto
			return first, nOperands > 2
		case b == 22 || b == 4: // hmoveto, vmoveto
			return first, nOperands > 1
		case b == 14: // endchar
			return first, nOperands == 1 || nOperands == 5
		default:
			// A subroutine call, or an arithmetic operator: the width, if
			// there is one, is no longer in front of us.
			return 0, false
		}
	}
	return 0, false
}

// t2Operand reads one Type 2 charstring operand, giving its value and where
// the next byte is.
func t2Operand(cs []byte, i int) (float64, int, bool) {
	b := cs[i]
	switch {
	case b == 28:
		if i+2 >= len(cs) {
			return 0, 0, false
		}
		return float64(int16(uint16(cs[i+1])<<8 | uint16(cs[i+2]))), i + 3, true
	case b < 247:
		return float64(int(b) - 139), i + 1, true
	case b < 251:
		if i+1 >= len(cs) {
			return 0, 0, false
		}
		return float64((int(b)-247)*256 + int(cs[i+1]) + 108), i + 2, true
	case b < 255:
		if i+1 >= len(cs) {
			return 0, 0, false
		}
		return float64(-(int(b)-251)*256 - int(cs[i+1]) - 108), i + 2, true
	default: // 255: a sixteen-sixteen fixed-point number
		if i+4 >= len(cs) {
			return 0, 0, false
		}
		v := int32(uint32(cs[i+1])<<24 | uint32(cs[i+2])<<16 | uint32(cs[i+3])<<8 | uint32(cs[i+4]))
		return float64(v) / 65536, i + 5, true
	}
}

// buildNames indexes the font by the names it gives its glyphs, so that a
// document addressing it by name has somewhere to look. A font addressed by
// identifier names nothing, and this leaves it alone.
func (f *Font) buildNames() {
	if f.cff == nil || f.cff.isCID {
		return
	}
	f.glyphNames = make(map[string]GlyphIndex, f.numGlyphs)
	for gid := 0; gid < f.numGlyphs; gid++ {
		name, ok := f.cff.glyphName(gid)
		if !ok || name == "" {
			continue
		}
		if _, seen := f.glyphNames[name]; !seen {
			f.glyphNames[name] = GlyphIndex(gid)
		}
	}
}

// GlyphName is what the font calls a glyph. ok is false for a font that does
// not name its glyphs — a TrueType font usually does not, and a CFF font
// addressed by character identifier never does.
func (f *Font) GlyphName(gid GlyphIndex) (string, bool) {
	if f.cff == nil || f.cff.isCID {
		return "", false
	}
	return f.cff.glyphName(int(gid))
}

// GlyphIndexByName maps a glyph name to its index. ok is false when the font
// names no glyphs or has none of that name.
func (f *Font) GlyphIndexByName(name string) (GlyphIndex, bool) {
	gid, ok := f.glyphNames[name]
	return gid, ok
}

// GlyphIndexByCode maps a byte to a glyph through the font program's own
// built-in encoding — the Standard Encoding unless the program said otherwise.
// ok is false for a font that carries no such encoding, which is every
// TrueType font and every CFF font addressed by character identifier.
func (f *Font) GlyphIndexByCode(code byte) (GlyphIndex, bool) {
	if f.cff == nil || f.cff.isCID {
		return 0, false
	}
	gid, ok := f.cff.glyphByCode(code)
	if !ok || gid >= f.numGlyphs {
		return 0, false
	}
	return GlyphIndex(gid), true
}
