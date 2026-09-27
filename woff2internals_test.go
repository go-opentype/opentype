// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
	"testing"
)

// The pieces the real fixture cannot reach, exercised where they live.
//
// The fixture in woff2_test.go establishes that the decoder is RIGHT, by
// matching a real font glyph for glyph. These establish that it is careful:
// every bound, every refusal, and the two transforms a font service happened
// not to use on the file we have.

// tripletDecode's wider flag ranges. A real face's deltas are small, so its
// points all take one data byte; the three- and four-byte forms exist for
// deltas past 4095 and are worth pinning by hand.
func TestTripletDecodeAllRanges(t *testing.T) {
	// flag, data, wanted dx, dy. The low bit of the flag is X's sign, the
	// next bit up is Y's.
	cases := []struct {
		flag   byte
		data   []byte
		dx, dy int
	}{
		{0, []byte{5}, 0, -5},                                    // <10: dy only, negative
		{1, []byte{5}, 0, 5},                                     // <10: dy only, positive
		{9, []byte{1}, 0, 1024 + 1},                              // <10: the top dy base
		{10, []byte{7}, -7, 0},                                   // <20: dx only, negative
		{11, []byte{7}, 7, 0},                                    // <20: dx only, positive
		{21, []byte{0x21}, 1 + 2, -(1 + 1)},                      // <84: 4 bits each
		{85, []byte{3, 4}, 1 + 3, -(1 + 4)},                      // <120: 8 bits each
		{121, []byte{0x12, 0x34, 0x56}, 0x123, -(0x456)},         // <124: 12 bits each
		{125, []byte{0x12, 0x34, 0x56, 0x78}, 0x1234, -(0x5678)}, // >=124: 16 bits each
	}
	for _, c := range cases {
		r := &woff2Reader{b: c.data}
		pts, err := tripletDecode([]byte{c.flag}, r)
		if err != nil {
			t.Errorf("flag %d: %v", c.flag, err)
			continue
		}
		if pts[0].x != c.dx || pts[0].y != c.dy {
			t.Errorf("flag %d: got (%d,%d), want (%d,%d)", c.flag, pts[0].x, pts[0].y, c.dx, c.dy)
		}
		if !pts[0].onCurve {
			t.Errorf("flag %d: the high bit was clear, so the point is on-curve", c.flag)
		}
	}
	// The high bit marks an off-curve point, and a truncated stream fails.
	pts, err := tripletDecode([]byte{0x80}, &woff2Reader{b: []byte{1}})
	if err != nil || pts[0].onCurve {
		t.Errorf("the high bit should mark an off-curve point: %+v %v", pts, err)
	}
	if _, err := tripletDecode([]byte{0}, &woff2Reader{}); err == nil {
		t.Error("a truncated glyph stream must fail")
	}
}

// storeLoca's two index formats, and the refusal that keeps the short one
// honest: it halves every offset, so an odd offset cannot be represented and
// an offset past 131070 does not fit.
func TestStoreLoca(t *testing.T) {
	long, err := storeLoca([]uint32{0, 7, 9}, 1)
	if err != nil || len(long) != 12 || be32(long[4:]) != 7 {
		t.Errorf("long format = %v, %v", long, err)
	}
	short, err := storeLoca([]uint32{0, 8, 16}, 0)
	if err != nil || len(short) != 6 || be16(short[2:]) != 4 {
		t.Errorf("short format = %v, %v", short, err)
	}
	if _, err := storeLoca([]uint32{0, 7}, 0); err == nil {
		t.Error("an odd offset must be refused by the short format")
	}
	if _, err := storeLoca([]uint32{0, 1 << 18}, 0); err == nil {
		t.Error("an offset past the short format's range must be refused")
	}
}

