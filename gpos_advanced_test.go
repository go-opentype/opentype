// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

// This file synthesises GPOS subtables of every advanced lookup type in memory
// (in the spirit of synth_test.go / layout_test.go) so the positioning tests
// exercise each parse and apply branch deterministically, without a font file.

// --- byte-blob builders for the advanced subtables --------------------------

func buildAnchor1(x, y int16) []byte {
	w := &bw{}
	w.u16(1)
	w.i16(x)
	w.i16(y)
	return w.bytes()
}

func buildAnchor2(x, y int16, point uint16) []byte {
	w := &bw{}
	w.u16(2)
	w.i16(x)
	w.i16(y)
	w.u16(point)
	return w.bytes()
}

func buildAnchor3(x, y int16) []byte {
	w := &bw{}
	w.u16(3)
	w.i16(x)
	w.i16(y)
	w.u16(0) // xDeviceOffset (NULL)
	w.u16(0) // yDeviceOffset (NULL)
	return w.bytes()
}

// valueRecFull encodes a ValueRecord filling placement/advance fields and
// zeroing the four Device/VariationIndex offset fields when their bit is set.
func valueRecFull(format uint16, xp, yp, xa, ya int16) []byte {
	w := &bw{}
	if format&0x0001 != 0 {
		w.i16(xp)
	}
	if format&0x0002 != 0 {
		w.i16(yp)
	}
	if format&0x0004 != 0 {
		w.i16(xa)
	}
	if format&0x0008 != 0 {
		w.i16(ya)
	}
	for _, bit := range []uint16{0x0010, 0x0020, 0x0040, 0x0080} {
		if format&bit != 0 {
			w.u16(0)
		}
	}
	return w.bytes()
}

func buildSinglePos1(cov []byte, vf uint16, val []byte) []byte {
	covOff := 6 + len(val)
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(vf)
	w.b = append(w.b, val...)
	w.b = append(w.b, cov...)
	return w.bytes()
}

func buildSinglePos2(cov []byte, vf uint16, vals [][]byte) []byte {
	body := &bw{}
	for _, v := range vals {
		body.b = append(body.b, v...)
	}
	covOff := 8 + len(body.b)
	w := &bw{}
	w.u16(2)
	w.u16(uint16(covOff))
	w.u16(vf)
	w.u16(uint16(len(vals)))
	w.b = append(w.b, body.b...)
	w.b = append(w.b, cov...)
	return w.bytes()
}

// eeRec is one cursive entry/exit anchor pair (nil = NULL anchor).
type eeRec struct{ entry, exit []byte }

