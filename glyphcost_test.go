// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// opentype#81: the work of outlining a glyph is observable, so a caller can
// bound a whole font by summing it. Expected costs are counted by hand from the
// glyphs' structure, never read back from the interpreter.

// cffCostFont wraps a CFF table of the given glyphs in an OTTO font.
func cffCostFont(t *testing.T, o cffOptions) *Font {
	t.Helper()
	o.includePriv, o.charType = true, 2
	tables := cffTables(make([][]byte, len(o.glyphs)), map[rune]uint16{'A': 1})
	tables["CFF "] = buildCFF(o)
	return mustParse(t, assemble(versionOTTO, tables))
}

// cff2CostFont wraps a CFF2 table of the given glyphs in an OTTO font.
func cff2CostFont(t *testing.T, gsubrs, glyphs [][]byte) *Font {
	t.Helper()
	tables := cff2OTTOTables(glyphs, nil, map[rune]uint16{'A': 1}, nil)
	tables["CFF2"] = buildCFF2(glyphs, gsubrs, nil, nil, nil)
	return mustParse(t, assemble(versionOTTO, tables))
}

// idleGlyph is the glyph of #81: it executes fan^levels hstem operators through
// a chain of subroutine levels and draws no point. (The test CFF builder writes
// one-byte INDEX offsets, so a level may not hold more than about a hundred
// calls: the 40,000 come from three levels of 34.)
func idleGlyph(fan, levels int) (gsubrs [][]byte, glyph []byte) {
	return subrTower(fan, levels, (&csb{}).num(0).num(10).op(1).b) // 0 10 hstem
}

// idleCost is the operators of idleGlyph with its endchar: one callgsubr per
// call at each level, then one hstem per execution of the leaf.
func idleCost(fan, levels int) int {
	n, p := 1, 1
	for i := 0; i < levels; i++ {
		p *= fan
		n += p // the calls of this level
	}
	return n + p // the hstems
}

func TestGlyphCostCFF(t *testing.T) {
	empty := (&csb{}).op(14).b
	f := cffCostFont(t, cffOptions{glyphs: [][]byte{empty, cffSquare()}})
	fc := f.NewFace(1000)
	for gid, want := range []int{1, 3} { // endchar; rmoveto rlineto endchar
		got, err := fc.GlyphCost(GlyphIndex(gid))
		if err != nil || got != want {
			t.Errorf("GlyphCost(%d) = %d, %v; want %d", gid, got, err, want)
		}
	}
	if _, err := fc.GlyphCost(2); err == nil || errors.Is(err, ErrOutlineBudget) {
		t.Errorf("an out-of-range glyph: err = %v, want a refusal that is not a budget", err)
	}
}

// The idle glyph of #81 reports its operator count: 39,304 hstem, the 39,304
// callgsubr of the last level, the 1,156 and 34 above it, and its endchar.
func TestGlyphCostIdleGlyph(t *testing.T) {
	const fan, levels = 34, 3
	const want = 39304 + (39304 + 1156 + 34) + 1 // spelled out, idleCost is its own check
	if idleCost(fan, levels) != want {
		t.Fatalf("idleCost = %d, want %d", idleCost(fan, levels), want)
	}
	gs, glyph := idleGlyph(fan, levels)
	segs := func(f *Font) {
		fc := f.NewFace(1000)
		cost, err := fc.GlyphCost(1)
		if err != nil || cost != want {
			t.Errorf("GlyphCost = %d, %v; want %d", cost, err, want)
		}
		got, cost, err := fc.GlyphOutlineCost(1)
		if err != nil || cost != want || len(got) != 0 {
			t.Errorf("GlyphOutlineCost = %d segments, %d, %v; want none, %d", len(got), cost, err, want)
		}
	}
	segs(cffCostFont(t, cffOptions{glyphs: [][]byte{(&csb{}).op(14).b, glyph}, gsubrs: gs}))
	// CFF2 has no endchar: the same glyph is one operator shorter.
	gs2, glyph2 := idleGlyph(fan, levels)
	f2 := cff2CostFont(t, gs2, [][]byte{glyph2[:len(glyph2)-1]})
	cost, err := f2.NewFace(1000).GlyphCost(0)
	if err != nil || cost != want-1 {
		t.Errorf("CFF2 GlyphCost = %d, %v; want %d", cost, err, want-1)
	}
}

