package opentype

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// opentype#75: a tiny font must not be able to make one glyph expand to
// exponentially many points. Every bound below is spelled through the constant
// it tests, never through its value.

// bombAllocLimit is how much one refused glyph may allocate: the refusal has to
// come before the expansion, so what is left is the font's own tables.
const bombAllocLimit = 8 << 20

func allocated(fn func()) uint64 {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func ipow(k, n int) int {
	r := 1
	for i := 0; i < n; i++ {
		r *= k
	}
	return r
}

// compositeBomb builds a TrueType font whose glyph 0 is one point and whose
// glyphs 1..levels are composites of k references to the glyph below.
func compositeBomb(t testing.TB, k, levels int) *Font {
	return longLocaFont(t, bombGlyphs(k, levels))
}

func bombGlyphs(k, levels int) [][]byte {
	glyphs := [][]byte{simpleGlyphBytes([][]point{{{x: 1, y: 1, on: true}}})}
	for g := 1; g <= levels; g++ {
		comps := make([]component, k)
		for j := range comps {
			comps[j] = component{glyphIndex: uint16(g - 1), argsAreXY: true, more: j < k-1}
		}
		glyphs = append(glyphs, compositeGlyphBytes(comps))
	}
	return glyphs
}

func compositeBombBytes(k, levels int) []byte { return longLocaBytes(bombGlyphs(k, levels)) }

// longLocaBytes assembles a TrueType font with a long loca, for glyphs past
// 128 KB in all.
func longLocaBytes(glyphs [][]byte) []byte {
	n := len(glyphs)
	loca, glyf := glyfAndLoca(glyphs, true)
	adv := make([]int, n)
	lsb := make([]int, n)
	return assemble(versionTrueType, map[string][]byte{
		"head": headTable(1000, 1),
		"maxp": maxpTable(n),
		"hhea": hheaTable(800, -200, 100, n),
		"hmtx": hmtxTable(adv, lsb, n),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1})}),
		"loca": loca,
		"glyf": glyf,
	})
}

func longLocaFont(t testing.TB, glyphs [][]byte) *Font {
	f, err := Parse(longLocaBytes(glyphs))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCompositeBombRefused(t *testing.T) {
	const levels = 8 // maxCompositeDepth: the deepest chain the loader walks
	for k := 1; k <= 12; k++ {
		f := compositeBomb(t, k, levels)
		var err error
		var n int
		delta := allocated(func() {
			var cs []contour
			cs, err = f.glyphContours(GlyphIndex(levels))
			for _, c := range cs {
				n += len(c)
			}
		})
		if delta > bombAllocLimit {
			t.Errorf("k=%d allocated %d bytes", k, delta)
		}
		points := ipow(k, levels)
		switch {
		case points > maxGlyphPoints:
			if !errors.Is(err, errGlyphBudget) {
				t.Errorf("k=%d (%d points): err = %v, want errGlyphBudget", k, points, err)
			}
		case err != nil || n != points:
			t.Errorf("k=%d: %d points, err %v; want %d points", k, n, err, points)
		}
	}
}

// The boundary itself: exactly maxGlyphPoints references to a one-point glyph
// load, one more is refused, and the error names the glyph.
func TestCompositeBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		comps := make([]component, maxGlyphPoints+extra)
		for j := range comps {
			comps[j] = component{glyphIndex: 0, argsAreXY: true, more: j < len(comps)-1}
		}
		f := longLocaFont(t, [][]byte{
			simpleGlyphBytes([][]point{{{x: 1, y: 1, on: true}}}),
			compositeGlyphBytes(comps),
		})
		_, err := f.glyphContours(1)
		if extra == 0 && err != nil {
			t.Fatalf("exactly %d points refused: %v", maxGlyphPoints, err)
		}
		if extra == 1 && (!errors.Is(err, errGlyphBudget) || err.Error() == errGlyphBudget.Error()) {
			t.Fatalf("one past the bound: err = %v", err)
		}
	}
}

// A composite that names another glyph's contours many times over (empty
// contours cost nothing in points) is bounded as well: each contour counts as
// one point.
func TestCompositeContourBomb(t *testing.T) {
	// One glyph of n empty contours: endPts all zero, so contour 0 holds the
	// single point and the others are empty.
	const n = 1000
	w := &bw{}
	w.i16(n)
	for i := 0; i < 4; i++ {
		w.i16(0)
	}
	for i := 0; i < n; i++ {
		w.u16(0)
	}
	w.u16(0) // instructionLength
	w.u8(flagOnCurve)
	w.b = append(w.b, 0, 0) // x, y deltas of a one-point glyph: both zero ("same")
	comps := make([]component, maxGlyphPoints/n+1)
	for j := range comps {
		comps[j] = component{glyphIndex: 0, argsAreXY: true, more: j < len(comps)-1}
	}
	f := longLocaFont(t, [][]byte{w.bytes(), compositeGlyphBytes(comps)})
	if _, err := f.glyphContours(1); !errors.Is(err, errGlyphBudget) {
		t.Fatalf("err = %v, want errGlyphBudget", err)
	}
}

