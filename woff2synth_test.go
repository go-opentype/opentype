// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"encoding/binary"
	"testing"
)

// A transformed-glyf writer for the tests, holding the variants a real font
// service's subset happened not to use: an explicit bounding box on a simple
// glyph, the three component transform forms, component instructions, and the
// long loca index format.

type synthGlyf struct {
	nContours, nPoints, flags, glyphs, composites, bboxVals, instructions []byte
	bboxBits                                                              []byte
	count                                                                 int
	indexFormat                                                           uint16
}

// encode255 is the inverse of woff2Reader.read255.
func encode255(v int) []byte {
	switch {
	case v < 253:
		return []byte{byte(v)}
	case v < 253+256:
		return []byte{255, byte(v - 253)}
	case v < 253*2+256:
		return []byte{254, byte(v - 253*2)}
	}
	return []byte{253, byte(v >> 8), byte(v)}
}

func (s *synthGlyf) markBbox() {
	i := s.count
	for len(s.bboxBits) <= i>>3 {
		s.bboxBits = append(s.bboxBits, 0)
	}
	s.bboxBits[i>>3] |= 0x80 >> (i & 7)
}

func (s *synthGlyf) addEmpty() {
	s.nContours = append(s.nContours, 0, 0)
	for len(s.bboxBits) <= s.count>>3 {
		s.bboxBits = append(s.bboxBits, 0)
	}
	s.count++
}

// addSimple writes one contour of n points, each moving by (+1,+1), with an
// explicit bbox when asked.
func (s *synthGlyf) addSimple(n int, withBbox bool) {
	s.nContours = append(s.nContours, 0, 1)
	s.nPoints = append(s.nPoints, encode255(n)...)
	for i := 0; i < n; i++ {
		s.flags = append(s.flags, 21)     // 4+4 bits, both signs positive
		s.glyphs = append(s.glyphs, 0x00) // dx = dy = 1
	}
	s.glyphs = append(s.glyphs, 0) // instruction length 0
	if withBbox {
		s.markBbox()
		s.bboxVals = append(s.bboxVals, 0, 1, 0, 1, 0, 9, 0, 9)
	} else {
		for len(s.bboxBits) <= s.count>>3 {
			s.bboxBits = append(s.bboxBits, 0)
		}
	}
	s.count++
}

// addComposite writes a one-component composite with the given flags; a
// composite always carries an explicit bbox.
func (s *synthGlyf) addComposite(flags uint16, extra int, instrLen int) {
	s.nContours = append(s.nContours, 0xff, 0xff)
	var f [2]byte
	binary.BigEndian.PutUint16(f[:], flags)
	s.composites = append(s.composites, f[:]...)
	s.composites = append(s.composites, 0, 1) // the component's glyph index
	s.composites = append(s.composites, make([]byte, extra)...)
	if instrLen > 0 {
		s.glyphs = append(s.glyphs, byte(instrLen)) // 255UInt16, small form
		s.instructions = append(s.instructions, make([]byte, instrLen)...)
	}
	s.markBbox()
	s.bboxVals = append(s.bboxVals, 0, 0, 0, 0, 0, 8, 0, 8)
	s.count++
}

// bytes assembles the transformed glyf buffer and the loca length that goes
// with it.
func (s *synthGlyf) bytes() ([]byte, uint32) {
	bitmapLen := ((s.count + 31) >> 5) << 2
	bitmap := make([]byte, bitmapLen)
	copy(bitmap, s.bboxBits)
	bbox := append(bitmap, s.bboxVals...)
	subs := [][]byte{s.nContours, s.nPoints, s.flags, s.glyphs, s.composites, bbox, s.instructions}

	out := make([]byte, 8)
	binary.BigEndian.PutUint16(out[4:], uint16(s.count))
	binary.BigEndian.PutUint16(out[6:], s.indexFormat)
	for _, sub := range subs {
		var u [4]byte
		binary.BigEndian.PutUint32(u[:], uint32(len(sub)))
		out = append(out, u[:]...)
	}
	for _, sub := range subs {
		out = append(out, sub...)
	}
	locaEntry := uint32(2)
	if s.indexFormat != 0 {
		locaEntry = 4
	}
	return out, locaEntry * uint32(s.count+1)
}

