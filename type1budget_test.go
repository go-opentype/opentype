package opentype

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// opentype#75, Type 1: maxT1Depth bounds the nesting of subroutines, not the
// number of calls, so a subroutine calling the one below it k times used to
// execute k^maxT1Depth operators.

// t1Tower returns the subroutines and glyph such that the glyph calls the top
// subroutine fan times, each subroutine calls the one below fan times, and
// subroutine 0 runs leaf: fan^levels executions of leaf.
func t1Tower(fan, levels int, leaf []byte) (subrs [][]byte, glyph []byte) {
	calls := func(idx int) []byte {
		var c []byte
		for j := 0; j < fan; j++ {
			c = append(c, t1num(idx)...)
			c = append(c, t1CallSubr)
		}
		return c
	}
	subrs = append(subrs, append(append([]byte{}, leaf...), t1Return))
	for i := 1; i < levels; i++ {
		subrs = append(subrs, append(calls(i-1), t1Return))
	}
	glyph = append(t1num(0, 500), t1HSbw)
	glyph = append(glyph, t1num(0, 0)...)
	glyph = append(glyph, t1RMoveTo)
	glyph = append(glyph, calls(levels-1)...)
	return subrs, append(glyph, t1EndChar)
}

func t1Lines(n int) []byte {
	var c []byte
	for i := 0; i < n; i++ {
		c = append(c, t1num(10, 10)...)
		c = append(c, t1RLineTo)
	}
	return c
}

func t1Curves(n int) []byte {
	var c []byte
	for i := 0; i < n; i++ {
		c = append(c, t1num(1, 1, 1, 1, 1, 1)...)
		c = append(c, t1RRCurveTo)
	}
	return c
}

func t1Of(t testing.TB, subrs [][]byte, glyphs map[string][]byte, order []string) *type1Font {
	f, err := ParseType1(buildType1(t1Options{glyphs: glyphs, order: order, subrs: subrs}))
	if err != nil {
		t.Fatal(err)
	}
	return f.t1
}

func t1TowerFont(t testing.TB, fan, levels int, leaf []byte) *type1Font {
	subrs, glyph := t1Tower(fan, levels, leaf)
	return t1Of(t, subrs, map[string][]byte{".notdef": {t1EndChar}, "A": glyph}, []string{".notdef", "A"})
}

func TestType1BombRefused(t *testing.T) {
	const levels = maxT1Depth - 1 // the deepest chain the interpreter walks
	for _, k := range []int{1, 2, 3, 4, 5, 6, 8, 16, 40} {
		f := t1TowerFont(t, k, levels, t1Lines(1))
		var n int
		var err error
		delta := allocated(func() {
			var cs []contour
			cs, err = f.outlineByName("A", 0)
			for _, c := range cs {
				n += len(c)
			}
		})
		if delta > bombAllocLimit {
			t.Errorf("k=%d allocated %d bytes", k, delta)
		}
		// k^levels overflows a 32-bit int from k=4, so only k=1 (one line) is
		// small enough to draw; every larger k is far past both budgets.
		if k == 1 {
			if err != nil || n != 2 {
				t.Errorf("k=1: %d points, err %v; want 2", n, err)
			}
		} else if !errors.Is(err, errT2Budget) || !strings.Contains(err.Error(), "type1") {
			t.Errorf("k=%d: err = %v, want errT2Budget from type1", k, err)
		}
	}
}

// The point budget on its own: ten curves per subroutine call draw 80 points
// for about a dozen operators, so the points run out first.
func TestType1PointBudget(t *testing.T) {
	const fan, levels = 2, 12
	if ipow(fan, levels)*10*cffCurveSteps <= maxT2Points || ipow(fan, levels)*13 >= maxT2Ops {
		t.Fatal("the fixture no longer reaches the point budget before the operator budget")
	}
	f := t1TowerFont(t, fan, levels, t1Curves(10))
	_, err := f.outlineByName("A", 0)
	if !errors.Is(err, errT2Budget) || !strings.Contains(err.Error(), "points") {
		t.Errorf("err = %v, want errT2Budget on points", err)
	}
}

