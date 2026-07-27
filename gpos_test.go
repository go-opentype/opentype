// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

// --- GPOS ValueRecord / PairPos builders ------------------------------------

// gpos ValueFormat bits used by the tests.
const (
	vfXPlacement = 0x0001
	vfXAdvance   = 0x0004
)

// valueRec builds a ValueRecord for the given format, filling X placement and X
// advance and zeroing any other present field.
func valueRec(format uint16, xPlacement, xAdvance int16) []byte {
	w := &bw{}
	for bit := 0; bit < 8; bit++ {
		mask := uint16(1) << uint(bit)
		if format&mask == 0 {
			continue
		}
		switch mask {
		case vfXPlacement:
			w.i16(xPlacement)
		case vfXAdvance:
			w.i16(xAdvance)
		default:
			w.i16(0)
		}
	}
	return w.bytes()
}

type pv struct {
	second   GlyphIndex
	xp1, xa1 int16
}

func buildPairSet(vf1, vf2 uint16, pairs []pv) []byte {
	w := &bw{}
	w.u16(uint16(len(pairs)))
	for _, p := range pairs {
		w.u16(uint16(p.second))
		w.b = append(w.b, valueRec(vf1, p.xp1, p.xa1)...)
		w.b = append(w.b, valueRec(vf2, 0, 0)...)
	}
	return w.bytes()
}

func buildPairPos1(cov []byte, vf1, vf2 uint16, sets [][]byte) []byte {
	header := 10 + 2*len(sets)
	body := &bw{}
	var setOffs []int
	for _, s := range sets {
		setOffs = append(setOffs, header+len(body.b))
		body.b = append(body.b, s...)
	}
	covOff := header + len(body.b)
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(vf1)
	w.u16(vf2)
	w.u16(uint16(len(sets)))
	for _, o := range setOffs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	return w.bytes()
}

// buildPairPos2 builds a format 2 subtable; adv holds class1Count*class2Count
// X-advance values for the first glyph, row-major (class1 outer, class2 inner).
func buildPairPos2(cov, cd1, cd2 []byte, vf1, vf2 uint16, c1, c2 int, adv []int16) []byte {
	rec := &bw{}
	for _, a := range adv {
		rec.b = append(rec.b, valueRec(vf1, 0, a)...)
		rec.b = append(rec.b, valueRec(vf2, 0, 0)...)
	}
	header := 16
	covOff := header + len(rec.b)
	cd1Off := covOff + len(cov)
	cd2Off := cd1Off + len(cd1)
	w := &bw{}
	w.u16(2)
	w.u16(uint16(covOff))
	w.u16(vf1)
	w.u16(vf2)
	w.u16(uint16(cd1Off))
	w.u16(uint16(cd2Off))
	w.u16(uint16(c1))
	w.u16(uint16(c2))
	w.b = append(w.b, rec.b...)
	w.b = append(w.b, cov...)
	w.b = append(w.b, cd1...)
	w.b = append(w.b, cd2...)
	return w.bytes()
}

// gposFixture builds a GPOS whose "kern" feature selects a PairPos-1 lookup and
// a PairPos-2 lookup (plus an out-of-range index), with an unsupported type-1
// lookup present to exercise the "not type 2" skip.
func gposFixture(t *testing.T) *gpos {
	t.Helper()
	const vf1 = uint16(vfXPlacement | vfXAdvance)

	pp1 := buildPairPos1(buildCoverage1(5), vf1, 0, [][]byte{
		buildPairSet(vf1, 0, []pv{{second: 6, xp1: 3, xa1: -50}}),
	})
	// PairPos-2: cover glyphs 10 and 11; cd1 maps 10->class 1 and 11->class 5
	// (out of range for class1Count=2). cd2 maps 6->class 1. Grid rows are
	// [class0: c2=0,c2=1][class1: c2=0,c2=1] = [0,0,-15,-30].
	pp2 := buildPairPos2(
		buildCoverage2([3]int{10, 11, 0}),
		buildClassDef2([3]int{10, 10, 1}, [3]int{11, 11, 5}),
		buildClassDef1(6, 1),
		vf1, 0, 2, 2, []int16{0, 0, -15, -30},
	)

	lookups := [][]byte{
		buildLookup(2, [][]byte{pp1}),
		buildLookup(2, [][]byte{pp2}),
		buildLookup(1, [][]byte{{0, 0}}), // unsupported single-pos, parsed as no-op
	}
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "kern", lookups: []uint16{0, 1, 99}}}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	return g
}

func TestGPOSKern(t *testing.T) {
	g := gposFixture(t)
	cases := []struct {
		l, r GlyphIndex
		want int
	}{
		{5, 6, -50},  // PairPos-1 hit
		{5, 7, 0},    // PairPos-1 covered, right glyph absent
		{8, 6, 0},    // PairPos-1 first glyph not covered
		{10, 6, -30}, // PairPos-2 class (1,1)
		{10, 7, -15}, // PairPos-2 class (1, default 0)
		{11, 6, 0},   // PairPos-2 class index out of range
		{50, 6, 0},   // PairPos-2 first glyph not covered
	}
	for _, c := range cases {
		if got := g.Kern(c.l, c.r); got != c.want {
			t.Errorf("Kern(%d,%d)=%d want %d", c.l, c.r, got, c.want)
		}
	}
}

