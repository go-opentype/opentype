// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"bytes"
	"encoding/binary"
	"os"
	"sort"
	"testing"

	"github.com/andybalholm/brotli"
)

// A WOFF2 writer, for the tests only. The real fixture proves the decoder
// right on a real file; this proves it careful on files no font service would
// ever send, which is the other half of a container decoder's job — a
// malformed one must be refused, not half-read into a font that draws wrongly.

type buildTable struct {
	tag       string
	data      []byte // the reconstructed bytes, i.e. origLength's worth
	transform uint8  // the transformation version to declare
	stream    []byte // what to put in the stream instead of data, when transformed
	origWrong uint32 // if non-zero, declare this origLength instead of len(data)
}

// buildWOFF2 assembles a WOFF2. flavor is the sfnt version it claims.
func buildWOFF2(tables []buildTable, flavor uint32, mangle func(hdr []byte)) []byte {
	var dir, stream bytes.Buffer
	for _, t := range tables {
		body := t.data
		transformed := (t.tag == "glyf" || t.tag == "loca") == (t.transform == 0)
		if transformed {
			body = t.stream
		}
		idx := byte(0x3f)
		for i, known := range woff2KnownTags {
			if known == t.tag {
				idx = byte(i)
				break
			}
		}
		dir.WriteByte(t.transform<<6 | idx)
		if idx == 0x3f {
			dir.WriteString(t.tag)
		}
		orig := uint32(len(t.data))
		if t.origWrong != 0 {
			orig = t.origWrong
		}
		dir.Write(base128Bytes(orig))
		if transformed {
			dir.Write(base128Bytes(uint32(len(body))))
		}
		stream.Write(body)
	}
	var comp bytes.Buffer
	zw := brotli.NewWriter(&comp)
	_, _ = zw.Write(stream.Bytes())
	_ = zw.Close()

	hdr := make([]byte, woff2HeaderSize)
	binary.BigEndian.PutUint32(hdr[0:], sigWOFF2)
	binary.BigEndian.PutUint32(hdr[4:], flavor)
	binary.BigEndian.PutUint16(hdr[12:], uint16(len(tables)))
	binary.BigEndian.PutUint32(hdr[16:], uint32(stream.Len()))
	binary.BigEndian.PutUint32(hdr[20:], uint32(comp.Len()))
	if mangle != nil {
		mangle(hdr)
	}
	out := append(hdr, dir.Bytes()...)
	binary.BigEndian.PutUint32(out[8:], uint32(len(out)+comp.Len())) // total length
	return append(out, comp.Bytes()...)
}

// base128Bytes encodes a UIntBase128, the inverse of woff2Reader.base128.
func base128Bytes(v uint32) []byte {
	if v == 0 {
		return []byte{0}
	}
	var groups []byte
	for v > 0 {
		groups = append(groups, byte(v&0x7f))
		v >>= 7
	}
	out := make([]byte, 0, len(groups))
	for i := len(groups) - 1; i >= 0; i-- {
		b := groups[i]
		if i != 0 {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}

// sfntBuildTables splits an SFNT into null-transform build tables.
func sfntBuildTables(t *testing.T, b []byte) []buildTable {
	t.Helper()
	n := int(be16(b[4:]))
	out := make([]buildTable, 0, n)
	for i := 0; i < n; i++ {
		e := 12 + 16*i
		tag := string(b[e : e+4])
		off, ln := be32(b[e+8:]), be32(b[e+12:])
		if int(off)+int(ln) > len(b) {
			t.Fatalf("fixture table %q out of range", tag)
		}
		tr := uint8(0)
		if tag == "glyf" || tag == "loca" {
			tr = 3 // the null transform for these two
		}
		out = append(out, buildTable{tag: tag, data: b[off : off+ln], transform: tr})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].tag < out[j].tag })
	return out
}

// A WOFF2 whose tables are all stored untransformed must come back byte for
// byte as the sfnt this package would write for them — so the container, the
// directory, the stream split, the padding and the checksums are all exercised
// without the glyf transform in the way.
func TestWOFF2NullTransformRoundTrips(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	tables := sfntBuildTables(t, src)
	got, err := Parse(buildWOFF2(tables, 0x00010000, nil))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := mustParseFile(t, ttfOracle)
	if got.NumGlyphs() != want.NumGlyphs() || got.unitsPerEm != want.unitsPerEm {
		t.Fatalf("got %d glyphs at %d upem, want %d at %d", got.NumGlyphs(), got.unitsPerEm, want.NumGlyphs(), want.unitsPerEm)
	}
	gi, _ := got.GlyphIndex('A')
	wi, _ := want.GlyphIndex('A')
	gc, err1 := got.glyphContours(gi)
	wc, err2 := want.glyphContours(wi)
	if err1 != nil || err2 != nil {
		t.Fatalf("contours: %v / %v", err1, err2)
	}
	if len(gc) != len(wc) || len(gc[0]) != len(wc[0]) {
		t.Fatalf("'A' round-tripped to %d contours", len(gc))
	}
	for i := range gc[0] {
		if gc[0][i] != wc[0][i] {
			t.Fatalf("'A' point %d = %+v, want %+v", i, gc[0][i], wc[0][i])
		}
	}
}