// writeBbox on no points at all writes nothing rather than reading past the
// slice — an empty contour is legal input.
func TestWriteBboxEmpty(t *testing.T) {
	dst := make([]byte, 8)
	writeBbox(dst, nil)
	for _, b := range dst {
		if b != 0 {
			t.Fatal("writeBbox touched the buffer for no points")
		}
	}
	writeBbox(dst, []glyfPoint{{x: -3, y: 4}, {x: 9, y: -8}})
	for i, want := range []int16{-3, -8, 9, 4} {
		if got := int16(be16(dst[2*i:])); got != want {
			t.Errorf("bbox[%d] = %d, want %d", i, got, want)
		}
	}
}

// reconstructGlyf's own refusals, each reached by a header it cannot act on.
func TestReconstructGlyfRefusals(t *testing.T) {
	// glyfHeader builds a transformed-glyf header with the substream sizes
	// given and nothing after it.
	glyfHeader := func(numGlyphs, indexFormat int, sizes [7]uint32, flags uint16) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint16(b[2:], flags)
		binary.BigEndian.PutUint16(b[4:], uint16(numGlyphs))
		binary.BigEndian.PutUint16(b[6:], uint16(indexFormat))
		for _, s := range sizes {
			var u [4]byte
			binary.BigEndian.PutUint32(u[:], s)
			b = append(b, u[:]...)
		}
		return b
	}
	for _, c := range []struct {
		name string
		data []byte
		loca uint32
	}{
		{"truncated header", []byte{0, 0}, 4},
		{"loca length disagrees", glyfHeader(1, 0, [7]uint32{}, 0), 999},
		{"substream past the end", glyfHeader(1, 0, [7]uint32{1 << 20}, 0), 4},
		{"bbox bitmap missing", glyfHeader(1, 0, [7]uint32{2, 0, 0, 0, 0, 0, 0}, 0), 4},
		{"overlap bitmap missing", glyfHeader(1, 0, [7]uint32{2, 0, 0, 0, 0, 4, 0}, flagOverlapSimpleBitmap), 4},
		{"nContours missing", glyfHeader(1, 0, [7]uint32{0, 0, 0, 0, 0, 4, 0}, 0), 4},
	} {
		if _, _, _, _, err := reconstructGlyf(c.data, c.loca); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// A composite glyph with no bbox, and an empty glyph WITH one, are both
	// refusals the spec names.
	noBbox := glyfHeader(1, 0, [7]uint32{2, 0, 0, 0, 0, 4, 0}, 0)
	noBbox = append(noBbox, 0xff, 0xff) // nContours = -1, composite
	noBbox = append(noBbox, 0, 0, 0, 0) // bbox bitmap: bit clear
	if _, _, _, _, err := reconstructGlyf(noBbox, 4); err == nil {
		t.Error("a composite glyph with no bbox must be refused")
	}
	emptyWithBbox := glyfHeader(1, 0, [7]uint32{2, 0, 0, 0, 0, 4, 0}, 0)
	emptyWithBbox = append(emptyWithBbox, 0, 0)          // nContours = 0, empty
	emptyWithBbox = append(emptyWithBbox, 0x80, 0, 0, 0) // bbox bitmap: bit set
	if _, _, _, _, err := reconstructGlyf(emptyWithBbox, 4); err == nil {
		t.Error("an empty glyph with a bbox must be refused")
	}
}

// The composite and simple glyph readers, on streams that stop early.
func TestGlyphReadersRefuseTruncation(t *testing.T) {
	empty := func() *woff2Reader { return &woff2Reader{} }
	if _, err := woff2CompositeGlyph(empty(), empty(), empty(), empty()); err == nil {
		t.Error("a composite with no component records must fail")
	}
	// A component saying MORE_COMPONENTS with nothing after it.
	more := &woff2Reader{b: []byte{0x00, 0x20, 0, 0, 0, 0}}
	if _, err := woff2CompositeGlyph(more, empty(), empty(), empty()); err == nil {
		t.Error("a truncated component chain must fail")
	}
	if _, err := woff2SimpleGlyph(1, empty(), empty(), empty(), empty(), empty(), false, false); err == nil {
		t.Error("a simple glyph with no point counts must fail")
	}
	// One contour of one point, but no flag byte for it.
	if _, err := woff2SimpleGlyph(1, &woff2Reader{b: []byte{1}}, empty(), empty(), empty(), empty(), false, false); err == nil {
		t.Error("a missing flag byte must fail")
	}
	// More than 65535 points in one glyph.
	huge := &woff2Reader{b: []byte{253, 0xff, 0xff, 253, 0xff, 0xff}}
	if _, err := woff2SimpleGlyph(2, huge, empty(), empty(), empty(), empty(), false, false); err == nil {
		t.Error("a glyph with more than 65535 points must fail")
	}
}

