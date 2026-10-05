// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func hmtxRecoveryTables(longLoca bool) map[string][]byte {
	positive := encodeSimpleGlyph([]contour{{{40, 0, true}, {140, 0, true}, {40, 100, true}}})
	negative := encodeSimpleGlyph([]contour{{{-30, 0, true}, {70, 0, true}, {-30, 100, true}}})
	composite := compositeGlyphBytes([]component{{glyphIndex: 2, arg1: 70, argsAreXY: true}})
	binary.BigEndian.PutUint16(composite[2:], 40) // xMin = -30 + 70
	binary.BigEndian.PutUint16(composite[6:], 140)
	binary.BigEndian.PutUint16(composite[8:], 100)
	zeroContours := make([]byte, 12)
	binary.BigEndian.PutUint16(zeroContours[2:], 123) // no outline, so no usable xMin
	loca, glyf := glyfAndLoca([][]byte{nil, positive, negative, composite, nil, zeroContours}, longLoca)
	locFmt := int16(0)
	if longLoca {
		locFmt = 1
	}
	return map[string][]byte{
		"head": headTable(1000, locFmt),
		"maxp": maxpTable(6),
		"hhea": hheaTable(800, -200, 0, 2),
		"hmtx": hmtxTable([]int{250, 600, 600, 600, 600, 600}, []int{0, 9, -13, -17, 19, 27}, 2),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1, 'B': 2, 'C': 3, ' ': 4})}),
		"loca": loca,
		"glyf": glyf,
	}
}

func TestParseHmtxMissingBearings(t *testing.T) {
	cases := []struct {
		name string
		size int
		lsbs []int
	}{
		{"complete", 16, []int{0, 9, -13, -17, 19, 27}},
		{"no trailing bearings", 8, []int{0, 9, -30, 40, 0, 0}},
		{"one trailing byte", 9, []int{0, 9, -30, 40, 0, 0}},
		{"one trailing bearing", 10, []int{0, 9, -13, 40, 0, 0}},
		{"partial second bearing", 11, []int{0, 9, -13, 40, 0, 0}},
		{"missing last byte", 15, []int{0, 9, -13, -17, 19, 0}},
	}
	for _, longLoca := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("longLoca=%v/%s", longLoca, tc.name), func(t *testing.T) {
				tables := hmtxRecoveryTables(longLoca)
				complete := mustParse(t, assemble(versionTrueType, tables))
				tables["hmtx"] = tables["hmtx"][:tc.size]
				f := mustParse(t, assemble(versionTrueType, tables))
				if !reflect.DeepEqual(f.lsbs, tc.lsbs) {
					t.Errorf("lsbs = %v, want %v", f.lsbs, tc.lsbs)
				}
				if !reflect.DeepEqual(f.advances, complete.advances) {
					t.Errorf("advances = %v, want %v", f.advances, complete.advances)
				}
				for _, r := range []rune{'A', 'B', 'C', ' '} {
					gid, ok := f.GlyphIndex(r)
					wantGID, wantOK := complete.GlyphIndex(r)
					if gid != wantGID || ok != wantOK {
						t.Fatalf("GlyphIndex(%q) = %d,%v, want %d,%v", r, gid, ok, wantGID, wantOK)
					}
					got, err := f.glyphContours(gid)
					if err != nil {
						t.Fatal(err)
					}
					want, err := complete.glyphContours(gid)
					if err != nil || !reflectContoursEqual(want, got) {
						t.Fatalf("glyph %d changed: got %v, want %v (err %v)", gid, got, want, err)
					}
				}
			})
		}
	}
}

func TestParseHmtxTruncatedPairs(t *testing.T) {
	for size := 0; size < 8; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			tables := hmtxRecoveryTables(false)
			tables["hmtx"] = tables["hmtx"][:size]
			if _, err := Parse(assemble(versionTrueType, tables)); !errors.Is(err, errTruncated) {
				t.Fatalf("Parse with %d hmtx bytes: %v, want errTruncated", size, err)
			}
		})
	}
}

