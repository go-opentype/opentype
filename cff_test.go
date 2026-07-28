// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"testing"
)

// This file synthesises minimal-but-valid CFF tables and Type2 charstrings in
// memory, so the CFF tests never depend on an external font file. Every parse
// and interpreter branch (including error paths) is exercised deterministically
// by crafting the bytes directly, mirroring synth_test.go's approach for glyf.

// --- Type2 charstring builder ----------------------------------------------

// csb assembles a Type2 charstring byte by byte.
type csb struct{ b []byte }

// num appends an integer operand using the compact Type2 encoding.
func (c *csb) num(v int) *csb {
	switch {
	case v >= -107 && v <= 107:
		c.b = append(c.b, byte(v+139))
	case v >= 108 && v <= 1131:
		v -= 108
		c.b = append(c.b, byte(v/256+247), byte(v%256))
	case v >= -1131 && v <= -108:
		v = -v - 108
		c.b = append(c.b, byte(v/256+251), byte(v%256))
	default:
		c.b = append(c.b, 28, byte(int16(v)>>8), byte(v))
	}
	return c
}

// fixed appends a 16.16 fixed-point operand (charstring leading byte 255).
func (c *csb) fixed(f float64) *csb {
	u := int32(f * 65536)
	c.b = append(c.b, 255, byte(u>>24), byte(u>>16), byte(u>>8), byte(u))
	return c
}

// op appends a one- or two-byte operator (two-byte operators use op>=1200).
func (c *csb) op(o int) *csb {
	if o >= 1200 {
		c.b = append(c.b, 12, byte(o-1200))
	} else {
		c.b = append(c.b, byte(o))
	}
	return c
}

// raw appends literal bytes.
func (c *csb) raw(bs ...byte) *csb { c.b = append(c.b, bs...); return c }

// --- interpreter driver -----------------------------------------------------

// runCS interprets code as glyph 0 of a cffTable with the given subrs.
func runCS(code []byte, gsubrs, lsubrs [][]byte) ([]contour, error) {
	c := &cffTable{
		charStrings:    [][]byte{code},
		globalSubrs:    gsubrs,
		gsubrBias:      subrBias(len(gsubrs)),
		localSubrs:     lsubrs,
		lsubrBias:      subrBias(len(lsubrs)),
		charstringType: 2,
	}
	return c.outline(0)
}

// mustRun interprets code and fails the test on error, returning the contours.
func mustRun(t *testing.T, code []byte, gsubrs, lsubrs [][]byte) []contour {
	t.Helper()
	got, err := runCS(code, gsubrs, lsubrs)
	if err != nil {
		t.Fatalf("runCS: unexpected error: %v", err)
	}
	return got
}

// wantErr interprets code and fails the test unless it errors.
func wantErr(t *testing.T, code []byte, gsubrs, lsubrs [][]byte) {
	t.Helper()
	if _, err := runCS(code, gsubrs, lsubrs); err == nil {
		t.Fatalf("runCS: expected error, got nil")
	}
}

// --- interpreter operator coverage ------------------------------------------

func TestT2LinesAndMoves(t *testing.T) {
	// rmoveto (no width), rlineto, hlineto (h/v/h), vlineto, endchar.
	c := &csb{}
	c.num(20).num(0).op(21)         // rmoveto -> (20,0)
	c.num(30).num(0).op(5)          // rlineto -> (50,0)
	c.num(0).num(40).num(-40).op(6) // hlineto: h,v,h
	c.num(0).num(50).op(7)          // vlineto
	c.op(14)                        // endchar
	cs := mustRun(t, c.b, nil, nil)
	if len(cs) != 1 {
		t.Fatalf("contours = %d, want 1", len(cs))
	}
	if len(cs[0]) < 5 {
		t.Fatalf("too few points: %d", len(cs[0]))
	}
}

