package opentype

// MathVariantUnits is MathVariant without the rounding: Advance is the variant's
// height (vertical) or width (horizontal) in pixels at the Face's size, as a
// fraction.
type MathVariantUnits struct {
	Glyph   GlyphIndex
	Advance float64
}

// MathAssemblyUnits is MathAssembly without the rounding.
type MathAssemblyUnits struct {
	ItalicsCorrection   float64
	MinConnectorOverlap float64
	Parts               []MathAssemblyPartUnits
}

// MathAssemblyPartUnits is MathAssemblyPart without the rounding.
type MathAssemblyPartUnits struct {
	Glyph          GlyphIndex
	StartConnector float64
	EndConnector   float64
	FullAdvance    float64
	Extender       bool
}

// MathVariantsUnits is MathVariants without the rounding. The OpenType MATH table
// stores every one of these measurements in design units (MathGlyphVariantRecord
// advanceMeasurement, GlyphPart startConnectorLength / endConnectorLength /
// fullAdvance, MathGlyphConstruction italicsCorrection, MathVariants
// minConnectorOverlap) and prescribes no rounding; a layout that picks the first
// variant reaching a target, or sums an assembly's parts, compares and accumulates
// these quantities, and rounding each to a whole pixel first moves the choice and
// the total by up to half a pixel per term.
func (fc *Face) MathVariantsUnits(gid GlyphIndex, vertical bool) ([]MathVariantUnits, *MathAssemblyUnits) {
	m := fc.font.math
	if m == nil {
		return nil, nil
	}
	table := m.horizConstruct
	if vertical {
		table = m.vertConstruct
	}
	gc := table.val(gid)
	if gc == nil {
		return nil, nil
	}
	var variants []MathVariantUnits
	for _, v := range gc.variants {
		variants = append(variants, MathVariantUnits{Glyph: v.glyph, Advance: fc.scaleUnitF(v.advance)})
	}
	var asm *MathAssemblyUnits
	if gc.assembly != nil {
		a := gc.assembly
		asm = &MathAssemblyUnits{
			ItalicsCorrection:   fc.scaleUnitF(a.italicsCorrection),
			MinConnectorOverlap: fc.scaleUnitF(m.minConnectorOverlap),
			Parts:               make([]MathAssemblyPartUnits, len(a.parts)),
		}
		for i, p := range a.parts {
			asm.Parts[i] = MathAssemblyPartUnits{
				Glyph:          p.glyph,
				StartConnector: fc.scaleUnitF(p.startConnector),
				EndConnector:   fc.scaleUnitF(p.endConnector),
				FullAdvance:    fc.scaleUnitF(p.fullAdvance),
				Extender:       p.extender,
			}
		}
	}
	return variants, asm
}