// Every glyph shape the transform can carry, in one buffer, with the long loca
// format. The real fixture covers none of these: its composites use no scale
// and carry no instructions, and none of its simple glyphs ships an explicit
// bounding box.
func TestReconstructGlyfAllShapes(t *testing.T) {
	const (
		argsAreWords    = 1 << 0
		weHaveAScale    = 1 << 3
		xAndYScale      = 1 << 6
		twoByTwo        = 1 << 7
		weHaveInstructs = 1 << 8
	)
	s := &synthGlyf{indexFormat: 1}
	s.addEmpty()
	s.addSimple(3, true)  // with an explicit bbox
	s.addSimple(4, false) // bbox computed from the points
	s.addComposite(weHaveAScale, 2+2, 0)
	s.addComposite(xAndYScale, 2+4, 0)
	s.addComposite(twoByTwo, 2+8, 0)
	s.addComposite(argsAreWords|weHaveInstructs, 4, 5)
	data, locaLen := s.bytes()

	glyf, loca, xMins, n, err := reconstructGlyf(data, locaLen)
	if err != nil {
		t.Fatalf("reconstructGlyf: %v", err)
	}
	if n != 7 {
		t.Fatalf("got %d glyphs, want 7", n)
	}
	if len(loca) != 4*8 {
		t.Errorf("loca is %d bytes, want 32 for the long format", len(loca))
	}
	// The empty glyph occupies nothing, so the first two offsets are equal.
	if be32(loca[0:]) != be32(loca[4:]) {
		t.Errorf("the empty glyph took %d bytes", be32(loca[4:])-be32(loca[0:]))
	}
	// The simple glyph with an explicit bbox kept the one given, xMin = 1.
	start := be32(loca[4:])
	if got := int16(be16(glyf[start+2:])); got != 1 {
		t.Errorf("explicit bbox xMin = %d, want 1", got)
	}
	if xMins[1] != 1 {
		t.Errorf("xMins[1] = %d, want 1", xMins[1])
	}
	// The one with no bbox got it computed: three... four points from (1,1).
	start = be32(loca[8:])
	if got := int16(be16(glyf[start+2:])); got != 1 {
		t.Errorf("computed bbox xMin = %d, want 1", got)
	}
	// Every glyph starts on a four-byte boundary.
	for i := 0; i < 8; i++ {
		if off := be32(loca[4*i:]); off%4 != 0 {
			t.Errorf("loca[%d] = %d is not four-byte aligned", i, off)
		}
	}
}

// The short loca format's own refusal, reached through a real reconstruction:
// it halves every offset, and a glyph table longer than 131070 bytes cannot be
// addressed by it.
func TestReconstructGlyfShortLocaOverflow(t *testing.T) {
	s := &synthGlyf{indexFormat: 0}
	// Enough points to push the glyph table past the short format's reach.
	for i := 0; i < 320; i++ {
		s.addSimple(250, false)
	}
	data, locaLen := s.bytes()
	if _, _, _, _, err := reconstructGlyf(data, locaLen); err == nil {
		t.Error("a glyf past the short loca format's range must be refused")
	}
}

