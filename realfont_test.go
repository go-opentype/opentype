// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"math"
	"os"
	"testing"
)

// This is a sanity check of the CFF grid-fitter (cffhint.go) against a real CFF
// OpenType font, Adobe's Source Serif 4 (SIL Open Font License; the font and its
// licence live in testdata/). It is not part of the deterministic coverage set —
// the synthetic cases in cffhint_test.go cover every branch — but it confirms
// the hinter runs end-to-end on production charstrings and Private DICTs and
// actually grid-fits the near-vertical stems of 'H' and 'I'. It skips cleanly
// when the font file is not present.

func loadSourceSerif(t *testing.T) *Font {
	t.Helper()
	b, err := os.ReadFile("testdata/SourceSerif4-Regular.otf")
	if err != nil {
		t.Skipf("real font not available: %v", err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(SourceSerif4): %v", err)
	}
	if f.cff == nil {
		t.Fatal("SourceSerif4-Regular.otf is expected to be a CFF (OTTO) font")
	}
	return f
}

func TestRealCFFHintingSourceSerif(t *testing.T) {
	f := loadSourceSerif(t)

	const ppem = 13 // in the 12-14 range where hinting matters most
	scale := float64(ppem) / float64(f.unitsPerEm)

	for _, r := range []rune{'H', 'I'} {
		gid, ok := f.GlyphIndex(r)
		if !ok {
			t.Fatalf("cmap has no glyph for %q", r)
		}

		fc := f.NewFace(ppem)
		unhinted, err := fc.outline(gid)
		if err != nil {
			t.Fatalf("unhinted outline %q: %v", r, err)
		}
		fc.SetHinting(true)
		hinted, err := fc.outline(gid)
		if err != nil {
			t.Fatalf("hinted outline %q: %v", r, err)
		}

		if reflectContoursEqual(unhinted, hinted) {
			t.Errorf("hinting left %q unchanged; expected the stems to be grid-fitted", r)
		}

		// Grid-fitting snaps vertical-stem edges onto whole device pixels, so the
		// hinted outline has strictly more grid-aligned x coordinates than the
		// unhinted one (whose stem edges land on arbitrary fractional pixels). The
		// serif tips, which merely interpolate between stems, stay off the grid —
		// exactly as they should.
		hGrid := gridAlignedX(hinted, scale)
		uGrid := gridAlignedX(unhinted, scale)
		if hGrid == 0 {
			t.Errorf("%q: no hinted x landed on the pixel grid", r)
		}
		if hGrid <= uGrid {
			t.Errorf("%q: hinted grid-aligned xs %d not more than unhinted %d", r, hGrid, uGrid)
		}

		// And it still rasterises.
		if _, mask, _, _, ok := fc.GlyphMaskIndex(gid, 0, 0); !ok || mask == nil {
			t.Errorf("hinted %q did not render", r)
		}
	}
}

// gridAlignedX counts contour points whose device-space x lands on a whole
// pixel (within a tight tolerance).
func gridAlignedX(cs []contour, scale float64) int {
	n := 0
	for _, c := range cs {
		for _, p := range c {
			x := p.x * scale
			if math.Abs(x-math.Round(x)) < 1e-6 {
				n++
			}
		}
	}
	return n
}
