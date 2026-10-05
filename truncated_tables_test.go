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

func TestParseVmtxMissingBearings(t *testing.T) {
	cases := []struct {
		name string
		size int
		tail [2]int
	}{
		{"complete", 40, [2]int{-95, 105}},
		{"no trailing bearings", 36, [2]int{0, 0}},
		{"one trailing byte", 37, [2]int{0, 0}},
		{"one trailing bearing", 38, [2]int{-95, 0}},
		{"partial second bearing", 39, [2]int{-95, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tables := vertTables()
			tsbs := append([]int(nil), vertTsb...)
			tsbs[9] = -95
			tables["vmtx"] = vmtxTable(vertAdv, tsbs, 9)
			complete := mustParse(t, assemble(versionTrueType, tables))
			tables["vmtx"] = tables["vmtx"][:tc.size]
			f := mustParse(t, assemble(versionTrueType, tables))
			tsbs[9], tsbs[10] = tc.tail[0], tc.tail[1]
			if !f.HasVerticalMetrics() || !reflect.DeepEqual(f.tsbs, tsbs) {
				t.Fatalf("vertical=%v, tsbs=%v, want true,%v", f.HasVerticalMetrics(), f.tsbs, tsbs)
			}
			if !reflect.DeepEqual(f.vertAdvances, complete.vertAdvances) || !reflect.DeepEqual(f.advances, complete.advances) {
				t.Fatal("horizontal or vertical advances changed")
			}
			if raw, _ := f.Table("vmtx"); !reflect.DeepEqual(raw, tables["vmtx"]) {
				t.Error("the raw vmtx table must retain the original bytes")
			}
			face, reference := f.NewFace(100), complete.NewFace(100)
			if face.VerticalAdvance('R') != 92 || face.VerticalAdvance(' ') != 92 {
				t.Fatal("trailing glyphs must retain the last vertical advance")
			}
			if origin, ok := face.VerticalOrigin('R'); !ok || origin != 88 {
				t.Fatalf("VORG origin = %d,%v, want 88,true", origin, ok)
			}
			for _, r := range []rune{'A', 'E', 'R', ' '} {
				gotBounds, gotMask, _, gotAdvance, gotOK := face.GlyphMask(r, 0, 0)
				wantBounds, wantMask, _, wantAdvance, wantOK := reference.GlyphMask(r, 0, 0)
				if gotOK != wantOK || gotBounds != wantBounds || gotAdvance != wantAdvance || !reflect.DeepEqual(gotMask, wantMask) {
					t.Fatalf("horizontal rendering of %q changed", r)
				}
			}
		})
	}
}

func TestParseVmtxTruncatedPairs(t *testing.T) {
	for size := 0; size < 36; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			tables := vertTables()
			tables["vmtx"] = tables["vmtx"][:size]
			if _, err := Parse(assemble(versionTrueType, tables)); !errors.Is(err, errTruncated) {
				t.Fatalf("Parse with %d vmtx bytes = %v, want errTruncated", size, err)
			}
		})
	}
}

func TestParseVmtxMissingBearingsCFF(t *testing.T) {
	tables := cffTables([][]byte{(&csb{}).op(14).b, cffSquare()}, map[rune]uint16{'A': 1})
	tables["vhea"] = vheaTable(800, -200, 0, 1)
	tables["vmtx"] = vmtxTable([]int{850, 850}, []int{-3, 25}, 1)[:4]
	f := mustParse(t, assemble(versionOTTO, tables))
	if !f.HasVerticalMetrics() || !reflect.DeepEqual(f.tsbs, []int{-3, 0}) {
		t.Fatalf("vertical=%v, tsbs=%v, want true,[-3 0]", f.HasVerticalMetrics(), f.tsbs)
	}
	if f.NewFace(100).VerticalAdvance('A') != 85 {
		t.Fatal("CFF glyph must retain the shared vertical advance")
	}
	if _, mask, _, _, ok := f.NewFace(20).GlyphMask('A', 0, 0); !ok || mask == nil {
		t.Fatal("CFF glyph must remain renderable")
	}
}