func TestGPOSKernNoFeature(t *testing.T) {
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "abcd", lookups: []uint16{0}}}
	lookups := [][]byte{buildLookup(2, [][]byte{
		buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
			buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -9}}),
		}),
	})}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	if got := g.Kern(5, 6); got != 0 {
		t.Errorf("Kern without kern feature = %d want 0", got)
	}
}

func TestGPOSParseErrors(t *testing.T) {
	if _, err := parseGPOS([]byte{0, 1}); err == nil {
		t.Error("truncated GPOS should error")
	}
	// A pair-pos subtable with an unknown format propagates an error.
	bad := buildLayoutTable(simpleScripts(0),
		[]tFeature{{tag: "kern", lookups: []uint16{0}}},
		[][]byte{buildLookup(2, [][]byte{{0, 9}})}) // pairpos format 9
	if _, err := parseGPOS(bad); err == nil {
		t.Error("bad pairpos format should propagate error")
	}
}

func TestGPOSLookupTruncated(t *testing.T) {
	if _, err := parseGPOSLookup([]byte{0, 2, 0, 0, 0, 5}); err == nil {
		t.Error("truncated gpos lookup should error")
	}
}

func TestParsePairPos1Errors(t *testing.T) {
	// Truncated header (before pairSet offsets).
	if _, err := parsePairPos1([]byte{0, 1, 0, 10, 0, 0, 0, 0, 0, 1}); err == nil {
		t.Error("truncated pairpos1 should error")
	}
	// Bad coverage offset.
	if _, err := parsePairPos1([]byte{0, 1, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("pairpos1 bad coverage should error")
	}
	// Bad pairSet offset (set offset corrupted past the end). The setOffset is
	// the 16-bit field at offset 10 of a PairPos-1 subtable.
	blob := buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -1}}),
	})
	blob[10] = 0xFF
	blob[11] = 0xFF
	if _, err := parsePairPos1(blob); err == nil {
		t.Error("truncated pairSet should error")
	}
}

func TestParsePairPos2Errors(t *testing.T) {
	full := buildPairPos2(buildCoverage1(5), buildClassDef1(5, 0), buildClassDef1(6, 0),
		vfXAdvance, 0, 1, 1, []int16{0})
	// A 16-byte header declaring a 1x1 grid but with no grid bytes: the grid
	// ValueRecord read runs off the end.
	short := []byte{0, 2, 0, 0, 0, vfXAdvance, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1}
	if _, err := parsePairPos2(short); err == nil {
		t.Error("truncated pairpos2 grid should error")
	}
	// Bad coverage / classDef offsets: corrupt each 16-bit offset in turn.
	for name, off := range map[string]int{"coverage": 2, "classDef1": 8, "classDef2": 10} {
		bad := append([]byte(nil), full...)
		bad[off] = 0xFF
		bad[off+1] = 0xFF
		if _, err := parsePairPos2(bad); err == nil {
			t.Errorf("pairpos2 bad %s offset should error", name)
		}
	}
}

func TestParsePairSetTruncated(t *testing.T) {
	if _, err := parsePairSet([]byte{0, 1}, vfXAdvance, 0); err == nil {
		t.Error("truncated pairSet should error")
	}
}

func TestParseGPOSSubtableUnsupportedFormat(t *testing.T) {
	if _, err := parseGPOSSubtable(2, []byte{0, 9}); err == nil {
		t.Error("pairpos format 9 should error")
	}
}

func TestKerner(t *testing.T) {
	g := gposFixture(t)
	// A kern table giving a fallback for the pair GPOS does not adjust.
	k := &kern{pairs: map[uint32]int{
		kernKey(5, 7): -8,
		kernKey(1, 2): -4,
	}}

	both := &Kerner{gpos: g, kern: k}
	if got := both.Kerning(5, 6); got != -50 { // GPOS wins (non-zero)
		t.Errorf("Kerning(5,6)=%d want -50", got)
	}
	if got := both.Kerning(5, 7); got != -8 { // GPOS zero -> kern fallback
		t.Errorf("Kerning(5,7)=%d want -8", got)
	}

	kernOnly := &Kerner{kern: k}
	if got := kernOnly.Kerning(1, 2); got != -4 {
		t.Errorf("kern-only Kerning(1,2)=%d want -4", got)
	}

	gposOnly := &Kerner{gpos: g}
	if got := gposOnly.Kerning(1, 2); got != 0 { // no GPOS pair, no kern table
		t.Errorf("gpos-only Kerning(1,2)=%d want 0", got)
	}

	empty := &Kerner{}
	if got := empty.Kerning(1, 2); got != 0 {
		t.Errorf("empty Kerning=%d want 0", got)
	}
}