// A glyph past the operator budget is refused with the sentinel, and the
// refusal is the one GlyphOutline reports as not ok.
func TestGlyphCostRefusal(t *testing.T) {
	gs, glyph := subrTower(6, maxT2Depth, lineLeaf())
	fc := cffCostFont(t, cffOptions{glyphs: [][]byte{(&csb{}).op(14).b, glyph}, gsubrs: gs}).NewFace(1000)
	if _, ok := fc.GlyphOutline(1); ok {
		t.Fatal("the fixture is not refused")
	}
	if c, err := fc.GlyphCost(1); !errors.Is(err, ErrOutlineBudget) || c != 0 {
		t.Errorf("GlyphCost = %d, %v; want 0 and ErrOutlineBudget", c, err)
	}
	if s, c, err := fc.GlyphOutlineCost(1); !errors.Is(err, ErrOutlineBudget) || s != nil || c != 0 {
		t.Errorf("GlyphOutlineCost = %v, %d, %v", s, c, err)
	}
	if d, c, err := fc.GlyphSVGPathCost(1); !errors.Is(err, ErrOutlineBudget) || d != "" || c != 0 {
		t.Errorf("GlyphSVGPathCost = %q, %d, %v", d, c, err)
	}
	// The message is the one it always had.
	if _, err := fc.GlyphCost(1); err == nil || !errors.Is(err, errT2Budget) {
		t.Errorf("err = %v, want errT2Budget too", err)
	}
	// The composite refusal is the same sentinel.
	tt := compositeBomb(t, 6, 8).NewFace(1000)
	if _, err := tt.GlyphCost(8); !errors.Is(err, ErrOutlineBudget) || !errors.Is(err, errGlyphBudget) {
		t.Errorf("composite: err = %v", err)
	}
	if errors.Is(errTruncated, ErrOutlineBudget) {
		t.Error("another error matches ErrOutlineBudget")
	}
}

// A seac glyph costs its own operators and its two components'.
func TestGlyphCostCFFSeac(t *testing.T) {
	seacCS := (&csb{}).num(100).num(50).num(65).num(193).op(14).b                            // endchar
	base := (&csb{}).num(0).num(0).op(21).num(50).num(0).op(5).num(0).num(50).op(5).op(14).b // 4
	accent := (&csb{}).num(10).num(10).op(21).num(20).num(0).op(5).op(14).b                  // 3
	fc := cffCostFont(t, cffOptions{glyphs: [][]byte{seacCS, base, accent}, charsetSIDs: []int{34, 124}}).NewFace(1000)
	for gid, want := range []int{1 + 4 + 3, 4, 3} {
		if got, err := fc.GlyphCost(GlyphIndex(gid)); err != nil || got != want {
			t.Errorf("GlyphCost(%d) = %d, %v; want %d", gid, got, err, want)
		}
	}
}