func buildCursivePos(cov []byte, recs []eeRec) []byte {
	n := len(recs)
	header := 6 + 4*n
	body := &bw{}
	covOff := header
	body.b = append(body.b, cov...)
	entryOffs := make([]int, n)
	exitOffs := make([]int, n)
	for i, rc := range recs {
		if rc.entry != nil {
			entryOffs[i] = header + len(body.b)
			body.b = append(body.b, rc.entry...)
		}
		if rc.exit != nil {
			exitOffs[i] = header + len(body.b)
			body.b = append(body.b, rc.exit...)
		}
	}
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(uint16(n))
	for i := 0; i < n; i++ {
		w.u16(uint16(entryOffs[i]))
		w.u16(uint16(exitOffs[i]))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// markSpec is one MarkArray record.
type markSpec struct {
	class  uint16
	anchor []byte
}

func buildMarkArray(marks []markSpec) []byte {
	n := len(marks)
	header := 2 + 4*n
	body := &bw{}
	offs := make([]int, n)
	for i, m := range marks {
		offs[i] = header + len(body.b)
		body.b = append(body.b, m.anchor...)
	}
	w := &bw{}
	w.u16(uint16(n))
	for i, m := range marks {
		w.u16(m.class)
		w.u16(uint16(offs[i]))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildAnchorMatrix builds a BaseArray/Mark2Array/LigatureAttach body: rowCount
// followed by rows*cols anchor offsets (nil = NULL) and the anchor tables.
func buildAnchorMatrix(cols int, rows [][][]byte) []byte {
	n := len(rows)
	header := 2 + 2*n*cols
	body := &bw{}
	offs := make([][]int, n)
	for i := range rows {
		offs[i] = make([]int, cols)
		for j := 0; j < cols; j++ {
			if rows[i][j] != nil {
				offs[i][j] = header + len(body.b)
				body.b = append(body.b, rows[i][j]...)
			}
		}
	}
	w := &bw{}
	w.u16(uint16(n))
	for i := range rows {
		for j := 0; j < cols; j++ {
			w.u16(uint16(offs[i][j]))
		}
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildLigatureArray(ligs [][]byte) []byte {
	n := len(ligs)
	header := 2 + 2*n
	body := &bw{}
	offs := make([]int, n)
	for i, l := range ligs {
		offs[i] = header + len(body.b)
		body.b = append(body.b, l...)
	}
	w := &bw{}
	w.u16(uint16(n))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildMarkPosSubtable builds the shared 12-byte-header layout of MarkBasePos,
// MarkLigPos and MarkMarkPos: two coverage tables, a class count, a MarkArray
// and a second array (BaseArray / LigatureArray / Mark2Array).
func buildMarkPosSubtable(covA, covB []byte, classCount int, arrMark, arrBase []byte) []byte {
	header := 12
	body := &bw{}
	covAOff := header + len(body.b)
	body.b = append(body.b, covA...)
	covBOff := header + len(body.b)
	body.b = append(body.b, covB...)
	arrMarkOff := header + len(body.b)
	body.b = append(body.b, arrMark...)
	arrBaseOff := header + len(body.b)
	body.b = append(body.b, arrBase...)
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covAOff))
	w.u16(uint16(covBOff))
	w.u16(uint16(classCount))
	w.u16(uint16(arrMarkOff))
	w.u16(uint16(arrBaseOff))
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// plr is one PosLookupRecord.
type plr struct{ seq, lookup uint16 }

func buildPosRule(input []GlyphIndex, recs []plr) []byte {
	w := &bw{}
	w.u16(uint16(len(input) + 1)) // glyphCount (includes the covered first glyph)
	w.u16(uint16(len(recs)))
	for _, g := range input {
		w.u16(uint16(g))
	}
	for _, r := range recs {
		w.u16(r.seq)
		w.u16(r.lookup)
	}
	return w.bytes()
}

// buildPosRuleSet wraps rule blobs (PosRule or ChainPosRule) into a *RuleSet table.
func buildPosRuleSet(rules [][]byte) []byte {
	n := len(rules)
	header := 2 + 2*n
	body := &bw{}
	offs := make([]int, n)
	for i, r := range rules {
		offs[i] = header + len(body.b)
		body.b = append(body.b, r...)
	}
	w := &bw{}
	w.u16(uint16(n))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildContextPos1 builds a format-1 contextual (or chaining, same outer shape)
// subtable from a coverage and a list of rule-set blobs.
func buildContextPos1(cov []byte, sets [][]byte) []byte {
	n := len(sets)
	header := 6 + 2*n
	body := &bw{}
	covOff := header + len(body.b)
	body.b = append(body.b, cov...)
	setOffs := make([]int, n)
	for i, s := range sets {
		setOffs[i] = header + len(body.b)
		body.b = append(body.b, s...)
	}
	w := &bw{}
	w.u16(1)
	w.u16(uint16(covOff))
	w.u16(uint16(n))
	for _, o := range setOffs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildContextPos3(covs [][]byte, recs []plr) []byte {
	gc := len(covs)
	header := 6 + 2*gc + 4*len(recs)
	body := &bw{}
	covOffs := make([]int, gc)
	for i, c := range covs {
		covOffs[i] = header + len(body.b)
		body.b = append(body.b, c...)
	}
	w := &bw{}
	w.u16(3)
	w.u16(uint16(gc))
	w.u16(uint16(len(recs)))
	for _, o := range covOffs {
		w.u16(uint16(o))
	}
	for _, r := range recs {
		w.u16(r.seq)
		w.u16(r.lookup)
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildPosChainRule(back, input, ahead []GlyphIndex, recs []plr) []byte {
	w := &bw{}
	w.u16(uint16(len(back)))
	for _, g := range back {
		w.u16(uint16(g))
	}
	w.u16(uint16(len(input) + 1))
	for _, g := range input {
		w.u16(uint16(g))
	}
	w.u16(uint16(len(ahead)))
	for _, g := range ahead {
		w.u16(uint16(g))
	}
	w.u16(uint16(len(recs)))
	for _, r := range recs {
		w.u16(r.seq)
		w.u16(r.lookup)
	}
	return w.bytes()
}

func buildChainPos3(back, input, ahead [][]byte, recs []plr) []byte {
	bn, in, an := len(back), len(input), len(ahead)
	header := 2 + (2 + 2*bn) + (2 + 2*in) + (2 + 2*an) + 2 + 4*len(recs)
	body := &bw{}
	off := func(blobs [][]byte) []int {
		offs := make([]int, len(blobs))
		for i, bl := range blobs {
			offs[i] = header + len(body.b)
			body.b = append(body.b, bl...)
		}
		return offs
	}
	backOffs := off(back)
	inputOffs := off(input)
	aheadOffs := off(ahead)
	w := &bw{}
	w.u16(3)
	w.u16(uint16(bn))
	for _, o := range backOffs {
		w.u16(uint16(o))
	}
	w.u16(uint16(in))
	for _, o := range inputOffs {
		w.u16(uint16(o))
	}
	w.u16(uint16(an))
	for _, o := range aheadOffs {
		w.u16(uint16(o))
	}
	w.u16(uint16(len(recs)))
	for _, r := range recs {
		w.u16(r.seq)
		w.u16(r.lookup)
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildExtensionPos(extType uint16, inner []byte) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(extType)
	w.u32(8) // extensionOffset (from the start of this subtable)
	w.b = append(w.b, inner...)
	return w.bytes()
}

// --- helpers ----------------------------------------------------------------

func mustSubtable(t *testing.T, lookupType uint16, b []byte) posSubtable {
	t.Helper()
	st, err := parseGPOSSubtable(lookupType, b)
	if err != nil {
		t.Fatalf("parseGPOSSubtable(%d): %v", lookupType, err)
	}
	if st == nil {
		t.Fatalf("parseGPOSSubtable(%d): nil subtable", lookupType)
	}
	return st
}

// applyPosOne parses a subtable and applies it once at index i over glyphs/advances.
func applyPosOne(t *testing.T, lookupType uint16, b []byte, glyphs []GlyphIndex, advances []int, i int) (GlyphPosition, bool, []GlyphPosition) {
	t.Helper()
	st := mustSubtable(t, lookupType, b)
	pos := make([]GlyphPosition, len(glyphs))
	ctx := &posContext{glyphs: glyphs, positions: pos, advances: advances}
	ok := st.apply(ctx, i)
	return pos[i], ok, pos
}

// gposWith assembles a GPOS whose DFLT default LangSys enables one feature that
// activates the given lookup indices.
func gposWith(t *testing.T, feature string, activate []uint16, lookups [][]byte) *gpos {
	t.Helper()
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: feature, lookups: activate}}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	return g
}

// --- Type 1: single adjustment ----------------------------------------------

func TestSinglePos1AllValueFields(t *testing.T) {
	// A full ValueRecord (all eight bits) exercises every readValueRecord branch
	// and addValue.
	const vf = uint16(0x00FF)
	st := buildSinglePos1(buildCoverage1(7), vf, valueRecFull(vf, 3, 4, 5, 6))
	got, ok, _ := applyPosOne(t, 1, st, []GlyphIndex{7}, nil, 0)
	if !ok || got != (GlyphPosition{XOffset: 3, YOffset: 4, XAdvance: 5, YAdvance: 6}) {
		t.Fatalf("single1 = %+v ok=%v", got, ok)
	}
	// An uncovered glyph is left unchanged.
	if _, ok, _ := applyPosOne(t, 1, st, []GlyphIndex{9}, nil, 0); ok {
		t.Error("single1 applied to uncovered glyph")
	}
}

func TestSinglePos2(t *testing.T) {
	const vf = vfXAdvance
	st := buildSinglePos2(buildCoverage1(10, 11), vf, [][]byte{
		valueRecFull(vf, 0, 0, -20, 0),
		valueRecFull(vf, 0, 0, -40, 0),
	})
	if got, ok, _ := applyPosOne(t, 1, st, []GlyphIndex{11}, nil, 0); !ok || got.XAdvance != -40 {
		t.Errorf("single2 covered = %+v ok=%v", got, ok)
	}
	if _, ok, _ := applyPosOne(t, 1, st, []GlyphIndex{99}, nil, 0); ok {
		t.Error("single2 applied to uncovered glyph")
	}
	// Coverage index beyond the value array (malformed): guarded, no apply.
	bad := buildSinglePos2(buildCoverage1(10, 11, 12), vf, [][]byte{valueRecFull(vf, 0, 0, -20, 0)})
	if _, ok, _ := applyPosOne(t, 1, bad, []GlyphIndex{12}, nil, 0); ok {
		t.Error("single2 applied with out-of-range coverage index")
	}
}

func TestSinglePosParseErrors(t *testing.T) {
	if _, err := parseSinglePos([]byte{0, 9, 0, 0, 0, 0}); err == nil {
		t.Error("singlepos bad format should error")
	}
	if _, err := parseSinglePos([]byte{0, 1, 0, 0}); err == nil {
		t.Error("singlepos1 truncated value should error")
	}
	// Bad coverage offset, format 1 and 2.
	if _, err := parseSinglePos(buildSinglePos1BadCov(vfXAdvance)); err == nil {
		t.Error("singlepos1 bad coverage should error")
	}
	bad2 := buildSinglePos2(buildCoverage1(5), vfXAdvance, [][]byte{valueRecFull(vfXAdvance, 0, 0, 1, 0)})
	bad2[2] = 0xFF // corrupt coverageOffset (bytes 2-3)
	bad2[3] = 0xFF
	if _, err := parseSinglePos(bad2); err == nil {
		t.Error("singlepos2 bad coverage should error")
	}
	// Format 2 with a value count but no value bytes.
	if _, err := parseSinglePos([]byte{0, 2, 0, 0, 0, vfXAdvance, 0, 1}); err == nil {
		t.Error("singlepos2 truncated values should error")
	}
}

func buildSinglePos1BadCov(vf uint16) []byte {
	b := buildSinglePos1(buildCoverage1(5), vf, valueRecFull(vf, 0, 0, 1, 0))
	b[2] = 0xFF // coverageOffset (bytes 2-3) -> past end
	b[3] = 0xFF
	return b
}

// --- Type 3: cursive attachment ---------------------------------------------

func TestCursivePos(t *testing.T) {
	// glyph A exit at x=700, glyph B entry at x=0: B shifts right by 700.
	st := buildCursivePos(buildCoverage1(1, 2), []eeRec{
		{entry: nil, exit: buildAnchor1(700, 10)},
		{entry: buildAnchor1(0, 0), exit: nil},
	})
	_, ok, pos := applyPosOne(t, 3, st, []GlyphIndex{1, 2}, nil, 0)
	if !ok || pos[1].XOffset != 700 || pos[1].YOffset != 10 {
		t.Fatalf("cursive join pos=%+v ok=%v", pos, ok)
	}
	// First glyph not covered.
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{9, 2}, nil, 0); ok {
		t.Error("cursive applied with uncovered first glyph")
	}
	// No following glyph.
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{1}, nil, 0); ok {
		t.Error("cursive applied at last glyph")
	}
	// Following glyph not covered.
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{1, 9}, nil, 0); ok {
		t.Error("cursive applied with uncovered second glyph")
	}
	// Exit anchor NULL on the first glyph (glyph 2 has no exit).
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{2, 1}, nil, 0); ok {
		t.Error("cursive applied with NULL exit anchor")
	}
	// Entry anchor NULL on the second glyph (glyph 1 has no entry).
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{1, 1}, nil, 0); ok {
		t.Error("cursive applied with NULL entry anchor")
	}
}

func TestCursivePosCoverageIndexOutOfRange(t *testing.T) {
	// Coverage covers three glyphs but only one entryExit record exists, so a
	// covered glyph can index past the anchor slices.
	st := buildCursivePos(buildCoverage1(5, 6), []eeRec{
		{entry: buildAnchor1(0, 0), exit: buildAnchor1(1, 0)},
	})
	// ci = cov[6] = 1 >= len(exit)=1.
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{6, 5}, nil, 0); ok {
		t.Error("cursive applied with exit index out of range")
	}
	// ni = cov[6] = 1 >= len(entry)=1.
	if _, ok, _ := applyPosOne(t, 3, st, []GlyphIndex{5, 6}, nil, 0); ok {
		t.Error("cursive applied with entry index out of range")
	}
}

func TestCursivePosParseErrors(t *testing.T) {
	if _, err := parseCursivePos([]byte{0, 1, 0, 6, 0, 1}); err == nil {
		t.Error("cursivepos truncated records should error")
	}
	bad := buildCursivePos(buildCoverage1(1), []eeRec{{entry: buildAnchor1(0, 0), exit: buildAnchor1(1, 0)}})
	bad[2] = 0xFF // coverageOffset
	bad[3] = 0xFF
	if _, err := parseCursivePos(bad); err == nil {
		t.Error("cursivepos bad coverage should error")
	}
	// A record pointing at a malformed anchor (bad anchor format).
	badAnchor := buildCursivePos(buildCoverage1(1), []eeRec{{entry: []byte{0, 9, 0, 0, 0, 0}, exit: nil}})
	if _, err := parseCursivePos(badAnchor); err == nil {
		t.Error("cursivepos bad entry anchor should error")
	}
	badExit := buildCursivePos(buildCoverage1(1), []eeRec{{entry: buildAnchor1(0, 0), exit: []byte{0, 9, 0, 0, 0, 0}}})
	if _, err := parseCursivePos(badExit); err == nil {
		t.Error("cursivepos bad exit anchor should error")
	}
}

// --- Anchors ----------------------------------------------------------------

func TestParseAnchorFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"format1", buildAnchor1(11, 22)},
		{"format2", buildAnchor2(11, 22, 3)},
		{"format3", buildAnchor3(11, 22)},
	} {
		a, err := parseAnchor(tc.b)
		if err != nil || a.x != 11 || a.y != 22 {
			t.Errorf("%s: anchor=%+v err=%v", tc.name, a, err)
		}
	}
	if _, err := parseAnchor([]byte{0, 9, 0, 0, 0, 0}); err == nil {
		t.Error("bad anchor format should error")
	}
	if _, err := parseAnchor([]byte{0, 1, 0}); err == nil {
		t.Error("truncated anchor should error")
	}
	if _, err := parseAnchor([]byte{0, 2, 0, 0, 0, 0}); err == nil {
		t.Error("truncated format-2 anchor point should error")
	}
	// parseAnchorAt returns nil for a NULL (zero) offset.
	if a, err := parseAnchorAt(nil, 0); a != nil || err != nil {
		t.Errorf("null anchor offset = %v, %v", a, err)
	}
}

