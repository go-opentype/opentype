// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"math"
	"testing"
)

// This file exercises the CFF/CFF2 grid-fitter (cffhint.go): Private-DICT hint
// parsing, blue-zone construction, stem/edge grid-fitting, the interpolation
// control map, and the end-to-end Face.SetHinting path on a synthesised CFF
// font (a real .otf is checked in realfont_test.go when the file is present).
// Every branch is driven deterministically from hand-built inputs.

// --- Private-DICT hint parsing ----------------------------------------------

func TestParsePrivateHints(t *testing.T) {
	d := map[int][]float64{
		6:    {-10, 10, 700, 10}, // BlueValues deltas -> [-10, 0, 700, 710]
		7:    {-20, 5},           // OtherBlues -> [-20, -15]
		8:    {0, 0, 698, 12},    // FamilyBlues
		9:    {-22, 5},           // FamilyOtherBlues
		10:   {55},               // StdHW
		11:   {66},               // StdVW
		1209: {0.05},             // BlueScale
		1210: {8},                // BlueShift
		1211: {2},                // BlueFuzz
		1212: {40, 5},            // StemSnapH -> [40, 45]
		1213: {60, 6},            // StemSnapV -> [60, 66]
	}
	p := parsePrivateHints(d)
	if got := p.blueValues; len(got) != 4 || got[0] != -10 || got[1] != 0 || got[2] != 700 || got[3] != 710 {
		t.Fatalf("blueValues = %v", got)
	}
	if p.otherBlues[1] != -15 || p.familyBlues[2] != 698 || p.familyOtherBlues[1] != -17 {
		t.Fatalf("blues = %v %v %v", p.otherBlues, p.familyBlues, p.familyOtherBlues)
	}
	if p.stdHW != 55 || p.stdVW != 66 {
		t.Fatalf("std = %v %v", p.stdHW, p.stdVW)
	}
	if p.blueScale != 0.05 || p.blueShift != 8 || p.blueFuzz != 2 {
		t.Fatalf("overshoot params = %v %v %v", p.blueScale, p.blueShift, p.blueFuzz)
	}
	if p.stemSnapH[1] != 45 || p.stemSnapV[1] != 66 {
		t.Fatalf("snaps = %v %v", p.stemSnapH, p.stemSnapV)
	}
}

func TestParsePrivateHintsDefaults(t *testing.T) {
	p := parsePrivateHints(map[int][]float64{})
	if p.blueScale != defaultBlueScale || p.blueShift != defaultBlueShift || p.blueFuzz != defaultBlueFuzz {
		t.Fatalf("defaults not applied: %v %v %v", p.blueScale, p.blueShift, p.blueFuzz)
	}
	if p.blueValues != nil || p.stemSnapH != nil {
		t.Fatalf("absent arrays should be nil: %v %v", p.blueValues, p.stemSnapH)
	}
}

func TestDictFloat(t *testing.T) {
	d := map[int][]float64{10: {3.5}, 11: {}}
	if v, ok := dictFloat(d, 10); !ok || v != 3.5 {
		t.Fatalf("present: %v %v", v, ok)
	}
	if _, ok := dictFloat(d, 11); ok { // present but empty
		t.Fatal("empty operand list should report ok=false")
	}
	if _, ok := dictFloat(d, 99); ok { // absent
		t.Fatal("absent operator should report ok=false")
	}
}

func TestAccumDelta(t *testing.T) {
	if accumDelta(nil) != nil {
		t.Fatal("nil input should yield nil")
	}
	got := accumDelta([]float64{5, 3, -2})
	if len(got) != 3 || got[0] != 5 || got[1] != 8 || got[2] != 6 {
		t.Fatalf("accumDelta = %v", got)
	}
}

// --- Blue-zone construction -------------------------------------------------

func TestMakeZone(t *testing.T) {
	top := makeZone(690, 700, true)
	if top.flat != 690 || top.over != 700 {
		t.Fatalf("top zone = %+v", top)
	}
	bot := makeZone(-15, 0, false)
	if bot.flat != 0 || bot.over != -15 {
		t.Fatalf("bottom zone = %+v", bot)
	}
}

