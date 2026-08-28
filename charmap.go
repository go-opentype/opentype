// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

// A font's cmap table carries several subtables, each written for one
// platform and encoding, and Font.GlyphIndex answers through whichever of
// them can represent the most codepoints. That is the right answer for a font
// addressed by character, and the wrong one for a font addressed some other
// way: a PDF says how its fonts are addressed, and a symbolic TrueType font
// embedded in one is addressed through its own Microsoft Symbol or Macintosh
// Roman subtable by raw byte, not through its Unicode subtable by codepoint.
//
// Only the document can make that choice, so the subtables are listed here
// rather than chosen here.

// NumCharacterMaps returns how many cmap subtables the font carries in a form
// this package decodes. It is zero for a font with no character map at all.
//
// Format 14 (Unicode Variation Sequences) is not one of them: it maps a pair
// rather than a code, and is reached through Font.GlyphIndexVariation.
func (f *Font) NumCharacterMaps() int { return len(f.cmaps) }

// CharacterMap reports which platform and encoding the i'th subtable was
// written for, and which format it is written in. ok is false when i is not
// the index of a subtable.
//
// The pairs worth recognising are (3,1) and (3,10), Microsoft Unicode, and
// platform 0, Unicode; (3,0), Microsoft Symbol, whose codes are the font's
// own and conventionally live at 0xF000 + code; and (1,0), Macintosh Roman.
func (f *Font) CharacterMap(i int) (platform, encoding, format uint16, ok bool) {
	if i < 0 || i >= len(f.cmaps) {
		return 0, 0, 0, false
	}
	c := f.cmaps[i]
	return c.platform, c.encoding, c.format, true
}

// GlyphIndexInMap maps a code to a glyph through one named subtable rather
// than through the one the font would choose. ok is false when i is not the
// index of a subtable, or when that subtable does not map the code (including
// a mapping to the .notdef glyph).
//
// The code is whatever the subtable is indexed by: a Unicode codepoint for a
// Unicode subtable, and a raw byte of the font's own encoding for a Microsoft
// Symbol or Macintosh Roman one.
func (f *Font) GlyphIndexInMap(i int, code rune) (GlyphIndex, bool) {
	if i < 0 || i >= len(f.cmaps) {
		return 0, false
	}
	return f.cmaps[i].lookup.lookup(code)
}

// RuneOfGlyphInMap inverts one subtable: which code reaches this glyph. ok is
// false when i is not the index of a subtable, or when no code in it reaches
// that glyph.
//
// A subtable maps many codes onto one glyph — a space and a no-break space
// commonly share one — and the lowest of them is the one reported. Inverting a
// Unicode subtable is how the character a glyph stands for is recovered when
// the document says nothing about it and the program names nothing: the font
// itself said which codepoint that glyph is for.
//
// The inverse is built once per subtable, on the first call, and kept.
func (f *Font) RuneOfGlyphInMap(i int, gid GlyphIndex) (rune, bool) {
	if i < 0 || i >= len(f.cmaps) {
		return 0, false
	}
	if f.cmaps[i].inverse == nil {
		f.cmaps[i].inverse = f.cmaps[i].lookup.reverse(maxReverseCodes)
	}
	r, ok := f.cmaps[i].inverse[gid]
	return r, ok
}
