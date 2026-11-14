// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

// parsePostNames decodes the optional TrueType glyph names in post versions
// 1.0, 2.0 and 2.5. Version 3.0 deliberately carries no names. Unsupported or
// malformed name data is ignored as a whole, just like optional descriptor
// data, so it cannot make an otherwise usable font fail Parse.
// See https://learn.microsoft.com/en-us/typography/opentype/spec/post.
func (f *Font) parsePostNames(b []byte) {
	if len(b) < 32 {
		return
	}
	var names []string
	switch be32(b) {
	case 0x00010000:
		// Version 1 uses exactly the standard Macintosh glyph order.
		if f.numGlyphs != len(postStandardNames) {
			return
		}
		names = append([]string(nil), postStandardNames[:]...)
	case 0x00020000:
		if len(b) < 34 || int(be16(b[32:])) != f.numGlyphs || 34+2*f.numGlyphs > len(b) {
			return
		}
		// Custom names are Pascal strings numbered from 258, independently of
		// glyph order. Only read through the highest referenced string index.
		maxIndex := len(postStandardNames) - 1
		for gid := 0; gid < f.numGlyphs; gid++ {
			maxIndex = max(maxIndex, int(be16(b[34+2*gid:])))
		}
		custom := make([]string, maxIndex-len(postStandardNames)+1)
		pos := 34 + 2*f.numGlyphs
		for i := range custom {
			if pos >= len(b) {
				return
			}
			n := int(b[pos])
			pos++
			if n > len(b)-pos {
				return
			}
			custom[i] = string(b[pos : pos+n])
			pos += n
		}
		names = make([]string, f.numGlyphs)
		for gid := range names {
			index := int(be16(b[34+2*gid:]))
			if index < len(postStandardNames) {
				names[gid] = postStandardNames[index]
			} else {
				names[gid] = custom[index-len(postStandardNames)]
			}
		}
	case 0x00025000:
		if len(b) < 34 || int(be16(b[32:])) != f.numGlyphs || 34+f.numGlyphs > len(b) {
			return
		}
		names = make([]string, f.numGlyphs)
		for gid := range names {
			index := gid + int(int8(b[34+gid]))
			if index < 0 || index >= len(postStandardNames) {
				return
			}
			names[gid] = postStandardNames[index]
		}
	default:
		return
	}
	f.postNames = names
	f.glyphNames = make(map[string]GlyphIndex, len(names))
	for gid, name := range names {
		if name == "" {
			continue
		}
		// Match bare CFF: duplicate names resolve to the lowest glyph index.
		if _, seen := f.glyphNames[name]; !seen {
			f.glyphNames[name] = GlyphIndex(gid)
		}
	}
}

// postStandardNames is the 258-name Macintosh standard glyph order. It is
// different from the CFF standard string order (quoteright is index 183 here).
// See https://developer.apple.com/fonts/TrueType-Reference-Manual/RM06/Chap6post.html.
var postStandardNames = [...]string{
	".notdef", ".null", "nonmarkingreturn", "space", "exclam", "quotedbl",
	"numbersign", "dollar", "percent", "ampersand", "quotesingle", "parenleft",
	"parenright", "asterisk", "plus", "comma", "hyphen", "period", "slash",
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "colon", "semicolon", "less", "equal", "greater", "question", "at",
	"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O",
	"P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "bracketleft",
	"backslash", "bracketright", "asciicircum", "underscore", "grave",
	"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o",
	"p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "braceleft", "bar",
	"braceright", "asciitilde", "Adieresis", "Aring", "Ccedilla", "Eacute",
	"Ntilde", "Odieresis", "Udieresis", "aacute", "agrave", "acircumflex",
	"adieresis", "atilde", "aring", "ccedilla", "eacute", "egrave", "ecircumflex",
	"edieresis", "iacute", "igrave", "icircumflex", "idieresis", "ntilde",
	"oacute", "ograve", "ocircumflex", "odieresis", "otilde", "uacute", "ugrave",
	"ucircumflex", "udieresis", "dagger", "degree", "cent", "sterling", "section",
	"bullet", "paragraph", "germandbls", "registered", "copyright", "trademark",
	"acute", "dieresis", "notequal", "AE", "Oslash", "infinity", "plusminus",
	"lessequal", "greaterequal", "yen", "mu", "partialdiff", "summation",
	"product", "pi", "integral", "ordfeminine", "ordmasculine", "Omega", "ae",
	"oslash", "questiondown", "exclamdown", "logicalnot", "radical", "florin",
	"approxequal", "Delta", "guillemotleft", "guillemotright", "ellipsis",
	"nonbreakingspace", "Agrave", "Atilde", "Otilde", "OE", "oe", "endash",
	"emdash", "quotedblleft", "quotedblright", "quoteleft", "quoteright",
	"divide", "lozenge", "ydieresis", "Ydieresis", "fraction", "currency",
	"guilsinglleft", "guilsinglright", "fi", "fl", "daggerdbl", "periodcentered",
	"quotesinglbase", "quotedblbase", "perthousand", "Acircumflex", "Ecircumflex",
	"Aacute", "Edieresis", "Egrave", "Iacute", "Icircumflex", "Idieresis",
	"Igrave", "Oacute", "Ocircumflex", "apple", "Ograve", "Uacute", "Ucircumflex",
	"Ugrave", "dotlessi", "circumflex", "tilde", "macron", "breve", "dotaccent",
	"ring", "cedilla", "hungarumlaut", "ogonek", "caron", "Lslash", "lslash",
	"Scaron", "scaron", "Zcaron", "zcaron", "brokenbar", "Eth", "eth", "Yacute",
	"yacute", "Thorn", "thorn", "minus", "multiply", "onesuperior", "twosuperior",
	"threesuperior", "onehalf", "onequarter", "threequarters", "franc", "Gbreve",
	"gbreve", "Idotaccent", "Scedilla", "scedilla", "Cacute", "cacute", "Ccaron",
	"ccaron", "dcroat",
}