// --- Type 4: mark-to-base ---------------------------------------------------

// baseMarkFixture builds a mark-to-base subtable: mark glyphs 10 (class0) and
// 11 (a second mark), base glyph 20; baseAnchor (300,600) for class 0.
func baseMarkFixture(markClass uint16, baseAnchor []byte) []byte {
	markArr := buildMarkArray([]markSpec{{class: markClass, anchor: buildAnchor1(50, 0)}})
	baseArr := buildAnchorMatrix(1, [][][]byte{{baseAnchor}})
	return buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(20), 1, markArr, baseArr)
}

func TestMarkBasePos(t *testing.T) {
	st := baseMarkFixture(0, buildAnchor1(300, 600))
	// Attach with advances: dx = 300-50 - advance(base)=500 -> -250; dy = 600.
	_, ok, pos := applyPosOne(t, 4, st, []GlyphIndex{20, 10}, []int{500, 0}, 1)
	if !ok || pos[1].XOffset != -250 || pos[1].YOffset != 600 {
		t.Fatalf("markbase with advances pos=%+v ok=%v", pos, ok)
	}
	// Attach with nil advances: dx = 250, dy = 600.
	_, ok, pos = applyPosOne(t, 4, st, []GlyphIndex{20, 10}, nil, 1)
	if !ok || pos[1].XOffset != 250 || pos[1].YOffset != 600 {
		t.Fatalf("markbase nil advances pos=%+v ok=%v", pos, ok)
	}
	// Mark glyph not covered.
	if _, ok, _ := applyPosOne(t, 4, st, []GlyphIndex{20, 99}, nil, 1); ok {
		t.Error("markbase applied to uncovered mark")
	}
	// No preceding base (mark first).
	if _, ok, _ := applyPosOne(t, 4, st, []GlyphIndex{10}, nil, 0); ok {
		t.Error("markbase applied with no base")
	}
	// Null base anchor -> not applied.
	stNull := baseMarkFixture(0, nil)
	if _, ok, _ := applyPosOne(t, 4, stNull, []GlyphIndex{20, 10}, nil, 1); ok {
		t.Error("markbase applied with NULL base anchor")
	}
	// Mark class beyond the base record's columns.
	stClass := baseMarkFixture(5, buildAnchor1(1, 1))
	if _, ok, _ := applyPosOne(t, 4, stClass, []GlyphIndex{20, 10}, nil, 1); ok {
		t.Error("markbase applied with class out of range")
	}
}