func TestBuildBlueZonesAndFamily(t *testing.T) {
	p := &cffPrivate{
		// baseline pair + top pair.
		blueValues: []float64{0, 0, 700, 712},
		// FamilyBlues: baseline within 1px of primary (substitutes), top far (kept).
		familyBlues: []float64{2, 2, 600, 612},
		otherBlues:  []float64{-25, -20},
		// FamilyOtherBlues far away -> no substitution (>=1px).
		familyOtherBlues: []float64{200, 205},
	}
	scale := 0.01 // 1 font unit = 0.01px, so |2-0|*scale = 0.02 < 1 -> substitute baseline
	zones := buildBlueZones(p, scale)
	if len(zones) != 3 {
		t.Fatalf("want 3 zones, got %d (%+v)", len(zones), zones)
	}
	// baseline flat substituted by family value 2.
	if zones[0].flat != 2 {
		t.Errorf("baseline family substitution: flat = %v want 2", zones[0].flat)
	}
	// top zone: family flat 600 is >1px away (|700-600|*0.01 = 1.0, not <1) -> kept.
	if zones[1].flat != 700 {
		t.Errorf("top zone flat = %v want 700 (family not applied)", zones[1].flat)
	}
	// OtherBlues bottom zone, family far -> kept at 700? no, flat=max(-25,-20)=-20.
	if zones[2].flat != -20 {
		t.Errorf("otherblue flat = %v want -20", zones[2].flat)
	}
}

func TestApplyFamilyNoPair(t *testing.T) {
	z := makeZone(0, 10, true)
	applyFamily(&z, []float64{1}, 0, true, 1) // fam too short -> early return
	if z.flat != 0 {
		t.Fatalf("flat changed unexpectedly: %v", z.flat)
	}
}

// --- Blue-zone edge capture -------------------------------------------------

func TestBlueZoneCapture(t *testing.T) {
	top := blueZone{flat: 0, over: 10} // top zone: overshoot above flat
	bot := blueZone{flat: 0, over: -10}

	// Out of range.
	if _, ok := top.capture(100, 1, 0.1, 1, false); ok {
		t.Error("edge far outside the zone should not be captured")
	}
	// Overshoot suppressed -> align to rounded flat.
	if v, ok := top.capture(5, 1, 0.1, 1, true); !ok || v != 0 {
		t.Errorf("suppressed capture = %v %v want 0 true", v, ok)
	}
	// Not suppressed but within BlueShift -> still align to flat.
	if v, ok := top.capture(0.05, 1, 0.1, 1, false); !ok || v != 0 {
		t.Errorf("within-shift capture = %v %v want 0 true", v, ok)
	}
	// Not suppressed, beyond BlueShift, positive overshoot rounding below 1 -> 1px.
	if v, ok := top.capture(0.2, 1, 0.1, 1, false); !ok || v != 1 {
		t.Errorf("small-overshoot capture = %v %v want 1 true", v, ok)
	}
	// Not suppressed, negative overshoot (bottom zone) -> negative preserved.
	if v, ok := bot.capture(-0.7, 1, 0.1, 1, false); !ok || v != -1 {
		t.Errorf("negative-overshoot capture = %v %v want -1 true", v, ok)
	}
}

// --- Stem grid-fitting ------------------------------------------------------

func TestHintStemVertical(t *testing.T) {
	p := &cffPrivate{stdVW: 100}
	s := cffStemHint{horizontal: false, min: 103, max: 203} // width 100
	scale := 0.1
	hs := hintStem(s, scale, p, nil, false)
	// Lower edge rounds to the grid: 103*0.1 = 10.3 -> 10.
	if hs.hLo != 10 {
		t.Errorf("hLo = %v want 10", hs.hLo)
	}
	// Width 100*0.1 = 10 (snaps to StdVW) -> hHi = 20.
	if hs.hHi != 20 {
		t.Errorf("hHi = %v want 20", hs.hHi)
	}
}