// Through the public API, and the CPU cost of a refusal is nothing.
func TestCompositeBombThroughFace(t *testing.T) {
	f := compositeBomb(t, 1000, 8) // 1000^8 contours from a 50 KB font
	start := time.Now()
	delta := allocated(func() {
		if _, ok := f.NewFace(16).GlyphOutline(8); ok {
			t.Error("GlyphOutline(8) of a 1000^8 composite succeeded")
		}
	})
	if delta > bombAllocLimit || time.Since(start) > 2*time.Second {
		t.Errorf("refusal took %s and %d bytes", time.Since(start), delta)
	}
}

// Faults the counting pass does not judge are still reported by the expansion
// with their own errors, wherever they sit among the components.
func TestCompositeFaultsAreNotMistakenForBudget(t *testing.T) {
	pt := simpleGlyphBytes([][]point{{{x: 1, y: 1, on: true}}})
	ref := func(g uint16, more bool) component { return component{glyphIndex: g, argsAreXY: true, more: more} }
	trunc := compositeGlyphBytes([]component{ref(0, true)}) // MORE set, record missing
	cases := map[string][][]byte{
		"out of range":     {pt, compositeGlyphBytes([]component{ref(0, true), ref(99, false)})},
		"self cycle":       {pt, compositeGlyphBytes([]component{ref(0, true), ref(1, false)})},
		"empty glyph":      {pt, nil, compositeGlyphBytes([]component{ref(1, true), ref(0, false)})},
		"truncated sub":    {pt, trunc, compositeGlyphBytes([]component{ref(1, false)})},
		"truncated simple": {pt, []byte{0, 1, 0, 0, 0, 0}, compositeGlyphBytes([]component{ref(1, false)})},
	}
	for name, glyphs := range cases {
		f := longLocaFont(t, glyphs)
		_, err := f.glyphContours(GlyphIndex(len(glyphs) - 1))
		if name == "empty glyph" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || errors.Is(err, errGlyphBudget) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// endPtsOfContours that decreases used to index past the last contour's points
// (a panic found by FuzzGlyphContours).
func TestSimpleGlyphDecreasingEndPts(t *testing.T) {
	w := &bw{}
	w.i16(2)
	for i := 0; i < 4; i++ {
		w.i16(0)
	}
	w.u16(1)
	w.u16(0)
	w.u16(0)                                    // instructionLength
	w.b = append(w.b, flagOnCurve, flagOnCurve) // flags of the 1 + 1 points
	w.b = append(w.b, 0, 0, 0, 0)
	f := longLocaFont(t, [][]byte{w.bytes()})
	if _, err := f.glyphContours(0); err == nil {
		t.Fatal("decreasing endPtsOfContours loaded")
	}
}

// A chain deeper than maxCompositeDepth is reported as before.
func TestCompositeTooDeepStillReported(t *testing.T) {
	f := compositeBomb(t, 1, maxCompositeDepth+2)
	_, err := f.glyphContours(GlyphIndex(maxCompositeDepth + 2))
	if err == nil || errors.Is(err, errGlyphBudget) {
		t.Fatalf("err = %v, want the nesting error", err)
	}
}

// --- Type 2 charstrings --------------------------------------------------

// subrTower returns global subrs and a glyph charstring such that the glyph
// calls the top subr fan times, each subr calls the one below fan times, and
// the bottom one runs leaf: fan^levels executions of leaf in all.
func subrTower(fan, levels int, leaf []byte) (gsubrs [][]byte, glyph []byte) {
	calls := func(idx int) []byte {
		c := &csb{}
		for j := 0; j < fan; j++ {
			c.num(idx - 107).op(29) // callgsubr, bias 107 for a small INDEX
		}
		return c.b
	}
	gsubrs = append(gsubrs, leaf)
	for i := 1; i < levels; i++ {
		gsubrs = append(gsubrs, calls(i-1))
	}
	return gsubrs, append(calls(levels-1), 14) // endchar
}

func lineLeaf() []byte { return (&csb{}).num(10).num(10).op(5).b } // 10 10 rlineto

// curveLeaf draws maxT2Stack/6 curves in one operator.
func curveLeaf() []byte {
	c := &csb{}
	for i := 0; i < maxT2Stack; i++ {
		c.num(1)
	}
	return c.op(8).b
}

func cffOf(t testing.TB, gsubrs [][]byte, glyph []byte) *cffTable {
	c, err := parseCFF(buildCFF(cffOptions{
		glyphs: [][]byte{(&csb{}).op(14).b, glyph}, gsubrs: gsubrs, includePriv: true, charType: 2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// cff2Of builds the CFF2 table; CFF2 has no endchar, so the glyph's trailing
// one is dropped.
func cff2Of(t testing.TB, gsubrs [][]byte, glyph []byte) *cff2Table {
	glyph = glyph[:len(glyph)-1]
	c, err := parseCFF2(buildCFF2([][]byte{glyph}, gsubrs, nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCharstringBombRefused(t *testing.T) {
	const levels = maxT2Depth // the deepest chain the interpreter walks
	for k := 1; k <= 6; k++ {
		gs, glyph := subrTower(k, levels, lineLeaf())
		lines := ipow(k, levels)
		for _, kind := range []string{"cff", "cff2"} {
			var n int
			var err error
			delta := allocated(func() {
				var cs []contour
				if kind == "cff" {
					cs, err = cffOf(t, gs, glyph).outline(1)
				} else {
					cs, err = cff2Of(t, gs, glyph).outline(0, nil)
				}
				for _, c := range cs {
					n += len(c)
				}
			})
			if delta > bombAllocLimit {
				t.Errorf("%s k=%d allocated %d bytes", kind, k, delta)
			}
			switch {
			case lines > maxT2Points:
				if !errors.Is(err, errT2Budget) {
					t.Errorf("%s k=%d (%d lines): err = %v, want errT2Budget", kind, k, lines, err)
				}
			case 2*lines <= min(maxT2Points, maxT2Ops):
				if err != nil || n != lines {
					t.Errorf("%s k=%d: %d points, err %v; want %d", kind, k, n, err, lines)
				}
			}
		}
	}
}

// The point budget on its own: operators that each draw many points stay far
// under the operator budget.
func TestCharstringPointBudget(t *testing.T) {
	pointsPerOp := maxT2Stack / 6 * cffCurveSteps
	levels := 1
	for ipow(8, levels)*pointsPerOp <= maxT2Points {
		levels++
	}
	if ipow(8, levels+1) > maxT2Ops/2 {
		t.Fatalf("the point budget is not reachable under the operator budget: %d levels", levels)
	}
	gs, glyph := subrTower(8, levels, curveLeaf())
	for _, err := range []error{
		func() error { _, err := cffOf(t, gs, glyph).outline(1); return err }(),
		func() error { _, err := cff2Of(t, gs, glyph).outline(0, nil); return err }(),
	} {
		if !errors.Is(err, errT2Budget) {
			t.Errorf("err = %v, want errT2Budget", err)
		}
	}
}

// Many subroutine calls under the budget are fine: it is the count that is
// bounded, not the depth (which maxT2Depth already bounded).
func TestCharstringBudgetAllowsLegitimateReuse(t *testing.T) {
	gs, glyph := subrTower(2, 5, lineLeaf())
	cs, err := cffOf(t, gs, glyph).outline(1)
	if err != nil || len(cs) != 1 || len(cs[0]) != ipow(2, 5) {
		t.Fatalf("outline: %v %d", err, len(cs))
	}
}

// --- fuzz targets ----------------------------------------------------------

func FuzzGlyphContours(f *testing.F) {
	for _, k := range []int{1, 2, 3, 5} {
		f.Add(compositeBombBytes(k, 8))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		font, err := Parse(b)
		if err != nil {
			return
		}
		start := time.Now()
		for g := 0; g < font.NumGlyphs() && g < 64; g++ {
			_, _ = font.glyphContours(GlyphIndex(g))
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("%d bytes took %s", len(b), d)
		}
	})
}

// FuzzCharstring runs the fuzzed bytes as a glyph that may call itself as
// global subr 0, in a CFF and in a CFF2 table.
func FuzzCharstring(f *testing.F) {
	for _, k := range []int{2, 4} {
		gs, glyph := subrTower(k, 3, lineLeaf())
		f.Add(glyph, gs[1])
	}
	f.Add(curveLeaf(), lineLeaf())
	f.Add((&csb{}).num(-107).op(29).b, (&csb{}).num(-107).op(29).b) // self-call
	f.Fuzz(func(t *testing.T, glyph, subr []byte) {
		if len(glyph) > 100 || len(subr) > 100 {
			return
		}
		start := time.Now()
		if c, err := parseCFF(buildCFF(cffOptions{
			glyphs: [][]byte{(&csb{}).op(14).b, glyph}, gsubrs: [][]byte{subr}, includePriv: true, charType: 2,
		})); err == nil {
			_, _ = c.outline(1)
		}
		if c, err := parseCFF2(buildCFF2([][]byte{glyph}, [][]byte{subr}, nil, nil, nil)); err == nil {
			_, _ = c.outline(0, nil)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("took %s", d)
		}
	})
}