// The point budget at its edge: the move is one point, every line one more, and
// the count is checked before each operator, endchar included.
func TestType1PointBudgetEdge(t *testing.T) {
	glyph := func(lines int) []byte {
		cs := append(t1num(0, 500), t1HSbw)
		cs = append(cs, t1num(0, 0)...)
		cs = append(cs, t1RMoveTo)
		cs = append(cs, t1Lines(lines)...)
		return append(cs, t1EndChar)
	}
	f := t1Of(t, nil, map[string][]byte{
		".notdef": {t1EndChar}, "A": glyph(maxT2Points - 1), "B": glyph(maxT2Points),
	}, []string{".notdef", "A", "B"})
	if _, err := f.outlineByName("A", 0); err != nil {
		t.Errorf("exactly maxT2Points points: %v", err)
	}
	if _, err := f.outlineByName("B", 0); !errors.Is(err, errT2Budget) || !strings.Contains(err.Error(), "points") {
		t.Errorf("one point more: err = %v", err)
	}
}

// The operator budget on its own: operators that draw nothing.
func TestType1OperatorBudget(t *testing.T) {
	flat := func(n int) []byte {
		cs := append(t1num(0, 500), t1HSbw)
		for i := 0; i < n; i++ {
			cs = append(cs, t1Escape, t1DotSection)
		}
		return append(cs, t1EndChar)
	}
	f := t1Of(t, nil, map[string][]byte{
		".notdef": {t1EndChar}, "A": flat(maxT2Ops - 1), "B": flat(maxT2Ops - 2),
	}, []string{".notdef", "A", "B"})
	// hsbw + n dotsections + endchar is n+2 operators.
	if _, err := f.outlineByName("A", 0); !errors.Is(err, errT2Budget) || !strings.Contains(err.Error(), "operators") {
		t.Errorf("past the operator budget: err = %v", err)
	}
	if _, err := f.outlineByName("B", 0); err != nil {
		t.Errorf("exactly at the operator budget: %v", err)
	}
}

// The accent components of a seac glyph spend the glyph's budget, not a budget
// of their own each.
func TestType1SeacSharesTheBudget(t *testing.T) {
	flat := func(n int, tail ...byte) []byte {
		cs := append(t1num(0, 500), t1HSbw)
		for i := 0; i < n; i++ {
			cs = append(cs, t1Escape, t1DotSection)
		}
		return append(cs, tail...)
	}
	part := flat(maxT2Ops*3/5, t1EndChar)
	composed := append(t1num(0, 600), t1HSbw)
	composed = append(composed, t1num(0, 0, 100, 65, 194)...)
	composed = append(composed, t1Escape, t1Seac)
	f := t1Of(t, nil, map[string][]byte{
		".notdef": {t1EndChar}, "A": part, "acute": part, "Aacute": composed,
	}, []string{".notdef", "A", "acute", "Aacute"})
	for _, name := range []string{"A", "acute"} {
		if _, err := f.outlineByName(name, 0); err != nil {
			t.Fatalf("%s alone: %v", name, err)
		}
	}
	if _, err := f.outlineByName("Aacute", 0); !errors.Is(err, errT2Budget) {
		t.Errorf("both components under one budget: err = %v, want errT2Budget", err)
	}
}

// A legitimate reuse of a subroutine is untouched.
func TestType1BudgetAllowsLegitimateReuse(t *testing.T) {
	cs, err := t1TowerFont(t, 2, 5, t1Lines(1)).outlineByName("A", 0)
	if err != nil || len(cs) != 1 || len(cs[0]) != ipow(2, 5)+1 {
		t.Fatalf("outline: %v %d", err, len(cs))
	}
}

// FuzzType1Charstring runs the fuzzed bytes as a glyph that may call itself as
// subroutine 0.
func FuzzType1Charstring(f *testing.F) {
	for _, k := range []int{2, 4} {
		subrs, glyph := t1Tower(k, 3, t1Lines(1))
		f.Add(glyph, subrs[1])
	}
	f.Add(t1Curves(3), t1Lines(2))
	f.Add(append(t1num(0), t1CallSubr), append(t1num(0), t1CallSubr)) // self-call
	f.Fuzz(func(t *testing.T, glyph, subr []byte) {
		if len(glyph) > 100 || len(subr) > 100 {
			return
		}
		start := time.Now()
		if p, err := ParseType1(buildType1(t1Options{
			glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": glyph},
			order:  []string{".notdef", "A"}, subrs: [][]byte{subr},
		})); err == nil {
			_, _ = p.t1.outlineByName("A", 0)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("took %s", d)
		}
	})
}