// Type 1: hsbw, rmoveto, fan callsubr, and each subroutine's own operators; the
// seac components share the glyph's count.
func TestGlyphCostType1(t *testing.T) {
	// subr0 = rlineto return (2); subr1 = 2 x (callsubr + subr0) + return (7);
	// subr2 = 2 x (callsubr + subr1) + return (17); glyph = hsbw rmoveto
	// 2 x (callsubr + subr2) endchar (2 + 36 + 1).
	subrs, glyph := t1Tower(2, 3, t1Lines(1))
	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": glyph}, order: []string{".notdef", "A"}, subrs: subrs,
	}))
	if err != nil {
		t.Fatal(err)
	}
	fc := f.NewFace(1000)
	for gid, want := range []int{1, 39} {
		if got, err := fc.GlyphCost(GlyphIndex(gid)); err != nil || got != want {
			t.Errorf("GlyphCost(%d) = %d, %v; want %d", gid, got, err, want)
		}
	}
	if _, err := fc.GlyphCost(9); err == nil || errors.Is(err, ErrOutlineBudget) {
		t.Errorf("out of range: err = %v", err)
	}

	part := append(t1num(0, 500), t1HSbw) // hsbw, 3 dotsections, endchar: 5
	part = append(part, t1Escape, t1DotSection, t1Escape, t1DotSection, t1Escape, t1DotSection, t1EndChar)
	composed := append(t1num(0, 600), t1HSbw)
	composed = append(composed, t1num(0, 0, 100, 65, 194)...)
	composed = append(composed, t1Escape, t1Seac) // hsbw, seac: 2, then both parts
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": part, "acute": part, "Aacute": composed},
		order:  []string{".notdef", "A", "acute", "Aacute"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.NewFace(1000).GlyphCost(3); err != nil || got != 2+5+5 {
		t.Errorf("seac GlyphCost = %d, %v; want 12", got, err)
	}
}

// glyf: the points a glyph expands to, a composite's references counted every
// time they are made, an empty glyph free.
func TestGlyphCostGlyf(t *testing.T) {
	tri := simpleGlyphBytes([][]point{{{x: 0, y: 0, on: true}, {x: 10, y: 0, on: true}, {x: 5, y: 9, on: true}}})
	two := simpleGlyphBytes([][]point{{{x: 0, y: 0, on: true}, {x: 5, y: 5}}, {{x: 1, y: 1, on: true}}}) // 2 + 1
	comp := compositeGlyphBytes([]component{
		{glyphIndex: 1, argsAreXY: true, more: true},
		{glyphIndex: 2, argsAreXY: true, more: true},
		{glyphIndex: 1, argsAreXY: true},
	})
	f := longLocaFont(t, [][]byte{nil, tri, two, comp})
	fc := f.NewFace(1000)
	for gid, want := range []int{0, 3, 3, 9} {
		got, err := fc.GlyphCost(GlyphIndex(gid))
		if err != nil || got != want {
			t.Errorf("GlyphCost(%d) = %d, %v; want %d", gid, got, err, want)
		}
	}
	// Hinting changes the outline, not the cost; the cost is not computed for
	// the caller that only draws.
	fc.SetHinting(true)
	if got, err := fc.GlyphCost(3); err != nil || got != 9 {
		t.Errorf("hinted GlyphCost(3) = %d, %v", got, err)
	}
	if _, err := fc.GlyphCost(4); err == nil || errors.Is(err, ErrOutlineBudget) {
		t.Errorf("out of range: err = %v", err)
	}
	// The expansion of the bomb of #75 at its edge: 65,535 points is allowed.
	big := compositeBomb(t, 4, 8).NewFace(1000) // 4^8 = 65,536 points
	if _, err := big.GlyphCost(8); !errors.Is(err, ErrOutlineBudget) {
		t.Errorf("4^8 points: err = %v", err)
	}
	if c, err := compositeBomb(t, 4, 7).NewFace(1000).GlyphCost(7); err != nil || c != 4*4*4*4*4*4*4 {
		t.Errorf("4^7 points: %d, %v", c, err)
	}
}

// The three methods are one interpretation seen three ways.
func TestGlyphCostAgreesWithOutline(t *testing.T) {
	fc := cffCostFont(t, cffOptions{glyphs: [][]byte{(&csb{}).op(14).b, cffSquare()}}).NewFace(1000)
	for gid := GlyphIndex(0); gid < 2; gid++ {
		segs, ok := fc.GlyphOutline(gid)
		d, dok := fc.GlyphSVGPath(gid)
		cost, err := fc.GlyphCost(gid)
		s2, c2, err2 := fc.GlyphOutlineCost(gid)
		d2, c3, err3 := fc.GlyphSVGPathCost(gid)
		if !ok || !dok || err != nil || err2 != nil || err3 != nil {
			t.Fatalf("gid %d: %v %v %v %v %v", gid, ok, dok, err, err2, err3)
		}
		if !reflect.DeepEqual(segs, s2) || d != d2 || cost != c2 || cost != c3 {
			t.Errorf("gid %d: the outline or the cost differs between the calls", gid)
		}
	}
}