func TestT2VMoveAndWidthOnStem(t *testing.T) {
	// hstem carrying a leading width (odd arg count), then vmoveto.
	c := &csb{}
	c.num(600).num(10).num(20).op(1) // hstem: width + 1 stem pair
	c.num(0).num(0).op(4)            // vmoveto -> (0,0)? but width already taken
	c.num(50).num(0).op(5)           // rlineto
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2Curves(t *testing.T) {
	c := &csb{}
	c.num(0).num(0).op(21)                                   // rmoveto
	c.num(10).num(10).num(10).num(-10).num(10).num(10).op(8) // rrcurveto
	c.op(14)
	got := mustRun(t, c.b, nil, nil)
	// cffCurveSteps points from the one curve plus the moveto start point.
	if len(got[0]) != 1+cffCurveSteps {
		t.Fatalf("curve points = %d, want %d", len(got[0]), 1+cffCurveSteps)
	}
}

func TestT2HHVVCurve(t *testing.T) {
	// hhcurveto and vvcurveto, each with and without the leading extra delta.
	c := &csb{}
	c.num(0).num(0).op(21)
	c.num(10).num(10).num(10).num(10).op(27)        // hhcurveto (no dy1)
	c.num(5).num(10).num(10).num(10).num(10).op(27) // hhcurveto (leading dy1)
	c.num(10).num(10).num(10).num(10).op(26)        // vvcurveto (no dx1)
	c.num(5).num(10).num(10).num(10).num(10).op(26) // vvcurveto (leading dx1)
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2AlternatingCurves(t *testing.T) {
	c := &csb{}
	c.num(0).num(0).op(21)
	// hvcurveto: 8 args (two alternating groups) then 4 args, plus a 5-arg
	// group with the final extra delta.
	c.num(10).num(10).num(10).num(10).num(10).num(10).num(10).num(10).op(31)
	c.num(10).num(10).num(10).num(10).num(5).op(30) // vhcurveto with extra
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2CurveLineMixes(t *testing.T) {
	c := &csb{}
	c.num(0).num(0).op(21)
	// rcurveline: one curve (6) + trailing line (2).
	c.num(10).num(10).num(10).num(-10).num(10).num(10).num(20).num(0).op(24)
	// rlinecurve: one line (2) + trailing curve (6).
	c.num(20).num(0).num(10).num(10).num(10).num(-10).num(10).num(10).op(25)
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2Hintmask(t *testing.T) {
	// hstem then hintmask with implicit vstem args (so 2 stems -> 1 mask byte),
	// then cntrmask.
	c := &csb{}
	c.num(10).num(20).op(1)         // hstem (1 stem)
	c.num(30).num(40).op(19).raw(0) // hintmask: implicit vstem + 1 mask byte
	c.op(20).raw(0)                 // cntrmask + 1 mask byte
	c.num(0).num(0).op(21)
	c.num(50).num(0).op(5)
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2Flex(t *testing.T) {
	c := &csb{}
	c.num(0).num(0).op(21)
	// hflex (7 args)
	c.num(10).num(10).num(10).num(10).num(10).num(-10).num(10).op(1234)
	// flex (13 args)
	for i := 0; i < 12; i++ {
		c.num(10)
	}
	c.num(50).op(1235)
	// hflex1 (9 args)
	c.num(10).num(5).num(10).num(10).num(10).num(10).num(10).num(-5).num(10).op(1236)
	// flex1 (11 args) with |dx| > |dy|
	c.num(30).num(0).num(10).num(5).num(10).num(0).num(10).num(-5).num(10).num(0).num(10).op(1237)
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2Flex1Vertical(t *testing.T) {
	// flex1 where |dy| >= |dx| takes the vertical-return branch.
	c := &csb{}
	c.num(0).num(0).op(21)
	c.num(0).num(30).num(5).num(10).num(0).num(10).num(-5).num(10).num(0).num(10).num(10).op(1237)
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

func TestT2HMoveAndNoEndchar(t *testing.T) {
	// hmoveto with an argument (normal path), and a charstring that ends
	// without an endchar so run returns at the end of the code.
	c := &csb{}
	c.num(30).op(22) // hmoveto -> (30,0)
	c.num(10).num(0).op(5)
	// no endchar
	got := mustRun(t, c.b, nil, nil)
	if len(got) != 1 {
		t.Fatalf("contours = %d, want 1", len(got))
	}
}

func TestT2SquareGeometryAndRaster(t *testing.T) {
	// Draw a 100..300 square with lines and verify exact corner coordinates,
	// then feed the resulting []contour through the existing rasteriser to prove
	// the CFF outline is the same type raster.go consumes.
	c := &csb{}
	c.num(100).num(100).op(21) // rmoveto -> (100,100)
	c.num(200).num(0).op(5)    // -> (300,100)
	c.num(0).num(200).op(5)    // -> (300,300)
	c.num(-200).num(0).op(5)   // -> (100,300)
	c.op(14)                   // endchar (close)
	got := mustRun(t, c.b, nil, nil)
	if len(got) != 1 || len(got[0]) != 4 {
		t.Fatalf("square: got %d contours, %d points", len(got), len(got[0]))
	}
	want := []outlinePoint{{100, 100, true}, {300, 100, true}, {300, 300, true}, {100, 300, true}}
	for i, p := range got[0] {
		if p != want[i] {
			t.Fatalf("point %d = %+v, want %+v", i, p, want[i])
		}
	}
	poly := flattenContour(got[0], 0.1)
	bounds, mask := rasterize([][]fpoint{poly})
	if mask == nil || bounds.Empty() {
		t.Fatalf("rasterize produced no coverage: bounds=%v", bounds)
	}
}

func TestT2Subrs(t *testing.T) {
	// A local subr (index 0 -> call value -107 with bias 107) draws, returns.
	sub := &csb{}
	sub.num(5).num(0).op(21) // rmoveto inside subr
	sub.num(10).num(0).op(5) // rlineto
	sub.op(11)               // return
	main := &csb{}
	main.num(-107).op(10) // callsubr 0
	main.op(14)
	mustRun(t, main.b, nil, [][]byte{sub.b})
}

func TestT2GlobalSubrEndchar(t *testing.T) {
	// A global subr calls endchar; the stop flag must propagate out of the
	// call so trailing main bytes are not executed.
	sub := &csb{}
	sub.num(0).num(0).op(21)
	sub.num(10).num(0).op(5)
	sub.op(14) // endchar inside subr
	main := &csb{}
	main.num(-107).op(29) // callgsubr 0
	main.op(0)            // would be an invalid operator if reached
	mustRun(t, main.b, [][]byte{sub.b}, nil)
}

func TestT2ShortIntAndFixedOperands(t *testing.T) {
	// Cover the 28 (shortint) and 255 (16.16 fixed) operand encodings, plus the
	// 247.. and 251.. two-byte integer encodings.
	c := &csb{}
	c.raw(28, 0x01, 0x2c)  // shortint 300 (width, stripped by rmoveto)
	c.num(0).num(0).op(21) // rmoveto with a preceding width value
	c.fixed(12.5).num(0).op(5)
	c.num(300).num(0).op(5)  // 247.. form
	c.num(-300).num(0).op(5) // 251.. form
	c.op(14)
	mustRun(t, c.b, nil, nil)
}

// --- interpreter error coverage ---------------------------------------------

func TestT2Errors(t *testing.T) {
	tests := map[string][]byte{
		"rmoveto short":    (&csb{}).num(5).op(21).b,
		"hmoveto empty":    (&csb{}).op(22).b,
		"vmoveto empty":    (&csb{}).op(4).b,
		"callsubr empty":   (&csb{}).op(10).b,
		"callgsubr empty":  (&csb{}).op(29).b,
		"unknown operator": (&csb{}).op(13).b,
		"escape truncated": (&csb{}).raw(12).b,
		"escape unknown":   (&csb{}).raw(12, 99).b,
		"hflex short":      (&csb{}).num(1).op(1234).b,
		"flex short":       (&csb{}).num(1).op(1235).b,
		"hflex1 short":     (&csb{}).num(1).op(1236).b,
		"flex1 short":      (&csb{}).num(1).op(1237).b,
		"op28 truncated":   (&csb{}).raw(28).b,
		"op255 truncated":  (&csb{}).raw(255).b,
		"op247 truncated":  (&csb{}).raw(247).b,
		"op251 truncated":  (&csb{}).raw(251).b,
		"hintmask short":   (&csb{}).num(10).num(20).op(1).op(19).b, // needs 1 mask byte
	}
	for name, code := range tests {
		t.Run(name, func(t *testing.T) { wantErr(t, code, nil, nil) })
	}
}

func TestT2CallSubrOutOfRange(t *testing.T) {
	wantErr(t, (&csb{}).num(5000).op(10).b, nil, nil)
	wantErr(t, (&csb{}).num(5000).op(29).b, nil, nil)
}

func TestT2StackOverflow(t *testing.T) {
	c := &csb{}
	for i := 0; i <= maxT2Stack; i++ {
		c.num(1)
	}
	c.op(14)
	wantErr(t, c.b, nil, nil)
}

func TestT2RecursionDepth(t *testing.T) {
	// Local subr 0 calls itself; the depth guard must stop the recursion.
	self := (&csb{}).num(-107).op(10).b
	wantErr(t, (&csb{}).num(-107).op(10).b, nil, [][]byte{self})
}

func TestOutlineGidRange(t *testing.T) {
	c := &cffTable{charStrings: [][]byte{{14}}}
	if _, err := c.outline(-1); err == nil {
		t.Fatal("expected error for negative gid")
	}
	if _, err := c.outline(5); err == nil {
		t.Fatal("expected error for out-of-range gid")
	}
}

func TestOutlineRunError(t *testing.T) {
	c := &cffTable{charStrings: [][]byte{{13}}} // unknown operator
	if _, err := c.outline(0); err == nil {
		t.Fatal("expected run error")
	}
}

// --- INDEX / DICT unit coverage ---------------------------------------------

func TestParseIndex(t *testing.T) {
	// offSize == 2 exercises the multi-byte readOffset path.
	b := []byte{0x00, 0x01, 0x02, 0x00, 0x01, 0x00, 0x03, 'A', 'B'}
	items, err := parseIndex(&reader{b: b})
	if err != nil {
		t.Fatalf("parseIndex: %v", err)
	}
	if len(items) != 1 || string(items[0]) != "AB" {
		t.Fatalf("items = %q", items)
	}
	// Empty INDEX (count 0).
	if items, err := parseIndex(&reader{b: []byte{0, 0}}); err != nil || items != nil {
		t.Fatalf("empty index: items=%v err=%v", items, err)
	}
}

func TestParseIndexErrors(t *testing.T) {
	cases := map[string][]byte{
		"count truncated":   {0},
		"bad offSize":       {0, 1, 0},
		"offsets truncated": {0, 1, 1, 1},    // count 1, offSize 1, only one offset
		"item out of range": {0, 1, 1, 1, 0}, // offsets [1,0] -> start>end
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseIndex(&reader{b: b}); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestParseDictOperand(t *testing.T) {
	cases := []struct {
		b    []byte
		want float64
	}{
		{[]byte{28, 0x01, 0x00}, 256},         // shortint
		{[]byte{29, 0, 0, 1, 0}, 256},         // longint
		{[]byte{100}, -39},                    // 32..246
		{[]byte{247, 0}, 108},                 // 247..250
		{[]byte{251, 0}, -108},                // 251..254
		{[]byte{30, 0x2a, 0x5f}, 2.5},         // real "2.5"
		{[]byte{30, 0x1b, 0x2f}, 100},         // real "1E2"
		{[]byte{30, 0xe1, 0xc2, 0xf0}, -0.01}, // real "-1E-2"
	}
	for _, tc := range cases {
		v, _, err := parseDictOperand(tc.b, 0)
		if err != nil {
			t.Fatalf("operand %v: %v", tc.b, err)
		}
		if v != tc.want {
			t.Fatalf("operand %v = %v, want %v", tc.b, v, tc.want)
		}
	}
}

func TestParseDictOperandErrors(t *testing.T) {
	cases := map[string][]byte{
		"reserved byte":    {31},
		"28 truncated":     {28},
		"29 truncated":     {29},
		"247 truncated":    {247},
		"251 truncated":    {251},
		"real truncated":   {30},
		"real bad nibble":  {30, 0xd0},
		"real parse error": {30, 0xaf}, // "." alone
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseDictOperand(b, 0); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestParseDictEscapeTruncated(t *testing.T) {
	if _, err := parseDict([]byte{12}); err == nil {
		t.Fatal("expected truncated-operator error")
	}
}

func TestParseDictOperandPropagates(t *testing.T) {
	// A bad operand inside a DICT must surface through parseDict.
	if _, err := parseDict([]byte{31}); err == nil {
		t.Fatal("expected operand error via parseDict")
	}
}

func TestSubrBias(t *testing.T) {
	if subrBias(0) != 107 || subrBias(2000) != 1131 || subrBias(40000) != 32768 {
		t.Fatalf("subrBias wrong: %d %d %d", subrBias(0), subrBias(2000), subrBias(40000))
	}
}

// --- full CFF assembly + parse ----------------------------------------------

// cffIndex encodes items as a CFF INDEX with 1-byte offsets.
func cffIndex(items [][]byte) []byte {
	w := &bw{}
	w.u16(uint16(len(items)))
	if len(items) == 0 {
		return w.bytes()
	}
	w.u8(1) // offSize
	off := 1
	w.u8(uint8(off))
	for _, it := range items {
		off += len(it)
		w.u8(uint8(off))
	}
	for _, it := range items {
		w.b = append(w.b, it...)
	}
	return w.bytes()
}

// dictLong encodes a DICT integer operand in the fixed-width 5-byte 29 form so
// the DICT length is independent of the value (which keeps offsets stable).
func dictLong(v int) []byte {
	w := &bw{}
	w.u8(29)
	w.u32(uint32(int32(v)))
	return w.bytes()
}

// dictOperator encodes a DICT operator (op>=1200 for the two-byte escape form).
func dictOperator(op int) []byte {
	if op >= 1200 {
		return []byte{12, byte(op - 1200)}
	}
	return []byte{byte(op)}
}

// cffOptions configures the synthetic CFF builder.
type cffOptions struct {
	glyphs      [][]byte
	gsubrs      [][]byte
	lsubrs      [][]byte
	includePriv bool
	charType    int    // 0 -> omit CharstringType operator
	omitCharStr bool   // omit the CharStrings operator entirely
	charsetSIDs []int  // when non-nil, emit a format-0 charset (SID per glyph 1..n-1)
	privExtra   []byte // extra Private-DICT bytes (hint params) placed before Subrs
}

// cffCharset0 encodes a format-0 charset: one uint16 string id per glyph 1..n-1
// (glyph 0 is always .notdef and is not listed).
func cffCharset0(sids []int) []byte {
	w := &bw{}
	w.u8(0) // format 0
	for _, s := range sids {
		w.u16(uint16(s))
	}
	return w.bytes()
}

// buildCFF assembles a complete, valid CFF table from o, resolving all internal
// offsets. CharStrings and Private data are laid out after the fixed-size
// top-level INDEXes, and the Top DICT uses fixed-width operands so its length
// (and hence every offset) is stable across the two encoding passes.
func buildCFF(o cffOptions) []byte {
	header := []byte{1, 0, 4, 1}
	name := cffIndex([][]byte{[]byte("SYNTH")})
	strIdx := cffIndex(nil)
	gsubrIdx := cffIndex(o.gsubrs)
	csIdx := cffIndex(o.glyphs)

	var privDict, lsubrIdx []byte
	if o.includePriv {
		// Any hint params come first; the Subrs operator (offset relative to the
		// Private DICT start) then points just past the whole Private DICT, where
		// the Local Subr INDEX is laid out. dictLong+operator is 6 bytes.
		subrOff := len(o.privExtra) + 6
		privDict = append(privDict, o.privExtra...)
		privDict = append(privDict, dictLong(subrOff)...)
		privDict = append(privDict, dictOperator(19)...)
		lsubrIdx = cffIndex(o.lsubrs)
	}
	var charsetData []byte
	if o.charsetSIDs != nil {
		charsetData = cffCharset0(o.charsetSIDs)
	}

	encodeTop := func(csOff, privOff, charsetOff int) []byte {
		var td []byte
		if !o.omitCharStr {
			td = append(td, dictLong(csOff)...)
			td = append(td, dictOperator(17)...)
		}
		if o.includePriv {
			td = append(td, dictLong(len(privDict))...)
			td = append(td, dictLong(privOff)...)
			td = append(td, dictOperator(18)...)
		}
		if o.charsetSIDs != nil {
			td = append(td, dictLong(charsetOff)...)
			td = append(td, dictOperator(15)...)
		}
		if o.charType != 0 {
			td = append(td, dictLong(o.charType)...)
			td = append(td, dictOperator(1206)...)
		}
		return td
	}

	// Pass 1: measure the Top DICT INDEX length using placeholder offsets.
	topLen := len(cffIndex([][]byte{encodeTop(0, 0, 0)}))
	csOff := len(header) + len(name) + topLen + len(strIdx) + len(gsubrIdx)
	privOff := csOff + len(csIdx)
	charsetOff := privOff + len(privDict) + len(lsubrIdx)
	// Pass 2: real offsets (same lengths, fixed-width encoding).
	topIdx := cffIndex([][]byte{encodeTop(csOff, privOff, charsetOff)})

	out := append([]byte{}, header...)
	out = append(out, name...)
	out = append(out, topIdx...)
	out = append(out, strIdx...)
	out = append(out, gsubrIdx...)
	out = append(out, csIdx...)
	out = append(out, privDict...)
	out = append(out, lsubrIdx...)
	out = append(out, charsetData...)
	return out
}

func TestParseCFFRoundTrip(t *testing.T) {
	// Glyph 0: draws with a local subr and a global subr, hints, curves, flex.
	lsub := (&csb{}).num(10).num(0).op(5).op(11).b // rlineto + return
	gsub := (&csb{}).num(0).num(20).op(5).op(11).b // rlineto + return

	g0 := &csb{}
	g0.num(700).num(50).num(50).op(1)                         // hstem with width + stem
	g0.num(20).num(30).op(19).raw(0)                          // hintmask (implicit vstem)
	g0.num(0).num(0).op(21)                                   // rmoveto
	g0.num(-107).op(10)                                       // callsubr (local 0)
	g0.num(-107).op(29)                                       // callgsubr (global 0)
	g0.num(10).num(10).num(10).num(-10).num(10).num(10).op(8) // rrcurveto
	g0.op(14)                                                 // endchar

	g1 := (&csb{}).op(14).b // empty glyph

	data := buildCFF(cffOptions{
		glyphs:      [][]byte{g0.b, g1},
		gsubrs:      [][]byte{gsub},
		lsubrs:      [][]byte{lsub},
		includePriv: true,
		charType:    2,
	})

	c, err := parseCFF(data)
	if err != nil {
		t.Fatalf("parseCFF: %v", err)
	}
	if len(c.charStrings) != 2 {
		t.Fatalf("charStrings = %d, want 2", len(c.charStrings))
	}
	if len(c.localSubrs) != 1 || len(c.globalSubrs) != 1 {
		t.Fatalf("subrs: local=%d global=%d", len(c.localSubrs), len(c.globalSubrs))
	}
	cs, err := c.outline(0)
	if err != nil {
		t.Fatalf("outline(0): %v", err)
	}
	if len(cs) == 0 {
		t.Fatal("outline(0): no contours")
	}
	// Glyph 1 is empty (endchar only) -> no contours, no error.
	if cs1, err := c.outline(1); err != nil || len(cs1) != 0 {
		t.Fatalf("outline(1) = %v, %v; want empty", cs1, err)
	}
}

func TestParseCFFNoPrivateNoType(t *testing.T) {
	// Omit Private DICT and CharstringType (default type 2 path, no local subrs).
	data := buildCFF(cffOptions{glyphs: [][]byte{(&csb{}).op(14).b}})
	c, err := parseCFF(data)
	if err != nil {
		t.Fatalf("parseCFF: %v", err)
	}
	if c.localSubrs != nil {
		t.Fatalf("expected no local subrs")
	}
}

func TestParseCFFBadCharType(t *testing.T) {
	data := buildCFF(cffOptions{glyphs: [][]byte{(&csb{}).op(14).b}, charType: 1})
	if _, err := parseCFF(data); err == nil {
		t.Fatal("expected unsupported CharstringType error")
	}
}

func TestParseCFFMissingCharStrings(t *testing.T) {
	data := buildCFF(cffOptions{glyphs: [][]byte{(&csb{}).op(14).b}, omitCharStr: true})
	if _, err := parseCFF(data); err == nil {
		t.Fatal("expected missing CharStrings error")
	}
}

func TestParseCFFHeaderErrors(t *testing.T) {
	cases := map[string][]byte{
		"too short":         {1, 0, 4},
		"hdrSize too small": {1, 0, 3, 1},
		"hdrSize too big":   {1, 0, 200, 1},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCFF(b); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseCFFTruncatedIndexes(t *testing.T) {
	// Valid header, then a Name INDEX whose count is truncated.
	if _, err := parseCFF([]byte{1, 0, 4, 1, 0}); err == nil {
		t.Fatal("expected name index error")
	}
	// Header + empty name INDEX, then truncated Top DICT INDEX.
	if _, err := parseCFF([]byte{1, 0, 4, 1, 0, 0, 0}); err == nil {
		t.Fatal("expected top dict index error")
	}
}

func TestParseCFFEmptyTopDict(t *testing.T) {
	// Header, empty name INDEX, empty Top DICT INDEX (count 0).
	b := []byte{1, 0, 4, 1, 0, 0, 0, 0}
	if _, err := parseCFF(b); err == nil {
		t.Fatal("expected empty-top-dict error")
	}
}

func TestParseCFFStringAndGsubrErrors(t *testing.T) {
	// Header + empty name + a Top DICT INDEX holding one valid dict, then a
	// truncated String INDEX.
	td := append(dictLong(4), dictOperator(17)...)
	prefix := []byte{1, 0, 4, 1}
	prefix = append(prefix, cffIndex(nil)...)          // name
	prefix = append(prefix, cffIndex([][]byte{td})...) // top dict
	// truncated string index (single byte)
	if _, err := parseCFF(append(append([]byte{}, prefix...), 0)); err == nil {
		t.Fatal("expected string index error")
	}
	// full empty string index, then truncated gsubr index
	withStr := append(append([]byte{}, prefix...), cffIndex(nil)...)
	if _, err := parseCFF(append(withStr, 0)); err == nil {
		t.Fatal("expected gsubr index error")
	}
}

func TestParseCFFTopDictParseError(t *testing.T) {
	// Top DICT item ends with a dangling escape operator (byte 12).
	prefix := []byte{1, 0, 4, 1}
	prefix = append(prefix, cffIndex(nil)...)
	prefix = append(prefix, cffIndex([][]byte{{12}})...) // bad top dict
	prefix = append(prefix, cffIndex(nil)...)            // string
	prefix = append(prefix, cffIndex(nil)...)            // gsubr
	if _, err := parseCFF(prefix); err == nil {
		t.Fatal("expected top dict parse error")
	}
}

func TestParseCFFCharStringsOffsetOutOfRange(t *testing.T) {
	td := append(dictLong(1<<20), dictOperator(17)...) // huge CharStrings offset
	prefix := []byte{1, 0, 4, 1}
	prefix = append(prefix, cffIndex(nil)...)
	prefix = append(prefix, cffIndex([][]byte{td})...)
	prefix = append(prefix, cffIndex(nil)...)
	prefix = append(prefix, cffIndex(nil)...)
	if _, err := parseCFF(prefix); err == nil {
		t.Fatal("expected CharStrings offset error")
	}
}

func TestParseCFFCharStringsIndexError(t *testing.T) {
	// CharStrings offset points at a truncated INDEX (a lone byte at the end).
	prefix := []byte{1, 0, 4, 1}
	prefix = append(prefix, cffIndex(nil)...)
	// placeholder, patched below once the offset is known
	strIdx := cffIndex(nil)
	gsubrIdx := cffIndex(nil)
	// Compute the offset of the byte just past the top-level indexes.
	td0 := append(dictLong(0), dictOperator(17)...)
	topLen := len(cffIndex([][]byte{td0}))
	csOff := len(prefix) + topLen + len(strIdx) + len(gsubrIdx)
	td := append(dictLong(csOff), dictOperator(17)...)
	prefix = append(prefix, cffIndex([][]byte{td})...)
	prefix = append(prefix, strIdx...)
	prefix = append(prefix, gsubrIdx...)
	prefix = append(prefix, 0) // truncated CharStrings INDEX (1 byte)
	if _, err := parseCFF(prefix); err == nil {
		t.Fatal("expected CharStrings index error")
	}
}

func TestParseLocalSubrsErrors(t *testing.T) {
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

	// Private entry with a single operand -> "bad Private DICT entry".
	if _, _, err := parseLocalSubrs(data, map[int][]float64{18: {4}}); err == nil {
		t.Fatal("expected bad-private-entry error")
	}
	// Private DICT out of range.
	if _, _, err := parseLocalSubrs(data, map[int][]float64{18: {100, 100}}); err == nil {
		t.Fatal("expected private-out-of-range error")
	}
	// No Private DICT at all -> nil, nil.
	if ls, _, err := parseLocalSubrs(data, map[int][]float64{}); err != nil || ls != nil {
		t.Fatalf("no private: %v %v", ls, err)
	}
	// Private DICT present but empty (no Subrs operator) -> nil, nil.
	if ls, _, err := parseLocalSubrs(data, map[int][]float64{18: {0, 0}}); err != nil || ls != nil {
		t.Fatalf("private without subrs: %v %v", ls, err)
	}
}

func TestParseCFFPrivateErrorPropagates(t *testing.T) {
	// A full CFF whose Top DICT declares a Private DICT that is out of range,
	// so the error surfaces from parseCFF (not just parseLocalSubrs directly).
	prefix := []byte{1, 0, 4, 1}
	prefix = append(prefix, cffIndex(nil)...) // name
	strIdx := cffIndex(nil)
	gsubrIdx := cffIndex(nil)
	// CharStrings offset points at the byte just past the top-level indexes,
	// where we place a valid single-glyph INDEX; Private is far out of range.
	td0 := append(dictLong(0), dictOperator(17)...)
	td0 = append(td0, dictLong(0)...)
	td0 = append(td0, dictLong(0)...)
	td0 = append(td0, dictOperator(18)...)
	topLen := len(cffIndex([][]byte{td0}))
	csOff := len(prefix) + topLen + len(strIdx) + len(gsubrIdx)
	td := append(dictLong(csOff), dictOperator(17)...)
	td = append(td, dictLong(1<<20)...) // Private size out of range
	td = append(td, dictLong(1<<20)...) // Private offset out of range
	td = append(td, dictOperator(18)...)
	prefix = append(prefix, cffIndex([][]byte{td})...)
	prefix = append(prefix, strIdx...)
	prefix = append(prefix, gsubrIdx...)
	prefix = append(prefix, cffIndex([][]byte{(&csb{}).op(14).b})...) // CharStrings
	if _, err := parseCFF(prefix); err == nil {
		t.Fatal("expected private dict error via parseCFF")
	}
}

func TestParseLocalSubrsPrivParseError(t *testing.T) {
	// Private DICT region contains a dangling escape operator byte.
	data := make([]byte, 16)
	data[0] = 12 // at offset 0, size 1 -> parseDict sees a lone escape byte
	top := map[int][]float64{18: {1, 0}}
	if _, _, err := parseLocalSubrs(data, top); err == nil {
		t.Fatal("expected private dict parse error")
	}
}

func TestParseLocalSubrsSubrOffsetOutOfRange(t *testing.T) {
	// Private DICT declares a Subrs offset that lands outside the data.
	priv := append(dictLong(1<<20), dictOperator(19)...)
	data := make([]byte, len(priv))
	copy(data, priv)
	top := map[int][]float64{18: {float64(len(priv)), 0}}
	if _, _, err := parseLocalSubrs(data, top); err == nil {
		t.Fatal("expected subr offset out of range error")
	}
}

// --- endchar seac (accent composition) --------------------------------------

func TestStandardEncodingSID(t *testing.T) {
	cases := []struct {
		code int
		sid  uint16
		ok   bool
	}{
		{32, 1, true},    // space (low range)
		{65, 34, true},   // 'A' (low range)
		{126, 95, true},  // asciitilde (top of low range)
		{193, 124, true}, // grave (high table)
		{251, 149, true}, // germandbls (top of high table)
		{160, 0, false},  // gap in the high range
		{300, 0, false},  // beyond any encoded code
		{31, 0, false},   // just below the low range
	}
	for _, c := range cases {
		sid, ok := standardEncodingSID(c.code)
		if ok != c.ok || (ok && sid != c.sid) {
			t.Fatalf("standardEncodingSID(%d) = (%d,%v), want (%d,%v)", c.code, sid, ok, c.sid, c.ok)
		}
	}
}

func TestParseCharset(t *testing.T) {
	// Predefined (offset 0/1/2) -> identity: string id i maps to glyph i.
	m, err := parseCharset(nil, 0, 3)
	if err != nil || m[0] != 0 || m[1] != 1 || m[2] != 2 {
		t.Fatalf("predefined charset = %v, %v", m, err)
	}
	// Embedded formats, placed at offset 3 (past a 3-byte pad).
	pad := []byte{0, 0, 0}
	f0 := append(append([]byte{}, pad...), 0x00, 0, 10, 0, 20) // gid1->10, gid2->20
	if m, err = parseCharset(f0, 3, 3); err != nil || m[10] != 1 || m[20] != 2 {
		t.Fatalf("format 0 = %v, %v", m, err)
	}
	f1 := append(append([]byte{}, pad...), 0x01, 0, 10, 2) // first 10, nLeft 2 -> 10,11,12
	if m, err = parseCharset(f1, 3, 4); err != nil || m[10] != 1 || m[11] != 2 || m[12] != 3 {
		t.Fatalf("format 1 = %v, %v", m, err)
	}
	f2 := append(append([]byte{}, pad...), 0x02, 0, 10, 0, 2) // first 10, nLeft 2 (uint16)
	if m, err = parseCharset(f2, 3, 4); err != nil || m[10] != 1 || m[11] != 2 || m[12] != 3 {
		t.Fatalf("format 2 = %v, %v", m, err)
	}
	// Error paths.
	if _, err = parseCharset(nil, 100, 2); err == nil {
		t.Fatal("expected offset-out-of-range error")
	}
	if _, err = parseCharset(append(append([]byte{}, pad...), 0x09), 3, 2); err == nil {
		t.Fatal("expected unsupported-format error")
	}
	if _, err = parseCharset(append(append([]byte{}, pad...), 0x00), 3, 3); err == nil {
		t.Fatal("expected truncated-charset error")
	}
}

func TestParseCFFSeac(t *testing.T) {
	adx, ady := 100, 50
	// Glyph 0 is a seac: base 'A' (code 65 -> SID 34) + accent grave (code 193
	// -> SID 124), the accent shifted by (adx, ady).
	seacCS := (&csb{}).num(adx).num(ady).num(65).num(193).op(14).b
	base := (&csb{}).num(0).num(0).op(21).num(50).num(0).op(5).num(0).num(50).op(5).op(14).b
	accent := (&csb{}).num(10).num(10).op(21).num(20).num(0).op(5).op(14).b
	data := buildCFF(cffOptions{
		glyphs:      [][]byte{seacCS, base, accent},
		charsetSIDs: []int{34, 124}, // glyph 1 -> SID 34 ('A'), glyph 2 -> SID 124 (grave)
	})
	c, err := parseCFF(data)
	if err != nil {
		t.Fatalf("parseCFF: %v", err)
	}
	baseC, err := c.outline(1)
	if err != nil {
		t.Fatalf("outline base: %v", err)
	}
	accC, err := c.outline(2)
	if err != nil {
		t.Fatalf("outline accent: %v", err)
	}
	got, err := c.outline(0)
	if err != nil {
		t.Fatalf("outline seac: %v", err)
	}
	if len(got) != len(baseC)+len(accC) {
		t.Fatalf("seac contours = %d, want %d", len(got), len(baseC)+len(accC))
	}
	for i := range baseC {
		for j := range baseC[i] {
			if got[i][j] != baseC[i][j] {
				t.Fatalf("base contour %d point %d = %+v, want %+v", i, j, got[i][j], baseC[i][j])
			}
		}
	}
	for i := range accC {
		gi := len(baseC) + i
		for j := range accC[i] {
			want := outlinePoint{x: accC[i][j].x + float64(adx), y: accC[i][j].y + float64(ady), on: accC[i][j].on}
			if got[gi][j] != want {
				t.Fatalf("accent contour %d point %d = %+v, want %+v", i, j, got[gi][j], want)
			}
		}
	}
}

func TestT2SeacErrors(t *testing.T) {
	goodBase := (&csb{}).num(0).num(0).op(21).num(10).num(0).op(5).op(14).b
	badBase := []byte{13} // unknown operator
	seac := func(bchar, achar int) []byte {
		return (&csb{}).num(0).num(0).num(bchar).num(achar).op(14).b
	}
	cases := []struct {
		name    string
		strings [][]byte
		sid     map[int]int
	}{
		// bchar code has no standard-encoding glyph.
		{"bchar unencoded", [][]byte{seac(160, 65)}, map[int]int{34: 0}},
		// bchar valid but its string id is not in the charset.
		{"bchar no glyph", [][]byte{seac(65, 65)}, nil},
		// accent code has no standard-encoding glyph (base resolves first).
		{"achar unencoded", [][]byte{seac(65, 160), goodBase}, map[int]int{34: 1}},
		// base component charstring is malformed.
		{"base outline error", [][]byte{seac(65, 65), badBase}, map[int]int{34: 1}},
		// accent component charstring is malformed.
		{"accent outline error", [][]byte{seac(65, 193), goodBase, badBase}, map[int]int{34: 1, 124: 2}},
		// seac whose base is itself -> composition-depth guard trips.
		{"cyclic seac", [][]byte{seac(65, 65)}, map[int]int{34: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &cffTable{charstringType: 2, charStrings: tc.strings, sidToGid: tc.sid}
			if _, err := c.outline(0); err == nil {
				t.Fatal("expected seac error, got nil")
			}
		})
	}
}

func TestParseCFFCharsetError(t *testing.T) {
	// Top DICT with a valid CharStrings offset and a charset offset (op 15)
	// that points past the data, so parseCFF surfaces the charset error.
	prefix := []byte{1, 0, 4, 1}
	prefix = append(prefix, cffIndex(nil)...) // name
	strIdx := cffIndex(nil)
	gsubrIdx := cffIndex(nil)
	td0 := append(dictLong(0), dictOperator(17)...)
	td0 = append(td0, dictLong(0)...)
	td0 = append(td0, dictOperator(15)...)
	topLen := len(cffIndex([][]byte{td0}))
	csOff := len(prefix) + topLen + len(strIdx) + len(gsubrIdx)
	td := append(dictLong(csOff), dictOperator(17)...)
	td = append(td, dictLong(1<<20)...) // charset offset out of range
	td = append(td, dictOperator(15)...)
	prefix = append(prefix, cffIndex([][]byte{td})...)
	prefix = append(prefix, strIdx...)
	prefix = append(prefix, gsubrIdx...)
	prefix = append(prefix, cffIndex([][]byte{(&csb{}).op(14).b})...) // CharStrings
	if _, err := parseCFF(prefix); err == nil {
		t.Fatal("expected charset error via parseCFF")
	}
}

func TestErrTruncatedIsWrapped(t *testing.T) {
	// A truncated CFF header wraps errTruncated so callers can match it.
	_, err := parseCFF([]byte{1, 0})
	if !errors.Is(err, errTruncated) {
		t.Fatalf("err = %v, want errTruncated", err)
	}
}
