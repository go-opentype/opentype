// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"testing"
)

// descHead builds a full 54-byte head table with a bounding box and macStyle.
func descHead(unitsPerEm, xMin, yMin, xMax, yMax int, macStyle uint16) []byte {
	b := make([]byte, 54)
	binary.BigEndian.PutUint16(b[18:], uint16(unitsPerEm))
	binary.BigEndian.PutUint16(b[36:], uint16(int16(xMin)))
	binary.BigEndian.PutUint16(b[38:], uint16(int16(yMin)))
	binary.BigEndian.PutUint16(b[40:], uint16(int16(xMax)))
	binary.BigEndian.PutUint16(b[42:], uint16(int16(yMax)))
	binary.BigEndian.PutUint16(b[44:], macStyle)
	binary.BigEndian.PutUint16(b[50:], 0) // short loca
	return b
}

// os2Table builds an OS/2 table of the given version. sxHeight and sCapHeight are
// written only for version >= 2 (a 96-byte table); a version-0 table is 78 bytes.
func os2Table(version, weight, width, familyClass int, fsSelection uint16, xHeight, capHeight int) []byte {
	n := 78
	if version >= 2 {
		n = 96
	}
	b := make([]byte, n)
	binary.BigEndian.PutUint16(b[0:], uint16(version))
	binary.BigEndian.PutUint16(b[4:], uint16(weight))
	binary.BigEndian.PutUint16(b[6:], uint16(width))
	binary.BigEndian.PutUint16(b[30:], uint16(int16(familyClass)))
	binary.BigEndian.PutUint16(b[62:], fsSelection)
	if version >= 2 {
		binary.BigEndian.PutUint16(b[86:], uint16(int16(xHeight)))
		binary.BigEndian.PutUint16(b[88:], uint16(int16(capHeight)))
	}
	return b
}

// postTable builds a 32-byte post table carrying an italic angle and fixed-pitch
// flag.
func postTable(italicAngleDeg float64, fixedPitch bool) []byte {
	b := make([]byte, 32)
	binary.BigEndian.PutUint32(b[0:], 0x00020000) // version 2.0
	binary.BigEndian.PutUint32(b[4:], uint32(int32(italicAngleDeg*65536)))
	if fixedPitch {
		binary.BigEndian.PutUint32(b[12:], 1)
	}
	return b
}

// descFont assembles a minimal glyf font with the given head/OS-2/post tables.
// A nil os2 or post table is omitted, exercising the absent-table paths.
func descFont(t *testing.T, head, os2, post []byte) *Font {
	t.Helper()
	loca, glyf := glyfAndLoca([][]byte{nil, squareGlyph()}, false)
	tables := map[string][]byte{
		"head": head,
		"maxp": maxpTable(2),
		"hhea": hheaTable(800, -200, 100, 2),
		"hmtx": hmtxTable([]int{500, 500}, []int{0, 0}, 2),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1})}),
		"loca": loca,
		"glyf": glyf,
	}
	if os2 != nil {
		tables["OS/2"] = os2
	}
	if post != nil {
		tables["post"] = post
	}
	return mustParse(t, assemble(versionTrueType, tables))
}

func TestDescriptorScalarsFull(t *testing.T) {
	head := descHead(1000, -50, -250, 1200, 900, 0)
	os2 := os2Table(2, 400, 5, 1<<8, 0, 480, 700) // serif family class 1
	post := postTable(0, false)
	f := descFont(t, head, os2, post)

	if f.UnitsPerEm() != 1000 {
		t.Errorf("UnitsPerEm = %d", f.UnitsPerEm())
	}
	if f.Ascent() != 800 || f.Descent() != -200 || f.LineGap() != 100 {
		t.Errorf("vmetrics = %d %d %d", f.Ascent(), f.Descent(), f.LineGap())
	}
	if x0, y0, x1, y1 := f.FontBBox(); x0 != -50 || y0 != -250 || x1 != 1200 || y1 != 900 {
		t.Errorf("FontBBox = %d %d %d %d", x0, y0, x1, y1)
	}
	if f.CapHeight() != 700 || f.XHeight() != 480 {
		t.Errorf("cap/x height = %d %d", f.CapHeight(), f.XHeight())
	}
	if f.ItalicAngle() != 0 {
		t.Errorf("ItalicAngle = %v", f.ItalicAngle())
	}
	if f.WeightClass() != 400 || f.WidthClass() != 5 {
		t.Errorf("weight/width class = %d %d", f.WeightClass(), f.WidthClass())
	}
	if !f.IsSerif() {
		t.Error("IsSerif = false, want true")
	}
	if f.IsItalic() {
		t.Error("IsItalic = true, want false")
	}
	if f.IsFixedPitch() {
		t.Error("IsFixedPitch = true, want false")
	}
	// serif + nonsymbolic (family class 1 is not symbolic).
	if got, want := f.Flags(), pdfFlagSerif|pdfFlagNonsymbolic; got != want {
		t.Errorf("Flags = %d, want %d", got, want)
	}
	if f.StemV() != 83 { // 50 + (400-100)*11/100
		t.Errorf("StemV = %d, want 83", f.StemV())
	}
}