func TestSubsetTrueTypeRecoveredBearings(t *testing.T) {
	tables := hmtxRecoveryTables(false)
	tables["hmtx"] = tables["hmtx"][:8]
	f := mustParse(t, assemble(versionTrueType, tables))
	data, remap, err := f.SubsetTrueType([]GlyphIndex{3, 4})
	if err != nil {
		t.Fatal(err)
	}
	sub := mustParse(t, data)
	if sub.numberOfHMetrics != sub.numGlyphs {
		t.Fatal("subset must write a complete metric pair for every glyph")
	}
	hmtx, ok := sub.Table("hmtx")
	if !ok || len(hmtx) != 4*sub.numGlyphs {
		t.Fatalf("subset hmtx length = %d, want %d", len(hmtx), 4*sub.numGlyphs)
	}
	for old, lsb := range map[GlyphIndex]int{0: 0, 2: -30, 3: 40, 4: 0} {
		gid, ok := remap[old]
		if !ok {
			t.Fatalf("glyph %d missing from subset closure", old)
		}
		if sub.GlyphAdvance(gid) != f.GlyphAdvance(old) || sub.lsbs[gid] != lsb || int(sbe16(hmtx[4*int(gid)+2:])) != lsb {
			t.Errorf("glyph %d -> %d: advance %d, lsb %d, want %d,%d", old, gid, sub.GlyphAdvance(gid), sub.lsbs[gid], f.GlyphAdvance(old), lsb)
		}
		want, err := f.glyphContours(old)
		if err != nil {
			t.Fatal(err)
		}
		got, err := sub.glyphContours(gid)
		if err != nil || !reflectContoursEqual(want, got) {
			t.Fatalf("subset glyph %d changed its outline (err %v)", old, err)
		}
	}
}

func TestParseHmtxMissingBearingsWithoutGlyf(t *testing.T) {
	for _, cff2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("CFF2=%v", cff2), func(t *testing.T) {
			var tables map[string][]byte
			if cff2 {
				g := &csb{}
				g.num(100).num(100).op(21).num(100).num(0).num(0).num(100).num(-100).num(0).op(5)
				tables = cff2OTTOTables([][]byte{nil, g.b}, nil, map[rune]uint16{'A': 1}, nil)
			} else {
				tables = cffTables([][]byte{(&csb{}).op(14).b, cffSquare()}, map[rune]uint16{'A': 1})
			}
			tables["hhea"] = hheaTable(800, -200, 0, 1)
			tables["hmtx"] = hmtxTable([]int{500, 500}, []int{7, 100}, 1)[:4]
			f := mustParse(t, assemble(versionOTTO, tables))
			if f.GlyphAdvance(1) != 500 || !reflect.DeepEqual(f.lsbs, []int{7, 0}) {
				t.Fatalf("advances = %v, lsbs = %v, want [500 500], [7 0]", f.advances, f.lsbs)
			}
			if _, mask, _, _, ok := f.NewFace(20).GlyphMask('A', 0, 0); !ok || mask == nil {
				t.Fatal("CFF glyph must remain renderable")
			}
		})
	}
}

func TestParseHmtxMissingBearingsBadGlyphRanges(t *testing.T) {
	cases := []struct {
		name       string
		start, end uint32
	}{
		{"reversed", 10, 0},
		{"past glyf", 0, 10000},
		{"short header", 0, 4},
		{"32-bit overflow", 0xfffffff0, 0xffffffff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tables := hmtxRecoveryTables(true)
			tables["hmtx"] = tables["hmtx"][:8]
			binary.BigEndian.PutUint32(tables["loca"][2*4:], tc.start)
			binary.BigEndian.PutUint32(tables["loca"][3*4:], tc.end)
			f := mustParse(t, assemble(versionTrueType, tables))
			if f.lsbs[2] != 0 || f.GlyphAdvance(2) != 600 {
				t.Fatalf("invalid glyph range: lsb = %d, advance = %d, want 0,600", f.lsbs[2], f.GlyphAdvance(2))
			}
		})
	}
}