func TestMarkBasePosIndexGuards(t *testing.T) {
	// markCov covers two glyphs but the MarkArray holds one record: the second
	// mark glyph's coverage index runs past the array.
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(50, 0)}})
	baseArr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(300, 600)}})
	st := buildMarkPosSubtable(buildCoverage1(10, 11), buildCoverage1(20), 1, markArr, baseArr)
	if _, ok, _ := applyPosOne(t, 4, st, []GlyphIndex{20, 11}, nil, 1); ok {
		t.Error("markbase applied with mark index out of range")
	}
	// baseCov covers two glyphs but BaseArray has one row.
	st2 := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(20, 21), 1, markArr, baseArr)
	if _, ok, _ := applyPosOne(t, 4, st2, []GlyphIndex{21, 10}, nil, 1); ok {
		t.Error("markbase applied with base index out of range")
	}
}

func TestMarkBasePosParseErrors(t *testing.T) {
	if _, err := parseMarkBasePos([]byte{0, 1, 0, 12}); err == nil {
		t.Error("markbasepos truncated header should error")
	}
	good := baseMarkFixture(0, buildAnchor1(1, 1))
	for name, off := range map[string]int{"markCov": 2, "baseCov": 4, "markArr": 8, "baseArr": 10} {
		bad := append([]byte(nil), good...)
		bad[off] = 0xFF
		bad[off+1] = 0xFF
		if _, err := parseMarkBasePos(bad); err == nil {
			t.Errorf("markbasepos bad %s offset should error", name)
		}
	}
}

// --- Type 5: mark-to-ligature -----------------------------------------------

func TestMarkLigPos(t *testing.T) {
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(50, 0)}})
	// One ligature, two components; component 0 has an anchor for class 0.
	ligAttach := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(400, 700)}, {buildAnchor1(900, 700)}})
	ligArr := buildLigatureArray([][]byte{ligAttach})
	st := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30), 1, markArr, ligArr)
	_, ok, pos := applyPosOne(t, 5, st, []GlyphIndex{30, 10}, nil, 1)
	if !ok || pos[1].XOffset != 400-50 || pos[1].YOffset != 700 {
		t.Fatalf("marklig pos=%+v ok=%v", pos, ok)
	}
	// Mark not covered.
	if _, ok, _ := applyPosOne(t, 5, st, []GlyphIndex{30, 99}, nil, 1); ok {
		t.Error("marklig applied to uncovered mark")
	}
	// No ligature before the mark.
	if _, ok, _ := applyPosOne(t, 5, st, []GlyphIndex{10}, nil, 0); ok {
		t.Error("marklig applied with no ligature")
	}
	// Ligature with zero components.
	empty := buildLigatureArray([][]byte{buildAnchorMatrix(1, nil)})
	stEmpty := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30), 1, markArr, empty)
	if _, ok, _ := applyPosOne(t, 5, stEmpty, []GlyphIndex{30, 10}, nil, 1); ok {
		t.Error("marklig applied with empty ligature")
	}
	// Class out of range.
	markArr5 := buildMarkArray([]markSpec{{class: 5, anchor: buildAnchor1(50, 0)}})
	stClass := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30), 1, markArr5, ligArr)
	if _, ok, _ := applyPosOne(t, 5, stClass, []GlyphIndex{30, 10}, nil, 1); ok {
		t.Error("marklig applied with class out of range")
	}
	// Null anchor in the component.
	ligNull := buildLigatureArray([][]byte{buildAnchorMatrix(1, [][][]byte{{nil}})})
	stNull := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30), 1, markArr, ligNull)
	if _, ok, _ := applyPosOne(t, 5, stNull, []GlyphIndex{30, 10}, nil, 1); ok {
		t.Error("marklig applied with NULL component anchor")
	}
	// Mark index out of range (two covered marks, one MarkArray record).
	stMi := buildMarkPosSubtable(buildCoverage1(10, 11), buildCoverage1(30), 1, markArr, ligArr)
	if _, ok, _ := applyPosOne(t, 5, stMi, []GlyphIndex{30, 11}, nil, 1); ok {
		t.Error("marklig applied with mark index out of range")
	}
	// Ligature index out of range (two covered ligs, one LigatureArray entry).
	stLi := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30, 31), 1, markArr, ligArr)
	if _, ok, _ := applyPosOne(t, 5, stLi, []GlyphIndex{31, 10}, nil, 1); ok {
		t.Error("marklig applied with ligature index out of range")
	}
}

func TestMarkLigPosParseErrors(t *testing.T) {
	if _, err := parseMarkLigPos([]byte{0, 1, 0, 12}); err == nil {
		t.Error("markligpos truncated header should error")
	}
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(50, 0)}})
	ligArr := buildLigatureArray([][]byte{buildAnchorMatrix(1, [][][]byte{{buildAnchor1(1, 1)}})})
	good := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30), 1, markArr, ligArr)
	for name, off := range map[string]int{"markCov": 2, "ligCov": 4, "markArr": 8, "ligArr": 10} {
		bad := append([]byte(nil), good...)
		bad[off] = 0xFF
		bad[off+1] = 0xFF
		if _, err := parseMarkLigPos(bad); err == nil {
			t.Errorf("markligpos bad %s offset should error", name)
		}
	}
	// Truncated LigatureArray.
	if _, err := parseLigatureArray([]byte{0, 1}, 1); err == nil {
		t.Error("truncated ligatureArray should error")
	}
	// LigatureArray with a bad LigatureAttach offset.
	if _, err := parseLigatureArray([]byte{0, 1, 0xFF, 0xFF}, 1); err == nil {
		t.Error("ligatureArray bad attach offset should error")
	}
}

// --- Type 6: mark-to-mark ---------------------------------------------------

func TestMarkMarkPos(t *testing.T) {
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(50, 0)}})
	mark2Arr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(60, 800)}})
	st := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(12), 1, markArr, mark2Arr)
	_, ok, pos := applyPosOne(t, 6, st, []GlyphIndex{12, 10}, nil, 1)
	if !ok || pos[1].XOffset != 60-50 || pos[1].YOffset != 800 {
		t.Fatalf("markmark pos=%+v ok=%v", pos, ok)
	}
	if _, ok, _ := applyPosOne(t, 6, st, []GlyphIndex{12, 99}, nil, 1); ok {
		t.Error("markmark applied to uncovered mark")
	}
	if _, ok, _ := applyPosOne(t, 6, st, []GlyphIndex{10}, nil, 0); ok {
		t.Error("markmark applied with no base mark")
	}
	// Null mark2 anchor.
	stNull := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(12), 1, markArr, buildAnchorMatrix(1, [][][]byte{{nil}}))
	if _, ok, _ := applyPosOne(t, 6, stNull, []GlyphIndex{12, 10}, nil, 1); ok {
		t.Error("markmark applied with NULL anchor")
	}
	// mi out of range.
	stMi := buildMarkPosSubtable(buildCoverage1(10, 11), buildCoverage1(12), 1, markArr, mark2Arr)
	if _, ok, _ := applyPosOne(t, 6, stMi, []GlyphIndex{12, 11}, nil, 1); ok {
		t.Error("markmark applied with mark index out of range")
	}
	// base mark index out of range.
	stBi := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(12, 13), 1, markArr, mark2Arr)
	if _, ok, _ := applyPosOne(t, 6, stBi, []GlyphIndex{13, 10}, nil, 1); ok {
		t.Error("markmark applied with base index out of range")
	}
	// class out of range.
	markArr5 := buildMarkArray([]markSpec{{class: 5, anchor: buildAnchor1(50, 0)}})
	stClass := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(12), 1, markArr5, mark2Arr)
	if _, ok, _ := applyPosOne(t, 6, stClass, []GlyphIndex{12, 10}, nil, 1); ok {
		t.Error("markmark applied with class out of range")
	}
}

