// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"math/rand"
	"testing"
)

// sameAsOracle checks every one of the 65536 glyphs: glyphMap m must answer
// exactly what the map built by the e4de2be decoder answers, and class(g) must be
// the map's value, 0 when absent.
func sameAsOracle(t *testing.T, what string, m *glyphMap, want map[GlyphIndex]int) {
	t.Helper()
	for x := 0; x <= 0xffff; x++ {
		g := GlyphIndex(x)
		wv, wok := want[g]
		gv, gok := m.get(g)
		if gv != wv || gok != wok || m.has(g) != wok || m.class(g) != wv {
			t.Fatalf("%s: glyph %d: got (%d,%v), the map holds (%d,%v)", what, x, gv, gok, wv, wok)
		}
	}
}

// randRanges returns n random RangeRecords. Half the time they are sorted and
// disjoint (the spec), otherwise they overlap, repeat, run backwards and sit out
// of order, where "last write wins" decides the answer.
func randRanges(rng *rand.Rand, n int, sorted bool) [][3]int {
	rs := make([][3]int, n)
	pos := 0
	for i := range rs {
		if sorted {
			s := pos + rng.Intn(40)
			e := s + rng.Intn(60)
			if e > 0xffff {
				e = 0xffff
			}
			if s > e {
				s = e
			}
			rs[i] = [3]int{s, e, rng.Intn(70000) & 0xffff}
			pos = e + 1
			if pos > 0xffff {
				pos = 0xffff
				rs[i][0] = 0xffff
			}
			continue
		}
		s := rng.Intn(300)
		e := s + rng.Intn(200) - 20 // some end before their start
		if rng.Intn(9) == 0 {
			s, e = 0, 0xffff
		}
		if e < 0 {
			e = 0
		}
		rs[i] = [3]int{s, e, rng.Intn(1000)}
	}
	// the sorted generator can still clash at the very top of the glyph space
	return rs
}

func TestGlyphMapMatchesTheMapItReplaced(t *testing.T) {
	rng := rand.New(rand.NewSource(75))
	for iter := 0; iter < 300; iter++ {
		n := rng.Intn(40)
		sorted := iter%2 == 0
		var cov, cd []byte
		switch iter % 4 {
		case 0, 1:
			cov = buildCoverage2(randRanges(rng, n, sorted)...)
			cd = buildClassDef2(randRanges(rng, n, sorted)...)
		case 2:
			// format 1 coverage: sorted, or with duplicates and out of order
			gs := make([]GlyphIndex, n)
			g := 0
			for i := range gs {
				if iter%8 == 2 {
					g += 1 + rng.Intn(50)
				} else {
					g = rng.Intn(100)
				}
				gs[i] = GlyphIndex(g)
			}
			cov = buildCoverage1(gs...)
			cd = buildClassDef1(rng.Intn(100), classesOf(rng, n)...)
		case 3:
			// classdef format 1 whose array runs past glyph 65535 and wraps
			cov = buildCoverage1(GlyphIndex(rng.Intn(70)))
			cd = buildClassDef1(0xffff-rng.Intn(10), classesOf(rng, 1+rng.Intn(30))...)
		}
		wc, err := oracleCoverage(cov)
		if err != nil {
			t.Fatal(err)
		}
		gc, err := parseCoverage(cov)
		if err != nil {
			t.Fatal(err)
		}
		sameAsOracle(t, "coverage", gc, wc)
		wd, err := oracleClassDef(cd)
		if err != nil {
			t.Fatal(err)
		}
		gd, err := parseClassDef(cd)
		if err != nil {
			t.Fatal(err)
		}
		sameAsOracle(t, "classdef", gd, wd)
	}
}

func classesOf(rng *rand.Rand, n int) []uint16 {
	cs := make([]uint16, n)
	for i := range cs {
		cs[i] = uint16(rng.Intn(5))
	}
	return cs
}

