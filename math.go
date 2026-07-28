// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"sort"
)

// This file decodes the OpenType MATH table: the font-level metrics a math
// typesetter needs. It is the layer LuaTeX, unicode-math, MathJax and Word's
// equation editor build their math layout on. The MATH table has three parts:
//
//   - MathConstants: ~56 global scalars (subscript/superscript shifts, fraction
//     and radical gaps, rule thicknesses, the math axis height, …).
//   - MathGlyphInfo: per-glyph italic correction, top-accent attachment points,
//     an "extended shape" set and cut-in math kerning for the four glyph corners.
//   - MathVariants: larger size variants of a glyph plus the recipe (a
//     GlyphAssembly of repeatable parts) for building an arbitrarily tall or wide
//     stretchy glyph (integrals, braces, radicals, arrows).
//
// This package exposes those metrics, pixel-scaled through a Face. The actual
// math LAYOUT — box building, TeX's math list rules, choosing a variant or
// assembling a stretchy glyph at a target size — is a higher-level engine that
// consumes these values; it is out of scope here.
//
// All offsets inside the table are unsigned 16-bit values measured from the
// start of the structure that contains them, so parsing re-slices with subslice
// and recurses, exactly as gsub.go/gpos.go/layout.go do.

// MathConstant identifies one scalar in the MATH table's MathConstants record.
// The constants are listed in their on-disk order. Face.MathConstant returns
// each pixel-scaled, except the three percentage constants
// (ScriptPercentScaleDown, ScriptScriptPercentScaleDown and
// RadicalDegreeBottomRaisePercent), which are pure ratios and returned verbatim.
type MathConstant int

// The MathConstants record fields, in table order. mathConstCount is the count
// sentinel (not a real constant).
const (
	ScriptPercentScaleDown MathConstant = iota
	ScriptScriptPercentScaleDown
	DelimitedSubFormulaMinHeight
	DisplayOperatorMinHeight
	MathLeading
	AxisHeight
	AccentBaseHeight
	FlattenedAccentBaseHeight
	SubscriptShiftDown
	SubscriptTopMax
	SubscriptBaselineDropMin
	SuperscriptShiftUp
	SuperscriptShiftUpCramped
	SuperscriptBottomMin
	SuperscriptBaselineDropMax
	SubSuperscriptGapMin
	SuperscriptBottomMaxWithSubscript
	SpaceAfterScript
	UpperLimitGapMin
	UpperLimitBaselineRiseMin
	LowerLimitGapMin
	LowerLimitBaselineDropMin
	StackTopShiftUp
	StackTopDisplayStyleShiftUp
	StackBottomShiftDown
	StackBottomDisplayStyleShiftDown
	StackGapMin
	StackDisplayStyleGapMin
	StretchStackTopShiftUp
	StretchStackBottomShiftDown
	StretchStackGapAboveMin
	StretchStackGapBelowMin
	FractionNumeratorShiftUp
	FractionNumeratorDisplayStyleShiftUp
	FractionDenominatorShiftDown
	FractionDenominatorDisplayStyleShiftDown
	FractionNumeratorGapMin
	FractionNumDisplayStyleGapMin
	FractionRuleThickness
	FractionDenominatorGapMin
	FractionDenomDisplayStyleGapMin
	SkewedFractionHorizontalGap
	SkewedFractionVerticalGap
	OverbarVerticalGap
	OverbarRuleThickness
	OverbarExtraAscender
	UnderbarVerticalGap
	UnderbarRuleThickness
	UnderbarExtraDescender
	RadicalVerticalGap
	RadicalDisplayStyleVerticalGap
	RadicalRuleThickness
	RadicalExtraAscender
	RadicalKernBeforeDegree
	RadicalKernAfterDegree
	RadicalDegreeBottomRaisePercent

	mathConstCount
)

// isPercent reports whether c is a percentage ratio rather than a design-unit
// length; such a constant is returned unscaled.
func (c MathConstant) isPercent() bool {
	return c == ScriptPercentScaleDown ||
		c == ScriptScriptPercentScaleDown ||
		c == RadicalDegreeBottomRaisePercent
}

// MathKernCorner selects one of a glyph's four math-kern corners, used for the
// staircase cut-in between a base glyph and an adjacent script.
type MathKernCorner int

// The four math-kern corners, in the on-disk order of a MathKernInfoRecord.
const (
	MathKernTopRight MathKernCorner = iota
	MathKernTopLeft
	MathKernBottomRight
	MathKernBottomLeft
)

// partFlagExtender marks a GlyphPart that may be repeated to lengthen a stretchy
// glyph assembly (PART_FLAG_EXTENDER).
const partFlagExtender = 0x0001