func TestMarkMarkPosParseErrors(t *testing.T) {
	if _, err := parseMarkMarkPos([]byte{0, 1, 0, 12}); err == nil {
		t.Error("markmarkpos truncated header should error")
	}
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(50, 0)}})
	mark2Arr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(1, 1)}})
	good := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(12), 1, markArr, mark2Arr)
	for name, off := range map[string]int{"mark1Cov": 2, "mark2Cov": 4, "mark1Arr": 8, "mark2Arr": 10} {
		bad := append([]byte(nil), good...)
		bad[off] = 0xFF
		bad[off+1] = 0xFF
		if _, err := parseMarkMarkPos(bad); err == nil {
			t.Errorf("markmarkpos bad %s offset should error", name)
		}
	}
}

func TestMarkArrayAndMatrixErrors(t *testing.T) {
	if _, err := parseMarkArray([]byte{0, 1}); err == nil {
		t.Error("truncated markArray should error")
	}
	if _, err := parseMarkArray([]byte{0, 1, 0, 0, 0xFF, 0xFF}); err == nil {
		t.Error("markArray bad anchor offset should error")
	}
	if _, err := parseAnchorMatrix([]byte{0, 1}, 1); err == nil {
		t.Error("truncated anchor matrix should error")
	}
	// Matrix with a bad anchor offset.
	if _, err := parseAnchorMatrix([]byte{0, 1, 0xFF, 0xFF}, 1); err == nil {
		t.Error("anchor matrix bad offset should error")
	}
}

// --- Type 7: contextual positioning -----------------------------------------

// singleAdvLookup is a type-1 lookup that adds delta to glyph g's X advance.
func singleAdvLookup(g GlyphIndex, delta int16) []byte {
	return buildLookup(1, [][]byte{buildSinglePos1(buildCoverage1(g), vfXAdvance, valueRecFull(vfXAdvance, 0, 0, delta, 0))})
}

func TestContextPos1(t *testing.T) {
	// Context: covered glyph 1 followed by glyph 2 triggers lookup 1 at seq 0.
	rule := buildPosRule([]GlyphIndex{2}, []plr{{seq: 0, lookup: 1}})
	ctx := buildContextPos1(buildCoverage1(1), [][]byte{buildPosRuleSet([][]byte{rule})})
	g := gposWith(t, "test", []uint16{0}, [][]byte{buildLookup(7, [][]byte{ctx}), singleAdvLookup(1, -30)})
	pos := g.position([]GlyphIndex{1, 2}, nil, "test")
	if pos[0].XAdvance != -30 {
		t.Fatalf("contextpos1 pos=%+v", pos)
	}
	// No match (second glyph differs) leaves positions untouched.
	if pos := g.position([]GlyphIndex{1, 9}, nil, "test"); pos[0].XAdvance != 0 {
		t.Errorf("contextpos1 unexpected apply pos=%+v", pos)
	}
	// First glyph uncovered.
	if pos := g.position([]GlyphIndex{9, 2}, nil, "test"); pos[0].XAdvance != 0 {
		t.Errorf("contextpos1 uncovered apply pos=%+v", pos)
	}
}

func TestContextPos1CoverageIndexGuard(t *testing.T) {
	// Coverage covers two glyphs but there is one rule set: the second glyph's
	// coverage index runs past the sets slice.
	rule := buildPosRule(nil, []plr{{seq: 0, lookup: 1}})
	st := buildContextPos1(buildCoverage1(1, 2), [][]byte{buildPosRuleSet([][]byte{rule})})
	pt := mustSubtable(t, 7, st)
	g := &gpos{lookups: []gposLookup{{}, {}}}
	ctx := &posContext{g: g, glyphs: []GlyphIndex{2}, positions: make([]GlyphPosition, 1)}
	if pt.apply(ctx, 0) {
		t.Error("contextpos1 applied with set index out of range")
	}
}

func TestContextPos3(t *testing.T) {
	// Three-position coverage context; on a full match, run lookup 1 at seq 1.
	st := buildContextPos3([][]byte{buildCoverage1(1), buildCoverage1(2), buildCoverage1(3)},
		[]plr{{seq: 1, lookup: 1}})
	g := gposWith(t, "test", []uint16{0}, [][]byte{buildLookup(7, [][]byte{st}), singleAdvLookup(2, -15)})
	pos := g.position([]GlyphIndex{1, 2, 3}, nil, "test")
	if pos[1].XAdvance != -15 {
		t.Fatalf("contextpos3 pos=%+v", pos)
	}
	// Run too short.
	if pos := g.position([]GlyphIndex{1, 2}, nil, "test"); pos[1].XAdvance != 0 {
		t.Errorf("contextpos3 short run applied pos=%+v", pos)
	}
	// Middle glyph mismatch.
	if pos := g.position([]GlyphIndex{1, 9, 3}, nil, "test"); pos[1].XAdvance != 0 {
		t.Errorf("contextpos3 mismatch applied pos=%+v", pos)
	}
}

func TestContextPosParseErrors(t *testing.T) {
	if _, err := parseContextPos([]byte{0}); err == nil {
		t.Error("contextpos truncated format should error")
	}
	if _, err := parseContextPos([]byte{0, 9}); err == nil {
		t.Error("contextpos bad format should error")
	}
	// Format 2 is deferred: a no-op subtable, dropped from the lookup.
	lk, err := parseGPOSLookup(buildLookup(7, [][]byte{{0, 2, 0, 0, 0, 0}}))
	if err != nil {
		t.Fatalf("contextpos format 2 parse: %v", err)
	}
	if len(lk.subtables) != 0 {
		t.Errorf("contextpos format 2 should be dropped, got %d subtables", len(lk.subtables))
	}
	// Truncated format-1 / format-3 headers and bad coverage.
	if _, err := parseContextPos1([]byte{0, 1, 0, 6, 0, 1}); err == nil {
		t.Error("contextpos1 truncated should error")
	}
	if _, err := parseContextPos3([]byte{0, 3, 0, 1, 0, 0}); err == nil {
		t.Error("contextpos3 truncated should error")
	}
	badCov1 := buildContextPos1(buildCoverage1(1), [][]byte{buildPosRuleSet([][]byte{buildPosRule(nil, nil)})})
	badCov1[2], badCov1[3] = 0xFF, 0xFF
	if _, err := parseContextPos1(badCov1); err == nil {
		t.Error("contextpos1 bad coverage should error")
	}
	// Bad rule-set offset (the first offset field sits at byte 6).
	badSet1 := buildContextPos1(buildCoverage1(1), [][]byte{buildPosRuleSet([][]byte{buildPosRule(nil, nil)})})
	badSet1[6], badSet1[7] = 0xFF, 0xFF
	if _, err := parseContextPos1(badSet1); err == nil {
		t.Error("contextpos1 bad rule-set offset should error")
	}
	badCov3 := buildContextPos3([][]byte{buildCoverage1(1)}, []plr{{seq: 0, lookup: 0}})
	// The single coverage offset field sits at byte 6 (after format, glyphCount,
	// posLookupCount).
	badCov3[6], badCov3[7] = 0xFF, 0xFF
	if _, err := parseContextPos3(badCov3); err == nil {
		t.Error("contextpos3 bad coverage should error")
	}
	// PosRuleSet / PosRule truncations.
	if _, err := parsePosRuleSet([]byte{0, 1}); err == nil {
		t.Error("truncated posRuleSet should error")
	}
	if _, err := parsePosRuleSet([]byte{0, 1, 0xFF, 0xFF}); err == nil {
		t.Error("posRuleSet bad rule offset should error")
	}
	if _, err := parsePosRule([]byte{0, 2, 0, 1}); err == nil {
		t.Error("truncated posRule should error")
	}
}