func TestHintStemHorizontalBlueCaptureTop(t *testing.T) {
	p := &cffPrivate{stdHW: 60, blueShift: defaultBlueShift, blueFuzz: defaultBlueFuzz}
	blues := []blueZone{{flat: 700, over: 712}}
	// A horizontal stem whose top edge sits in the cap zone.
	s := cffStemHint{horizontal: true, min: 640, max: 700}
	scale := 0.02
	hs := hintStem(s, scale, p, blues, true) // suppress overshoot
	// Top edge captured to round(700*0.02) = round(14) = 14.
	if hs.hHi != 14 {
		t.Errorf("captured top hHi = %v want 14", hs.hHi)
	}
	if hs.hLo != hs.hHi-math.Round(60*scale) { // width 60 -> snapWidth
		t.Errorf("hLo = %v inconsistent with width", hs.hLo)
	}
}

func TestHintStemHorizontalBlueCaptureBottom(t *testing.T) {
	p := &cffPrivate{stdHW: 60, blueShift: defaultBlueShift, blueFuzz: defaultBlueFuzz}
	blues := []blueZone{{flat: 0, over: -14}}
	// A horizontal stem whose bottom edge sits in the baseline zone.
	s := cffStemHint{horizontal: true, min: 0, max: 60}
	scale := 0.05
	hs := hintStem(s, scale, p, blues, true)
	if hs.hLo != 0 { // round(0*scale)
		t.Errorf("captured bottom hLo = %v want 0", hs.hLo)
	}
	if hs.hHi != hs.hLo+math.Round(60*scale) {
		t.Errorf("hHi = %v inconsistent", hs.hHi)
	}
}

func TestHintStemHorizontalNoCapture(t *testing.T) {
	p := &cffPrivate{stdHW: 60}
	blues := []blueZone{{flat: 700, over: 712}}
	// Mid-glyph horizontal stem, nowhere near a blue zone.
	s := cffStemHint{horizontal: true, min: 300, max: 360}
	scale := 0.05
	hs := hintStem(s, scale, p, blues, false)
	if hs.hLo != math.Round(300*scale) {
		t.Errorf("no-capture hLo = %v want %v", hs.hLo, math.Round(300*scale))
	}
}

// --- Width snapping ---------------------------------------------------------

func TestSnapWidth(t *testing.T) {
	// Snap to a standard width within tolerance, then round.
	if w := snapWidth(1.4, 1.45, nil, 1); w != 1 { // 1.45 within 0.5 -> round(1.45)=1
		t.Errorf("snap-to-std = %v want 1", w)
	}
	// Snap to a stem-snap value (scaled) within tolerance.
	if w := snapWidth(2.1, 0, []float64{2.0}, 1); w != 2 {
		t.Errorf("snap-to-snap = %v want 2", w)
	}
	// Beyond tolerance -> keep the raw width, rounded.
	if w := snapWidth(3.9, 1.0, nil, 1); w != 4 {
		t.Errorf("beyond-tol = %v want 4", w)
	}
	// A sub-pixel stem still renders as at least 1px.
	if w := snapWidth(0.2, 0, nil, 1); w != 1 {
		t.Errorf("min-1px = %v want 1", w)
	}
}

// --- Control map ------------------------------------------------------------

func TestControlMapRemap(t *testing.T) {
	var empty controlMap
	empty.finalize()
	if got := empty.remap(5); got != 5 {
		t.Errorf("empty map is identity: %v", got)
	}

	var cm controlMap
	cm.add(10, 12) // duplicate original 10 with a second target -> averaged
	cm.add(10, 14)
	cm.add(2, 3)
	cm.add(20, 25)
	cm.finalize()
	// After finalize orig should be [2,10,20] with hint[1]=(12+14)/2=13.
	if len(cm.orig) != 3 || cm.orig[0] != 2 || cm.orig[1] != 10 || cm.hint[1] != 13 {
		t.Fatalf("finalize = %v -> %v", cm.orig, cm.hint)
	}
	// Below the first control point: shift by first delta (3-2=+1).
	if got := cm.remap(0); got != 1 {
		t.Errorf("below-first remap = %v want 1", got)
	}
	// Above the last: shift by last delta (25-20=+5).
	if got := cm.remap(30); got != 35 {
		t.Errorf("above-last remap = %v want 35", got)
	}
	// Exactly on a control point.
	if got := cm.remap(10); got != 13 {
		t.Errorf("on-point remap = %v want 13", got)
	}
	// Between two control points: linear interpolation between (2->3) and (10->13).
	// t = (6-2)/(10-2) = 0.5 -> 3 + 0.5*(13-3) = 8.
	if got := cm.remap(6); got != 8 {
		t.Errorf("mid remap = %v want 8", got)
	}
}

