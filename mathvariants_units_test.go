package opentype

import (
	"math"
	"reflect"
	"testing"
)

// At a scale where design units are not pixels (upem 1000, 1.5 px per em) the
// rounded form loses the fraction and the Units form keeps it. A face built at
// scale 1.0 could not tell the two apart.
func TestMathVariantsUnitsKeepTheFraction(t *testing.T) {
	f := fontWithMATH(t, populatedMATH())
	fc := f.NewFaceAt(1.5)
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

	vars, asm := fc.MathVariantsUnits(0, true)
	if len(vars) != 2 || !near(vars[0].Advance, 0.9) || !near(vars[1].Advance, 1.35) {
		t.Fatalf("variants = %+v, want advances 0.9 and 1.35", vars)
	}
	if asm == nil || !near(asm.ItalicsCorrection, 0.045) || !near(asm.MinConnectorOverlap, 0.03) {
		t.Fatalf("assembly = %+v", asm)
	}
	p := asm.Parts[0]
	if !near(p.StartConnector, 0.075) || !near(p.EndConnector, 0.09) || !near(p.FullAdvance, 1.05) || p.Extender {
		t.Errorf("part 0 = %+v", p)
	}
	if !asm.Parts[1].Extender {
		t.Errorf("part 1 lost its Extender flag")
	}

	// The rounded form of the same face, to pin what the Units form avoids.
	rv, _ := fc.MathVariants(0, true)
	if rv[0].Advance != 1 || rv[1].Advance != 1 {
		t.Errorf("rounded advances = %+v, want 1 and 1", rv)
	}

	// Same shape as MathVariants: horizontal, absent glyph, no MATH.
	hv, ha := fc.MathVariantsUnits(0, false)
	if len(hv) != 1 || ha != nil {
		t.Errorf("horizontal = %+v, %+v", hv, ha)
	}
	if v, a := fc.MathVariantsUnits(99, true); v != nil || a != nil {
		t.Errorf("uncovered = %+v, %+v", v, a)
	}
	if v, a := fontWithMATH(t, nil).NewFace(1000).MathVariantsUnits(0, true); v != nil || a != nil {
		t.Errorf("no MATH = %+v, %+v", v, a)
	}

	// At scale 1.0 the two forms agree.
	one := f.NewFace(1000)
	uv, ua := one.MathVariantsUnits(0, true)
	rv1, ra := one.MathVariants(0, true)
	for i := range uv {
		if uv[i].Glyph != rv1[i].Glyph || uv[i].Advance != float64(rv1[i].Advance) {
			t.Errorf("scale 1 variant %d: %+v vs %+v", i, uv[i], rv1[i])
		}
	}
	if !reflect.DeepEqual(len(ua.Parts), len(ra.Parts)) {
		t.Errorf("part counts differ")
	}
}