// --- Type 8: chaining contextual positioning --------------------------------

func TestChainPos1(t *testing.T) {
	// backtrack [1], input [2 (covered), 3], lookahead [4] -> run lookup 1 at 2.
	rule := buildPosChainRule([]GlyphIndex{1}, []GlyphIndex{3}, []GlyphIndex{4}, []plr{{seq: 0, lookup: 1}})
	st := buildContextPos1(buildCoverage1(2), [][]byte{buildPosRuleSet([][]byte{rule})})
	g := gposWith(t, "test", []uint16{0}, [][]byte{buildLookup(8, [][]byte{st}), singleAdvLookup(2, -25)})
	pos := g.position([]GlyphIndex{1, 2, 3, 4}, nil, "test")
	if pos[1].XAdvance != -25 {
		t.Fatalf("chainpos1 pos=%+v", pos)
	}
	// Backtrack mismatch.
	if pos := g.position([]GlyphIndex{9, 2, 3, 4}, nil, "test"); pos[1].XAdvance != 0 {
		t.Errorf("chainpos1 backtrack mismatch applied pos=%+v", pos)
	}
	// Input mismatch.
	if pos := g.position([]GlyphIndex{1, 2, 9, 4}, nil, "test"); pos[1].XAdvance != 0 {
		t.Errorf("chainpos1 input mismatch applied pos=%+v", pos)
	}
	// Lookahead mismatch.
	if pos := g.position([]GlyphIndex{1, 2, 3, 9}, nil, "test"); pos[1].XAdvance != 0 {
		t.Errorf("chainpos1 lookahead mismatch applied pos=%+v", pos)
	}
	// First glyph uncovered.
	if pos := g.position([]GlyphIndex{1, 9, 3, 4}, nil, "test"); pos[1].XAdvance != 0 {
		t.Errorf("chainpos1 uncovered applied pos=%+v", pos)
	}
}

func TestChainPos1CoverageIndexGuard(t *testing.T) {
	rule := buildPosChainRule(nil, nil, nil, []plr{{seq: 0, lookup: 1}})
	st := buildContextPos1(buildCoverage1(1, 2), [][]byte{buildPosRuleSet([][]byte{rule})})
	pt := mustSubtable(t, 8, st)
	ctx := &posContext{g: &gpos{lookups: make([]gposLookup, 2)}, glyphs: []GlyphIndex{2}, positions: make([]GlyphPosition, 1)}
	if pt.apply(ctx, 0) {
		t.Error("chainpos1 applied with set index out of range")
	}
}

func TestChainPos3(t *testing.T) {
	st := buildChainPos3(
		[][]byte{buildCoverage1(1)},                    // backtrack
		[][]byte{buildCoverage1(2), buildCoverage1(3)}, // input
		[][]byte{buildCoverage1(4)},                    // lookahead
		[]plr{{seq: 1, lookup: 1}})
	g := gposWith(t, "test", []uint16{0}, [][]byte{buildLookup(8, [][]byte{st}), singleAdvLookup(3, -12)})
	pos := g.position([]GlyphIndex{1, 2, 3, 4}, nil, "test")
	if pos[2].XAdvance != -12 {
		t.Fatalf("chainpos3 pos=%+v", pos)
	}
	// Backtrack off the front (j<0): apply at i=0.
	pt := mustSubtable(t, 8, st)
	ctx := &posContext{g: g, glyphs: []GlyphIndex{2, 3, 4}, positions: make([]GlyphPosition, 3)}
	if pt.apply(ctx, 0) {
		t.Error("chainpos3 applied with backtrack before start")
	}
	// Backtrack glyph mismatch.
	if pos := g.position([]GlyphIndex{9, 2, 3, 4}, nil, "test"); pos[2].XAdvance != 0 {
		t.Errorf("chainpos3 backtrack mismatch pos=%+v", pos)
	}
	// Input mismatch.
	if pos := g.position([]GlyphIndex{1, 2, 9, 4}, nil, "test"); pos[2].XAdvance != 0 {
		t.Errorf("chainpos3 input mismatch pos=%+v", pos)
	}
	// Input runs off the end.
	ctx2 := &posContext{g: g, glyphs: []GlyphIndex{1, 2}, positions: make([]GlyphPosition, 2)}
	if pt.apply(ctx2, 1) {
		t.Error("chainpos3 applied with input past end")
	}
	// Lookahead mismatch.
	if pos := g.position([]GlyphIndex{1, 2, 3, 9}, nil, "test"); pos[2].XAdvance != 0 {
		t.Errorf("chainpos3 lookahead mismatch pos=%+v", pos)
	}
	// Lookahead runs off the end.
	ctx3 := &posContext{g: g, glyphs: []GlyphIndex{1, 2, 3}, positions: make([]GlyphPosition, 3)}
	if pt.apply(ctx3, 1) {
		t.Error("chainpos3 applied with lookahead past end")
	}
}

