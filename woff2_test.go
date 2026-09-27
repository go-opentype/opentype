// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"os"
	"testing"
)

// The two fixtures are the SAME face served two ways by a real font service:
// the WOFF2 a browser is given (subsetted, glyf/loca transformed) and the
// TrueType a plain client is given (the whole font, untransformed). They are
// IBM Plex Sans, under the SIL Open Font License 1.1 — see
// testdata/IBMPlexSans-OFL.txt.
const (
	woff2Fixture = "testdata/IBMPlexSans-Regular-subset.woff2"
	ttfOracle    = "testdata/IBMPlexSans-Regular-oracle.ttf"
)

func mustParseFile(t *testing.T, path string) *Font {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return f
}

// TestWOFF2MatchesTheSameFaceAsTrueType is the test this decoder needed.
//
// "It parses" proves nothing about a container decoder: one that drops a
// contour, or gets a delta's sign wrong, still yields a font every reader
// accepts and every renderer draws — wrongly. So every glyph the WOFF2 carries
// is compared, outline point by outline point and advance by advance, against
// the plain TrueType of the same face. A wrong triplet sign, a mis-sized
// substream, an off-by-one in the flag run-length encoding: each of them moves
// a point, and a moved point fails here.
func TestWOFF2MatchesTheSameFaceAsTrueType(t *testing.T) {
	got := mustParseFile(t, woff2Fixture)
	want := mustParseFile(t, ttfOracle)

	if got.unitsPerEm != want.unitsPerEm {
		t.Fatalf("unitsPerEm %d, oracle says %d", got.unitsPerEm, want.unitsPerEm)
	}
	// A spread of the Latin the subset carries: plain letters, the accented
	// forms that are usually COMPOSITE glyphs (the case the composite stream
	// exists for), digits, punctuation and a space (an empty glyph, which the
	// transform stores as no bytes at all).
	runes := []rune(" !,.0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" +
		"àâäçéèêëîïôöùûüÿÀÂÄÇÉÈÊËÎÏÔÖÙÛÜŒœ«»—’")
	gf, wf := got.NewFace(1000), want.NewFace(1000)
	compared, composites := 0, 0
	for _, r := range runes {
		gi, ok1 := got.GlyphIndex(r)
		wi, ok2 := want.GlyphIndex(r)
		if !ok1 || !ok2 {
			continue // the subset does not carry it
		}
		gc, err := got.glyphContours(gi)
		if err != nil {
			t.Errorf("%q: contours from the woff2: %v", r, err)
			continue
		}
		wc, err := want.glyphContours(wi)
		if err != nil {
			t.Errorf("%q: contours from the oracle: %v", r, err)
			continue
		}
		if len(gc) != len(wc) {
			t.Errorf("%q: %d contours, the oracle has %d", r, len(gc), len(wc))
			continue
		}
		if len(gc) > 1 {
			composites++
		}
		for ci := range gc {
			if len(gc[ci]) != len(wc[ci]) {
				t.Errorf("%q contour %d: %d points, the oracle has %d", r, ci, len(gc[ci]), len(wc[ci]))
				continue
			}
			for pi := range gc[ci] {
				if gc[ci][pi] != wc[ci][pi] {
					t.Errorf("%q contour %d point %d = %+v, the oracle says %+v", r, ci, pi, gc[ci][pi], wc[ci][pi])
				}
			}
		}
		if ga, wa := gf.AdvanceIndexUnits(gi), wf.AdvanceIndexUnits(wi); ga != wa {
			t.Errorf("%q: advance %v units, the oracle says %v", r, ga, wa)
		}
		compared++
	}
	if compared < 60 {
		t.Fatalf("only %d glyphs compared — the fixture is not exercising this", compared)
	}
	if composites < 5 {
		t.Errorf("only %d multi-contour glyphs compared; the composite path is barely exercised", composites)
	}
	t.Logf("%d glyphs matched the oracle exactly, %d of them with several contours", compared, composites)
}