// reconstructHmtx: the transform drops the left side bearings it can recover
// from the glyphs' own xMin, so this puts them back. The file we have does not
// use it, which is exactly why it is pinned here.
func TestReconstructHmtx(t *testing.T) {
	xMins := []int16{10, 20, 30}
	// Flags bit 0 SET means the proportional bearings are absent from the
	// stream, so those come from each glyph's xMin; bit 1 clear then means the
	// monospace ones ARE present and are read. So: flags, two advances, and
	// one trailing bearing for the third glyph.
	in := []byte{0x01, 0x01, 0x00, 0x02, 0x00, 0x00, 0x2a}
	out, err := reconstructHmtx(in, 3, 2, xMins)
	if err != nil {
		t.Fatalf("reconstructHmtx: %v", err)
	}
	// Two long metrics then one trailing bearing.
	if len(out) != 4*2+2 {
		t.Fatalf("hmtx is %d bytes, want 10", len(out))
	}
	if be16(out[0:]) != 0x100 || int16(be16(out[2:])) != 10 {
		t.Errorf("metric 0 = advance %d lsb %d, want 256 and the xMin 10", be16(out[0:]), int16(be16(out[2:])))
	}
	if int16(be16(out[6:])) != 20 {
		t.Errorf("metric 1 lsb = %d, want the xMin 20", int16(be16(out[6:])))
	}
	if int16(be16(out[8:])) != 0x2a {
		t.Errorf("trailing bearing = %d, want the read 42", int16(be16(out[8:])))
	}
	// flags bit 1 set: the MONOSPACE bearings are absent instead, so the
	// proportional ones are read and the trailing ones come from xMin.
	in2 := []byte{0x02, 0x01, 0x00, 0x02, 0x00, 0x00, 0x63, 0x00, 0x64}
	out2, err := reconstructHmtx(in2, 3, 2, xMins)
	if err != nil {
		t.Fatalf("monospace form: %v", err)
	}
	if int16(be16(out2[2:])) != 0x63 || int16(be16(out2[6:])) != 0x64 {
		t.Errorf("read bearings = %d, %d", int16(be16(out2[2:])), int16(be16(out2[6:])))
	}
	if int16(be16(out2[8:])) != 30 {
		t.Errorf("trailing bearing = %d, want xMin 30", int16(be16(out2[8:])))
	}

	for _, c := range []struct {
		name                 string
		in                   []byte
		numGlyphs, numMetric int
	}{
		{"no flags byte", nil, 3, 2},
		{"reserved bits set", []byte{0xfc}, 3, 2},
		{"claims a transform but drops nothing", []byte{0x00}, 3, 2},
		{"no metrics", []byte{0x01}, 3, 0},
		{"more metrics than glyphs", []byte{0x01}, 1, 2},
		{"advances truncated", []byte{0x01, 0x01}, 3, 2},
		{"proportional bearings truncated", []byte{0x02, 0x01, 0x00, 0x02, 0x00}, 3, 2},
		{"monospace bearings truncated", []byte{0x01, 0x01, 0x00, 0x02, 0x00}, 3, 2},
	} {
		if _, err := reconstructHmtx(c.in, c.numGlyphs, c.numMetric, xMins); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := reconstructHmtx([]byte{0x01, 0, 1}, 3, 1, []int16{1}); err == nil {
		t.Error("too few xMins for the glyph count must be refused")
	}
}

// WOFF version 1: a per-table zlib container, no transforms. Built here and
// round-tripped, since the web has moved to WOFF2 and a version 1 file is not
// something a font service will hand us to test against.
func TestDecodeWOFF1(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	n := int(be16(src[4:]))
	type ent struct {
		tag        string
		body       []byte
		orig, comp uint32
	}
	var ents []ent
	for i := 0; i < n; i++ {
		e := 12 + 16*i
		tag := string(src[e : e+4])
		off, ln := be32(src[e+8:]), be32(src[e+12:])
		raw := src[off : off+ln]
		// Deflate every other table, store the rest, so both branches run.
		if i%2 == 0 {
			var buf bytes.Buffer
			zw := zlib.NewWriter(&buf)
			_, _ = zw.Write(raw)
			_ = zw.Close()
			ents = append(ents, ent{tag, buf.Bytes(), ln, uint32(buf.Len())})
		} else {
			ents = append(ents, ent{tag, raw, ln, ln})
		}
	}
	const headerSize = 44
	hdr := make([]byte, headerSize)
	binary.BigEndian.PutUint32(hdr[0:], sigWOFF)
	binary.BigEndian.PutUint32(hdr[4:], 0x00010000)
	binary.BigEndian.PutUint16(hdr[12:], uint16(len(ents)))
	dir := make([]byte, 20*len(ents))
	body := []byte{}
	for i, e := range ents {
		off := headerSize + len(dir) + len(body)
		copy(dir[20*i:], e.tag)
		binary.BigEndian.PutUint32(dir[20*i+4:], uint32(off))
		binary.BigEndian.PutUint32(dir[20*i+8:], e.comp)
		binary.BigEndian.PutUint32(dir[20*i+12:], e.orig)
		body = append(body, e.body...)
	}
	file := append(append(hdr, dir...), body...)

	got, err := Parse(file)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := mustParseFile(t, ttfOracle)
	if got.NumGlyphs() != want.NumGlyphs() || got.unitsPerEm != want.unitsPerEm {
		t.Errorf("got %d glyphs at %d upem, want %d at %d", got.NumGlyphs(), got.unitsPerEm, want.NumGlyphs(), want.unitsPerEm)
	}

	// Its refusals.
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"short header", []byte("wOFF")},
		{"no tables", func() []byte { b := append([]byte(nil), file...); binary.BigEndian.PutUint16(b[12:], 0); return b }()},
		{"directory truncated", file[:headerSize+10]},
		{"table past the end", func() []byte {
			b := append([]byte(nil), file...)
			binary.BigEndian.PutUint32(b[headerSize+4:], uint32(len(file)))
			return b
		}()},
		{"not zlib", func() []byte {
			b := append([]byte(nil), file...)
			off := be32(b[headerSize+4:])
			b[off] ^= 0xff
			b[off+1] ^= 0xff
			return b
		}()},
		{"inflates to the wrong length", func() []byte {
			b := append([]byte(nil), file...)
			binary.BigEndian.PutUint32(b[headerSize+12:], be32(b[headerSize+12:])+1)
			return b
		}()},
	} {
		if _, err := Parse(c.in); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// isWOFFContainer and the guard against a container that yields another.
func TestWOFFContainerSniff(t *testing.T) {
	if isWOFFContainer([]byte("wO")) {
		t.Error("two bytes cannot be a container")
	}
	if !isWOFFContainer([]byte("wOFF....")) || !isWOFFContainer([]byte("wOF2....")) {
		t.Error("both signatures should be recognised")
	}
	if isWOFFContainer([]byte("OTTO....")) {
		t.Error("an sfnt is not a container")
	}
	if _, err := decodeWOFFContainer([]byte("ab")); err == nil {
		t.Error("a short buffer must be refused")
	}
	if _, err := decodeWOFFContainer([]byte("OTTOxxxx")); err == nil {
		t.Error("an sfnt must be refused by the container decoder")
	}
}
