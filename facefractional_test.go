// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"math"
	"testing"
)

// ⛔⛔ A CALLER THAT WORKS IN A FINER UNIT THAN THE PIXEL MUST BE ABLE TO SAY SO.
//
// head.flags bit 3 reads "Force ppem to integer values for all internal scaler
// math; may use fractional ppem sizes if this bit is clear. It is strongly
// recommended that this be set in hinted fonts." The integer constraint belongs to
// the SCALER, not to the metrics.
//
// NewFace took an int, so a caller could not ask for 14.4 pixels per em. LaTeX's
// size ladder is a table of fractional points — \Large 14.4, \LARGE 17.28, \huge
// 20.74 — and go-tex, which measures in 1/65536 pt, had to round every step before
// building a face: \Large landed on 14, 2.8% narrow, on every section heading of
// every document (#61).
func TestNewFaceAtKeepsAFractionalSize(t *testing.T) {
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), nil, nil)
	upem := float64(f.UnitsPerEm())

	fc := f.NewFaceAt(14.4)
	if got := fc.Size(); got != 14.4 {
		t.Errorf("Size() = %v, want 14.4", got)
	}
	if got, want := fc.Scale(), 14.4/upem; math.Abs(got-want) > 1e-12 {
		t.Errorf("Scale() = %v, want %v — the metric scale must be the size asked "+
			"for, divided by unitsPerEm, with no rounding in between", got, want)
	}
}

// ⛔ THE RASTERISER STILL GETS A WHOLE PPEM, and that is the half that makes the
// change safe rather than merely convenient: hinting is defined on a whole-pixel
// grid, and the TrueType interpreter and CFF stem darkening are handed the rounded
// value. A fractional size must not reach them.
func TestAFractionalFaceRastersAtAWholePpem(t *testing.T) {
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), nil, nil)
	for _, c := range []struct {
		size float64
		ppem int
	}{{14.4, 14}, {17.28, 17}, {20.74, 21}, {10, 10}} {
		if got := f.NewFaceAt(c.size).sizePx; got != c.ppem {
			t.Errorf("NewFaceAt(%v) rasters at ppem %d, want %d", c.size, got, c.ppem)
		}
	}
}

// ⛔ AND NewFace IS UNCHANGED. It is the whole-pixel spelling of the same thing, so
// every existing caller keeps the scale it had — if this drifted, every metric in
// every dependent would move at once and nothing here would say so.
func TestNewFaceIsNewFaceAtWithAWholeSize(t *testing.T) {
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), nil, nil)
	for _, px := range []int{8, 10, 12, 1000} {
		a, b := f.NewFace(px), f.NewFaceAt(float64(px))
		if a.Scale() != b.Scale() || a.sizePx != b.sizePx || a.Size() != b.Size() {
			t.Errorf("NewFace(%d) and NewFaceAt(%v) disagree: %v/%d/%v against %v/%d/%v",
				px, float64(px), a.Scale(), a.sizePx, a.Size(), b.Scale(), b.sizePx, b.Size())
		}
	}
}

// ⭐ AND THE ARITHMETIC A UNIT-COUNTING CALLER ACTUALLY DOES comes out exact: an
// advance read in font units and multiplied by Scale() is the same number as the
// advance at a face built on the ratio directly. This is the shape go-tex uses
// (AdvanceIndexUnits × Scale), and it is the reason the fix belongs in Scale and
// not in a new rounding rule.
func TestUnitAdvanceTimesScaleIsExactAtAFractionalSize(t *testing.T) {
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), nil, nil)
	const gid GlyphIndex = 1 // descFont's hmtx gives glyph 1 an advance of 500 units
	fc := f.NewFaceAt(14.4)
	units := fc.AdvanceIndexUnits(gid)
	want := units * 14.4 / float64(f.UnitsPerEm())
	got := units * fc.Scale()
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("units×Scale() = %v, want %v", got, want)
	}
	// And it must NOT equal the whole-pixel answer, or the test is measuring
	// nothing: 14.4 and 14 have to give different advances.
	if whole := units * f.NewFace(14).Scale(); math.Abs(got-whole) < 1e-9 {
		t.Errorf("a 14.4 face and a 14 face give the same advance %v — the "+
			"fractional size is not reaching the scale", got)
	}
}
