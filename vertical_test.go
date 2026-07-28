// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"sort"
	"testing"
)

// vheaTable builds a 36-byte vhea table, mirroring hheaTable: ascender,
// descender and lineGap are int16 at offsets 4/6/8 and numOfLongVerMetrics is
// the uint16 at offset 34.
func vheaTable(ascender, descender, lineGap, numOfLongVerMetrics int) []byte {
	b := make([]byte, 36)
	binary.BigEndian.PutUint16(b[4:], uint16(int16(ascender)))
	binary.BigEndian.PutUint16(b[6:], uint16(int16(descender)))
	binary.BigEndian.PutUint16(b[8:], uint16(int16(lineGap)))
	binary.BigEndian.PutUint16(b[34:], uint16(numOfLongVerMetrics))
	return b
}

// vmtxTable builds a vmtx table, mirroring hmtxTable: the first
// numOfLongVerMetrics glyphs carry (advanceHeight, topSideBearing) pairs and the
// rest carry only a top side bearing (sharing the last advance height).
func vmtxTable(advances, tsbs []int, numOfLongVerMetrics int) []byte {
	w := &bw{}
	for i := range advances {
		if i < numOfLongVerMetrics {
			w.u16(uint16(advances[i]))
			w.i16(int16(tsbs[i]))
		} else {
			w.i16(int16(tsbs[i]))
		}
	}
	return w.bytes()
}

// vorgTable builds a VORG table from a default vertical origin and a map of
// per-glyph overrides (records are emitted sorted by glyph index).
func vorgTable(defaultY int, overrides map[uint16]int) []byte {
	gids := make([]int, 0, len(overrides))
	for g := range overrides {
		gids = append(gids, int(g))
	}
	sort.Ints(gids)
	w := &bw{}
	w.u16(1) // majorVersion
	w.u16(0) // minorVersion
	w.i16(int16(defaultY))
	w.u16(uint16(len(gids)))
	for _, g := range gids {
		w.u16(uint16(g))
		w.i16(int16(overrides[uint16(g)]))
	}
	return w.bytes()
}

// vertAdv/vertTsb are the 11 per-glyph vertical advance heights and top side
// bearings of the standard test font.
var (
	vertAdv = []int{1000, 900, 900, 850, 700, 900, 900, 910, 920, 740, 800}
	vertTsb = []int{5, 15, 25, 35, 45, 55, 65, 75, 85, 95, 105}
)

// vertTables returns the standard font's table set with vhea, vmtx and VORG
// added (numOfLongVerMetrics=9, so glyphs 9 and 10 share glyph 8's advance).
func vertTables() map[string][]byte {
	tb := stdTables(false, false)
	tb["vhea"] = vheaTable(880, -120, 0, 9)
	tb["vmtx"] = vmtxTable(vertAdv, vertTsb, 9)
	tb["VORG"] = vorgTable(880, map[uint16]int{1: 800})
	return tb
}

func TestVerticalMetricsPresent(t *testing.T) {
	f := mustParse(t, assemble(versionTrueType, vertTables()))
	if !f.HasVerticalMetrics() {
		t.Fatal("HasVerticalMetrics=false want true")
	}
	// Trailing-run advance: glyphs 9 and 10 share glyph 8's advance (920).
	if f.vertAdvances[9] != 920 || f.vertAdvances[10] != 920 {
		t.Errorf("trailing-run advances=%d,%d want 920,920", f.vertAdvances[9], f.vertAdvances[10])
	}
	if f.tsbs[10] != 105 {
		t.Errorf("trailing tsb=%d want 105", f.tsbs[10])
	}

	fc := f.NewFace(100) // scale 0.1
	if asc, desc, gap := fc.VerticalMetrics(); asc != 88 || desc != -12 || gap != 0 {
		t.Errorf("VerticalMetrics=%d,%d,%d want 88,-12,0", asc, desc, gap)
	}
	if got := fc.VerticalAdvance('A'); got != 90 { // glyph 1 advance 900 * 0.1
		t.Errorf("VerticalAdvance('A')=%d want 90", got)
	}
	if got := fc.VerticalAdvance('Z'); got != 0 {
		t.Errorf("VerticalAdvance(unmapped)=%d want 0", got)
	}
	// VORG: glyph 1 ('A') overridden to 800; glyph 9 ('R') takes the default 880.
	if y, ok := fc.VerticalOrigin('A'); !ok || y != 80 {
		t.Errorf("VerticalOrigin('A')=%d,%v want 80,true", y, ok)
	}
	if y, ok := fc.VerticalOrigin('R'); !ok || y != 88 {
		t.Errorf("VerticalOrigin('R')=%d,%v want 88,true", y, ok)
	}
	if _, ok := fc.VerticalOrigin('Z'); ok {
		t.Error("VerticalOrigin(unmapped) ok=true want false")
	}
}