// --- activeMaskAt ------------------------------------------------------------

func TestActiveMaskAt(t *testing.T) {
	gh := &cffGlyphHints{}
	if gh.activeMaskAt(0) != nil {
		t.Error("no masks -> nil (all active)")
	}
	gh.hintMasks = []cffHintMask{
		{pointIndex: 0, active: []bool{true, false}},
		{pointIndex: 5, active: []bool{false, true}},
	}
	if m := gh.activeMaskAt(2); m == nil || !m[0] || m[1] {
		t.Errorf("mask at 2 = %v want [true false]", m)
	}
	if m := gh.activeMaskAt(9); m == nil || m[0] || !m[1] {
		t.Errorf("mask at 9 = %v want [false true]", m)
	}
}

// --- buildMaps / cffGridFit --------------------------------------------------

func TestBuildMapsMasking(t *testing.T) {
	hs := []hintedStem{
		{oLo: 1, oHi: 2, hLo: 1, hHi: 2}, // vstem
		{oLo: 3, oHi: 4, hLo: 3, hHi: 4}, // vstem, inactive
		{oLo: 5, oHi: 6, hLo: 5, hHi: 6}, // hstem
	}
	stems := []cffStemHint{{horizontal: false}, {horizontal: false}, {horizontal: true}}
	x, y := buildMaps(hs, stems, []bool{true, false, true}, nil, 1)
	if len(x.orig) != 2 { // only the first (active) vertical stem's two edges
		t.Fatalf("masked x map has %d points want 2", len(x.orig))
	}
	if len(y.orig) != 2 { // the active horizontal stem's two edges
		t.Fatalf("horizontal stem y map has %d points want 2", len(y.orig))
	}
}

func TestCffGridFitNilPriv(t *testing.T) {
	// A stem with no Private DICT still grid-fits (no blues, defaults applied).
	gh := &cffGlyphHints{
		stems: []cffStemHint{{horizontal: false, min: 33, max: 133}},
		priv:  nil,
	}
	in := []contour{{{x: 33, y: 0, on: true}, {x: 133, y: 0, on: true}}}
	out := cffGridFit(in, gh, 0.1, 12, false)
	// x = 33*0.1 = 3.3 -> rounds to 3 device -> 30 font units.
	if math.Abs(out[0][0].x*0.1-math.Round(out[0][0].x*0.1)) > 1e-9 {
		t.Errorf("nil-priv grid fit did not land edge on grid: %v", out[0][0].x)
	}
}

// --- End-to-end via Face.SetHinting -----------------------------------------

// privBlueValues encodes a BlueValues (op 6) delta array for the Private DICT.
func privBlueValues(deltas ...int) []byte {
	var b []byte
	for _, d := range deltas {
		b = append(b, dictLong(d)...)
	}
	return append(b, dictOperator(6)...)
}

// privNum encodes a single-operand Private-DICT entry (op, value).
func privNum(op, v int) []byte {
	return append(dictLong(v), dictOperator(op)...)
}

// hintedBoxGlyph draws a 100x700 box with a vertical stem hint at its two side
// edges [x0, x0+100], for the SetHinting end-to-end test.
func hintedBoxGlyph(x0 int) []byte {
	g := &csb{}
	g.num(x0).num(100).op(3) // vstem: edges [x0, x0+100]
	g.num(x0).num(0).op(21)  // rmoveto -> (x0, 0)
	g.num(100).num(0).op(5)  // rlineto -> (x0+100, 0)
	g.num(0).num(700).op(5)  // rlineto -> (x0+100, 700)
	g.num(-100).num(0).op(5) // rlineto -> (x0, 700)
	g.op(14)                 // endchar (closes to (x0,0))
	return g.b
}

