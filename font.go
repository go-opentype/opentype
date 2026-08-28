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
	cmaps            []cmapSubtable        // every decoded cmap subtable, with the platform and encoding it was written for
	cmapVS           *cmap14               // format-14 Unicode Variation Sequences subtable, if present
	cff              *cffTable             // CFF/Type2 outlines for an OpenType ("OTTO") font, if present
	t1               *type1Font            // PostScript Type 1 outlines, for a program read by ParseType1
	glyphNames       map[string]GlyphIndex // what the font calls its glyphs, when it names them
	cff2             *cff2Table            // CFF2 (variable Compact Font Format) outlines, if present
	fvar             *fvarTable            // optional: variation axes and named instances
	avar             *avarTable            // optional: axis-value segment maps
	gvar             *gvarTable            // optional: glyph variation (delta) data
	hvar             *hvarTable            // optional: horizontal metrics variation (advances)
	vvar             *vvarTable            // optional: vertical metrics variation (advances)
	mvar             *mvarTable            // optional: global font-metric variation

	// OpenType Layout tables, all optional. Absence is not an error.
	gdef *gdefTable // glyph definitions (classes, mark sets) for lookup flags
	gsub *gsub      // glyph substitution (ligatures, single substitution)
	gpos *gpos      // glyph positioning (pair kerning)
	kern *kern      // legacy kern table (kerning fallback)

	// OpenType MATH table (math-typesetting metrics), optional. Absence is not an
	// error; it powers Font.HasMath and the Face math accessors (see math.go).
	math *mathTable

	// Vertical writing-mode metrics (vhea/vmtx/VORG), all optional. They enable
	// top-to-bottom (CJK tategaki) layout; absence means vertical metrics are
	// unavailable. See vertical.go.
	vertAscender        int                // vhea vertTypoAscender, font units
	vertDescender       int                // vhea vertTypoDescender, font units
	vertLineGap         int                // vhea vertTypoLineGap, font units
	numOfLongVerMetrics int                // vhea count of full vmtx entries
	vertAdvances        []int              // advanceHeight per glyph, font units (vmtx)
	tsbs                []int              // top side bearing per glyph, font units (vmtx)
	vorgDefault         int                // VORG defaultVertOriginY, font units
	vorg                map[GlyphIndex]int // VORG per-glyph vertical origin overrides
	hasVhea             bool
	hasVmtx             bool
	hasVORG             bool

	// TrueType instruction (hinting) tables and limits. These are optional:
	// a font without them simply cannot be hinted. See hint.go.
	fpgm             []byte // font program (function definitions)
	prep             []byte // control-value program
	cvt              []int16
	maxStorage       int
	maxFunctionDefs  int
	maxStackElements int
	maxTwilightPts   int

	// Descriptor scalars decoded from head, OS/2 and post at Parse time. They
	// back the PDF FontDescriptor accessors and general layout use (descriptor.go).
	xMin, yMin, xMax, yMax int    // head glyph bounding box, font units
	macStyle               uint16 // head macStyle bits (bit 0 bold, bit 1 italic)

	os2Present   bool   // an OS/2 table was present and long enough to decode
	os2Version   int    // OS/2 table version (governs the availability of x/CapHeight)
	weightClass  int    // OS/2 usWeightClass (100..900), 0 when absent
	widthClass   int    // OS/2 usWidthClass (1..9), 0 when absent
	sFamilyClass int    // OS/2 sFamilyClass (high byte is the IBM font class)
	fsSelection  uint16 // OS/2 fsSelection bits (bit 0 italic)
	sCapHeight   int    // OS/2 sCapHeight, font units (version >= 2), 0 when absent
	sxHeight     int    // OS/2 sxHeight, font units (version >= 2), 0 when absent

	postPresent  bool    // a post table was present and long enough to decode
	italicAngle  float64 // post italicAngle (degrees, 0 for upright)
	isFixedPitch bool    // post isFixedPitch (monospaced)

	// tables retains the raw, undecoded sfnt table slices keyed by four-byte tag,
	// so callers can read table bytes (Font.Table/Font.TableTags) and the
	// subsetting/instancing code can rebuild a container (subset.go, instance.go).
	tables map[string][]byte
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
// data, or a missing required table — which does not include the character
// map: see [Font.HasCharacterMap]. All three outline flavours are supported:
// TrueType ('glyf'/'loca'), CFF/OpenType (a "OTTO" sfnt, or any sfnt carrying a
// 'CFF ' table, decoded via cff.go) and variable CFF2 (a sfnt carrying a 'CFF2'
// table, decoded via cff2.go).
func Parse(b []byte) (*Font, error) {
	if len(b) < 12 {
		return nil, fmt.Errorf("opentype: short header: %w", errTruncated)
	}
	version := be32(b)
	switch version {
	case versionTrueType, versionTrue, versionOTTO:
		// supported: TrueType ('glyf') and CFF/OpenType ("OTTO") outlines.
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

	// The outline flavour is CFF/CFF2 when the sfnt is "OTTO" or carries a 'CFF '
	// or 'CFF2' table; such a font needs its CFF table but not 'glyf'/'loca'. A
	// 'CFF2' table (variable CFF) takes precedence and requires 'CFF2'; otherwise
	// a CFF font requires 'CFF '. A TrueType font needs 'glyf'/'loca'.
	_, hasCFF := tables["CFF "]
	_, hasCFF2 := tables["CFF2"]
	isCFF := version == versionOTTO || hasCFF || hasCFF2
	// A 'cmap' is not among them. A font embedded in a PDF as a CIDFontType2
	// is addressed by glyph number through the document's own map, and the
	// subset that is embedded routinely leaves its character map out; refusing
	// such a font would leave every glyph in it undrawable for want of a table
	// nothing was going to consult.
	required := []string{"head", "maxp", "hhea", "hmtx"}
	switch {
	case hasCFF2:
		required = append(required, "CFF2")
	case isCFF:
		required = append(required, "CFF ")
	default:
		required = append(required, "loca", "glyf")
	}
	for _, name := range required {
		if _, ok := tables[name]; !ok {
			return nil, fmt.Errorf("opentype: missing required table %q", name)
		}
	}

	f := &Font{tables: tables}
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
	switch {
	case hasCFF2:
		c2, err := parseCFF2(tables["CFF2"])
		if err != nil {
			return nil, err
		}
		f.cff2 = c2
	case isCFF:
		cff, err := parseCFF(tables["CFF "])
		if err != nil {
			return nil, err
		}
		f.cff = cff
	default:
		if err := f.parseLoca(tables["loca"]); err != nil {
			return nil, err
		}
		f.glyf = tables["glyf"]
	}
	if cm, ok := tables["cmap"]; ok {
		if err := f.parseCmap(cm); err != nil {
			return nil, err
		}
	}
	// Optional TrueType instruction tables. Absence is not an error; it just
	// means the font carries no hinting program (see hint.go).
	f.fpgm = tables["fpgm"]
	f.prep = tables["prep"]
	f.parseCvt(tables["cvt "])
	// Optional OpenType Font Variations tables. A font without them parses and
	// renders exactly as before; when present they enable instancing at a
	// chosen axis coordinate (see fvar.go, avar.go, gvar.go).
	if err := f.parseVariations(tables); err != nil {
		return nil, err
	}
	// Optional metric-variation tables (HVAR, VVAR, MVAR): advance widths,
	// advance heights and global font metrics that track the axis coordinate.
	// Absence is not an error (see metricvar.go).
	if err := f.parseMetricVariations(tables); err != nil {
		return nil, err
	}
	// Optional OpenType Layout tables: substitution (GSUB), positioning (GPOS)
	// and the legacy kern table. Absence is not an error (see gsub.go, gpos.go,
	// kern.go); they power Face.Shape and Face.Kern.
	if err := f.parseLayout(tables); err != nil {
		return nil, err
	}
	// Optional vertical writing-mode tables (vhea, vmtx, VORG). Absence is not an
	// error; they supply the metrics for top-to-bottom layout (see vertical.go).
	if err := f.parseVertical(tables); err != nil {
		return nil, err
	}
	// Optional OpenType MATH table: the font-level metrics a math-typesetting
	// engine consumes. Absence is not an error (see math.go).
	if err := f.parseMath(tables); err != nil {
		return nil, err
	}
	// Optional descriptor tables (OS/2, post): the metadata a PDF FontDescriptor
	// and general styling consume. Absence is not an error (see descriptor.go).
	f.parseDescriptor(tables)
	return f, nil
}

// parseLayout decodes the optional GSUB, GPOS and kern tables. Each is
// independent; any subset (or none) may be present, and a malformed table is
// reported as an error so a corrupt font fails cleanly.
func (f *Font) parseLayout(tables map[string][]byte) error {
	// GDEF is parsed first so its glyph classes and mark sets can be shared with
	// the GSUB and GPOS appliers, which honour it through their lookup flags.
	if b, ok := tables["GDEF"]; ok {
		gd, err := parseGDEF(b)
		if err != nil {
			return err
		}
		f.gdef = gd
	}
	if b, ok := tables["GSUB"]; ok {
		g, err := parseGSUB(b)
		if err != nil {
			return err
		}
		g.gdef = f.gdef
		f.gsub = g
	}
	if b, ok := tables["GPOS"]; ok {
		g, err := parseGPOS(b)
		if err != nil {
			return err
		}
		g.gdef = f.gdef
		f.gpos = g
	}
	if b, ok := tables["kern"]; ok {
		k, err := parseKern(b)
		if err != nil {
			return err
		}
		f.kern = k
	}
	return nil
}

// parseCvt decodes the Control Value Table: a run of int16 FUnit values. A
// missing or odd-length table yields as many whole entries as fit.
func (f *Font) parseCvt(b []byte) {
	f.cvt = make([]int16, len(b)/2)
	for i := range f.cvt {
		f.cvt[i] = sbe16(b[i*2:])
	}
}

// NumGlyphs returns the number of glyphs in the font (from the maxp table).
func (f *Font) NumGlyphs() int { return f.numGlyphs }

// GlyphIndex maps a rune to its glyph index via the selected cmap subtable.
// ok is false when the rune has no glyph in that subtable, and for a font that
// carries no character map at all — one addressed by glyph number, which
// [Font.HasCharacterMap] reports.
func (f *Font) GlyphIndex(r rune) (GlyphIndex, bool) {
	if f.cmap == nil {
		return 0, false
	}
	return f.cmap.lookup(r)
}

// HasCharacterMap reports whether the font can say which glyph a rune is. A
// font subset embedded in a PDF as a CIDFontType2 usually cannot: the document
// carries that map instead, and the font is addressed by glyph number.
func (f *Font) HasCharacterMap() bool { return f.cmap != nil }

// GlyphIndexVariation resolves a Unicode variation sequence (a base rune
// followed by a variation selector) to a glyph index, using the font's
// format-14 cmap subtable.
//
// ok is false when the font has no format-14 subtable, the variation
// selector is not registered in it, or the sequence is registered as a
// "default" mapping whose base rune has no glyph in the ordinary cmap.
func (f *Font) GlyphIndexVariation(r, vs rune) (GlyphIndex, bool) {
	if f.cmapVS == nil {
		return 0, false
	}
	return f.cmapVS.lookupVariation(f, r, vs)
}
