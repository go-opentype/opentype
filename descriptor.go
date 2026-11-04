// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

// This file surfaces the font-level descriptor scalars a PDF FontDescriptor (and
// general typographic use) needs, without a consumer having to re-parse the sfnt
// container itself. The values are decoded from head (bounding box, macStyle),
// OS/2 (weight/width class, cap/x height, family class, selection flags) and post
// (italic angle, fixed-pitch) at Parse time; the accessors below expose them.
//
// Every scalar has a defined value even when its source table is absent: the
// head-derived ones are always available, while the OS/2 and post ones fall back
// to a documented default (0, or the ascender for a missing cap height) so a
// caller never has to special-case a partial font.

// parseDescriptor decodes the optional OS/2 and post tables into the descriptor
// scalars. Both are optional: a font without them keeps the zero defaults, which
// the accessors translate into sensible fallbacks. A too-short table is treated
// as absent rather than an error, matching the tolerant handling a descriptor
// consumer expects (the required metrics all come from head/hhea, already parsed).
func (f *Font) parseDescriptor(tables map[string][]byte) {
	if b, ok := tables["OS/2"]; ok && len(b) >= 78 {
		f.os2Present = true
		f.os2Version = int(be16(b))
		f.weightClass = int(be16(b[4:]))
		f.widthClass = int(be16(b[6:]))
		f.sFamilyClass = int(sbe16(b[30:]))
		f.fsSelection = be16(b[62:])
		// The line-metric set, present from OS/2 version 0 and so covered by the
		// len(b) >= 78 guard above: sTypoAscender at 68, sTypoDescender at 70,
		// sTypoLineGap at 72, usWinAscent at 74, usWinDescent at 76.
		//
		// ⛔ THE TWO DESCENDERS HAVE OPPOSITE SIGNS. sTypoDescender is signed and
		// negative below the baseline, as hhea's is; usWinDescent is UNSIGNED and
		// positive below it. Decoding both with the same helper would make one of
		// them nonsense — usually usWinDescent, read as a huge negative.
		f.sTypoAscender = int(sbe16(b[68:]))
		f.sTypoDescender = int(sbe16(b[70:]))
		f.sTypoLineGap = int(sbe16(b[72:]))
		f.usWinAscent = int(be16(b[74:]))
		f.usWinDescent = int(be16(b[76:]))
		// sxHeight and sCapHeight were added in OS/2 version 2; older tables (and
		// truncated version-2 ones) simply do not carry them.
		if f.os2Version >= 2 && len(b) >= 90 {
			f.sxHeight = int(sbe16(b[86:]))
			f.sCapHeight = int(sbe16(b[88:]))
		}
	}
	if b, ok := tables["post"]; ok && len(b) >= 16 {
		f.postPresent = true
		f.italicAngle = float64(int32(be32(b[4:]))) / 65536.0
		f.isFixedPitch = be32(b[12:]) != 0
	}
}

// UnitsPerEm returns the font's design units per em (from head), the scale in
// which every other font-unit metric this package reports is expressed. It is
// never zero (Parse rejects a font whose head declares zero).
func (f *Font) UnitsPerEm() int { return f.unitsPerEm }

// Ascent returns the typographic ascender in font units (hhea.ascender): the
// height above the baseline. It is the same value Face.Metrics scales to pixels.
func (f *Font) Ascent() int { return f.ascender }

// Descent returns the typographic descender in font units (hhea.descender). It
// is conventionally negative (below the baseline), as stored in hhea.
func (f *Font) Descent() int { return f.descender }

// LineGap returns the typographic line gap in font units (hhea.lineGap): the
// recommended extra spacing between lines beyond ascent minus descent.
func (f *Font) LineGap() int { return f.lineGap }

// TypoAscent, TypoDescent and TypoLineGap return OS/2's sTypoAscender,
// sTypoDescender and sTypoLineGap in font units, and UseTypoMetrics reports the
// font's own instruction to prefer them.
//
// ⭐ THESE, NOT Ascent/Descent/LineGap, ARE WHAT THE SPEC NAMES FOR NEW WORK.
// OpenType 1.9.1, hhea:
//
//	The ascender, descender and linegap values in this table are Apple specific …
//	The sTypoAscender, sTypoDescender and sTypoLineGap fields in the OS/2 table are
//	used on the Windows platform, and are recommended for new text-layout
//	implementations.
//
// and OS/2, sTypoAscender: "This field should be combined with the sTypoDescender
// and sTypoLineGap values to determine default line spacing … intended to allow
// applications to lay out documents in a typographically correct and portable
// fashion."
//
// Ascent, Descent and LineGap keep returning hhea unchanged: they say which table
// they read, and silently changing what an existing accessor means would reflow
// every document laid out with this package.
//
// ⛔ TypoDescent is NEGATIVE below the baseline. WinDescent is POSITIVE below it —
// the spec stores it unsigned — so the two cannot be compared or substituted
// without a sign change.
//
// All five are zero when the font has no OS/2 table, the convention CapHeight
// already follows. sTypoAscender − sTypoDescender is NOT required to equal
// UnitsPerEm, and the spec says so explicitly, so a caller must not use that
// identity as a validity check.
func (f *Font) TypoAscent() int  { return f.sTypoAscender }
func (f *Font) TypoDescent() int { return f.sTypoDescender }
func (f *Font) TypoLineGap() int { return f.sTypoLineGap }