// A synthetic font's total is the sum of its glyphs' costs: nothing is stored,
// so asking in another order, or twice, gives the same numbers.
func TestGlyphCostSumOverFont(t *testing.T) {
	gs, idle := idleGlyph(10, 2)
	f := cffCostFont(t, cffOptions{glyphs: [][]byte{(&csb{}).op(14).b, cffSquare(), idle}, gsubrs: gs})
	fc := f.NewFace(1000)
	total := func(order []GlyphIndex) (sum int) {
		for _, g := range order {
			c, err := fc.GlyphCost(g)
			if err != nil {
				t.Fatal(err)
			}
			sum += c
		}
		return sum
	}
	const want = 1 + 3 + (10 + 100 + 100 + 1)
	if a, b := total([]GlyphIndex{0, 1, 2}), total([]GlyphIndex{2, 1, 0, 2}); a != want || b != want+211 {
		t.Errorf("totals %d and %d; want %d and %d", a, b, want, want+211)
	}
}

// FuzzGlyphCost: on any font that parses, a glyph is refused by GlyphCost
// exactly when GlyphOutline says not ok, a cost is never negative, and the
// three methods agree.
func FuzzGlyphCost(f *testing.F) {
	gs, idle := idleGlyph(4, 2)
	o := cffOptions{glyphs: [][]byte{(&csb{}).op(14).b, cffSquare(), idle}, gsubrs: gs, includePriv: true, charType: 2}
	tables := cffTables(make([][]byte, 3), map[rune]uint16{'A': 1})
	tables["CFF "] = buildCFF(o)
	f.Add(assemble(versionOTTO, tables))
	f.Add(compositeBombBytes(3, 8))
	f.Add(compositeBombBytes(1, 2))
	f.Fuzz(func(t *testing.T, b []byte) {
		font, err := Parse(b)
		if err != nil {
			return
		}
		fc := font.NewFace(100)
		for g := 0; g < min(font.NumGlyphs(), 8); g++ {
			gid := GlyphIndex(g)
			segs, ok := fc.GlyphOutline(gid)
			cost, err := fc.GlyphCost(gid)
			s2, c2, err2 := fc.GlyphOutlineCost(gid)
			if ok != (err == nil) || ok != (err2 == nil) || cost != c2 || cost < 0 || len(segs) != len(s2) {
				t.Fatalf("glyph %d: ok=%v err=%v err2=%v cost=%d/%d", g, ok, err, err2, cost, c2)
			}
		}
	})
}

// The scale the doc comment states: STIX Two Math's busiest glyph and total.
func TestGlyphCostSTIXTwoMath(t *testing.T) {
	b, err := os.ReadFile("testdata/STIXTwoMath-Regular.otf")
	if err != nil {
		t.Skip(err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	fc := f.NewFace(1000)
	var total, busiest int
	var busiestGid GlyphIndex
	for g := 0; g < f.NumGlyphs(); g++ {
		c, err := fc.GlyphCost(GlyphIndex(g))
		if err != nil {
			t.Fatalf("glyph %d: %v", g, err)
		}
		total += c
		if c > busiest {
			busiest, busiestGid = c, GlyphIndex(g)
		}
	}
	t.Logf("STIX Two Math: %d glyphs, busiest glyph %d = %d operators, total %d", f.NumGlyphs(), busiestGid, busiest, total)
	if busiest != 1267 || total != stixTwoMathTotalCost {
		t.Errorf("busiest %d (want 1267), total %d (want %d)", busiest, total, stixTwoMathTotalCost)
	}
}

const stixTwoMathTotalCost = 166885