func TestDescriptorNoOptionalTables(t *testing.T) {
	f := descFont(t, descHead(2048, 0, 0, 0, 0, 0), nil, nil)
	if f.CapHeight() != 0 || f.XHeight() != 0 || f.WeightClass() != 0 {
		t.Errorf("absent OS/2 should zero cap/x/weight: %d %d %d", f.CapHeight(), f.XHeight(), f.WeightClass())
	}
	if f.ItalicAngle() != 0 || f.IsFixedPitch() {
		t.Errorf("absent post should zero angle/fixedpitch")
	}
	// No OS/2: not serif, not symbolic -> nonsymbolic only.
	if f.Flags() != pdfFlagNonsymbolic {
		t.Errorf("Flags = %d, want %d", f.Flags(), pdfFlagNonsymbolic)
	}
	if f.StemV() != 83 { // absent weight class defaults to 400
		t.Errorf("StemV = %d, want 83", f.StemV())
	}
}

func TestDescriptorOS2Version0HasNoCapHeight(t *testing.T) {
	os2 := os2Table(0, 700, 3, 8<<8, 0, 0, 0) // version 0, sans-serif class 8
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), os2, nil)
	if f.CapHeight() != 0 || f.XHeight() != 0 {
		t.Errorf("version-0 OS/2 must not report cap/x height: %d %d", f.CapHeight(), f.XHeight())
	}
	if f.IsSerif() {
		t.Error("class 8 is sans-serif")
	}
	if f.StemV() != 116 { // 50 + (700-100)*11/100
		t.Errorf("StemV = %d, want 116", f.StemV())
	}
}

func TestDescriptorOS2Version2Truncated(t *testing.T) {
	// A version-2 OS/2 truncated before sxHeight exercises the length guard.
	os2 := os2Table(2, 400, 5, 0, 0, 0, 0)[:88]
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), os2, nil)
	if f.CapHeight() != 0 || f.XHeight() != 0 {
		t.Errorf("truncated version-2 OS/2 must not report cap/x height")
	}
}

func TestDescriptorItalicSources(t *testing.T) {
	base := descHead(1000, 0, 0, 0, 0, 0)
	// via post italic angle
	if f := descFont(t, base, nil, postTable(-9, false)); !f.IsItalic() || f.ItalicAngle() != -9 {
		t.Error("post italic angle should make IsItalic true and report the angle")
	}
	// via head macStyle italic bit (bit 1)
	if f := descFont(t, descHead(1000, 0, 0, 0, 0, 0x02), nil, nil); !f.IsItalic() {
		t.Error("macStyle italic bit should make IsItalic true")
	}
	// via OS/2 fsSelection italic bit (bit 0)
	if f := descFont(t, base, os2Table(1, 400, 5, 0, 0x01, 0, 0), nil); !f.IsItalic() {
		t.Error("fsSelection italic bit should make IsItalic true")
	}
	// italic contributes the Italic flag bit.
	f := descFont(t, base, nil, postTable(-9, false))
	if f.Flags()&pdfFlagItalic == 0 {
		t.Error("Flags should carry Italic")
	}
}

func TestDescriptorFixedPitchAndSymbolic(t *testing.T) {
	os2 := os2Table(2, 400, 5, 12<<8, 0, 0, 0) // family class 12 = symbolic
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), os2, postTable(0, true))
	if !f.IsFixedPitch() {
		t.Error("IsFixedPitch should be true")
	}
	if !f.isSymbolic() {
		t.Error("family class 12 should be symbolic")
	}
	want := pdfFlagFixedPitch | pdfFlagSymbolic
	if f.Flags() != want {
		t.Errorf("Flags = %d, want %d", f.Flags(), want)
	}
}

func TestStemVFloor(t *testing.T) {
	os2 := os2Table(2, 1, 5, 0, 0, 0, 0) // weight 1 -> estimate below the floor
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), os2, nil)
	if f.StemV() != 50 {
		t.Errorf("StemV = %d, want floor 50", f.StemV())
	}
}
