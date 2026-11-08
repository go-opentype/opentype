package opentype

import (
	"math"
	"os"
	"testing"
)

// ⛔⛔ THE CONTROL THE EXISTING REAL-FONT TEST COULD NOT BE.
//
// TestRealMathFontSTIX builds its face with NewFace(unitsPerEm) — scale 1.0,
// where a pixel IS a design unit — and that is the one scale at which rounding
// changes nothing. Every assertion it makes would hold with the rounding in
// place or removed, so it was green through the whole defect.
//
// These run at 10pt, which is what a typesetter faces a font at, and assert the
// magnitudes measured there on testdata/STIXTwoMath-Regular.otf.
func TestScaledMetricsAreNotRoundedAtATextSize(t *testing.T) {
	b, err := os.ReadFile("testdata/STIXTwoMath-Regular.otf")
	if err != nil {
		t.Skipf("real math font not available: %v", err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(STIXTwoMath): %v", err)
	}
	if !f.HasMath() {
		t.Fatal("STIXTwoMath-Regular.otf is expected to carry a MATH table")
	}
	fc := f.NewFaceAt(10) // 10pt: one pixel is one point, as a typesetter faces it

	// The math axis: where every fraction bar and every relation symbol is
	// centred, so an error here is on every formula of every page.
	const wantAxis = 2.58
	if got := fc.MathConstantUnits(AxisHeight); math.Abs(got-wantAxis) > 0.001 {
		t.Errorf("MathConstantUnits(AxisHeight) = %.4f, want %.2f", got, wantAxis)
	}
	if got := fc.MathConstant(AxisHeight); got != 3 {
		t.Errorf("MathConstant(AxisHeight) = %d, want 3 — the rounded form is "+
			"documented to be coarse and this pins what it costs", got)
	}

	// The space advance, which is what a typesetter reads for interword glue.
	const wantSpace = 2.35
	if got := fc.AdvanceUnits(' '); math.Abs(got-wantSpace) > 0.001 {
		t.Errorf("AdvanceUnits(' ') = %.4f, want %.2f", got, wantSpace)
	}
	if got := fc.Advance(' '); got != 2 {
		t.Errorf("Advance(' ') = %d, want 2", got)
	}

	// ⛔ The population, not one example: a single glyph that happens to round
	// cleanly would let this pass over a defect affecting the whole font.
	differ, nonzero := 0, 0
	worst := 0.0
	for gid := GlyphIndex(0); int(gid) < f.NumGlyphs(); gid++ {
		exact := fc.ItalicCorrectionUnits(gid)
		if exact == 0 {
			continue
		}
		nonzero++
		if d := math.Abs(exact - float64(fc.ItalicCorrection(gid))); d > 1e-9 {
			differ++
			if d > worst {
				worst = d
			}
		}
	}
	if nonzero < 800 {
		t.Fatalf("%d non-zero italic corrections, expected ~882 — wrong font or a decoder regression", nonzero)
	}
	if differ < nonzero/2 {
		t.Errorf("only %d of %d italic corrections differ from their rounded form; "+
			"measured 840 of 882, so this font's corrections are NOT whole points", differ, nonzero)
	}
	if worst < 0.4 {
		t.Errorf("worst italic-correction rounding loss %.4f, measured 0.5pt", worst)
	}
}

// At scale 1.0 the rounded and unrounded forms agree, because a design unit IS a
// pixel there. Asserted so that the reason the old test saw nothing is itself
// pinned: if this ever diverges, the two scaling paths have drifted apart.
func TestScaledMetricsAgreeAtUnitScale(t *testing.T) {
	b, err := os.ReadFile("testdata/STIXTwoMath-Regular.otf")
	if err != nil {
		t.Skipf("real math font not available: %v", err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	fc := f.NewFace(f.unitsPerEm) // scale 1.0

	if a, b := fc.MathConstantUnits(AxisHeight), float64(fc.MathConstant(AxisHeight)); a != b {
		t.Errorf("AxisHeight at unit scale: unrounded %.4f, rounded %.0f — they must agree here", a, b)
	}
	for gid := GlyphIndex(0); int(gid) < f.NumGlyphs() && int(gid) < 500; gid++ {
		if a, b := fc.ItalicCorrectionUnits(gid), float64(fc.ItalicCorrection(gid)); a != b {
			t.Fatalf("glyph %d italic correction at unit scale: unrounded %.4f, rounded %.0f", gid, a, b)
		}
	}
}

// A percentage constant is a ratio, not a length, so it must come back verbatim
// from both forms rather than be scaled by one of them.
func TestMathPercentConstantsAreNotScaled(t *testing.T) {
	b, err := os.ReadFile("testdata/STIXTwoMath-Regular.otf")
	if err != nil {
		t.Skipf("real math font not available: %v", err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	fc := f.NewFaceAt(10)
	for which := MathConstant(0); which < mathConstCount; which++ {
		if !which.isPercent() {
			continue
		}
		if a, b := fc.MathConstantUnits(which), float64(fc.MathConstant(which)); a != b {
			t.Errorf("percent constant %d: unrounded %.4f, rounded %.0f — a ratio must not be scaled", which, a, b)
		}
	}
}

// AdvanceUnits must answer for an unmapped rune exactly as Advance does, so a
// caller swapping one for the other cannot change how a missing glyph reads.
func TestAdvanceUnitsOnAnUnmappedRune(t *testing.T) {
	b, err := os.ReadFile("testdata/STIXTwoMath-Regular.otf")
	if err != nil {
		t.Skipf("real math font not available: %v", err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	fc := f.NewFaceAt(10)
	const unmapped = rune(0x10FFFD) // a private-use plane 16 code point
	if got := fc.AdvanceUnits(unmapped); got != 0 {
		t.Errorf("AdvanceUnits(unmapped) = %.4f, want 0", got)
	}
	if got := fc.Advance(unmapped); got != 0 {
		t.Errorf("Advance(unmapped) = %d, want 0", got)
	}
}