// tableEntry's three ways of naming a table, and its refusal.
func TestWOFF2TableEntry(t *testing.T) {
	// A known tag by index, null transform: index 0 is cmap, and for anything
	// but glyf/loca version 0 IS the null transform, so only one length
	// follows.
	r := &woff2Reader{b: append([]byte{0}, base128Bytes(7)...)}
	e, err := r.tableEntry()
	if err != nil || e.tag != "cmap" || e.origLength != 7 || e.transformed() {
		t.Fatalf("known tag = %+v, %v", e, err)
	}
	// glyf with version 0 is TRANSFORMED, so a second length follows it.
	r = &woff2Reader{b: append(append([]byte{10}, base128Bytes(7)...), base128Bytes(5)...)}
	if e, err = r.tableEntry(); err != nil || e.tag != "glyf" || !e.transformed() || e.xformLen != 5 {
		t.Fatalf("transformed glyf = %+v, %v", e, err)
	}
	// The escape, with a four-byte tag.
	r = &woff2Reader{b: append([]byte{0x3f, 'Z', 'Z', 'Z', 'Z'}, base128Bytes(9)...)}
	if e, err = r.tableEntry(); err != nil || e.tag != "ZZZZ" || e.origLength != 9 {
		t.Fatalf("custom tag = %+v, %v", e, err)
	}
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"no flags byte", nil},
		{"truncated tag", []byte{0x3f, 'Z'}},
		{"no origLength", []byte{1}},
		{"no transformLength", append([]byte{10}, base128Bytes(7)...)},
	} {
		r := &woff2Reader{b: c.in}
		if _, err := r.tableEntry(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// xMinsFromGlyf's guards, which only a font transforming hmtx but not glyf
// reaches.
func TestXMinsFromGlyfGuards(t *testing.T) {
	ok := &woff2Table{out: make([]byte, 64)}
	for _, c := range []struct {
		name                   string
		maxp, head, glyf, loca *woff2Table
	}{
		{"no maxp", nil, ok, ok, ok},
		{"short maxp", &woff2Table{out: []byte{0}}, ok, ok, ok},
		{"no head", ok, nil, ok, ok},
		{"short head", ok, &woff2Table{out: []byte{0}}, ok, ok},
		{"no glyf", ok, ok, nil, ok},
		{"no loca", ok, ok, ok, nil},
	} {
		if _, _, err := xMinsFromGlyf(c.maxp, c.head, c.glyf, c.loca); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// A loca too short for the glyph count maxp declares.
	maxp := &woff2Table{out: []byte{0, 0, 0, 0, 0, 5}} // numGlyphs = 5
	head := &woff2Table{out: make([]byte, 52)}
	if _, _, err := xMinsFromGlyf(maxp, head, ok, &woff2Table{out: []byte{0, 0}}); err == nil {
		t.Error("a loca too short for the glyph count must be refused")
	}
	// The long format, and an empty glyph contributing a zero bearing.
	binary.BigEndian.PutUint16(head.out[50:], 1) // indexFormat = long
	maxp = &woff2Table{out: []byte{0, 0, 0, 0, 0, 2}}
	loca := make([]byte, 12) // 0, 0, 0 — both glyphs empty
	glyf := &woff2Table{out: make([]byte, 8)}
	n, xMins, err := xMinsFromGlyf(maxp, head, glyf, &woff2Table{out: loca})
	if err != nil || n != 2 || xMins[0] != 0 || xMins[1] != 0 {
		t.Errorf("empty glyphs gave %d, %v, %v", n, xMins, err)
	}
}

// The same truncation sweep as for the real fixture, over the synthetic buffer
// — which carries the shapes the real one does not, so this is what reaches the
// bounds checks inside the composite and explicit-bbox readers.
func TestReconstructGlyfSyntheticTruncationSweep(t *testing.T) {
	const (
		argsAreWords    = 1 << 0
		weHaveAScale    = 1 << 3
		weHaveInstructs = 1 << 8
	)
	s := &synthGlyf{indexFormat: 1}
	s.addEmpty()
	s.addSimple(3, true)
	s.addSimple(4, false)
	s.addComposite(weHaveAScale, 2+2, 0)
	s.addComposite(argsAreWords|weHaveInstructs, 4, 5)
	data, locaLen := s.bytes()
	if _, _, _, _, err := reconstructGlyf(data, locaLen); err != nil {
		t.Fatalf("the whole buffer must decode: %v", err)
	}
	for n := 0; n < len(data); n++ {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("truncated to %d bytes: panicked with %v", n, p)
				}
			}()
			_, _, _, _, _ = reconstructGlyf(data[:n], locaLen)
		}()
	}
	// And shrinking each declared substream size in turn, which truncates a
	// stream from the inside rather than cutting the buffer's tail.
	for sub := 0; sub < glyfNumSubstreams; sub++ {
		off := 8 + 4*sub
		orig := be32(data[off:])
		if orig == 0 {
			continue
		}
		for _, less := range []uint32{1, orig} {
			bad := append([]byte(nil), data...)
			binary.BigEndian.PutUint32(bad[off:], orig-less)
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("substream %d short by %d: panicked with %v", sub, less, p)
					}
				}()
				_, _, _, _, _ = reconstructGlyf(bad, locaLen)
			}()
		}
	}
}

