// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

type kernPair struct {
	left, right GlyphIndex
	value       int16
}

// buildKernFormat0 builds a Microsoft/OpenType format-0 kern subtable.
func buildKernFormat0(pairs []kernPair) []byte {
	body := &bw{}
	body.u16(uint16(len(pairs)))
	body.u16(0) // searchRange
	body.u16(0) // entrySelector
	body.u16(0) // rangeShift
	for _, p := range pairs {
		body.u16(uint16(p.left))
		body.u16(uint16(p.right))
		body.i16(p.value)
	}
	length := 6 + len(body.b)
	w := &bw{}
	w.u16(0)              // subtable version
	w.u16(uint16(length)) // length (incl. 6-byte header)
	w.u16(0x0001)         // coverage: horizontal, format 0 (high byte 0)
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildKernOther builds a non-format-0 subtable (skipped by parseKern).
func buildKernOther() []byte {
	body := []byte{0, 0, 0, 0}
	length := 6 + len(body)
	w := &bw{}
	w.u16(0)
	w.u16(uint16(length))
	w.u16(0x0100) // format 1 (high byte), skipped
	w.b = append(w.b, body...)
	return w.bytes()
}

func buildKernTable(subtables [][]byte) []byte {
	w := &bw{}
	w.u16(0) // table version
	w.u16(uint16(len(subtables)))
	for _, s := range subtables {
		w.b = append(w.b, s...)
	}
	return w.bytes()
}

func TestKernParseAndLookup(t *testing.T) {
	blob := buildKernTable([][]byte{
		buildKernFormat0([]kernPair{
			{left: 1, right: 2, value: -40},
			{left: 3, right: 4, value: 15},
		}),
		buildKernOther(), // an unsupported subtable, skipped
	})
	k, err := parseKern(blob)
	if err != nil {
		t.Fatalf("parseKern: %v", err)
	}
	if got := k.Pair(1, 2); got != -40 {
		t.Errorf("Pair(1,2)=%d want -40", got)
	}
	if got := k.Pair(3, 4); got != 15 {
		t.Errorf("Pair(3,4)=%d want 15", got)
	}
	if got := k.Pair(9, 9); got != 0 { // absent pair
		t.Errorf("Pair(9,9)=%d want 0", got)
	}
}

func TestKernParseErrors(t *testing.T) {
	// Truncated table header.
	if _, err := parseKern([]byte{0, 0, 0}); err == nil {
		t.Error("truncated kern header should error")
	}
	// Truncated subtable header (nTables=1 but no subtable).
	if _, err := parseKern([]byte{0, 0, 0, 1}); err == nil {
		t.Error("truncated kern subtable header should error")
	}
	// Truncated format-0 body: nPairs (at subtable offset 6) claims 5 pairs but
	// only one is present.
	st := buildKernFormat0([]kernPair{{left: 1, right: 2, value: 5}})
	st[6] = 0x00
	st[7] = 0x05 // nPairs = 5 (only one present)
	if _, err := parseKern(buildKernTable([][]byte{st})); err == nil {
		t.Error("truncated format-0 body should error")
	}
}

func TestKernSubtableLengthOverrun(t *testing.T) {
	st := buildKernFormat0([]kernPair{{left: 1, right: 2, value: 5}})
	// Overwrite the length field with a value past the end of the table.
	st[2] = 0xFF
	st[3] = 0xFF
	if _, err := parseKern(buildKernTable([][]byte{st})); err == nil {
		t.Error("subtable length overrun should error")
	}
}
