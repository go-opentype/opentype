// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"testing"
)

// The decoders of issue #75 are also entered directly, with a table that
// declares a count its bytes cannot back. Each case here is the smallest table
// that does so; the decoder must refuse it with errDeclaredCount (or, where the
// count is sound and the table is merely short, with errTruncated) and must not
// build what the count names.
func TestDecodersRefuseDeclaredCounts(t *testing.T) {
	const big = 0xFFFF
	mv := func(b []byte) []byte { return b }
	cases := []struct {
		name     string
		call     func() error
		declared bool // errDeclaredCount, else plain errTruncated
	}{
		{"script langSysCount", func() error { _, err := parseScript(bu16s(0, big)); return err }, true},
		{"langSys featureIndexCount", func() error { _, err := parseLangSys(bu16s(0, 0, big)); return err }, true},
		{"featureList featureCount", func() error { _, err := parseFeatureList(bu16s(big)); return err }, true},
		{"feature lookupIndexCount", func() error { _, err := parseFeature(bu16s(0, big)); return err }, true},
		{"ligatureSet ligatureCount", func() error { _, err := parseLigatureSet(bu16s(big)); return err }, true},
		{"ligature componentCount", func() error { _, err := parseLigature(bu16s(1, big)); return err }, true},
		{"MATH kernInfo mathKernCount", func() error { _, err := parseMathKernInfo(bu16s(0, big)); return err }, true},
		{"MATH variants counts", func() error { return (&mathTable{}).parseVariants(bu16s(0, 0, 0, big, big)) }, true},
		{"MATH glyph assembly partCount", func() error { _, err := parseGlyphAssembly(mv(bu16s(0, 0, big))); return err }, true},
		{"ItemVariationStore ivdCount", func() error {
			w := &bw{}
			w.u16(1)
			w.u32(0)
			w.u16(big)
			_, err := parseMetricVarStore(w.bytes(), 0)
			return err
		}, true},
		{"ItemVariationData regionIndexCount", func() error { _, err := parseItemVarData(bu16s(0, 0, big), 0, 0); return err }, true},
		{"ItemVariationData deltas short", func() error {
			// two items of one 16-bit delta need 4 bytes; the 2 present pass the
			// one-byte-per-delta floor and fall short of the real width.
			_, err := parseItemVarData(joinB(bu16s(2, 1, 1, 0), zeros(2)), 0, 1)
			return err
		}, false},
		{"single subst 2 header short", func() error { _, err := parseGSUBSingle(bu16s(2)); return err }, false},
		{"ligature subst header short", func() error { _, err := parseGSUBLigature(bu16s(1)); return err }, false},
		{"MATH per-glyph values header short", func() error { _, err := parsePerGlyphValues(nil); return err }, false},
		{"DeltaSetIndexMap header short", func() error { _, err := parseDeltaSetMap([]byte{0, 0}, 1); return err }, false},
		{"PairPos2 header short, no value formats", func() error { _, err := parsePairPos2(bu16s(2, 0, 0, 0)); return err }, false},
		{"GSUB lookup markFilteringSet short", func() error {
			_, err := parseGSUBLookup(bu16s(1, flagUseMarkFilteringSet, 0))
			return err
		}, false},
		{"fvar axis record shorter than its fields", func() error {
			// one axis declared as 4 bytes, which fit, and 20 are read.
			b := joinB(bu16s(1, 0, 16, 0, 1, 4, 0, 0), zeros(4))
			return (&Font{}).parseFvar(b)
		}, false},
		{"fvar instance record shorter than its fields", func() error {
			b := joinB(bu16s(1, 0, 16, 0, 0, 20, 1, 2), zeros(2))
			return (&Font{}).parseFvar(b)
		}, false},
	}
	for _, c := range cases {
		err := c.call()
		if err == nil {
			t.Errorf("%s: parsed", c.name)
			continue
		}
		if !errors.Is(err, errTruncated) {
			t.Errorf("%s: %v is not errTruncated", c.name, err)
		}
		if got := errors.Is(err, errDeclaredCount); got != c.declared {
			t.Errorf("%s: errDeclaredCount = %v, want %v (%v)", c.name, got, c.declared, err)
		}
	}
}

// refusal runs f and returns its error; a panic is reported as a failure of the
// test, not left to take the whole run down, so that a guard that is removed
// fails an assertion about what it refuses.
func refusal(t *testing.T, name string, f func() error) (err error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("%s: panicked: %v", name, p)
			err = nil
		}
	}()
	return f()
}