// WinAscent and WinDescent return OS/2's usWinAscent and usWinDescent in font
// units. WinDescent is POSITIVE below the baseline.
//
// ⛔ THESE ARE A CLIPPING EXTENT, NOT LINE SPACING, and the spec is unusually
// blunt about it: "Some legacy applications use the usWinAscent and usWinDescent
// values to determine default line spacing. This is strongly discouraged. The
// sTypo* fields should be used for this purpose." They are the right values for
// sizing a redraw or selection rectangle, and the wrong ones for leading.
func (f *Font) WinAscent() int  { return f.usWinAscent }
func (f *Font) WinDescent() int { return f.usWinDescent }

// UseTypoMetrics reports fsSelection bit 7, USE_TYPO_METRICS: the font's own
// statement that its sTypo* values, rather than its usWin* values, should decide
// default line spacing. The bit was defined in OS/2 version 4, so a font with an
// earlier table always reports false — which is not "prefer usWin*", only "this
// font does not say".
func (f *Font) UseTypoMetrics() bool { return f.fsSelection&useTypoMetricsBit != 0 }

// useTypoMetricsBit is fsSelection bit 7 (OpenType 1.9.1, OS/2 fsSelection).
const useTypoMetricsBit = 1 << 7

// FontBBox returns the font bounding box in font units (head.xMin/yMin/xMax/yMax):
// the union of every glyph's bounds. A PDF FontDescriptor's /FontBBox is this box
// scaled to glyph space.
func (f *Font) FontBBox() (xMin, yMin, xMax, yMax int) {
	return f.xMin, f.yMin, f.xMax, f.yMax
}

// CapHeight returns the cap height in font units (OS/2.sCapHeight). It is zero
// when the font has no OS/2 table or an OS/2 version below 2, which do not carry
// it; a PDF consumer that needs a non-zero value substitutes the ascender.
func (f *Font) CapHeight() int { return f.sCapHeight }

// XHeight returns the x height in font units (OS/2.sxHeight). It is zero when the
// font has no OS/2 table or an OS/2 version below 2 (which do not carry it).
func (f *Font) XHeight() int { return f.sxHeight }

// ItalicAngle returns the italic (slant) angle in counter-clockwise degrees from
// vertical (post.italicAngle): zero for an upright font, negative for the usual
// forward slant. It is zero when the font has no post table.
func (f *Font) ItalicAngle() float64 { return f.italicAngle }

// WeightClass returns the OS/2 usWeightClass (100 thin .. 400 normal .. 900 black),
// or 0 when the font carries no OS/2 table.
func (f *Font) WeightClass() int { return f.weightClass }

// WidthClass returns the OS/2 usWidthClass (1 ultra-condensed .. 5 normal ..
// 9 ultra-expanded), or 0 when the font carries no OS/2 table.
func (f *Font) WidthClass() int { return f.widthClass }

// IsFixedPitch reports whether the font is monospaced (post.isFixedPitch).
func (f *Font) IsFixedPitch() bool { return f.isFixedPitch }

// IsItalic reports whether the font is italic or oblique, from any of the three
// places a font may record it: a non-zero post italic angle, the head macStyle
// italic bit, or the OS/2 fsSelection italic bit.
func (f *Font) IsItalic() bool {
	return f.italicAngle != 0 || f.macStyle&0x02 != 0 || f.fsSelection&0x01 != 0
}

// IsSerif reports whether the font is a serif design, read from the high byte of
// OS/2.sFamilyClass (the IBM font-class id): classes 1-7 are serif families,
// class 8 is sans-serif, and the rest (script, symbolic, ...) are neither. It is
// false when the font has no OS/2 table.
func (f *Font) IsSerif() bool {
	class := f.sFamilyClass >> 8
	return class >= 1 && class <= 7
}

// isSymbolic reports whether the font is a symbol font (OS/2 sFamilyClass 12).
// It drives the PDF Symbolic/Nonsymbolic flag: symbol fonts must be flagged
// Symbolic, every other font Nonsymbolic.
func (f *Font) isSymbolic() bool { return f.sFamilyClass>>8 == 12 }

// PDF FontDescriptor flag bits (PDF 32000-1:2008, Table 123).
const (
	pdfFlagFixedPitch  = 1 << 0
	pdfFlagSerif       = 1 << 1
	pdfFlagSymbolic    = 1 << 2
	pdfFlagNonsymbolic = 1 << 5
	pdfFlagItalic      = 1 << 6
)

// Flags returns the PDF FontDescriptor /Flags value for the font: a bit set
// combining FixedPitch, Serif, Symbolic-or-Nonsymbolic and Italic, derived from
// the post, OS/2 and head tables. A caller embeds it verbatim as /Flags. Exactly
// one of the Symbolic and Nonsymbolic bits is always set.
func (f *Font) Flags() int {
	flags := 0
	if f.isFixedPitch {
		flags |= pdfFlagFixedPitch
	}
	if f.IsSerif() {
		flags |= pdfFlagSerif
	}
	if f.isSymbolic() {
		flags |= pdfFlagSymbolic
	} else {
		flags |= pdfFlagNonsymbolic
	}
	if f.IsItalic() {
		flags |= pdfFlagItalic
	}
	return flags
}

// StemV returns an estimate, in font units, of the dominant vertical stem width
// a PDF FontDescriptor requires as /StemV. OpenType fonts do not store StemV, so
// it is approximated from the OS/2 weight class with the linear relation
// 50 + (weightClass-100)*11/100 (about 83 at the normal weight 400, ~116 at bold
// 700), clamped to a floor of 50. A font without an OS/2 table is treated as the
// normal weight. Callers that can do better should read WeightClass directly.
func (f *Font) StemV() int {
	w := f.weightClass
	if w == 0 {
		w = 400
	}
	v := 50 + (w-100)*11/100
	if v < 50 {
		v = 50
	}
	return v
}
