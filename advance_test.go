// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "testing"

func TestAdvanceIndexStatic(t *testing.T) {
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), nil, nil)
	fc := f.NewFace(1000) // scale 1: pixels == font units

	// Glyph 1 has advance 500 (set by descFont's hmtx).
	if got := fc.AdvanceIndex(1); got != 500 {
		t.Errorf("AdvanceIndex(1) = %d, want 500", got)
	}
	if got := fc.AdvanceIndexUnits(1); got != 500 {
		t.Errorf("AdvanceIndexUnits(1) = %v, want 500", got)
	}
	// No vmtx: vertical advance falls back to one em (1000 units).
	if got := fc.VerticalAdvanceIndex(1); got != 1000 {
		t.Errorf("VerticalAdvanceIndex(1) = %d, want 1000", got)
	}
	// Out-of-range glyph advances by zero.
	if got := fc.AdvanceIndex(99); got != 0 {
		t.Errorf("AdvanceIndex(out of range) = %d, want 0", got)
	}
}

func TestAdvanceIndexWithVariation(t *testing.T) {
	fv := buildFvar(wghtAxis(), nil, false)
	f := makeVarFont(t, [][]byte{nil, squareGlyph()}, fv, nil, nil)
	fc := f.NewFace(1000)
	fc.SetVariation(map[string]float64{"wght": 900})

	// The variation path (varCoords != nil) is exercised; with no HVAR/gvar the
	// delta is zero, so the advance is the base 500 units.
	if got := fc.AdvanceIndex(1); got != 500 {
		t.Errorf("AdvanceIndex(1) under variation = %d, want 500", got)
	}
	if got := fc.VerticalAdvanceIndex(1); got != 1000 {
		t.Errorf("VerticalAdvanceIndex(1) under variation = %d, want 1000", got)
	}
}