func TestVerticalMetricsAbsent(t *testing.T) {
	f := mustParse(t, stdBytes(false, false))
	if f.HasVerticalMetrics() {
		t.Fatal("HasVerticalMetrics=true want false")
	}
	fc := f.NewFace(100) // scale 0.1
	// No vhea: zero line metrics.
	if asc, desc, gap := fc.VerticalMetrics(); asc != 0 || desc != 0 || gap != 0 {
		t.Errorf("VerticalMetrics=%d,%d,%d want 0,0,0", asc, desc, gap)
	}
	// No vmtx: vertical advance falls back to one em (1000 * 0.1 = 100).
	if got := fc.VerticalAdvance('A'); got != 100 {
		t.Errorf("VerticalAdvance('A')=%d want 100 (em fallback)", got)
	}
	// No VORG: no vertical origin.
	if _, ok := fc.VerticalOrigin('A'); ok {
		t.Error("VerticalOrigin without VORG ok=true want false")
	}
}

func TestVmtxWithoutVheaIgnored(t *testing.T) {
	// A vmtx without a vhea supplies no numOfLongVerMetrics and is ignored.
	tb := stdTables(false, false)
	tb["vmtx"] = vmtxTable(vertAdv, vertTsb, 9)
	f := mustParse(t, assemble(versionTrueType, tb))
	if f.HasVerticalMetrics() {
		t.Fatal("HasVerticalMetrics=true want false (vmtx has no vhea)")
	}
	if f.hasVmtx {
		t.Error("hasVmtx=true want false")
	}
	fc := f.NewFace(100)
	if got := fc.VerticalAdvance('A'); got != 100 {
		t.Errorf("VerticalAdvance('A')=%d want 100 (em fallback)", got)
	}
}

func TestVerticalParseErrors(t *testing.T) {
	cases := []struct {
		name  string
		build func(tb map[string][]byte)
	}{
		{"vhea-short", func(tb map[string][]byte) { tb["vhea"] = make([]byte, 35) }},
		{"vhea-zero-metrics", func(tb map[string][]byte) { tb["vhea"] = vheaTable(880, -120, 0, 0) }},
		{"vhea-too-many-metrics", func(tb map[string][]byte) { tb["vhea"] = vheaTable(880, -120, 0, 12) }},
		{"vmtx-short", func(tb map[string][]byte) {
			tb["vhea"] = vheaTable(880, -120, 0, 9)
			tb["vmtx"] = vmtxTable(vertAdv, vertTsb, 9)[:10]
		}},
		{"vorg-short", func(tb map[string][]byte) { tb["VORG"] = make([]byte, 7) }},
		{"vorg-truncated-records", func(tb map[string][]byte) {
			full := vorgTable(880, map[uint16]int{1: 800})
			tb["VORG"] = full[:len(full)-1] // drop a byte from the single record
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := stdTables(false, false)
			c.build(tb)
			if _, err := Parse(assemble(versionTrueType, tb)); err == nil {
				t.Fatalf("%s: Parse err=nil want error", c.name)
			}
		})
	}
}
