// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"math"
	"testing"
)

// The oracle is FreeType through XeTeX (tectonic): at 1000pt on a 1000-unit
// font, \XeTeXglyphbounds 1..4 print xMin, yMax, advance − xMax and −yMin of
// FT_Glyph_Get_CBox(…, FT_GLYPH_BBOX_UNSCALED, …) in points equal to font units.
//
//	\font\ss="[SourceSerif4-Regular.otf]" at 1000pt
//	f: l=34 t=750 r=-72 b=0 w=354    a: l=45 t=488 r=6  b=13  w=509
//	o: l=45 t=488 r=45  b=13 w=549   S: l=40 t=685 r=40 b=15  w=512
//	g: l=36 t=488 r=23  b=240 w=518
func TestGlyphControlBoxIsFreeTypes(t *testing.T) {
	f, fc := realFaceAt(t, "SourceSerif4-Regular.otf", 10)
	for _, w := range []struct {
		r                      rune
		xMin, yMin, xMax, yMax float64
	}{
		{'f', 34, 0, 426, 750},
		{'a', 45, -13, 503, 488},
		{'o', 45, -13, 504, 488},
		{'S', 40, -15, 472, 685},
		{'g', 36, -240, 495, 488},
	} {
		gid, ok := f.GlyphIndex(w.r)
		if !ok {
			t.Fatalf("no glyph for %q", w.r)
		}
		x0, y0, x1, y1, ok := fc.GlyphControlBox(gid)
		if !ok {
			t.Fatalf("%q: not ok", w.r)
		}
		got := [4]float64{x0, y0, x1, y1}
		want := [4]float64{w.xMin, w.yMin, w.xMax, w.yMax}
		for i := range got {
			if math.Abs(got[i]-want[i]) > 1e-6 {
				t.Errorf("%q control box %v, FreeType %v", w.r, got, want)
				break
			}
		}
	}
}

// The box holds every point of the outline, and for TrueType — whose off-curve
// points GlyphOutline keeps — it is exactly the outline's point box.
func TestGlyphControlBoxHoldsTheOutline(t *testing.T) {
	for _, file := range []string{"SourceSerif4-Regular.otf", "STIXTwoMath-Regular.otf", "IBMPlexSans-Regular-oracle.ttf"} {
		f, fc := realFaceAt(t, file, 10)
		trueType := f.cff == nil && f.cff2 == nil && f.t1 == nil
		for g := 0; g < f.NumGlyphs(); g++ {
			gid := GlyphIndex(g)
			segs, ok := fc.GlyphOutline(gid)
			if !ok {
				continue
			}
			x0, y0, x1, y1, ok := fc.GlyphControlBox(gid)
			if !ok {
				t.Fatalf("%s gid %d: outline ok, control box not", file, g)
			}
			var b ctrlBox
			for _, s := range segs {
				n := 0
				switch s.Op {
				case SegMoveTo, SegLineTo:
					n = 1
				case SegQuadTo:
					n = 2
				}
				for _, p := range s.P[:n] {
					b.add(p.X, p.Y)
				}
			}
			if !b.set {
				if x0 != 0 || y0 != 0 || x1 != 0 || y1 != 0 {
					t.Errorf("%s gid %d: empty glyph has box %v %v %v %v", file, g, x0, y0, x1, y1)
				}
				continue
			}
			const eps = 1e-9
			if b.xMin < x0-eps || b.yMin < y0-eps || b.xMax > x1+eps || b.yMax > y1+eps {
				t.Fatalf("%s gid %d: outline %v outside control box %v %v %v %v", file, g, b, x0, y0, x1, y1)
			}
			if trueType && (b.xMin != x0 || b.yMin != y0 || b.xMax != x1 || b.yMax != y1) {
				t.Fatalf("%s gid %d: TrueType box %v %v %v %v, its points %v", file, g, x0, y0, x1, y1, b)
			}
		}
	}
}