func hintedCFFFont(t *testing.T) *Font {
	t.Helper()
	// BlueValues absolute [0,0, 700,710]: baseline bottom zone + cap top zone at
	// flat y=700; StdVW=100 to snap the stem width.
	priv := append(privBlueValues(0, 0, 700, 10), privNum(11, 100)...)
	glyphs := [][]byte{(&csb{}).op(14).b, hintedBoxGlyph(103)}
	cff := buildCFF(cffOptions{glyphs: glyphs, includePriv: true, charType: 2, privExtra: priv})
	adv := []int{500, 500}
	lsb := []int{0, 0}
	tables := map[string][]byte{
		"head": headTable(1000, 0),
		"maxp": maxpTable(2),
		"hhea": hheaTable(800, -200, 100, 2),
		"hmtx": hmtxTable(adv, lsb, 2),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1})}),
		"CFF ": cff,
	}
	return mustParse(t, assemble(versionOTTO, tables))
}

func TestCFFHintingEndToEnd(t *testing.T) {
	f := hintedCFFFont(t)
	if f.cff == nil || f.cff.priv == nil {
		t.Fatal("CFF font parsed without Private-DICT hints")
	}
	if len(f.cff.priv.blueValues) != 4 || f.cff.priv.stdVW != 100 {
		t.Fatalf("Private hints not parsed: %+v", f.cff.priv)
	}

	const ppem = 14
	scale := float64(ppem) / 1000

	fc := f.NewFace(ppem)
	unhinted, err := fc.outline(1)
	if err != nil {
		t.Fatalf("unhinted outline: %v", err)
	}
	fc.SetHinting(true)
	hinted, err := fc.outline(1)
	if err != nil {
		t.Fatalf("hinted outline: %v", err)
	}

	// Hinting must change the geometry.
	if reflectContoursEqual(unhinted, hinted) {
		t.Fatal("hinting did not change the CFF outline")
	}

	// Every hinted point's stem edge (x) and blue-zoned edge (y) lands on an
	// integer device-pixel boundary.
	for _, c := range hinted {
		for _, p := range c {
			dx, dy := p.x*scale, p.y*scale
			if math.Abs(dx-math.Round(dx)) > 1e-6 {
				t.Errorf("hinted x %v (device %v) not grid-aligned", p.x, dx)
			}
			if math.Abs(dy-math.Round(dy)) > 1e-6 {
				t.Errorf("hinted y %v (device %v) not grid-aligned", p.y, dy)
			}
		}
	}
	// The unhinted stem edge was NOT on the grid (proving hinting did work).
	if dx := unhinted[0][0].x * scale; math.Abs(dx-math.Round(dx)) < 1e-6 {
		t.Fatalf("unhinted edge already grid-aligned (%v); test is vacuous", dx)
	}

	// The whole pipeline still rasterises with hinting on.
	if _, mask, _, _, ok := fc.GlyphMask('A', 0, 0); !ok || mask == nil {
		t.Fatal("hinted CFF glyph did not render")
	}
}

// TestRecordHintMaskBits drives a charstring whose hintmask activates specific
// stems and appears after a completed subpath, exercising the mask-bit decode
// and the multi-contour point count.
func TestRecordHintMaskBits(t *testing.T) {
	c := &csb{}
	c.num(0).num(50).num(650).num(50).op(1) // hstem: 2 stems [0,50] and [700,750]
	c.num(0).num(0).op(21)                  // rmoveto -> contour 1
	c.num(10).num(0).op(5)                  // rlineto
	c.num(0).num(20).op(21)                 // rmoveto -> finishes contour 1, starts contour 2
	c.num(10).num(0).op(5)                  // rlineto
	c.op(19).raw(0x80)                      // hintmask with only stem 0 active
	c.op(14)                                // endchar

	tbl := &cffTable{charStrings: [][]byte{c.b}, charstringType: 2}
	_, gh, err := tbl.outlineHints(0)
	if err != nil {
		t.Fatalf("outlineHints: %v", err)
	}
	if len(gh.stems) != 2 {
		t.Fatalf("recorded %d stems want 2", len(gh.stems))
	}
	if len(gh.hintMasks) != 1 {
		t.Fatalf("recorded %d hintmasks want 1", len(gh.hintMasks))
	}
	hm := gh.hintMasks[0]
	if len(hm.active) != 2 || !hm.active[0] || hm.active[1] {
		t.Fatalf("hintmask active = %v want [true false]", hm.active)
	}
	// The hintmask followed one finished contour (2 pts) plus the in-progress
	// second contour (2 pts) -> pointIndex 4.
	if hm.pointIndex != 4 {
		t.Errorf("hintmask pointIndex = %d want 4", hm.pointIndex)
	}
}