func TestChainPosParseErrors(t *testing.T) {
	if _, err := parseChainPos([]byte{0}); err == nil {
		t.Error("chainpos truncated format should error")
	}
	if _, err := parseChainPos([]byte{0, 9}); err == nil {
		t.Error("chainpos bad format should error")
	}
	lk, err := parseGPOSLookup(buildLookup(8, [][]byte{{0, 2, 0, 0, 0, 0}}))
	if err != nil {
		t.Fatalf("chainpos format 2 parse: %v", err)
	}
	if len(lk.subtables) != 0 {
		t.Errorf("chainpos format 2 should be dropped, got %d", len(lk.subtables))
	}
	if _, err := parseChainPos1([]byte{0, 1, 0, 6, 0, 1}); err == nil {
		t.Error("chainpos1 truncated should error")
	}
	if _, err := parseChainPos3([]byte{0, 3, 0, 1, 0, 0}); err == nil {
		t.Error("chainpos3 truncated should error")
	}
	badCov1 := buildContextPos1(buildCoverage1(1), [][]byte{buildPosRuleSet([][]byte{buildPosChainRule(nil, nil, nil, nil)})})
	badCov1[2], badCov1[3] = 0xFF, 0xFF
	if _, err := parseChainPos1(badCov1); err == nil {
		t.Error("chainpos1 bad coverage should error")
	}
	// Bad rule-set offset (first offset field at byte 6).
	badSet1 := buildContextPos1(buildCoverage1(1), [][]byte{buildPosRuleSet([][]byte{buildPosChainRule(nil, nil, nil, nil)})})
	badSet1[6], badSet1[7] = 0xFF, 0xFF
	if _, err := parseChainPos1(badSet1); err == nil {
		t.Error("chainpos1 bad rule-set offset should error")
	}
	// Bad coverage in each of the three chainpos3 slices.
	for _, tc := range []struct {
		name               string
		back, input, ahead [][]byte
	}{
		{"backtrack", [][]byte{buildCoverage1(1)}, nil, nil},
		{"input", nil, [][]byte{buildCoverage1(1)}, nil},
		{"lookahead", nil, nil, [][]byte{buildCoverage1(1)}},
	} {
		blob := buildChainPos3(tc.back, tc.input, tc.ahead, nil)
		// The first coverage offset sits right after the header; corrupt it.
		hdr := 2 + (2 + 2*len(tc.back)) + (2 + 2*len(tc.input)) + (2 + 2*len(tc.ahead)) + 2
		// find the single non-zero offset slot: it is the one at the covered list.
		off := headerOffsetOfFirstCov(tc.back, tc.input, tc.ahead)
		_ = hdr
		blob[off], blob[off+1] = 0xFF, 0xFF
		if _, err := parseChainPos3(blob); err == nil {
			t.Errorf("chainpos3 bad %s coverage should error", tc.name)
		}
	}
	// ChainPosRuleSet / ChainPosRule truncations.
	if _, err := parsePosChainRuleSet([]byte{0, 1}); err == nil {
		t.Error("truncated chainRuleSet should error")
	}
	if _, err := parsePosChainRuleSet([]byte{0, 1, 0xFF, 0xFF}); err == nil {
		t.Error("chainRuleSet bad offset should error")
	}
	if _, err := parsePosChainRule([]byte{0, 1}); err == nil {
		t.Error("truncated chainRule should error")
	}
}

// headerOffsetOfFirstCov returns the byte offset of the first coverage offset
// field in a chainpos3 blob for the given (back, input, ahead) shapes.
func headerOffsetOfFirstCov(back, input, ahead [][]byte) int {
	off := 2 + 2 // format + backtrackGlyphCount
	if len(back) > 0 {
		return off
	}
	off += 2 // inputGlyphCount
	if len(input) > 0 {
		return off
	}
	off += 2 // lookaheadGlyphCount
	return off
}

// --- Type 9: extension positioning ------------------------------------------

func TestExtensionPos(t *testing.T) {
	inner := buildSinglePos1(buildCoverage1(7), vfXAdvance, valueRecFull(vfXAdvance, 0, 0, -33, 0))
	ext := buildExtensionPos(1, inner) // extensionLookupType 1 (single pos)
	got, ok, _ := applyPosOne(t, 9, ext, []GlyphIndex{7}, nil, 0)
	if !ok || got.XAdvance != -33 {
		t.Fatalf("extension pos = %+v ok=%v", got, ok)
	}
	// Nested extension (type 9 inside 9) is rejected.
	if _, err := parseExtensionPos(buildExtensionPos(9, []byte{0, 1})); err == nil {
		t.Error("nested extension should error")
	}
	// Truncated extension header.
	if _, err := parseExtensionPos([]byte{0, 1, 0, 1}); err == nil {
		t.Error("truncated extension should error")
	}
	// Inner parse error propagates.
	if _, err := parseExtensionPos(buildExtensionPos(1, []byte{0, 9, 0, 0, 0, 0})); err == nil {
		t.Error("extension inner parse error should propagate")
	}
}

func TestParseGPOSSubtableUnknownType(t *testing.T) {
	if _, err := parseGPOSSubtable(42, []byte{0, 0}); err == nil {
		t.Error("unknown gpos lookup type should error")
	}
}

// --- run application, selection, position -----------------------------------

func TestPositionRunAndSelection(t *testing.T) {
	// Two lookups both activated by "kern": a single adjustment and a pair.
	single := singleAdvLookup(5, 7)
	pp := buildLookup(2, [][]byte{buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -40}}),
	})})
	g := twoLookupKern(t, single, pp)
	pos := g.position([]GlyphIndex{5, 6}, nil, "kern")
	if pos[0].XAdvance != 7-40 { // single (+7) then pair (-40) both on glyph 5
		t.Fatalf("position run pos=%+v", pos)
	}
	// Empty run returns an empty slice.
	if p := g.position(nil, nil, "kern"); len(p) != 0 {
		t.Errorf("empty run position = %v", p)
	}
	// No matching feature leaves everything zero.
	if p := g.position([]GlyphIndex{5, 6}, nil, "zzzz"); p[0] != (GlyphPosition{}) {
		t.Errorf("no-feature position = %+v", p)
	}
	// Pair on the last glyph (no following glyph) does not apply.
	if p := g.position([]GlyphIndex{5}, nil, "kern"); p[0].XAdvance != 7 {
		t.Errorf("single-only position = %+v", p)
	}
}

// twoLookupKern builds a GPOS whose DFLT default langsys activates a single
// "kern" feature listing both provided lookups (indices 0 and 1).
func twoLookupKern(t *testing.T, lk0, lk1 []byte) *gpos {
	t.Helper()
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "kern", lookups: []uint16{0, 1}}}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, [][]byte{lk0, lk1}))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	return g
}

func TestPairPos2Apply(t *testing.T) {
	// A format-2 pair applied through position adjusts the first glyph's advance.
	pp2 := buildPairPos2(
		buildCoverage1(10),
		buildClassDef1(10, 1),
		buildClassDef1(6, 1),
		vfXAdvance, 0, 2, 2, []int16{0, 0, 0, -60},
	)
	g := twoLookupKern(t, buildLookup(2, [][]byte{pp2}), singleAdvLookup(99, 0))
	pos := g.position([]GlyphIndex{10, 6}, nil, "kern")
	if pos[0].XAdvance != -60 {
		t.Fatalf("pairpos2 apply pos=%+v", pos)
	}
	// First glyph not covered -> no change.
	if pos := g.position([]GlyphIndex{7, 6}, nil, "kern"); pos[0].XAdvance != 0 {
		t.Errorf("pairpos2 uncovered pos=%+v", pos)
	}
}

func TestSelectLookupsDefaultRealFontFallback(t *testing.T) {
	// Reproduce the real-font layout that used to return 0 kern: a bare DFLT
	// script with no kern feature, and the kern feature registered only under a
	// real script (latn) default langsys.
	scripts := []tScript{
		{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{}}},
		{tag: "latn", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}},
	}
	feats := []tFeature{{tag: "kern", lookups: []uint16{0}}}
	lookups := [][]byte{buildLookup(2, [][]byte{
		buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
			buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -50}}),
		}),
	})}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	if got := g.Kern(5, 6); got != -50 {
		t.Errorf("real-font fallback Kern(5,6)=%d want -50", got)
	}
	// position also resolves via the fallback.
	if pos := g.position([]GlyphIndex{5, 6}, nil, "kern"); pos[0].XAdvance != -50 {
		t.Errorf("real-font fallback position=%+v", pos)
	}
}