// The arch (0,0) → (100,0) through control points (−20,130) and (150,110) lies
// inside them: its control box, which FreeType reports, is (−20,0)–(150,130),
// each control point holding one edge of it alone; a box read off the
// flattened outline is narrower and lower.
func TestGlyphControlBoxCountsTheCubicsControlPoints(t *testing.T) {
	want := ctrlBox{-20, 0, 150, 130, true}

	arch := (&csb{}).num(0).num(0).op(21).num(-20).num(130).num(170).num(-20).num(-50).num(-110).op(8).op(14).b
	c, err := parseCFF(buildCFF(cffOptions{glyphs: [][]byte{arch}}))
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.runGlyph(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.box != want {
		t.Errorf("CFF arch: box %+v, want %+v", m.box, want)
	}
	if ink := contoursBox(m.contours); ink.yMax >= 130 || ink.xMax >= 150 || ink.xMin <= -20 {
		t.Errorf("CFF arch: flattened box %+v reaches a control point", ink)
	}

	t1arch := append(t1num(0, 500), t1HSbw)
	t1arch = append(t1arch, t1num(0, 0)...)
	t1arch = append(t1arch, t1RMoveTo)
	t1arch = append(t1arch, t1num(-20, 130, 170, -20, -50, -110)...)
	t1arch = append(t1arch, t1RRCurveTo, t1ClosePath, t1EndChar)
	f := t1Of(t, nil, map[string][]byte{".notdef": {t1EndChar}, "arch": t1arch}, []string{".notdef", "arch"})
	tm, err := f.runBudgeted("arch", 0, &t1budget{})
	if err != nil {
		t.Fatal(err)
	}
	if tm.box != want {
		t.Errorf("Type 1 arch: box %+v, want %+v", tm.box, want)
	}
}

// A seac's box is its base's and its accent's, the accent's moved to where the
// accent is drawn.
func TestGlyphControlBoxOfAnAccentedGlyph(t *testing.T) {
	seacCS := (&csb{}).num(100).num(50).num(65).num(193).op(14).b
	base := (&csb{}).num(0).num(0).op(21).num(50).num(0).op(5).num(0).num(50).op(5).op(14).b
	accent := (&csb{}).num(10).num(10).op(21).num(20).num(0).op(5).op(14).b
	c, err := parseCFF(buildCFF(cffOptions{
		glyphs:      [][]byte{seacCS, base, accent},
		charsetSIDs: []int{34, 124},
	}))
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.runGlyph(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// base (0,0)–(50,50); accent (10,10)–(30,10) moved by (100,50).
	if want := (ctrlBox{0, 0, 130, 60, true}); m.box != want {
		t.Errorf("CFF seac: box %+v, want %+v", m.box, want)
	}

	acute := append(t1num(0, 300), t1HSbw)
	acute = append(acute, t1num(0, 700)...)
	acute = append(acute, t1RMoveTo)
	acute = append(acute, t1num(50)...)
	acute = append(acute, t1HLineTo)
	acute = append(acute, t1num(50)...)
	acute = append(acute, t1VLineTo, t1ClosePath, t1EndChar)
	composed := append(t1num(0, 600), t1HSbw)
	composed = append(composed, t1num(0, 0, 100, 65, 194)...)
	composed = append(composed, t1Escape, t1Seac)
	f := t1Of(t, nil, map[string][]byte{
		".notdef": {t1EndChar}, "A": t1Box(600), "acute": acute, "Aacute": composed,
	}, []string{".notdef", "A", "acute", "Aacute"})
	bm, err := f.runBudgeted("A", 0, &t1budget{})
	if err != nil {
		t.Fatal(err)
	}
	tm, err := f.runBudgeted("Aacute", 0, &t1budget{})
	if err != nil {
		t.Fatal(err)
	}
	// The acute (0,700)–(50,750), raised by the seac's ady of 100.
	want := bm.box
	want.union(ctrlBox{0, 700, 50, 750, true}, 0, 100)
	if tm.box != want {
		t.Errorf("Type 1 seac: box %+v, want %+v", tm.box, want)
	}
}

// A glyph that does not exist, or whose program fails, has no box — in every
// outline flavour, not a zero box reported as found.
func TestGlyphControlBoxOutOfRange(t *testing.T) {
	for _, file := range []string{"SourceSerif4-Regular.otf", "IBMPlexSans-Regular-oracle.ttf"} {
		f, fc := realFaceAt(t, file, 10)
		if _, _, _, _, ok := fc.GlyphControlBox(GlyphIndex(f.NumGlyphs())); ok {
			t.Errorf("%s: a glyph past the last one has a box", file)
		}
	}

	arch := (&csb{}).num(0).num(0).op(21).num(10).num(0).op(5).b
	c2, err := parseCFF2(buildCFF2([][]byte{arch}, nil, nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	fc2 := &Face{font: &Font{cff2: c2}}
	if _, _, x1, _, ok := fc2.GlyphControlBox(0); !ok || x1 != 10 {
		t.Errorf("CFF2 glyph 0: xMax %v ok %v, want 10 true", x1, ok)
	}
	if _, _, _, _, ok := fc2.GlyphControlBox(1); ok {
		t.Error("CFF2: a glyph past the last one has a box")
	}

	// seac naming code 0, which the standard encoding leaves empty, fails.
	bad := append(t1num(0, 600), t1HSbw)
	bad = append(bad, t1num(0, 0, 100, 65, 0)...)
	bad = append(bad, t1Escape, t1Seac)
	t1 := t1Of(t, nil, map[string][]byte{".notdef": {t1EndChar}, "A": t1Box(600), "bad": bad}, []string{".notdef", "A", "bad"})
	fc1 := &Face{font: &Font{t1: t1}}
	gidA, gidBad := 1, 2
	if n, _ := t1.glyphName(gidA); n != "A" {
		t.Fatalf("glyph %d is %q, want A", gidA, n)
	}
	if n, _ := t1.glyphName(gidBad); n != "bad" {
		t.Fatalf("glyph %d is %q, want bad", gidBad, n)
	}
	if _, _, _, _, ok := fc1.GlyphControlBox(GlyphIndex(gidA)); !ok {
		t.Error("Type 1: A has no box")
	}
	if _, _, _, _, ok := fc1.GlyphControlBox(GlyphIndex(gidBad)); ok {
		t.Error("Type 1: a glyph whose seac fails has a box")
	}
	// A name is PostScript's to choose, the empty one included: an index past the
	// last glyph must be refused as an index, not looked up as the name "".
	t1.charstrings[""] = t1Box(600)
	if _, _, _, _, ok := fc1.GlyphControlBox(99); ok {
		t.Error("Type 1: a glyph past the last one has a box")
	}
	if _, err := t1.outline(99); err == nil {
		t.Error("Type 1: a glyph past the last one has an outline")
	}
}

// An empty component — a seac half that draws nothing — widens no box: it has
// no points, so it must not add the point it is moved to.
func TestControlBoxUnionOfAnEmptyBox(t *testing.T) {
	b := ctrlBox{1, 2, 3, 4, true}
	b.union(ctrlBox{}, 50, 50)
	if want := (ctrlBox{1, 2, 3, 4, true}); b != want {
		t.Errorf("box %+v after an empty union, want %+v", b, want)
	}
}