// TestParseLocalSubrsSubrIndexError makes the Local Subr INDEX itself malformed
// so parseLocalSubrs surfaces the parseIndex error.
func TestParseLocalSubrsSubrIndexError(t *testing.T) {
	// Private DICT (6 bytes) declares Subrs at offset 6; the bytes there form an
	// INDEX with a non-zero count but a bad (zero) offSize -> parseIndex fails.
	priv := append(dictLong(6), dictOperator(19)...)
	badIndex := []byte{0x00, 0x01, 0x00} // count=1, offSize=0
	data := append(append([]byte{}, priv...), badIndex...)
	top := map[int][]float64{18: {float64(len(priv)), 0}}
	if _, _, err := parseLocalSubrs(data, top); err == nil {
		t.Fatal("expected a Local Subr INDEX parse error")
	}
}

// TestCFF2HintingViaFace grid-fits a CFF2 glyph through Face.SetHinting and
// covers the out-of-range glyph error path.
func TestCFF2HintingViaFace(t *testing.T) {
	g := &csb{}
	g.num(103).num(100).op(3)                                // vstem: edges [103,203]
	g.num(103).num(100).op(21)                               // rmoveto -> (103,100)
	g.num(100).num(0).num(0).num(200).num(-100).num(0).op(5) // rlineto box
	tables := cff2OTTOTables([][]byte{(&csb{}).b, g.b}, nil, map[rune]uint16{'A': 1}, nil)
	f := mustParse(t, assemble(versionOTTO, tables))

	const ppem = 16
	scale := float64(ppem) / 1000
	fc := f.NewFace(ppem)
	unhinted, err := fc.outline(1)
	if err != nil {
		t.Fatalf("unhinted CFF2 outline: %v", err)
	}
	fc.SetHinting(true)
	hinted, err := fc.outline(1)
	if err != nil {
		t.Fatalf("hinted CFF2 outline: %v", err)
	}
	if reflectContoursEqual(unhinted, hinted) {
		t.Fatal("hinting did not change the CFF2 outline")
	}
	// The vertical-stem edge (x=103) is grid-aligned after hinting.
	if dx := hinted[0][0].x * scale; math.Abs(dx-math.Round(dx)) > 1e-6 {
		t.Errorf("hinted CFF2 stem edge %v not grid-aligned", dx)
	}
	// An out-of-range glyph index surfaces the outline error (ok=false render).
	if _, _, _, _, ok := fc.GlyphMaskIndex(99, 0, 0); ok {
		t.Error("out-of-range CFF2 glyph should not render")
	}
}

// --- Counter control (cntrmask) ---------------------------------------------

// TestRecordCntrMask drives a cntrmask (operator 20) so the interpreter records
// its selected stems as a counter-control group.
func TestRecordCntrMask(t *testing.T) {
	c := &csb{}
	c.num(0).num(50).num(650).num(50).op(3) // vstem: 2 stems
	c.op(20).raw(0x80)                      // cntrmask: only stem 0 selected
	c.num(0).num(0).op(21)                  // rmoveto
	c.op(14)                                // endchar
	tbl := &cffTable{charStrings: [][]byte{c.b}, charstringType: 2}
	_, gh, err := tbl.outlineHints(0)
	if err != nil {
		t.Fatalf("outlineHints: %v", err)
	}
	if len(gh.cntrGroups) != 1 || len(gh.cntrGroups[0]) != 1 || gh.cntrGroups[0][0] != 0 {
		t.Fatalf("cntrGroups = %v want [[0]]", gh.cntrGroups)
	}
}

// TestCounterControlEqualizesGaps grid-fits three vertical stems whose original
// counters (gaps) are uneven and checks the cntrmask counter control chains them
// at the rounded average gap.
func TestCounterControlEqualizesGaps(t *testing.T) {
	gh := &cffGlyphHints{
		stems: []cffStemHint{
			{horizontal: false, min: 0, max: 10},
			{horizontal: false, min: 30, max: 40}, // gap 20 from stem0
			{horizontal: false, min: 45, max: 55}, // gap 5 from stem1
		},
		cntrGroups: [][]int{{0, 1, 2}},
	}
	in := []contour{{
		{x: 0, on: true}, {x: 10, on: true},
		{x: 30, on: true}, {x: 40, on: true},
		{x: 45, on: true}, {x: 55, on: true},
	}}
	out := cffGridFit(in, gh, 1, 100, false)
	// avg gap = round((20+5)/2) = round(12.5) = 13, chained from stem0's edges.
	want := []float64{0, 10, 23, 33, 46, 56}
	for i, w := range want {
		if math.Abs(out[0][i].x-w) > 1e-9 {
			t.Errorf("point %d x = %v want %v", i, out[0][i].x, w)
		}
	}
}