// MathVariant is one size variant of a stretchy glyph: the variant's glyph and
// its advance (height for a vertical variant, width for a horizontal one) in
// pixels at the Face's size.
type MathVariant struct {
	Glyph   GlyphIndex
	Advance int
}

// MathAssembly is the recipe for building a stretchy glyph out of repeatable
// parts, with all measurements in pixels at the Face's size. A layout engine
// stacks the parts (vertically or horizontally), overlapping neighbours by at
// least MinConnectorOverlap and repeating the Extender parts until the assembly
// reaches the target size.
type MathAssembly struct {
	ItalicsCorrection   int
	MinConnectorOverlap int
	Parts               []MathAssemblyPart
}

// MathAssemblyPart is one component of a MathAssembly. StartConnector and
// EndConnector are the maximum overlap lengths at the part's leading and
// trailing ends; FullAdvance is the part's full extent along the stretch axis.
type MathAssemblyPart struct {
	Glyph          GlyphIndex
	StartConnector int
	EndConnector   int
	FullAdvance    int
	Extender       bool
}

// mathTable is a decoded MATH table. Every sub-part is optional; a font may
// carry constants only, glyph info only, variants only, or any combination.
type mathTable struct {
	constants    [mathConstCount]int32 // design-unit or percent scalars, indexed by MathConstant
	hasConstants bool

	italicCorr    map[GlyphIndex]int32 // MathItalicsCorrectionInfo: per-glyph italic correction
	topAccent     map[GlyphIndex]int32 // MathTopAccentAttachment: per-glyph top-accent x
	extendedShape map[GlyphIndex]int   // ExtendedShapeCoverage, used as a set
	kern          map[GlyphIndex]*mathKernRecord

	minConnectorOverlap int32
	vertConstruct       map[GlyphIndex]*mathGlyphConstruction // vertical (tall) stretch
	horizConstruct      map[GlyphIndex]*mathGlyphConstruction // horizontal (wide) stretch
}

// mathKernRecord holds a glyph's four corner MathKerns (any may be nil).
type mathKernRecord struct {
	corners [4]*mathKern
}

// mathKern is a step function of correction height: heights has n entries and
// kerns has n+1, so a height selects the kern in the interval it falls into.
type mathKern struct {
	heights []int32 // ascending correctionHeight values (design units)
	kerns   []int32 // kern values (design units), one more than heights
}

// valueAt returns the kern for correction height h (design units) by locating
// the interval h falls into: the first heights[i] strictly greater than h, or
// the final kern when h is at or above every height.
func (k *mathKern) valueAt(h int32) int32 {
	i := sort.Search(len(k.heights), func(i int) bool { return h < k.heights[i] })
	return k.kerns[i]
}

// mathGlyphConstruction lists a glyph's size variants and, optionally, the
// assembly recipe for an arbitrarily stretched glyph.
type mathGlyphConstruction struct {
	variants []mathVariantRecord
	assembly *mathAssemblyData
}

// mathVariantRecord is one MathGlyphVariantRecord (design units).
type mathVariantRecord struct {
	glyph   GlyphIndex
	advance int32
}

// mathAssemblyData is a decoded GlyphAssembly (design units).
type mathAssemblyData struct {
	italicsCorrection int32
	parts             []mathPart
}

// mathPart is one GlyphPart of an assembly (design units).
type mathPart struct {
	glyph          GlyphIndex
	startConnector int32
	endConnector   int32
	fullAdvance    int32
	extender       bool
}

// mathValue reads a MathValueRecord: an int16 design-unit value followed by an
// Offset16 to an optional Device table, which this package tolerates and
// ignores (device deltas fine-tune a value at a specific ppem; a scalable
// renderer does not need them).
func (r *reader) mathValue() int32 {
	v := r.i16()
	r.skip(2) // deviceOffset, ignored
	return int32(v)
}

// parseMath decodes the optional MATH table and stores it on the font. Absence
// is not an error; a malformed table fails the whole parse so a corrupt font is
// rejected cleanly, matching the other layout tables.
func (f *Font) parseMath(tables map[string][]byte) error {
	b, ok := tables["MATH"]
	if !ok {
		return nil
	}
	m, err := parseMATH(b)
	if err != nil {
		return err
	}
	f.math = m
	return nil
}

