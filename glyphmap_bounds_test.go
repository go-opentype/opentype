// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"testing"
	"time"
)

// Bomb tests for the last unbounded Parse amplification of issue #75: a Coverage
// or ClassDef RangeRecord is six bytes and may span 65536 glyphs. Expanded into a
// map (the code at e4de2be) one record built ~3 MB, ten thousand of them 655
// million map writes, and a thousand distinct tables in a thousand lookups ~3 GB.
// Each case below is a legitimate-looking font that parses, and must do so with
// work and memory proportional to its own bytes: TestBombs asserts the
// allocation ceiling, TestCoverageBombsAreFast the wall time.

// fullRanges returns n RangeRecords, each spanning every glyph.
func fullRanges(n, class int) [][3]int {
	rs := make([][3]int, n)
	for i := range rs {
		rs[i] = [3]int{0, 0xffff, class}
	}
	return rs
}

// layoutBomb is a GSUB or GPOS font whose one feature selects every lookup.
func layoutBomb(tag, feature string, lookups [][]byte) []byte {
	idx := make([]uint16, len(lookups))
	for i := range idx {
		idx[i] = uint16(i)
	}
	return bombFont(tag, buildLayoutTable(simpleScripts(0), []tFeature{{tag: feature, lookups: idx}}, lookups))
}

func coverageBombCases() []bombCase {
	var cs []bombCase
	add := func(name string, font []byte) {
		cs = append(cs, bombCase{name: name, font: font})
	}
	const n = 1000

	// (a) one Coverage of 10,000 records, each spanning every glyph: 60 KB of
	// table, 655 million map writes when expanded.
	add("coverage-10000-full-ranges", layoutBomb("GSUB", "liga", [][]byte{
		buildLookup(1, [][]byte{buildSingle1(1, buildCoverage2(fullRanges(10000, 0)...))}),
	}))
	// the same, as the one ClassDef of a PairPos format 2
	add("classdef-10000-full-ranges", layoutBomb("GPOS", "kern", [][]byte{
		buildLookup(2, [][]byte{buildPairPos2(buildCoverage1(1),
			buildClassDef2(fullRanges(10000, 1)...), buildClassDef2(fullRanges(1, 1)...),
			0, 0, 2, 2, make([]int16, 4))}),
	}))

	// (b) a thousand distinct lookups, each with its own six-byte full-range
	// Coverage: the memo dedups one list, not a thousand lookups.
	single := make([][]byte, n)
	for i := range single {
		single[i] = buildLookup(1, [][]byte{buildSingle1(1, buildCoverage2(fullRanges(1, 0)...))})
	}
	add("coverage-1000-lookups", layoutBomb("GSUB", "liga", single))

	// (c) the same for ClassDef, in a contextual substitution format 2 and a
	// pair positioning format 2.
	ctx := make([][]byte, n)
	pair := make([][]byte, n)
	for i := range ctx {
		ctx[i] = buildLookup(5, [][]byte{buildContext2(buildCoverage1(1), buildClassDef2(fullRanges(1, 1)...), nil)})
		pair[i] = buildLookup(2, [][]byte{buildPairPos2(buildCoverage1(1),
			buildClassDef2(fullRanges(1, 1)...), buildClassDef2(fullRanges(1, 1)...),
			0, 0, 2, 2, make([]int16, 4))})
	}
	add("classdef-1000-lookups-context2", layoutBomb("GSUB", "liga", ctx))
	add("classdef-1000-lookups-pairpos2", layoutBomb("GPOS", "kern", pair))
	return cs
}

// TestCoverageBombsAreFast parses each bomb and bounds the wall time, the
// measure the allocation ceiling cannot give: ten thousand expanded ranges build
// their 655 million entries in a loop, not in one allocation.
func TestCoverageBombsAreFast(t *testing.T) {
	for _, c := range coverageBombCases() {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			var err error
			n := allocated(func() { _, err = Parse(c.font) })
			if err != nil {
				t.Fatalf("a legitimate font was refused: %v", err)
			}
			if n > 8<<20 {
				t.Errorf("allocated %d bytes for a %d-byte font, over 8 MiB", n, len(c.font))
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("Parse took %v for a %d-byte font", d, len(c.font))
			}
		})
	}
}