// TestEqualizeCountersMinGap forces a group whose average counter rounds below
// one pixel, so the equaliser clamps the gap to a whole pixel.
func TestEqualizeCountersMinGap(t *testing.T) {
	hs := []hintedStem{
		{oLo: 0, oHi: 10, hLo: 0, hHi: 10},
		{oLo: 10, oHi: 20, hLo: 10, hHi: 20}, // zero original counter
	}
	stems := []cffStemHint{{horizontal: false}, {horizontal: false}}
	equalizeCounters(hs, stems, []int{0, 1}, false)
	if hs[1].hLo != 11 || hs[1].hHi != 21 { // gap clamped to 1px
		t.Fatalf("min-gap chain = %+v want hLo 11 hHi 21", hs[1])
	}
}

// --- Mid-subpath hintmask changes -------------------------------------------

// TestMidSubpathHintmask puts two hintmasks at different points of one contour so
// the same x coordinate is hinted by different stems before and after the switch.
func TestMidSubpathHintmask(t *testing.T) {
	gh := &cffGlyphHints{
		stems: []cffStemHint{
			{horizontal: false, min: 3, max: 13},   // device 0.3..1.3 at scale 0.1
			{horizontal: false, min: 100, max: 110}, // device 10..11
		},
		hintMasks: []cffHintMask{
			{pointIndex: 0, active: []bool{true, false}}, // stem0 only
			{pointIndex: 2, active: []bool{false, true}}, // switch to stem1 at point 2
		},
	}
	in := []contour{{
		{x: 100, on: true}, // point0: under stem0 mask -> rigid shift
		{x: 3, on: true},   // point1: still stem0 mask
		{x: 100, on: true}, // point2: now stem1 mask -> snaps to stem1 edge
		{x: 110, on: true}, // point3: stem1 mask
	}}
	out := cffGridFit(in, gh, 0.1, 12, false)
	// point0 (device 10) under stem0: above stem0's far edge (1.3->1), shift -0.3
	// -> device 9.7 -> font 97.
	if math.Abs(out[0][0].x-97) > 1e-6 {
		t.Errorf("point0 x = %v want 97 (stem0 rigid shift)", out[0][0].x)
	}
	// point2 (device 10) under stem1: lands on stem1's grid-fitted edge -> font 100.
	if math.Abs(out[0][2].x-100) > 1e-6 {
		t.Errorf("point2 x = %v want 100 (stem1 snap)", out[0][2].x)
	}
}

// --- Flex regions -----------------------------------------------------------

// TestRecordFlexRange drives an hflex and checks its emitted point span is
// recorded as a flexRange (entry anchor at start-1, exit anchor at end).
func TestRecordFlexRange(t *testing.T) {
	c := &csb{}
	c.num(0).num(0).op(21)                                            // rmoveto (1 point)
	c.num(10).num(5).num(10).num(-5).num(10).num(10).num(10).op(1234) // hflex (7 args)
	c.op(14)
	tbl := &cffTable{charStrings: [][]byte{c.b}, charstringType: 2}
	_, gh, err := tbl.outlineHints(0)
	if err != nil {
		t.Fatalf("outlineHints: %v", err)
	}
	// hflex draws 2*cffCurveSteps points after the single moveto point.
	if len(gh.flexRanges) != 1 || gh.flexRanges[0].start != 1 || gh.flexRanges[0].end != 2*cffCurveSteps {
		t.Fatalf("flexRanges = %v want [{1 %d}]", gh.flexRanges, 2*cffCurveSteps)
	}
}

