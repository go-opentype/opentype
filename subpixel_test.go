// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"math"
	"os"
	"testing"
)

func subpixelFace(t *testing.T, size float64) (*Face, GlyphIndex) {
	t.Helper()
	b, err := os.ReadFile("testdata/IBMPlexSans-Regular-oracle.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	gid, ok := f.GlyphIndex('l')
	if !ok {
		t.Fatal("no l")
	}
	return f.NewFaceAt(size), gid
}

// centroid is the horizontal centre of mass of a coverage mask, in destination
// pixels.
func centroid(t *testing.T, fc *Face, gid GlyphIndex, x float64) float64 {
	t.Helper()
	b, m, _, ok := fc.GlyphMaskIndexAt(gid, x, 0)
	if !ok || m == nil {
		t.Fatalf("no mask at %v", x)
	}
	var sum, w float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for xx := b.Min.X; xx < b.Max.X; xx++ {
			c := float64(m.AlphaAt(xx-b.Min.X, y-b.Min.Y).A)
			sum += c * (float64(xx) + 0.5)
			w += c
		}
	}
	return sum / w
}

// A glyph drawn at x+k/SubpixelPhases lands k/SubpixelPhases of a pixel to the
// right of the glyph drawn at x (to within the rasteriser's own coverage
// rounding), and phase 0 is the whole-pixel mask GlyphMaskIndex returns.
func TestGlyphMaskIndexAtShiftsBySubpixelPhases(t *testing.T) {
	fc, gid := subpixelFace(t, 22.4)
	base := centroid(t, fc, gid, 10)
	prev := 0.0
	for k := 1; k < SubpixelPhases; k++ {
		got := centroid(t, fc, gid, 10+float64(k)/SubpixelPhases) - base
		if want := float64(k) / SubpixelPhases; math.Abs(got-want) > 0.13 || got <= prev {
			t.Errorf("phase %d: shifted %.3f px, want %.3f (supersampled coverage: within 1/8 px)", k, got, want)
		}
		prev = got
	}
	b0, m0, _, _ := fc.GlyphMaskIndexAt(gid, 10, 7)
	b1, m1, _, _, ok := fc.GlyphMaskIndex(gid, 10, 7)
	if !ok || b0 != b1 || len(m0.Pix) != len(m1.Pix) {
		t.Fatalf("phase 0 differs from GlyphMaskIndex: %v %v", b0, b1)
	}
	for i := range m0.Pix {
		if m0.Pix[i] != m1.Pix[i] {
			t.Fatal("phase 0 mask differs from GlyphMaskIndex")
		}
	}
}

// The origin rounds to the nearest phase and carries into the next pixel, a
// negative origin floors, and the advance is the exact unrounded one.
func TestGlyphMaskIndexAtRoundingAndAdvance(t *testing.T) {
	fc, gid := subpixelFace(t, 22.4)
	bA, _, adv, _ := fc.GlyphMaskIndexAt(gid, 10.9, 0)
	bB, _, _, _ := fc.GlyphMaskIndexAt(gid, 11, 0)
	if bA != bB {
		t.Errorf("10.9 should round to the pixel at 11: %v vs %v", bA, bB)
	}
	bN, _, _, _ := fc.GlyphMaskIndexAt(gid, -0.5, 0)
	bP, _, _, _ := fc.GlyphMaskIndexAt(gid, 0.5, 0)
	if bP.Min.X-bN.Min.X != 1 {
		t.Errorf("negative origin: %v vs %v", bN, bP)
	}
	if want := fc.hAdvanceUnits(gid) * fc.Scale(); adv != want || adv == math.Trunc(adv) {
		t.Errorf("advance %v, want the exact %v", adv, want)
	}
	// The cache is per phase and is dropped when the face changes.
	fc.SetHinting(true)
	fc.SetStemDarkening(true)
	fc.SetVariation(nil)
	if fc.phased != nil {
		t.Error("the phase cache survived a face change")
	}
	if _, _, _, ok := fc.GlyphMaskIndexAt(GlyphIndex(60000), 0, 0); ok {
		t.Error("an out-of-range glyph drew")
	}
}

// The per-face caches are bounded: a font may hold 65535 glyphs and a page may
// ask for all of them. At the bound a cache is emptied, and everything it held
// is rebuilt on demand with the same result.
func TestFaceCachesAreBounded(t *testing.T) {
	defer func(a, b int) { maxCachedGlyphs, maxPhasedGlyphs = a, b }(maxCachedGlyphs, maxPhasedGlyphs)
	maxCachedGlyphs, maxPhasedGlyphs = 8, 8
	fc, _ := subpixelFace(t, 20)
	want := map[GlyphIndex]int{}
	for g := 0; g < 100; g++ {
		gid := GlyphIndex(g)
		_, m, _, _, ok := fc.GlyphMaskIndex(gid, 0, 0)
		if ok && m != nil {
			want[gid] = len(m.Pix)
		}
		fc.GlyphMaskIndexAt(gid, 0.25, 0)
		fc.GlyphMaskIndexAt(gid, 0.5, 0)
		if len(fc.cache) > 8 || len(fc.outlines) > 8 || len(fc.phased) > 8 {
			t.Fatalf("glyph %d: caches hold %d, %d, %d entries, bound 8", g, len(fc.cache), len(fc.outlines), len(fc.phased))
		}
	}
	if len(want) < 50 {
		t.Fatalf("only %d glyphs drew", len(want))
	}
	for gid, size := range want {
		if _, m, _, _, _ := fc.GlyphMaskIndex(gid, 0, 0); len(m.Pix) != size {
			t.Fatalf("glyph %d: rebuilt mask differs", gid)
		}
	}
}

// One decode serves every phase: the flattened outline is cached per glyph, and
// reset when the face changes.
func TestFlatOutlineIsCachedPerGlyph(t *testing.T) {
	fc, gid := subpixelFace(t, 20)
	a := fc.flat(gid)
	fc.GlyphMaskIndexAt(gid, 0.25, 0)
	fc.GlyphMaskIndexAt(gid, 0.75, 0)
	if len(fc.outlines) != 1 || len(a.polys) == 0 || &fc.flat(gid).polys[0] != &a.polys[0] {
		t.Error("the flattened outline was decoded more than once")
	}
	fc.SetVariation(nil)
	if fc.outlines != nil {
		t.Error("the outline cache survived a face change")
	}
	if fc.flat(GlyphIndex(60000)).ok {
		t.Error("an out-of-range glyph has an outline")
	}
}