// TestGlyphMapRefusesWhatTheMapRefused cuts every table at every length: the
// decoder errors exactly where the e4de2be one did, and a table that is not cut
// answers as before.
func TestGlyphMapRefusesWhatTheMapRefused(t *testing.T) {
	tables := [][]byte{
		buildCoverage1(3, 1, 2), buildCoverage2([3]int{4, 9, 0}, [3]int{1, 2, 6}),
		buildClassDef1(5, 1, 2, 3), buildClassDef2([3]int{4, 9, 1}, [3]int{1, 2, 6}),
		{0, 9, 0, 0}, {0, 1}, {},
	}
	for _, b := range tables {
		for cut := 0; cut <= len(b); cut++ {
			c := b[:cut]
			_, werr := oracleCoverage(c)
			_, gerr := parseCoverage(c)
			if (werr == nil) != (gerr == nil) {
				t.Errorf("coverage % x: oracle err %v, got %v", c, werr, gerr)
			}
			_, werr = oracleClassDef(c)
			_, gerr = parseClassDef(c)
			if (werr == nil) != (gerr == nil) {
				t.Errorf("classdef % x: oracle err %v, got %v", c, werr, gerr)
			}
		}
	}
}

func TestGlyphMapNilAndLiteral(t *testing.T) {
	var m *glyphMap
	if _, ok := m.get(5); ok || m.has(5) || m.class(5) != 0 {
		t.Error("a nil glyphMap must be empty")
	}
	lit := map[GlyphIndex]int{0: 4, 7: 0, 65535: 9}
	sameAsOracle(t, "literal", glyphMapFromMap(lit), lit)

	cv := covValues[string]{cov: glyphMapFromMap(map[GlyphIndex]int{3: 1, 4: 5}), vals: []string{"a", "b"}}
	if v, ok := cv.get(3); !ok || v != "b" {
		t.Errorf("get(3) = %q,%v", v, ok)
	}
	if _, ok := cv.get(4); ok { // index 5 is past the values
		t.Error("an index past the values must have no value")
	}
	if cv.val(9) != "" {
		t.Error("an uncovered glyph has the zero value")
	}
}

// TestGlyphMapAllocatesNothingWhenSorted pins the claim of the type: a table in
// spec order is searched in the font's own bytes.
func TestGlyphMapAllocatesNothingWhenSorted(t *testing.T) {
	b := buildCoverage2([3]int{0, 0xffff, 0})
	var gm *glyphMap
	if n := testing.AllocsPerRun(20, func() { gm, _ = parseCoverage(b) }); n > 1 {
		t.Errorf("parseCoverage allocated %v times for a sorted table, want the one header", n)
	}
	if n := testing.AllocsPerRun(20, func() { gm.get(77) }); n != 0 {
		t.Errorf("get allocated %v times", n)
	}
}

// TestMatchContextChecksTheAnchor: the first coverage must cover the anchor
// glyph even when the rest of the sequence would match.
func TestMatchContextChecksTheAnchor(t *testing.T) {
	covs := []*glyphMap{
		glyphMapFromMap(map[GlyphIndex]int{5: 0}),
		glyphMapFromMap(map[GlyphIndex]int{6: 0}),
	}
	if _, _, ok := (skipper{}).matchContext([]GlyphIndex{9, 6}, 0, covs); ok {
		t.Error("an uncovered anchor matched")
	}
	if pos, _, ok := (skipper{}).matchContext([]GlyphIndex{5, 6}, 0, covs); !ok || len(pos) != 2 {
		t.Errorf("a covered sequence did not match: %v %v", pos, ok)
	}
}

// TestSortedTablesAreSearchedInPlace: a table in spec order keeps the font's
// bytes (the fast path); one out of order is normalised. Both answer alike, so
// only the kind tells the paths apart.
func TestSortedTablesAreSearchedInPlace(t *testing.T) {
	cases := []struct {
		name  string
		parse func([]byte) (*glyphMap, error)
		b     []byte
		want  gmKind
	}{
		{"coverage1 sorted", parseCoverage, buildCoverage1(1, 2, 3), gmCov1},
		{"coverage1 unsorted", parseCoverage, buildCoverage1(3, 1, 2), gmSegs},
		{"coverage2 sorted", parseCoverage, buildCoverage2([3]int{1, 2, 0}, [3]int{4, 5, 2}), gmCov2},
		{"coverage2 unsorted", parseCoverage, buildCoverage2([3]int{4, 5, 2}, [3]int{1, 2, 0}), gmSegs},
		{"classdef2 sorted", parseClassDef, buildClassDef2([3]int{1, 2, 1}, [3]int{4, 5, 2}), gmClass2},
		{"classdef2 unsorted", parseClassDef, buildClassDef2([3]int{4, 5, 2}, [3]int{1, 2, 1}), gmSegs},
	}
	for _, c := range cases {
		m, err := c.parse(c.b)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if m.kind != c.want {
			t.Errorf("%s: kind %d, want %d", c.name, m.kind, c.want)
		}
	}
}