// TestInterpolateFlex checks the interior-point interpolation both when the two
// anchors span an original interval (x) and when they are degenerate (y).
func TestInterpolateFlex(t *testing.T) {
	pts := []hintPoint{
		{ox: 0, hx: 1, oy: 2, hy: 2},   // entry anchor: x moved +1, y degenerate
		{ox: 5, hx: 5, oy: 2, hy: 2},   // interior
		{ox: 10, hx: 13, oy: 2, hy: 2}, // exit anchor: x moved +3
	}
	interpolateFlex(pts, 0, 2)
	// x: dA=1, dB=3, t=0.5 -> 5 + 1 + 0.5*((13-10)-1) = 7.
	if math.Abs(pts[1].hx-7) > 1e-9 {
		t.Errorf("interior hx = %v want 7", pts[1].hx)
	}
	// y: oB==oA -> takes anchor A's (zero) delta -> unchanged.
	if math.Abs(pts[1].hy-2) > 1e-9 {
		t.Errorf("interior hy = %v want 2", pts[1].hy)
	}
}

// TestCffGridFitFlexRegions covers the grid-fitter's flex loop: a flex whose
// entry anchor is missing (start 0) is skipped, a well-formed flex interpolates.
func TestCffGridFitFlexRegions(t *testing.T) {
	gh := &cffGlyphHints{
		flexRanges: []flexRange{{start: 0, end: 1}, {start: 2, end: 4}},
	}
	in := []contour{{
		{x: 0, on: true}, {x: 1, on: true},
		{x: 2, on: true}, {x: 3, on: true}, {x: 4, on: true},
	}}
	out := cffGridFit(in, gh, 1, 24, false)
	// No stems -> identity remap; the flex interpolation is identity too.
	for i := range in[0] {
		if out[0][i].x != in[0][i].x {
			t.Errorf("point %d x = %v want %v", i, out[0][i].x, in[0][i].x)
		}
	}
}

// --- Stem darkening ---------------------------------------------------------

func TestStemDarkenPixels(t *testing.T) {
	if stemDarkenPixels(0) != 0 {
		t.Error("non-positive ppem should not darken")
	}
	if stemDarkenPixels(darkenMaxPPEM) != 0 || stemDarkenPixels(darkenMaxPPEM+10) != 0 {
		t.Error("ppem at/above the cutoff should not darken")
	}
	if d := stemDarkenPixels(darkenMaxPPEM / 2); math.Abs(d-darkenMaxPixels/2) > 1e-9 {
		t.Errorf("mid-range darkening = %v want %v", d, darkenMaxPixels/2)
	}
}

// TestCffGridFitDarken widens a single stem's edges by the ppem-dependent amount.
func TestCffGridFitDarken(t *testing.T) {
	gh := &cffGlyphHints{stems: []cffStemHint{{horizontal: false, min: 0, max: 10}}}
	in := []contour{{{x: 0, on: true}, {x: 10, on: true}}}
	out := cffGridFit(in, gh, 1, darkenMaxPPEM/2, true) // d = darkenMaxPixels/2 = 0.075
	if math.Abs(out[0][0].x-(-0.075)) > 1e-9 {
		t.Errorf("darkened low edge = %v want -0.075", out[0][0].x)
	}
	if math.Abs(out[0][1].x-10.075) > 1e-9 {
		t.Errorf("darkened high edge = %v want 10.075", out[0][1].x)
	}
}

// TestStemDarkeningFace exercises the Face toggle: darkening changes the hinted
// CFF outline, and turning it back off reproduces the undarkened result exactly.
func TestStemDarkeningFace(t *testing.T) {
	f := hintedCFFFont(t)
	fc := f.NewFace(14)
	fc.SetHinting(true)
	base, err := fc.outline(1)
	if err != nil {
		t.Fatalf("hinted outline: %v", err)
	}
	fc.SetStemDarkening(true)
	dark, err := fc.outline(1)
	if err != nil {
		t.Fatalf("darkened outline: %v", err)
	}
	if reflectContoursEqual(base, dark) {
		t.Fatal("stem darkening did not change the outline")
	}
	fc.SetStemDarkening(false)
	off, err := fc.outline(1)
	if err != nil {
		t.Fatalf("undarkened outline: %v", err)
	}
	if !reflectContoursEqual(base, off) {
		t.Fatal("stem darkening off is not byte-identical to the undarkened result")
	}
}

func reflectContoursEqual(a, b []contour) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}
