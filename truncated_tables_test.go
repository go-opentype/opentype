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

func locaTailTables(longLoca bool) map[string][]byte {
	loca, glyf := glyfAndLoca([][]byte{nil, squareGlyph(), nil, nil, nil}, longLoca)
	format := int16(0)
	if longLoca {
		format = 1
	}
	return map[string][]byte{
		"head": headTable(1000, format),
		"maxp": maxpTable(5),
		"hhea": hheaTable(800, -200, 0, 5),
		"hmtx": hmtxTable([]int{250, 500, 510, 520, 530}, []int{0, 0, 0, 0, 0}, 5),
		"cmap": cmapTable([][]byte{cmap4FromMap(map[rune]uint16{'A': 1, ' ': 4})}),
		"loca": loca,
		"glyf": glyf,
	}
}

func TestParseLocaMissingEmptyTail(t *testing.T) {
	for _, longLoca := range []bool{false, true} {
		size := 2
		if longLoca {
			size = 4
		}
		for entries := 3; entries <= 6; entries++ {
			t.Run(fmt.Sprintf("long=%v/entries=%d", longLoca, entries), func(t *testing.T) {
				tables := locaTailTables(longLoca)
				complete := mustParse(t, assemble(versionTrueType, tables))
				tables["loca"] = tables["loca"][:entries*size]
				f := mustParse(t, assemble(versionTrueType, tables))
				if !reflect.DeepEqual(f.loca, complete.loca) {
					t.Errorf("loca = %v, want %v", f.loca, complete.loca)
				}
				if raw, _ := f.Table("loca"); !reflect.DeepEqual(raw, tables["loca"]) {
					t.Error("the raw loca table must retain the original bytes")
				}
				for gid := GlyphIndex(0); int(gid) < f.NumGlyphs(); gid++ {
					want, err := complete.glyphContours(gid)
					if err != nil {
						t.Fatal(err)
					}
					got, err := f.glyphContours(gid)
					if err != nil || !reflectContoursEqual(want, got) {
						t.Fatalf("glyph %d changed (err %v)", gid, err)
					}
					if f.GlyphAdvance(gid) != complete.GlyphAdvance(gid) {
						t.Errorf("glyph %d advance changed", gid)
					}
				}
				if gid, ok := f.GlyphIndex(' '); !ok || gid != 4 {
					t.Fatalf("trailing space mapping = %d,%v, want 4,true", gid, ok)
				}
				data, remap, err := f.SubsetTrueType([]GlyphIndex{1, 4})
				if err != nil {
					t.Fatal(err)
				}
				sub := mustParse(t, data)
				for _, old := range []GlyphIndex{0, 1, 4} {
					gid, ok := remap[old]
					if !ok {
						t.Fatalf("glyph %d missing from subset", old)
					}
					want, err := f.glyphContours(old)
					if err != nil {
						t.Fatal(err)
					}
					got, err := sub.glyphContours(gid)
					if err != nil || !reflectContoursEqual(want, got) || sub.GlyphAdvance(gid) != f.GlyphAdvance(old) {
						t.Fatalf("subset glyph %d changed (err %v)", old, err)
					}
				}
			})
		}
	}
}

func TestParseLocaMissingTailEmptyGlyf(t *testing.T) {
	for _, longLoca := range []bool{false, true} {
		t.Run(fmt.Sprintf("long=%v", longLoca), func(t *testing.T) {
			tables := locaTailTables(longLoca)
			size := 2
			if longLoca {
				size = 4
			}
			tables["loca"] = make([]byte, size) // one complete zero offset
			tables["glyf"] = []byte{}
			f := mustParse(t, assemble(versionTrueType, tables))
			for gid := GlyphIndex(0); int(gid) < f.NumGlyphs(); gid++ {
				if outline, err := f.glyphContours(gid); err != nil || len(outline) != 0 {
					t.Fatalf("glyph %d = %v, %v, want empty", gid, outline, err)
				}
			}
		})
	}
}

func TestParseLocaUnrecoverableTail(t *testing.T) {
	for _, longLoca := range []bool{false, true} {
		size := 2
		if longLoca {
			size = 4
		}
		setOffset := func(b []byte, index int, value uint32) {
			if longLoca {
				binary.BigEndian.PutUint32(b[index*size:], value)
			} else {
				binary.BigEndian.PutUint16(b[index*size:], uint16(value/2))
			}
		}
		cases := []struct {
			name  string
			alter func(map[string][]byte)
		}{
			{"no offsets", func(tb map[string][]byte) { tb["loca"] = nil }},
			{"no offsets and empty glyf", func(tb map[string][]byte) { tb["loca"], tb["glyf"] = nil, nil }},
			{"partial first offset", func(tb map[string][]byte) { tb["loca"] = tb["loca"][:size-1] }},
			{"unindexed glyph data", func(tb map[string][]byte) { tb["loca"] = tb["loca"][:2*size] }},
			{"partial terminal offset", func(tb map[string][]byte) { tb["loca"] = tb["loca"][:3*size-1] }},
			{"partial offset after EOF", func(tb map[string][]byte) { tb["loca"] = tb["loca"][:3*size+1] }},
			{"offset past glyf", func(tb map[string][]byte) {
				tb["loca"] = tb["loca"][:3*size]
				setOffset(tb["loca"], 2, uint32(len(tb["glyf"])+2))
			}},
			{"non-monotonic prefix", func(tb map[string][]byte) {
				tb["loca"] = tb["loca"][:3*size]
				setOffset(tb["loca"], 0, 2)
			}},
			{"32-bit overflow", func(tb map[string][]byte) {
				tb["loca"] = tb["loca"][:3*size]
				setOffset(tb["loca"], 2, 0xffffffff)
			}},
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("long=%v/%s", longLoca, tc.name), func(t *testing.T) {
				tables := locaTailTables(longLoca)
				tc.alter(tables)
				if _, err := Parse(assemble(versionTrueType, tables)); !errors.Is(err, errTruncated) {
					t.Fatalf("Parse = %v, want errTruncated", err)
				}
			})
		}
	}
}