// parseMATH decodes a MATH table: a version header followed by up to three
// independent sub-offsets.
func parseMATH(b []byte) (*mathTable, error) {
	r := reader{b: b}
	r.skip(4) // majorVersion, minorVersion
	constOff := int(r.u16())
	glyphInfoOff := int(r.u16())
	variantsOff := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: MATH header: %w", r.err)
	}
	m := &mathTable{}
	if constOff != 0 {
		if err := m.parseConstants(subslice(b, constOff)); err != nil {
			return nil, err
		}
	}
	if glyphInfoOff != 0 {
		if err := m.parseGlyphInfo(subslice(b, glyphInfoOff)); err != nil {
			return nil, err
		}
	}
	if variantsOff != 0 {
		if err := m.parseVariants(subslice(b, variantsOff)); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// parseConstants decodes the MathConstants record into the constants array. The
// layout is two int16 percentages, two UFWORD minimum heights, 51 MathValue
// records and a trailing int16 percentage.
func (m *mathTable) parseConstants(b []byte) error {
	r := reader{b: b}
	var c [mathConstCount]int32
	c[ScriptPercentScaleDown] = int32(r.i16())
	c[ScriptScriptPercentScaleDown] = int32(r.i16())
	c[DelimitedSubFormulaMinHeight] = int32(r.u16())
	c[DisplayOperatorMinHeight] = int32(r.u16())
	for i := MathLeading; i <= RadicalKernAfterDegree; i++ {
		c[i] = r.mathValue()
	}
	c[RadicalDegreeBottomRaisePercent] = int32(r.i16())
	if r.err != nil {
		return fmt.Errorf("opentype: MATH constants: %w", r.err)
	}
	m.constants = c
	m.hasConstants = true
	return nil
}

// parseGlyphInfo decodes the MathGlyphInfo table: four optional sub-offsets.
func (m *mathTable) parseGlyphInfo(b []byte) error {
	r := reader{b: b}
	italicOff := int(r.u16())
	topAccentOff := int(r.u16())
	extShapeOff := int(r.u16())
	kernOff := int(r.u16())
	if r.err != nil {
		return fmt.Errorf("opentype: MATH glyphInfo: %w", r.err)
	}
	if italicOff != 0 {
		vals, err := parsePerGlyphValues(subslice(b, italicOff))
		if err != nil {
			return err
		}
		m.italicCorr = vals
	}
	if topAccentOff != 0 {
		vals, err := parsePerGlyphValues(subslice(b, topAccentOff))
		if err != nil {
			return err
		}
		m.topAccent = vals
	}
	if extShapeOff != 0 {
		cov, err := parseCoverage(subslice(b, extShapeOff))
		if err != nil {
			return err
		}
		m.extendedShape = cov
	}
	if kernOff != 0 {
		k, err := parseMathKernInfo(subslice(b, kernOff))
		if err != nil {
			return err
		}
		m.kern = k
	}
	return nil
}

// parsePerGlyphValues decodes the shape shared by MathItalicsCorrectionInfo and
// MathTopAccentAttachment: a Coverage offset, a count and that many MathValue
// records, joined into a per-glyph map.
func parsePerGlyphValues(b []byte) (map[GlyphIndex]int32, error) {
	r := reader{b: b}
	covOff := int(r.u16())
	count := int(r.u16())
	vals := make([]int32, count)
	for i := range vals {
		vals[i] = r.mathValue()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: MATH per-glyph values: %w", r.err)
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	return coverageValues(cov, vals), nil
}

// coverageValues joins a Coverage (glyph→index) with a parallel value slice into
// a glyph→value map.
func coverageValues(cov map[GlyphIndex]int, vals []int32) map[GlyphIndex]int32 {
	out := make(map[GlyphIndex]int32, len(cov))
	for g, i := range cov {
		if i < len(vals) {
			out[g] = vals[i]
		}
	}
	return out
}

// parseMathKernInfo decodes the MathKernInfo table: a Coverage plus one
// MathKernInfoRecord (four corner offsets) per covered glyph.
func parseMathKernInfo(b []byte) (map[GlyphIndex]*mathKernRecord, error) {
	r := reader{b: b}
	covOff := int(r.u16())
	count := int(r.u16())
	offs := make([][4]int, count)
	for i := range offs {
		for c := 0; c < 4; c++ {
			offs[i][c] = int(r.u16())
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: MATH kernInfo: %w", r.err)
	}
	recs := make([]*mathKernRecord, count)
	for i := range offs {
		rec := &mathKernRecord{}
		for c := 0; c < 4; c++ {
			if offs[i][c] == 0 {
				continue // this corner has no math kern
			}
			k, err := parseMathKern(subslice(b, offs[i][c]))
			if err != nil {
				return nil, err
			}
			rec.corners[c] = k
		}
		recs[i] = rec
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	out := make(map[GlyphIndex]*mathKernRecord, len(cov))
	for g, i := range cov {
		if i < len(recs) {
			out[g] = recs[i]
		}
	}
	return out, nil
}

// parseMathKern decodes a MathKern subtable: heightCount correction heights and
// heightCount+1 kern values.
func parseMathKern(b []byte) (*mathKern, error) {
	r := reader{b: b}
	n := int(r.u16())
	heights := make([]int32, n)
	for i := range heights {
		heights[i] = r.mathValue()
	}
	kerns := make([]int32, n+1)
	for i := range kerns {
		kerns[i] = r.mathValue()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: MATH kern: %w", r.err)
	}
	return &mathKern{heights: heights, kerns: kerns}, nil
}

// parseVariants decodes the MathVariants table: the minimum connector overlap,
// two Coverage offsets and the vertical and horizontal glyph constructions.
func (m *mathTable) parseVariants(b []byte) error {
	r := reader{b: b}
	m.minConnectorOverlap = int32(r.u16())
	vertCovOff := int(r.u16())
	horizCovOff := int(r.u16())
	vertCount := int(r.u16())
	horizCount := int(r.u16())
	vertOffs := make([]int, vertCount)
	for i := range vertOffs {
		vertOffs[i] = int(r.u16())
	}
	horizOffs := make([]int, horizCount)
	for i := range horizOffs {
		horizOffs[i] = int(r.u16())
	}
	if r.err != nil {
		return fmt.Errorf("opentype: MATH variants: %w", r.err)
	}
	vert, err := buildConstructions(b, vertCovOff, vertOffs)
	if err != nil {
		return err
	}
	horiz, err := buildConstructions(b, horizCovOff, horizOffs)
	if err != nil {
		return err
	}
	m.vertConstruct = vert
	m.horizConstruct = horiz
	return nil
}

// buildConstructions decodes each MathGlyphConstruction at offs and keys them by
// the glyphs in the Coverage at covOff. A zero covOff (an empty axis) yields an
// empty map without touching the coverage bytes.
func buildConstructions(b []byte, covOff int, offs []int) (map[GlyphIndex]*mathGlyphConstruction, error) {
	out := map[GlyphIndex]*mathGlyphConstruction{}
	if covOff == 0 {
		return out, nil
	}
	constructs := make([]*mathGlyphConstruction, len(offs))
	for i, off := range offs {
		gc, err := parseGlyphConstruction(subslice(b, off))
		if err != nil {
			return nil, err
		}
		constructs[i] = gc
	}
	cov, err := parseCoverage(subslice(b, covOff))
	if err != nil {
		return nil, err
	}
	for g, i := range cov {
		if i < len(constructs) {
			out[g] = constructs[i]
		}
	}
	return out, nil
}

// parseGlyphConstruction decodes a MathGlyphConstruction: an optional
// GlyphAssembly offset and a list of MathGlyphVariantRecords.
func parseGlyphConstruction(b []byte) (*mathGlyphConstruction, error) {
	r := reader{b: b}
	asmOff := int(r.u16())
	variantCount := int(r.u16())
	variants := make([]mathVariantRecord, variantCount)
	for i := range variants {
		variants[i].glyph = GlyphIndex(r.u16())
		variants[i].advance = int32(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: MATH glyph construction: %w", r.err)
	}
	gc := &mathGlyphConstruction{variants: variants}
	if asmOff != 0 {
		asm, err := parseGlyphAssembly(subslice(b, asmOff))
		if err != nil {
			return nil, err
		}
		gc.assembly = asm
	}
	return gc, nil
}

// parseGlyphAssembly decodes a GlyphAssembly: an italics-correction MathValue
// and a list of GlyphParts.
func parseGlyphAssembly(b []byte) (*mathAssemblyData, error) {
	r := reader{b: b}
	italic := r.mathValue()
	partCount := int(r.u16())
	parts := make([]mathPart, partCount)
	for i := range parts {
		parts[i].glyph = GlyphIndex(r.u16())
		parts[i].startConnector = int32(r.u16())
		parts[i].endConnector = int32(r.u16())
		parts[i].fullAdvance = int32(r.u16())
		parts[i].extender = r.u16()&partFlagExtender != 0
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: MATH glyph assembly: %w", r.err)
	}
	return &mathAssemblyData{italicsCorrection: italic, parts: parts}, nil
}

// --- public API -------------------------------------------------------------

// HasMath reports whether the font carries an OpenType MATH table (the metrics
// a math-typesetting engine needs). When false, all Face math accessors return
// their zero result.
func (f *Font) HasMath() bool { return f.math != nil }

// IsExtendedShapeGlyph reports whether gid is in the MATH table's extended-shape
// coverage: a tall glyph (such as a large integral) whose superscript is
// positioned as if the glyph were a stretched, rather than a fixed-size, shape.
func (f *Font) IsExtendedShapeGlyph(gid GlyphIndex) bool {
	if f.math == nil {
		return false
	}
	_, ok := f.math.extendedShape[gid]
	return ok
}

// scaleUnit converts a design-unit value to whole pixels at the face's size.
func (fc *Face) scaleUnit(v int32) int { return roundInt(float64(v) * fc.scale) }

// MathConstant returns the MathConstants value which, pixel-scaled at the face's
// size — except the three percentage constants, which are pure ratios and
// returned verbatim. It returns 0 when the font has no MATH constants or which
// is out of range.
func (fc *Face) MathConstant(which MathConstant) int {
	m := fc.font.math
	if m == nil || !m.hasConstants || which < 0 || which >= mathConstCount {
		return 0
	}
	v := m.constants[which]
	if which.isPercent() {
		return int(v)
	}
	return fc.scaleUnit(v)
}

// ItalicCorrection returns glyph gid's math italic correction in pixels, or 0
// when the glyph has none (or the font carries no MATH table). It is the space
// to insert after a slanted glyph before a following upright one, and the
// horizontal offset for a subscript that follows a superscript.
func (fc *Face) ItalicCorrection(gid GlyphIndex) int {
	m := fc.font.math
	if m == nil {
		return 0
	}
	return fc.scaleUnit(m.italicCorr[gid])
}

// TopAccentAttachment returns the x position (in pixels from the glyph origin)
// at which a top accent should be centred over glyph gid. ok is false when the
// glyph has no entry, in which case a layout engine centres on the glyph's
// advance instead.
func (fc *Face) TopAccentAttachment(gid GlyphIndex) (int, bool) {
	m := fc.font.math
	if m == nil {
		return 0, false
	}
	v, ok := m.topAccent[gid]
	if !ok {
		return 0, false
	}
	return fc.scaleUnit(v), true
}

// MathKern returns the math cut-in kern (in pixels) for glyph gid at the given
// corner and correction height, the height (in pixels above the baseline) at
// which the adjacent script attaches. It returns 0 when the glyph has no kern
// for that corner or the font carries no MATH table.
func (fc *Face) MathKern(gid GlyphIndex, corner MathKernCorner, correctionHeight int) int {
	m := fc.font.math
	if m == nil {
		return 0
	}
	rec := m.kern[gid]
	if rec == nil {
		return 0
	}
	if corner < 0 || corner > MathKernBottomLeft {
		return 0
	}
	k := rec.corners[corner]
	if k == nil {
		return 0
	}
	hUnits := int32(roundInt(float64(correctionHeight) / fc.scale))
	return fc.scaleUnit(k.valueAt(hUnits))
}

// MathVariants returns the size variants and the stretchy-glyph assembly for
// glyph gid along the requested axis (vertical when vertical is true, otherwise
// horizontal), with every measurement pixel-scaled at the face's size. Variants
// are ordered smallest-first as stored in the font; the assembly is nil when the
// glyph has no assembly recipe. Both results are nil when the glyph has no
// construction on that axis (or the font carries no MATH table). A math layout
// engine picks the first variant whose advance meets the target size, or builds
// the assembly when even the largest variant is too small.
func (fc *Face) MathVariants(gid GlyphIndex, vertical bool) ([]MathVariant, *MathAssembly) {
	m := fc.font.math
	if m == nil {
		return nil, nil
	}
	table := m.horizConstruct
	if vertical {
		table = m.vertConstruct
	}
	gc := table[gid]
	if gc == nil {
		return nil, nil
	}
	var variants []MathVariant
	for _, v := range gc.variants {
		variants = append(variants, MathVariant{Glyph: v.glyph, Advance: fc.scaleUnit(v.advance)})
	}
	var asm *MathAssembly
	if gc.assembly != nil {
		a := gc.assembly
		asm = &MathAssembly{
			ItalicsCorrection:   fc.scaleUnit(a.italicsCorrection),
			MinConnectorOverlap: fc.scaleUnit(m.minConnectorOverlap),
			Parts:               make([]MathAssemblyPart, len(a.parts)),
		}
		for i, p := range a.parts {
			asm.Parts[i] = MathAssemblyPart{
				Glyph:          p.glyph,
				StartConnector: fc.scaleUnit(p.startConnector),
				EndConnector:   fc.scaleUnit(p.endConnector),
				FullAdvance:    fc.scaleUnit(p.fullAdvance),
				Extender:       p.extender,
			}
		}
	}
	return variants, asm
}