// An offset that names a byte outside its table is refused, on either side of
// the last byte that holds what the decoder reads first.
func TestOffsetsOutsideTheTableAreRefused(t *testing.T) {
	// cmap record offsets are uint32: 0xFFFFFFFF must not wrap on a 32-bit build.
	rec := func(off uint32) []byte {
		w := &bw{}
		w.u16(0)
		w.u16(1)
		w.u16(3)
		w.u16(1)
		w.u32(off)
		return w.bytes()
	}
	for _, off := range []uint32{0xFFFFFFFF, 0x80000000, 12, 11} { // the table is 12 bytes
		off := off
		err := refusal(t, "cmap record offset", func() error { return (&Font{}).parseCmap(rec(off)) })
		if !errors.Is(err, errTruncated) {
			t.Errorf("cmap record offset %#x: %v, want errTruncated", off, err)
		}
	}
	sub := zeros(16)
	for _, off := range []int{len(sub) + 1, 1 << 30, -1, -1 << 30} {
		off := off
		err := refusal(t, "UVS default", func() error { _, e := parseUVSDefaultTable(sub, off, &allocBudget{left: 1 << 20}); return e })
		if !errors.Is(err, errTruncated) {
			t.Errorf("default UVS offset %d: %v, want errTruncated", off, err)
		}
		err = refusal(t, "UVS non-default", func() error { _, e := parseUVSNonDefaultTable(sub, off, &allocBudget{left: 1 << 20}); return e })
		if !errors.Is(err, errTruncated) {
			t.Errorf("non-default UVS offset %d: %v, want errTruncated", off, err)
		}
	}
}

// readU16s returns nothing for a count that is not positive and refuses, without
// reading, a count the table cannot hold.
func TestReadU16sBounds(t *testing.T) {
	r := reader{b: zeros(4)}
	if got := readU16s(&r, -3); got != nil || r.err != nil {
		t.Errorf("readU16s(-3) = %v, err %v", got, r.err)
	}
	if got := readU16s(&r, 2); len(got) != 2 || r.err != nil {
		t.Errorf("readU16s(2) on 4 bytes = %v, err %v", got, r.err)
	}
	r = reader{b: zeros(4)}
	var got []uint16
	_ = refusal(t, "readU16s", func() error { got = readU16s(&r, 3); return nil })
	if got != nil || !errors.Is(r.err, errTruncated) {
		t.Errorf("readU16s(3) on 4 bytes = %v, err %v, want nil and errTruncated", got, r.err)
	}
}

// Font.budget is the Parse's own budget when there is one, and a fresh one sized
// from the table otherwise.
func TestFontBudget(t *testing.T) {
	own := &allocBudget{left: 5}
	f := &Font{bud: own}
	if got := f.budget(1 << 20); got != own {
		t.Errorf("budget() = %p, want the font's own %p", got, own)
	}
	fresh := (&Font{}).budget(10)
	if want := newAllocBudget(10); fresh == nil || fresh.left != want.left {
		t.Errorf("budget() on a font with none = %+v, want %+v", fresh, want)
	}
}

// The bytes a sequence of records needs are checked before any is read.
func TestSeqLookupRecordsRefuseShortTable(t *testing.T) {
	r := reader{b: zeros(4)}
	if got := parseSeqLookupRecords(&r, 3); got != nil || !errors.Is(r.err, errTruncated) {
		t.Errorf("parseSeqLookupRecords(3) on 4 bytes = %v, err %v", got, r.err)
	}
}

// A decoder handed a budget that is spent must refuse what it would still
// build: the budget is shared by every decoder of one Parse.
func TestDecodersRefuseSpentBudget(t *testing.T) {
	spent := func() *allocBudget { return &allocBudget{} }
	// format 4: no segments, then a glyphIdArray of 32 entries.
	f4 := joinB(bu16s(4, 0, 0, 0), zeros(8), zeros(64))
	// format 2: 256 keys naming subheader 0, one subheader, a glyphIndexArray.
	f2 := joinB(bu16s(2, 0, 0), zeros(512), zeros(8), zeros(64))
	// format 14: one record, which needs room in the budget for its decoded form.
	w := &bw{}
	w.u16(14)
	w.u32(0)
	w.u32(1)
	w.u24(0xFE00)
	w.u32(0)
	w.u32(0)
	cases := []struct {
		name string
		call func() error
	}{
		{"format 4 glyphIdArray", func() error { _, err := parseCmap4(f4, spent()); return err }},
		{"format 2 glyphIndexArray", func() error {
			_, err := parseCmap2(f2, &allocBudget{left: 8}) // the subheader fits, the array does not
			return err
		}},
		{"format 14 records", func() error { _, err := parseCmap14(w.bytes(), spent()); return err }},
	}
	for _, c := range cases {
		if err := c.call(); !errors.Is(err, errAllocBudget) {
			t.Errorf("%s: %v, want errAllocBudget", c.name, err)
		}
	}
}

// A format-4 subtable whose segments fit but which ends before its reservedPad.
func TestCmap4RefusesMissingReservedPad(t *testing.T) {
	// one segment: segCountX2 = 2, then 4 arrays of one uint16 = 8 bytes, and no
	// pad between endCode and startCode.
	sub := joinB(bu16s(4, 0, 0, 2, 0, 0, 0), zeros(8))
	if _, err := parseCmap4(sub); !errors.Is(err, errTruncated) {
		t.Errorf("parseCmap4 = %v, want errTruncated", err)
	}
}

// reader.tag and reader.u24 fail soft: a short read sets the error and yields zero.
func TestReaderShortTagAndU24(t *testing.T) {
	r := reader{b: []byte{1, 2}}
	if got := r.tag(); got != "" || !errors.Is(r.err, errTruncated) {
		t.Errorf("tag() = %q, err %v", got, r.err)
	}
	r = reader{b: []byte{1, 2}}
	if got := r.u24(); got != 0 || !errors.Is(r.err, errTruncated) {
		t.Errorf("u24() = %d, err %v", got, r.err)
	}
}
