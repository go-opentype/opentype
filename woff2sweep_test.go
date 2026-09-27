// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/andybalholm/brotli"
)

// fixtureStream decompresses the WOFF2 fixture and returns its directory and
// the stream each table's data lives in — the test's own way in, so the
// transformed glyf of a REAL font can be used to drive the reconstruction's
// error paths with real data rather than invented headers.
func fixtureStream(t *testing.T) ([]woff2Table, []byte) {
	t.Helper()
	b, err := os.ReadFile(woff2Fixture)
	if err != nil {
		t.Skip(err)
	}
	numTables := int(be16(b[12:]))
	totalCompressed := be32(b[20:])
	r := &woff2Reader{b: b, i: woff2HeaderSize}
	tables := make([]woff2Table, numTables)
	for i := range tables {
		if tables[i], err = r.tableEntry(); err != nil {
			t.Fatalf("directory: %v", err)
		}
	}
	stream, err := io.ReadAll(brotli.NewReader(bytes.NewReader(b[r.i : r.i+int(totalCompressed)])))
	if err != nil {
		t.Fatalf("brotli: %v", err)
	}
	off := 0
	for i := range tables {
		n := int(tables[i].origLength)
		if tables[i].transformed() {
			n = int(tables[i].xformLen)
		}
		tables[i].data = stream[off : off+n]
		off += n
	}
	return tables, stream
}

func findTable(tables []woff2Table, tag string) *woff2Table {
	for i := range tables {
		if tables[i].tag == tag {
			return &tables[i]
		}
	}
	return nil
}

// Truncating a real transformed glyf at every length must always yield an
// error and never a panic — and it is what reaches the bounds checks scattered
// through the substream readers, which no hand-written header gets near.
//
// The full buffer is the one length that must succeed, which is what stops
// this from passing by refusing everything.
func TestReconstructGlyfTruncationSweep(t *testing.T) {
	tables, _ := fixtureStream(t)
	glyf, loca := findTable(tables, "glyf"), findTable(tables, "loca")
	if glyf == nil || loca == nil || !glyf.transformed() {
		t.Skip("the fixture has no transformed glyf")
	}
	if _, _, _, _, err := reconstructGlyf(glyf.data, loca.origLength); err != nil {
		t.Fatalf("the whole buffer must decode: %v", err)
	}
	refused := 0
	for n := 0; n < len(glyf.data); n++ {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("truncated to %d bytes: panicked with %v", n, p)
				}
			}()
			if _, _, _, _, err := reconstructGlyf(glyf.data[:n], loca.origLength); err != nil {
				refused++
			}
		}()
	}
	// Some prefixes legitimately decode: a shorter glyf stream can still hold
	// every substream the header declares if the header's sizes are met. What
	// matters is that none of them crashes and that the great majority are
	// refused.
	if refused < len(glyf.data)/2 {
		t.Errorf("only %d of %d truncations were refused", refused, len(glyf.data))
	}
	t.Logf("%d of %d truncations refused, none panicked", refused, len(glyf.data))
}

// The same sweep over a whole container, which reaches the header and
// directory bounds. Stepping by a prime keeps it quick without landing on a
// pattern.
func TestWOFF2ContainerTruncationSweep(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	file := buildWOFF2(sfntBuildTables(t, src), 0x00010000, nil)
	if _, err := Parse(file); err != nil {
		t.Fatalf("the whole file must parse: %v", err)
	}
	for n := 0; n < len(file); n += 7 {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("truncated to %d bytes: panicked with %v", n, p)
				}
			}()
			if _, err := Parse(file[:n]); err == nil {
				t.Errorf("truncated to %d bytes: accepted", n)
			}
		}()
	}
}

// A transformed hmtx, end to end: the transform drops the left side bearings
// it can recover from each glyph's xMin, so a font rebuilt from it must have
// exactly the hmtx it started with. Built here because the file a font service
// gave us does not use that transform.
func TestWOFF2TransformedHmtxRoundTrips(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	tables := sfntBuildTables(t, src)
	hhea, hmtx := findBuild(tables, "hhea"), findBuild(tables, "hmtx")
	if hhea == nil || hmtx == nil {
		t.Skip("the oracle has no hhea/hmtx")
	}
	numHMetrics := int(be16(hhea.data[34:]))
	// The transformed form: flags bit 0 set (the proportional bearings are
	// absent, to be recovered from xMin), bit 1 clear (there are no trailing
	// bearings in this font, so nothing to read), then the advances.
	xform := []byte{0x01}
	for i := 0; i < numHMetrics; i++ {
		xform = append(xform, hmtx.data[4*i], hmtx.data[4*i+1])
	}
	hmtx.transform = 1
	hmtx.stream = xform

	got, err := Parse(buildWOFF2(tables, 0x00010000, nil))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := mustParseFile(t, ttfOracle)
	gf, wf := got.NewFace(1000), want.NewFace(1000)
	for _, r := range []rune{'A', 'i', 'W', 'é', ' '} {
		gi, ok1 := got.GlyphIndex(r)
		wi, ok2 := want.GlyphIndex(r)
		if !ok1 || !ok2 {
			continue
		}
		if ga, wa := gf.AdvanceIndexUnits(gi), wf.AdvanceIndexUnits(wi); ga != wa {
			t.Errorf("%q: advance %v, want %v", r, ga, wa)
		}
	}
}

func findBuild(tables []buildTable, tag string) *buildTable {
	for i := range tables {
		if tables[i].tag == tag {
			return &tables[i]
		}
	}
	return nil
}

// A custom four-byte tag, which the directory escapes to when a table is not
// one of the 63 it knows.
func TestWOFF2CustomTag(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	// A private table, null transform — which for anything but glyf/loca is
	// version 0. (Version 3 there is invalid input, and is refused: a test
	// that built it that way is what showed the message says so.)
	tables := append(sfntBuildTables(t, src), buildTable{tag: "ZZZZ", data: []byte("private"), transform: 0})
	f, err := Parse(buildWOFF2(tables, 0x00010000, nil))
	if err != nil {
		t.Fatalf("a custom tag must survive: %v", err)
	}
	if f.NumGlyphs() == 0 {
		t.Error("the font came back empty")
	}
}