func TestApplyLookupBounds(t *testing.T) {
	g := &gpos{lookups: []gposLookup{{}}}
	ctx := &posContext{g: g, glyphs: []GlyphIndex{1}, positions: make([]GlyphPosition, 1)}
	// applyLookup out-of-range indices are no-ops.
	g.applyLookup(-1, ctx)
	g.applyLookup(99, ctx)
	// applyLookupAt boundary conditions.
	if g.applyLookupAt(-1, ctx, 0) {
		t.Error("applyLookupAt negative lookup")
	}
	if g.applyLookupAt(99, ctx, 0) {
		t.Error("applyLookupAt lookup out of range")
	}
	if g.applyLookupAt(0, ctx, -1) {
		t.Error("applyLookupAt negative index")
	}
	if g.applyLookupAt(0, ctx, 99) {
		t.Error("applyLookupAt index out of range")
	}
}

func TestMatchHelpers(t *testing.T) {
	glyphs := []GlyphIndex{10, 11, 12}
	if !matchGlyphs(glyphs, 0, []GlyphIndex{10, 11}) {
		t.Error("matchGlyphs should match")
	}
	if matchGlyphs(glyphs, 2, []GlyphIndex{12, 13}) { // runs off the end
		t.Error("matchGlyphs overrun should fail")
	}
	if matchGlyphs(glyphs, -1, []GlyphIndex{10}) { // negative start
		t.Error("matchGlyphs negative start should fail")
	}
	if matchGlyphs(glyphs, 0, []GlyphIndex{10, 99}) { // value mismatch
		t.Error("matchGlyphs mismatch should fail")
	}
	if !matchBacktrack(glyphs, 2, []GlyphIndex{11, 10}) {
		t.Error("matchBacktrack should match")
	}
	if matchBacktrack(glyphs, 1, []GlyphIndex{10, 9}) { // second step off the front
		t.Error("matchBacktrack overrun should fail")
	}
	if matchBacktrack(glyphs, 2, []GlyphIndex{99}) { // value mismatch
		t.Error("matchBacktrack mismatch should fail")
	}
}

func TestPairPosApplyMiss(t *testing.T) {
	// pairPos applied through position where the first glyph is covered but the
	// second has no pair entry: the kern lookup does not adjust.
	pp := buildLookup(2, [][]byte{buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -40}}),
	})})
	g := twoLookupKern(t, singleAdvLookup(5, 7), pp)
	pos := g.position([]GlyphIndex{5, 9}, nil, "kern")
	if pos[0].XAdvance != 7 { // single adds 7; pair(5,9) has no entry
		t.Fatalf("pairpos miss pos=%+v", pos)
	}
}

func TestMarkScanSkipsNonBase(t *testing.T) {
	// A non-base glyph between the base and the mark forces the scan to continue
	// past it. Exercised for all three mark-attachment types.
	markArr := buildMarkArray([]markSpec{{class: 0, anchor: buildAnchor1(50, 0)}})
	baseArr := buildAnchorMatrix(1, [][][]byte{{buildAnchor1(300, 600)}})

	mb := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(20), 1, markArr, baseArr)
	if _, ok, _ := applyPosOne(t, 4, mb, []GlyphIndex{20, 99, 10}, nil, 2); !ok {
		t.Error("markbase should skip the filler glyph and attach")
	}
	ligArr := buildLigatureArray([][]byte{buildAnchorMatrix(1, [][][]byte{{buildAnchor1(300, 600)}})})
	ml := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(30), 1, markArr, ligArr)
	if _, ok, _ := applyPosOne(t, 5, ml, []GlyphIndex{30, 99, 10}, nil, 2); !ok {
		t.Error("marklig should skip the filler glyph and attach")
	}
	mm := buildMarkPosSubtable(buildCoverage1(10), buildCoverage1(12), 1, markArr, baseArr)
	if _, ok, _ := applyPosOne(t, 6, mm, []GlyphIndex{12, 99, 10}, nil, 2); !ok {
		t.Error("markmark should skip the filler glyph and attach")
	}
}

func TestApplyLookupAtNoMatch(t *testing.T) {
	// A valid lookup whose subtable does not match at the position returns false
	// through the final fall-through.
	single := singleAdvLookup(5, 1)
	g := gposWith(t, "test", []uint16{0}, [][]byte{single})
	ctx := &posContext{g: g, glyphs: []GlyphIndex{9}, positions: make([]GlyphPosition, 1)}
	if g.applyLookupAt(0, ctx, 0) {
		t.Error("applyLookupAt should not match an uncovered glyph")
	}
}

func TestKernFallbackRequiredAndOutOfRangeFeature(t *testing.T) {
	// DFLT has no kern; latn's default LangSys resolves kern through its required
	// feature and also lists an out-of-range feature index, exercising both
	// featureLookups branches via the script-agnostic fallback.
	scripts := []tScript{
		{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{}}},
		{tag: "latn", def: &tLangSys{required: 0, features: []uint16{99}}},
	}
	feats := []tFeature{{tag: "kern", lookups: []uint16{0}}}
	lookups := [][]byte{buildLookup(2, [][]byte{
		buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
			buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -50}}),
		}),
	})}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	if got := g.Kern(5, 6); got != -50 {
		t.Errorf("required-feature fallback Kern(5,6)=%d want -50", got)
	}
}

func TestKernSkipsNonPairSubtable(t *testing.T) {
	// The kern feature activates a single-adjustment (non-pair) lookup ahead of
	// the real pair lookup; Kern skips the non-pair subtable.
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "kern", lookups: []uint16{0, 1}}}
	lookups := [][]byte{
		singleAdvLookup(5, 3), // type 1, not a pairPos
		buildLookup(2, [][]byte{buildPairPos1(buildCoverage1(5), vfXAdvance, 0, [][]byte{
			buildPairSet(vfXAdvance, 0, []pv{{second: 6, xa1: -50}}),
		})}),
	}
	g, err := parseGPOS(buildLayoutTable(scripts, feats, lookups))
	if err != nil {
		t.Fatalf("parseGPOS: %v", err)
	}
	if got := g.Kern(5, 6); got != -50 {
		t.Errorf("Kern skipping non-pair subtable = %d want -50", got)
	}
}

func TestParseGPOSAdvancedLookupInLayout(t *testing.T) {
	// A GPOS carrying one of every advanced lookup type parses without error and
	// each lookup keeps a subtable, ensuring parseGPOSSubtable's dispatch is
	// wholly reachable through the public parse path.
	lookups := [][]byte{
		singleAdvLookup(1, 1),
		buildLookup(3, [][]byte{buildCursivePos(buildCoverage1(1), []eeRec{{entry: buildAnchor1(0, 0), exit: buildAnchor1(1, 0)}})}),
		buildLookup(9, [][]byte{buildExtensionPos(1, buildSinglePos1(buildCoverage1(1), vfXAdvance, valueRecFull(vfXAdvance, 0, 0, 1, 0)))}),
	}
	g := gposWith(t, "test", []uint16{0, 1, 2}, lookups)
	if len(g.lookups) != 3 {
		t.Fatalf("expected 3 lookups, got %d", len(g.lookups))
	}
	for i, lk := range g.lookups {
		if len(lk.subtables) != 1 {
			t.Errorf("lookup %d has %d subtables", i, len(lk.subtables))
		}
	}
}
