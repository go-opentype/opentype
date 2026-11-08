package opentype

import (
	"math"
	"os"
	"testing"
)

// realFaceAt loads a production font at a real text size, skipping when the
// fixture is absent.
func realFaceAt(t *testing.T, file string, pt float64) (*Font, *Face) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Skipf("%s not available: %v", file, err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(%s): %v", file, err)
	}
	return f, f.NewFaceAt(pt)
}

// ⛔⛔ THE RUN IS WHERE ROUNDING COMPOUNDS, so this measures a RUN and not a
// glyph: the rounded form's error is the sum of per-glyph errors, which is the
// reason a typesetter could not use ShapePositioned and hand-rolled its metrics
// instead — losing ligatures and mark attachment to keep its advances exact.
func TestShapePositionedUnitsKeepsWhatTheRunAccumulates(t *testing.T) {
	_, fc := realFaceAt(t, "SourceSerif4-Regular.otf", 10)

	// A line's worth of ordinary text, which is the unit a line breaker decides on.
	const line = "The quick brown fox jumps over the lazy dog, and then does it again."
	exact := fc.ShapePositionedUnits(line, "liga")
	rounded := fc.ShapePositioned(line, "liga")
	if len(exact) != len(rounded) {
		t.Fatalf("%d exact glyphs vs %d rounded: the two must shape identically",
			len(exact), len(rounded))
	}
	if len(exact) == 0 {
		t.Fatal("the run is empty")
	}

	var sumExact, sumRounded float64
	for i := range exact {
		if exact[i].Glyph != rounded[i].Glyph {
			t.Fatalf("glyph %d differs: %d exact vs %d rounded — only the NUMBERS "+
				"may differ between the two forms, never the glyphs", i, exact[i].Glyph, rounded[i].Glyph)
		}
		sumExact += exact[i].XAdvance
		sumRounded += float64(rounded[i].XAdvance)
	}
	drift := math.Abs(sumExact - sumRounded)
	if drift < 1 {
		t.Errorf("the rounded run drifts by only %.4fpt over %d glyphs; if that is "+
			"really so small the premise of this function is wrong and it should go",
			drift, len(exact))
	}
	t.Logf("run of %d glyphs: exact %.4fpt, rounded %.0fpt, drift %.4fpt",
		len(exact), sumExact, sumRounded, drift)

	// ⛔ And each rounded value must be its exact one rounded — ShapePositioned is
	// defined as this function rounded, so the two cannot drift apart by
	// construction. A test that only compared sums would miss a sign error.
	for i := range exact {
		if got, want := rounded[i].XAdvance, roundInt(exact[i].XAdvance); got != want {
			t.Errorf("glyph %d: rounded advance %d, want %d (= round(%.4f))",
				i, got, want, exact[i].XAdvance)
		}
		if got, want := rounded[i].XOffset, roundInt(exact[i].XOffset); got != want {
			t.Errorf("glyph %d: rounded x offset %d, want %d", i, got, want)
		}
		if got, want := rounded[i].YOffset, roundInt(exact[i].YOffset); got != want {
			t.Errorf("glyph %d: rounded y offset %d, want %d", i, got, want)
		}
	}
}

// ⛔⛔ THE EQUIVALENCE A CONSUMER NEEDS BEFORE IT CAN ADOPT THIS.
//
// A text engine that measures by hand does advance + kern per pair:
// AdvanceIndexUnits × Scale, plus KernIndexUnits × Scale against the next glyph.
// The shaped run must come out the SAME, because "kern" is always on in
// ShapePositioned's feature set and GPOS pair positioning adds its adjustment to
// the advance of the FIRST glyph of the pair. If the two disagreed, adopting the
// shared path would silently change the metrics of every document — which is a
// good reason not to adopt it, and the thing to prove before asking anyone to.
//
// Written first as "shaped == raw advance", which FAILED on 'w' (7.44 vs 7.64)
// and 'r' (4.13 vs 4.23) in "nonsense words": those are kern pairs, and the
// shaped run was right while the test was wrong.
func TestShapePositionedUnitsAgreesWithTheHandRolledPath(t *testing.T) {
	f, fc := realFaceAt(t, "SourceSerif4-Regular.otf", 10)

	const plain = "nonsense words" // no fi/fl/ffi, so one glyph per rune
	run := fc.ShapePositionedUnits(plain)

	gids := make([]GlyphIndex, 0, len(plain))
	for _, r := range plain {
		gid, ok := f.GlyphIndex(r)
		if !ok {
			t.Fatalf("cmap has no glyph for %q", r)
		}
		gids = append(gids, gid)
	}
	if len(run) != len(gids) {
		t.Fatalf("the run has %d glyphs for %d runes — a ligature in supposedly "+
			"plain text invalidates this comparison", len(run), len(gids))
	}

	kerned := 0
	for i, gid := range gids {
		if run[i].Glyph != gid {
			t.Fatalf("glyph %d is %d, want %d", i, run[i].Glyph, gid)
		}
		want := fc.AdvanceIndexUnits(gid) * fc.Scale()
		if i+1 < len(gids) {
			k := fc.KernIndexUnits(gid, gids[i+1]) * fc.Scale()
			if k != 0 {
				kerned++
			}
			want += k
		}
		if got := run[i].XAdvance; math.Abs(got-want) > 1e-9 {
			t.Errorf("glyph %d (%q): shaped %.6f, advance+kern %.6f", i, plain[i], got, want)
		}
	}
	// ⛔ A run with no kern pair in it would make the comparison vacuous: it would
	// hold for a shaper that ignored GPOS entirely.
	if kerned == 0 {
		t.Error("no pair in this run kerns, so the agreement proves nothing about GPOS")
	}
}

// The no-layout-tables path is NOT re-tested here: TestShapePositionedNoLayoutTables
// and TestFaceShapeNoGSUB already drive it, and they now reach it THROUGH this
// function, since ShapePositioned is defined as this one rounded. Writing a
// second synthetic font for it would re-prove covered ground.
