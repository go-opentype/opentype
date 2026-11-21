// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Bomb tests for issue #75, Parse half: each case is a font of a few hundred
// bytes to a few hundred kilobytes whose tables declare counts or share offsets
// so that an unguarded Parse would allocate gigabytes. A case either must be
// REFUSED with a specific error, or, where a shared table is decoded once and
// the font is legitimate, must parse while allocating only what it holds.
//
// bombAllocLimit is the ceiling on what Parse may allocate for any of them.
// It is the test's own limit, far below what the unguarded code would take (the
// smallest bomb here would take over 60 MB), and unrelated to the budget.
const bombAllocLimit = 6 << 20

// bombFont is a standard font with one table replaced or added.
func bombFont(tag string, table []byte) []byte {
	tables := stdTables(false, false)
	tables[tag] = table
	return assemble(versionTrueType, tables)
}

func bu16s(v ...uint16) []byte {
	w := &bw{}
	for _, x := range v {
		w.u16(x)
	}
	return w.bytes()
}

func joinB(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func zeros(n int) []byte { return make([]byte, n) }

// repeatU16 returns n copies of v as big-endian uint16s.
func repeatU16(n int, v uint16) []byte {
	w := &bw{}
	for i := 0; i < n; i++ {
		w.u16(v)
	}
	return w.bytes()
}

func cmapHeader12(format uint16, numGroups uint32) []byte {
	w := &bw{}
	w.u16(format)
	w.u16(0)
	w.u32(0) // length (not read)
	w.u32(0) // language
	w.u32(numGroups)
	return w.bytes()
}

// bombCase is one font and the verdict Parse must reach.
type bombCase struct {
	name string
	font []byte
	want error  // the refusal, or nil when the font must parse
	text string // a phrase the refusal must contain
}

func bombCases() []bombCase {
	var cs []bombCase
	add := func(name string, font []byte, want error, text string) {
		cs = append(cs, bombCase{name, font, want, text})
	}

	// --- cmap, 32-bit counts -------------------------------------------------
	add("cmap12-numGroups", bombFont("cmap", cmapTable([][]byte{cmapHeader12(12, 0xFFFFFFFF)})),
		errDeclaredCount, "cmap format 12 groups")
	add("cmap13-numGroups", bombFont("cmap", cmapTable([][]byte{cmapHeader12(13, 0xFFFFFFFF)})),
		errDeclaredCount, "cmap format 13 groups")
	c8 := &bw{}
	c8.u16(8)
	c8.u16(0)
	c8.u32(0)
	c8.u32(0)
	c8.b = append(c8.b, zeros(8192)...)
	c8.u32(0xFFFFFFFF)
	add("cmap8-numGroups", bombFont("cmap", cmapTable([][]byte{c8.bytes()})),
		errDeclaredCount, "cmap format 8 groups")
	c10 := &bw{}
	c10.u16(10)
	c10.u16(0)
	c10.u32(0)
	c10.u32(0)
	c10.u32(0) // startCharCode
	c10.u32(0xFFFFFFFF)
	add("cmap10-numChars", bombFont("cmap", cmapTable([][]byte{c10.bytes()})),
		errDeclaredCount, "cmap format 10")
	c14 := &bw{}
	c14.u16(14)
	c14.u32(0)
	c14.u32(0xFFFFFFFF)
	add("cmap14-numRecords", bombFont("cmap", cmapTable([][]byte{c14.bytes()})),
		errDeclaredCount, "cmap format 14 records")
	for _, sub := range []struct{ name, text string }{
		{"cmap14-default-numRanges", "default UVS ranges"},
		{"cmap14-nondefault-numMappings", "non-default UVS mappings"},
	} {
		w := &bw{}
		w.u16(14)
		w.u32(0)
		w.u32(1) // one record, 11 bytes, so the table starts at 21
		w.u24(0xFE00)
		if strings.HasPrefix(sub.name, "cmap14-default") {
			w.u32(21)
			w.u32(0)
		} else {
			w.u32(0)
			w.u32(21)
		}
		w.u32(0xFFFFFFFF)
		add(sub.name, bombFont("cmap", cmapTable([][]byte{w.bytes()})), errDeclaredCount, sub.text)
	}
	// Fifty records on one 16000-group subtable: 192 KB of font, decoded once.
	// Decoded per record it would be 9.6 MB, over bombAllocLimit.
	shared := joinB(cmapHeader12(12, 16000), zeros(16000*12))
	ct := &bw{}
	ct.u16(0)
	ct.u16(50)
	for i := 0; i < 50; i++ {
		ct.u16(3)
		ct.u16(uint16(i + 1))
		ct.u32(uint32(4 + 8*50))
	}
	add("cmap-records-share-subtable", bombFont("cmap", joinB(ct.bytes(), shared)), nil, "")

	// --- CFF2 INDEX count (u32) ---------------------------------------------
	// A font needs a CFF2 table, which the standard TrueType set does not have;
	// the decoder is entered directly in TestBombCFF2Index.

	// --- GPOS ---------------------------------------------------------------
	pairPos2 := func(vf1 uint16, class1, class2 uint16, grid []byte) []byte {
		hdr := bu16s(2, uint16(16+len(grid)), vf1, 0, uint16(16+len(grid)+4), uint16(16+len(grid)+10), class1, class2)
		return joinB(hdr, grid, bu16s(1, 0), bu16s(1, 0, 0), bu16s(1, 0, 0))
	}
	gposOf := func(lookup []byte) []byte {
		return buildLayoutTable(simpleScripts(0), []tFeature{{tag: "kern", lookups: []uint16{0}}}, [][]byte{lookup})
	}
	lookupOf := func(typ uint16, offs []uint16, subtable []byte) []byte {
		hdr := bu16s(typ, 0, uint16(len(offs)))
		for _, o := range offs {
			hdr = append(hdr, bu16s(o)...)
		}
		return append(hdr, subtable...)
	}
	// 4096 x 4096 classes, a 16-byte subtable: 128 MiB of grid if the product is
	// not checked against the table. The format allows 65535 x 65535 (32 GiB); a
	// size that an unguarded decoder still finishes is what lets this test fail
	// quickly, instead of exhausting the machine, when the check is removed.
	add("gpos-pairpos2-class-product", bombFont("GPOS", gposOf(lookupOf(2, []uint16{8}, pairPos2(4, 4096, 4096, nil)))),
		errDeclaredCount, "pairpos2")
	// The same with both value formats empty: no byte backs a cell, so no grid
	// is built and the font parses.
	add("gpos-pairpos2-empty-values", bombFont("GPOS", gposOf(lookupOf(2, []uint16{8}, pairPos2(0, 0xFFFF, 0xFFFF, nil)))),
		nil, "")
	// 30000 subtable offsets to one 150 x 150 PairPos2: decoded once.
	offs := make([]uint16, 30000)
	for i := range offs {
		offs[i] = uint16(6 + 2*len(offs))
	}
	add("gpos-subtables-share-offset", bombFont("GPOS", gposOf(lookupOf(2, offs, pairPos2(4, 150, 150, zeros(150*150*2))))),
		nil, "")
	// A LookupList that declares 65535 lookups in two bytes.
	add("gpos-lookuplist-count", bombFont("GPOS", joinB(bu16s(1, 0, 10, 12, 14), bu16s(0), bu16s(0), bu16s(0xFFFF))),
		errDeclaredCount, "lookupList")

	// --- GSUB ---------------------------------------------------------------
	// 15000 sequence offsets to one 15000-glyph Sequence: 450 MB if decoded per
	// reference, from a 90 KB font.
	const nseq = 15000
	seqOffs := repeatU16(nseq, uint16(10+2*nseq))
	seqSub := joinB(bu16s(1, uint16(6+2*nseq), nseq), seqOffs, bu16s(1, 0), bu16s(nseq), zeros(2*nseq))
	gsubLookup := lookupOf(2, []uint16{8}, seqSub)
	add("gsub-sequences-share-offset", bombFont("GSUB",
		buildLayoutTable(simpleScripts(0), []tFeature{{tag: "ccmp", lookups: []uint16{0}}}, [][]byte{gsubLookup})),
		nil, "")

	// --- layout lists --------------------------------------------------------
	// 10000 script records on one Script, which has 10000 language records on
	// one LangSys of 5000 features.
	const nrec = 10000
	var sl, sc []byte
	sl = bu16s(nrec)
	for i := 0; i < nrec; i++ {
		sl = append(sl, 'a', 'b', 'c', byte('a'+i%26))
		sl = append(sl, bu16s(uint16(2+6*nrec))...)
	}
	sc = bu16s(0, nrec)
	for i := 0; i < nrec; i++ {
		sc = append(sc, 'x', 'y', 'z', byte('a'+i%26))
		sc = append(sc, bu16s(uint16(4+6*nrec))...)
	}
	langSys := joinB(bu16s(0, 0xFFFF, 5000), zeros(10000))
	scriptList := joinB(sl, sc, langSys)
	// header, an empty FeatureList (10) and LookupList (12), then the ScriptList (14).
	add("layout-scriptlist-shared", bombFont("GSUB", joinB(bu16s(1, 0, 14, 10, 12), bu16s(0), bu16s(0), scriptList)),
		nil, "")
	add("layout-scriptlist-count", bombFont("GSUB", joinB(bu16s(1, 0, 14, 10, 12), bu16s(0), bu16s(0), bu16s(0xFFFF))),
		errDeclaredCount, "scriptList")

	// --- MATH ---------------------------------------------------------------
	// 1000 kern records whose four corners all name one MathKern of 2000 heights.
	const nkern = 1000
	kernOff := 4 + 8*nkern + 4
	kernInfo := joinB(bu16s(4+8*nkern, nkern), repeatU16(4*nkern, uint16(kernOff)), bu16s(1, 0), bu16s(2000), zeros((2*2000+1)*4))
	// A MATH header with only a glyphInfo (at 10) whose kern info is at 8 from it.
	mathHdrKern := joinB(bu16s(1, 0, 0, 10, 0), bu16s(0, 0, 0, 8))
	add("math-kern-shared-offsets", bombFont("MATH", joinB(mathHdrKern, kernInfo)), nil, "")
	// One MathKern that declares 65535 heights in two bytes.
	kernInfo2 := joinB(bu16s(12, 1), repeatU16(4, 16), bu16s(1, 0), bu16s(0xFFFF))
	add("math-kern-heightcount", bombFont("MATH", joinB(mathHdrKern, kernInfo2)),
		errDeclaredCount, "MATH kern")
	// 5000 variant offsets to one construction of 5000 variants.
	const nvar = 5000
	mathHdrVar := bu16s(1, 0, 0, 0, 10)
	variants := joinB(bu16s(0, uint16(10+2*nvar), 0, nvar, 0), repeatU16(nvar, uint16(10+2*nvar+4)), bu16s(1, 0), bu16s(0, nvar), zeros(nvar*4))
	add("math-variants-shared-offsets", bombFont("MATH", joinB(mathHdrVar, variants)), nil, "")
	// One construction that declares 65535 variants in two bytes.
	variants2 := joinB(bu16s(0, 12, 0, 1, 0), bu16s(16), bu16s(1, 0), bu16s(0, 0xFFFF))
	add("math-construction-variantcount", bombFont("MATH", joinB(mathHdrVar, variants2)),
		errDeclaredCount, "MATH glyph construction")

	// --- gvar, HVAR/ItemVariationStore, fvar --------------------------------
	gv := &bw{}
	gv.u16(1)
	gv.u16(0)
	gv.u16(0xFFFF) // axisCount
	gv.u16(0xFFFF) // sharedTupleCount
	gv.u32(22)     // sharedTuplesOffset
	gv.u16(0)      // glyphCount
	gv.u16(0)      // flags
	gv.u32(22)     // dataArrayOffset
	gv.u16(0)      // the one offset
	add("gvar-sharedtuples-product", bombFont("gvar", gv.bytes()), errDeclaredCount, "gvar")

	// 2000 ItemVariationData offsets to one subtable of 60 rows x 1000 columns.
	const nivd = 2000
	ivdAt := 8 + 4*nivd + 10
	store := &bw{}
	store.u16(1)
	store.u32(uint32(8 + 4*nivd)) // region list
	store.u16(nivd)
	for i := 0; i < nivd; i++ {
		store.u32(uint32(ivdAt))
	}
	store.u16(1) // axisCount
	store.u16(1) // regionCount
	store.b = append(store.b, zeros(6)...)
	store.u16(60)   // itemCount
	store.u16(0)    // wordDeltaCount
	store.u16(1000) // regionIndexCount
	store.b = append(store.b, zeros(2000)...)
	store.b = append(store.b, zeros(60*1000)...)
	add("hvar-ivd-shared-offsets", bombFont("HVAR", hvarWithStore(store.bytes())), nil, "")

	rl := &bw{}
	rl.u16(1)
	rl.u32(8)
	rl.u16(0)
	rl.u16(0xFFFF) // axisCount
	rl.u16(0xFFFF) // regionCount
	add("hvar-regionlist-product", bombFont("HVAR", hvarWithStore(rl.bytes())), errDeclaredCount, "ItemVariationStore")

	// An fvar of 66 KB whose 65535 instances of one byte each, over 1000 axes,
	// are 65 million coordinates: refused by the budget, not the room check.
	fv := &bw{}
	fv.u16(1)
	fv.u16(0)
	fv.u16(16)
	fv.u16(0)
	fv.u16(1000) // axisCount
	fv.u16(1)    // axisSize
	fv.u16(0xFFFF)
	fv.u16(1) // instanceSize
	fv.b = append(fv.b, zeros(1000+65535+64)...)
	add("fvar-instances-x-axes", bombFont("fvar", fv.bytes()), errAllocBudget, "fvar")
	fv2 := &bw{}
	fv2.u16(1)
	fv2.u16(0)
	fv2.u16(16)
	fv2.u16(0)
	fv2.u16(1)
	fv2.u16(20)
	fv2.u16(0xFFFF)
	fv2.u16(0xFFFF)
	fv2.b = append(fv2.b, zeros(40)...)
	add("fvar-instancecount", bombFont("fvar", fv2.bytes()), errDeclaredCount, "fvar")
	return append(cs, coverageBombCases()...)
}

func hvarWithStore(store []byte) []byte {
	return joinB(bu16s(1, 0), []byte{0, 0, 0, 20}, zeros(12), store)
}

// allocated reports the bytes the runtime allocated while fn ran.
func allocated(fn func()) uint64 {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestBombs(t *testing.T) {
	for _, c := range bombCases() {
		t.Run(c.name, func(t *testing.T) {
			var err error
			n := allocated(func() { _, err = Parse(c.font) })
			if n > bombAllocLimit {
				t.Errorf("Parse allocated %d bytes for a %d-byte font, over the %d ceiling", n, len(c.font), bombAllocLimit)
			}
			if c.want == nil {
				if err != nil {
					t.Fatalf("a legitimate font was refused: %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want one wrapping %v", err, c.want)
			}
			if !strings.Contains(err.Error(), c.text) {
				t.Errorf("err = %q does not name %q", err, c.text)
			}
		})
	}
}

// TestBombCFF2Index drives the CFF2 INDEX reader directly: a CFF2 font needs
// more scaffolding than the count is worth.
func TestBombCFF2Index(t *testing.T) {
	b := []byte{0xFF, 0xFF, 0xFF, 0xFF, 1, 0, 0}
	var err error
	n := allocated(func() { _, err = parseIndex2(&reader{b: b}) })
	if !errors.Is(err, errDeclaredCount) || !strings.Contains(err.Error(), "cff2 index") {
		t.Fatalf("err = %v, want errDeclaredCount naming the cff2 index", err)
	}
	if n > bombAllocLimit {
		t.Errorf("allocated %d bytes", n)
	}
}

// TestBombSeedsCommitted keeps testdata/fuzz/FuzzParse in step with bombCases:
// every bomb is a seed of the Parse fuzz target. OPENTYPE_WRITE_SEEDS=1 writes
// them.
func TestBombSeedsCommitted(t *testing.T) {
	dir := filepath.Join("testdata", "fuzz", "FuzzParse")
	for _, c := range bombCases() {
		path := filepath.Join(dir, "bomb-"+c.name)
		want := "go test fuzz v1\n[]byte(" + strconv.Quote(string(c.font)) + ")\n"
		if os.Getenv("OPENTYPE_WRITE_SEEDS") == "1" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: no fuzz seed (run with OPENTYPE_WRITE_SEEDS=1): %v", c.name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s: the committed seed differs from the bomb", c.name)
		}
	}
}