// reconstructTables' own guards, each reached by a container built to trip it.
func TestReconstructTablesGuards(t *testing.T) {
	tbl := func(tag string, transform uint8, data []byte, orig uint32) woff2Table {
		return woff2Table{tag: tag, transform: transform, data: data, origLength: orig}
	}
	for _, c := range []struct {
		name   string
		tables []woff2Table
	}{
		{"origLength disagrees", []woff2Table{tbl("cmap", 0, []byte{1, 2}, 3)}},
		{"transformed glyf with no loca", []woff2Table{tbl("glyf", 0, []byte{0, 0}, 4)}},
		{"glyf reconstruction fails", []woff2Table{
			tbl("glyf", 0, []byte{0, 0}, 4), tbl("loca", 0, nil, 4)}},
		{"transformed loca with an untransformed glyf", []woff2Table{
			tbl("glyf", 3, []byte{}, 0), tbl("loca", 0, []byte{}, 4)}},
		{"transformed hmtx with no hhea", []woff2Table{tbl("hmtx", 1, []byte{1}, 4)}},
		{"transformed hmtx with no maxp", []woff2Table{
			tbl("hmtx", 1, []byte{1}, 4), tbl("hhea", 0, make([]byte, 36), 36)}},
		{"a table nothing reconstructed", []woff2Table{tbl("cmap", 2, []byte{1}, 1)}},
	} {
		if err := reconstructTables(c.tables); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// An hmtx whose reconstruction itself fails, past every structural guard.
	tables := []woff2Table{
		tbl("hmtx", 1, []byte{0xfc}, 4), // reserved flag bits set
		tbl("hhea", 0, make([]byte, 36), 36),
		tbl("maxp", 0, []byte{0, 0, 0, 0, 0, 1}, 6),
		tbl("head", 0, make([]byte, 52), 52),
		tbl("glyf", 3, make([]byte, 4), 4),
		tbl("loca", 3, make([]byte, 4), 4),
	}
	if err := reconstructTables(tables); err == nil {
		t.Error("an hmtx with reserved bits set must be refused")
	}
}

// The short loca format's own bounds inside xMinsFromGlyf.
func TestXMinsFromGlyfShortLoca(t *testing.T) {
	maxp := &woff2Table{out: []byte{0, 0, 0, 0, 0, 2}}
	head := &woff2Table{out: make([]byte, 52)} // indexFormat 0 = short
	glyf := &woff2Table{out: make([]byte, 16)}
	// A glyph with an outline, so its xMin is read rather than skipped.
	binary.BigEndian.PutUint16(glyf.out[2:], uint16(0xfff9)) // -7 as an int16
	loca := []byte{0, 0, 0, 4, 0, 8}                         // 0, 8, 16 once doubled
	n, xMins, err := xMinsFromGlyf(maxp, head, glyf, &woff2Table{out: loca})
	if err != nil || n != 2 || xMins[0] != -7 {
		t.Errorf("short loca gave %d, %v, %v", n, xMins, err)
	}
	if _, _, err := xMinsFromGlyf(maxp, head, glyf, &woff2Table{out: []byte{0, 0}}); err == nil {
		t.Error("a short loca too short for the count must be refused")
	}
}

// The last bounds, each reached by a stream that stops exactly there. They are
// the reads that follow a check which passed, so only an input built for each
// one gets to them.
func TestGlyphReaderLastBounds(t *testing.T) {
	empty := func() *woff2Reader { return &woff2Reader{} }

	// A component saying its arguments are words, with too few bytes for them.
	if _, err := woff2CompositeGlyph(&woff2Reader{b: []byte{0x00, 0x01, 0, 0}}, empty(), empty(), empty()); err == nil {
		t.Error("a component short of its word arguments must fail")
	}
	// A component saying it has instructions, with no length in the glyph stream.
	if _, err := woff2CompositeGlyph(&woff2Reader{b: []byte{0x01, 0x00, 0, 0, 0, 0}}, empty(), empty(), empty()); err == nil {
		t.Error("a component with no instruction length must fail")
	}
	// ... and with a length but no instruction bytes, and no bbox.
	comp := &woff2Reader{b: []byte{0x01, 0x00, 0, 0, 0, 0}}
	if _, err := woff2CompositeGlyph(comp, &woff2Reader{b: []byte{5}}, empty(), &woff2Reader{b: make([]byte, 8)}); err == nil {
		t.Error("a component short of its instruction bytes must fail")
	}

	one := func() *woff2Reader { return &woff2Reader{b: []byte{1}} }
	// Flags read, but nothing for the triplets.
	if _, err := woff2SimpleGlyph(1, one(), &woff2Reader{b: []byte{0}}, empty(), empty(), empty(), false, false); err == nil {
		t.Error("a glyph short of its triplets must fail")
	}
	// Triplets read, but no instruction length after them.
	if _, err := woff2SimpleGlyph(1, one(), &woff2Reader{b: []byte{0}}, &woff2Reader{b: []byte{0}}, empty(), empty(), false, false); err == nil {
		t.Error("a glyph with no instruction length must fail")
	}
	// Everything read, but an explicit bbox promised and absent.
	if _, err := woff2SimpleGlyph(1, one(), &woff2Reader{b: []byte{0}}, &woff2Reader{b: []byte{0, 0}}, empty(), empty(), true, false); err == nil {
		t.Error("a glyph promising a bbox it has not got must fail")
	}
	// And an instruction length with no bytes behind it.
	if _, err := woff2SimpleGlyph(1, one(), &woff2Reader{b: []byte{0}}, &woff2Reader{b: []byte{0, 3}}, empty(), empty(), false, false); err == nil {
		t.Error("a glyph short of its instruction bytes must fail")
	}
}

// The long loca format's bound in xMinsFromGlyf.
func TestXMinsFromGlyfLongLocaTooShort(t *testing.T) {
	maxp := &woff2Table{out: []byte{0, 0, 0, 0, 0, 2}}
	head := &woff2Table{out: make([]byte, 52)}
	binary.BigEndian.PutUint16(head.out[50:], 1) // long
	if _, _, err := xMinsFromGlyf(maxp, head, &woff2Table{out: make([]byte, 8)}, &woff2Table{out: make([]byte, 4)}); err == nil {
		t.Error("a long loca too short for the count must be refused")
	}
}

// decodeWOFF1's own two remaining guards, reached directly: Parse rejects a
// buffer under twelve bytes before it ever gets here, and a table whose bytes
// are not zlib at all.
func TestDecodeWOFF1Guards(t *testing.T) {
	short := make([]byte, 20)
	copy(short, "wOFF")
	if _, err := decodeWOFF1(short); err == nil {
		t.Error("a header under 44 bytes must be refused")
	}
	const headerSize = 44
	file := make([]byte, headerSize+20+4)
	copy(file, "wOFF")
	binary.BigEndian.PutUint32(file[4:], 0x00010000)
	binary.BigEndian.PutUint16(file[12:], 1)
	copy(file[headerSize:], "cmap")
	binary.BigEndian.PutUint32(file[headerSize+4:], headerSize+20) // offset
	binary.BigEndian.PutUint32(file[headerSize+8:], 4)             // compressed
	binary.BigEndian.PutUint32(file[headerSize+12:], 99)           // original
	copy(file[headerSize+20:], []byte{0xff, 0xff, 0xff, 0xff})     // not zlib
	if _, err := decodeWOFF1(file); err == nil {
		t.Error("bytes that are not zlib must be refused")
	}
}

// A container that declares a CONTAINER as its inner sfnt version. Nothing
// legitimate does this, but the flavor field is attacker-controlled: without
// the guard, unwrapping would hand Parse another container and recurse.
func TestWOFF2ContainerDeclaringAContainer(t *testing.T) {
	tables := []buildTable{{tag: "cmap", data: []byte{0, 0, 0, 0}, transform: 0}}
	if _, err := Parse(buildWOFF2(tables, sigWOFF2, nil)); err == nil {
		t.Error("a container whose flavor is a container signature must be refused")
	}
}

// A WOFF table whose zlib header is valid and whose stream is not: the reader
// is built, and the read fails.
func TestDecodeWOFF1CorruptDeflate(t *testing.T) {
	const headerSize = 44
	body := []byte{0x78, 0x9c, 0xff, 0xff, 0xff, 0xff} // a zlib header, then noise
	file := make([]byte, headerSize+20+len(body))
	copy(file, "wOFF")
	binary.BigEndian.PutUint32(file[4:], 0x00010000)
	binary.BigEndian.PutUint16(file[12:], 1)
	copy(file[headerSize:], "cmap")
	binary.BigEndian.PutUint32(file[headerSize+4:], headerSize+20)
	binary.BigEndian.PutUint32(file[headerSize+8:], uint32(len(body)))
	binary.BigEndian.PutUint32(file[headerSize+12:], 99)
	copy(file[headerSize+20:], body)
	if _, err := decodeWOFF1(file); err == nil {
		t.Error("a corrupt deflate stream must be refused")
	}
}