// Every refusal, each with the input that reaches it. A container decoder that
// accepts these would hand its caller a font built from nonsense.
func TestWOFF2Refusals(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	good := sfntBuildTables(t, src)

	cases := []struct {
		name string
		in   []byte
	}{
		{"short file", []byte("wOF")},
		{"not a container", []byte("nope")},
		{"header cut short", buildWOFF2(good, 0x00010000, nil)[:40]},
		{"font collection", buildWOFF2(good, 0x74746366, nil)},
		{"no tables", buildWOFF2(nil, 0x00010000, nil)},
		{"reserved tag index", func() []byte {
			b := buildWOFF2(good[:1], 0x00010000, nil)
			b[woff2HeaderSize] = (b[woff2HeaderSize] & 0xc0) | 62 + 1 // index 63 is the escape; 62 is the last real one
			return b
		}()},
		{"compressed stream past end", func() []byte {
			b := buildWOFF2(good, 0x00010000, nil)
			binary.BigEndian.PutUint32(b[20:], uint32(len(b))) // claim more than is there
			return b
		}()},
		{"brotli garbage", func() []byte {
			b := buildWOFF2(good, 0x00010000, nil)
			for i := len(b) - 8; i < len(b); i++ {
				b[i] ^= 0xff
			}
			return b
		}()},
		{"table longer than the stream", func() []byte {
			bad := append([]buildTable(nil), good...)
			bad[0].origWrong = 1 << 20
			return buildWOFF2(bad, 0x00010000, nil)
		}()},
		{"origLength disagrees with the data", func() []byte {
			bad := append([]buildTable(nil), good...)
			bad[0].origWrong = uint32(len(bad[0].data)) - 1
			return buildWOFF2(bad, 0x00010000, nil)
		}()},
		{"loca transformed without glyf", []byte(nil)},
	}
	// The last case needs a table set with a transformed loca and no glyf.
	locaOnly := []buildTable{{tag: "loca", transform: 0, stream: []byte{0, 0}, data: make([]byte, 4)}}
	cases[len(cases)-1].in = buildWOFF2(locaOnly, 0x00010000, nil)

	for _, c := range cases {
		if _, err := Parse(c.in); err == nil {
			t.Errorf("%s: accepted, want a refusal", c.name)
		}
	}
}

// A truncated directory, and the two UIntBase128 forms that are invalid so
// that one value has exactly one encoding.
func TestWOFF2Base128Refusals(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"leading zero group", []byte{0x80, 0x01}},
		{"overflows 32 bits", []byte{0xff, 0xff, 0xff, 0xff, 0x7f}},
		{"longer than five bytes", []byte{0x80 | 1, 0x80 | 1, 0x80 | 1, 0x80 | 1, 0x80 | 1, 1}},
		{"truncated", []byte{0x80 | 1}},
	} {
		r := &woff2Reader{b: c.in}
		if _, err := r.base128(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// And the valid forms, including the multi-group one.
	for _, c := range []struct {
		in   []byte
		want uint32
	}{
		{[]byte{0}, 0},
		{[]byte{0x7f}, 127},
		{[]byte{0x81, 0x00}, 128},
		{[]byte{0x8f, 0x7f}, 0x7ff},
	} {
		r := &woff2Reader{b: c.in}
		got, err := r.base128()
		if err != nil || got != c.want {
			t.Errorf("base128(%v) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	// base128Bytes is the test's own encoder; it must agree.
	for _, v := range []uint32{0, 1, 127, 128, 300, 1 << 20, 1<<25 - 1} {
		r := &woff2Reader{b: base128Bytes(v)}
		got, err := r.base128()
		if err != nil || got != v {
			t.Errorf("round trip %d gave %d, %v", v, got, err)
		}
	}
}

// 255UInt16's four forms, including its two one-byte escapes.
func TestWOFF2Read255(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want int
	}{
		{[]byte{0}, 0},
		{[]byte{252}, 252},
		{[]byte{253, 0x12, 0x34}, 0x1234},
		{[]byte{254, 5}, 5 + 253*2},
		{[]byte{255, 5}, 5 + 253},
	} {
		r := &woff2Reader{b: c.in}
		got, err := r.read255()
		if err != nil || got != c.want {
			t.Errorf("read255(%v) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	for _, in := range [][]byte{{}, {253}, {254}, {255}} {
		r := &woff2Reader{b: in}
		if _, err := r.read255(); err == nil {
			t.Errorf("read255(%v) accepted a truncated value", in)
		}
	}
}

// The reader's own bounds.
func TestWOFF2ReaderBounds(t *testing.T) {
	r := &woff2Reader{b: []byte{1, 2, 3, 4, 5, 6, 7}}
	if _, err := r.bytes(-1); err == nil {
		t.Error("a negative length must be refused")
	}
	if v, err := r.u8(); err != nil || v != 1 {
		t.Errorf("u8 = %d, %v", v, err)
	}
	if v, err := r.u16(); err != nil || v != 0x0203 {
		t.Errorf("u16 = %#x, %v", v, err)
	}
	if v, err := r.u32(); err != nil || v != 0x04050607 {
		t.Errorf("u32 = %#x, %v", v, err)
	}
	for _, f := range []func() error{
		func() error { _, err := r.u8(); return err },
		func() error { _, err := r.u16(); return err },
		func() error { _, err := r.u32(); return err },
	} {
		if err := f(); err == nil {
			t.Error("a read past the end must fail")
		}
	}
}
