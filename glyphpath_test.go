// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"strings"
	"testing"
)

// on/off build an outlinePoint of each curve kind, keeping the synthetic-contour
// tests terse.
func on(x, y float64) outlinePoint  { return outlinePoint{x: x, y: y, on: true} }
func off(x, y float64) outlinePoint { return outlinePoint{x: x, y: y, on: false} }

func TestGlyphOutline_RealCFF(t *testing.T) {
	f := loadSourceSerif(t)
	fc := f.NewFace(1000)
	gid, ok := f.GlyphIndex('A')
	if !ok {
		t.Fatal("SourceSerif has no glyph for 'A'")
	}
	segs, ok := fc.GlyphOutline(gid)
	if !ok || len(segs) == 0 {
		t.Fatalf("GlyphOutline('A') ok=%v len=%d", ok, len(segs))
	}
	if segs[0].Op != SegMoveTo {
		t.Errorf("first op = %v, want SegMoveTo", segs[0].Op)
	}
	if segs[len(segs)-1].Op != SegClose {
		t.Errorf("last op = %v, want SegClose", segs[len(segs)-1].Op)
	}
	// CFF outlines are pre-flattened to polylines: no quadratics, but at least
	// one line.
	lines := 0
	for _, s := range segs {
		if s.Op == SegQuadTo {
			t.Error("CFF outline unexpectedly produced a SegQuadTo")
		}
		if s.Op == SegLineTo {
			lines++
		}
	}
	if lines == 0 {
		t.Error("expected at least one SegLineTo in a CFF glyph")
	}
}

func TestGlyphOutline_BadGidAndEmpty(t *testing.T) {
	f := loadSourceSerif(t)
	fc := f.NewFace(1000)

	// One past the last valid glyph index → outline error → ok false.
	if segs, ok := fc.GlyphOutline(GlyphIndex(f.NumGlyphs())); ok || segs != nil {
		t.Errorf("out-of-range gid: got ok=%v segs=%v, want false,nil", ok, segs)
	}
	if d, ok := fc.GlyphSVGPath(GlyphIndex(f.NumGlyphs())); ok || d != "" {
		t.Errorf("out-of-range GlyphSVGPath: got ok=%v d=%q, want false,\"\"", ok, d)
	}

	// The space glyph is valid but has no contours: nil segments, ok true.
	if sp, ok := f.GlyphIndex(' '); ok {
		segs, ok := fc.GlyphOutline(sp)
		if !ok || len(segs) != 0 {
			t.Errorf("space GlyphOutline: ok=%v len=%d, want true,0", ok, len(segs))
		}
		if d, ok := fc.GlyphSVGPath(sp); !ok || d != "" {
			t.Errorf("space GlyphSVGPath: ok=%v d=%q, want true,\"\"", ok, d)
		}
	}
}

func TestGlyphSVGPath_RealCFFScales(t *testing.T) {
	f := loadSourceSerif(t)
	gid, ok := f.GlyphIndex('H')
	if !ok {
		t.Fatal("no 'H'")
	}
	d, ok := f.NewFace(1000).GlyphSVGPath(gid)
	if !ok || !strings.HasPrefix(d, "M") || !strings.Contains(d, "Z") {
		t.Fatalf("GlyphSVGPath('H') = %q ok=%v", d, ok)
	}
	// The path must not leak NaN/Inf tokens.
	for _, bad := range []string{"NaN", "Inf"} {
		if strings.Contains(d, bad) {
			t.Errorf("path contains %s: %q", bad, d)
		}
	}
	// Doubling the pixel size doubles the emitted coordinates: the y-extent of
	// the 2000px face is ~2× that of the 1000px face.
	if ext1, ext2 := yExtent(t, f, gid, 1000), yExtent(t, f, gid, 2000); ext2 < 1.8*ext1 || ext2 > 2.2*ext1 {
		t.Errorf("scaling off: ext@1000=%.2f ext@2000=%.2f", ext1, ext2)
	}
}

// yExtent is the peak absolute Y magnitude across a glyph's outline segments at
// the given pixel size — a cheap proxy for glyph height that tracks scale.
func yExtent(t *testing.T, f *Font, gid GlyphIndex, sizePx int) float64 {
	t.Helper()
	segs, ok := f.NewFace(sizePx).GlyphOutline(gid)
	if !ok {
		t.Fatalf("GlyphOutline at %dpx failed", sizePx)
	}
	max := 0.0
	for _, s := range segs {
		for _, p := range s.P {
			if v := p.Y * f.NewFace(sizePx).scale; v > max {
				max = v
			}
		}
	}
	return max
}

func TestAppendContour_Synthetic(t *testing.T) {
	seg := func(op SegmentOp, pts ...Point) Segment {
		var s Segment
		s.Op = op
		copy(s.P[:], pts)
		return s
	}
	cases := []struct {
		name string
		in   contour
		want []Segment
	}{
		{"empty", contour{}, nil},
		{
			"triangle on-curve",
			contour{on(0, 0), on(10, 0), on(5, 8)},
			[]Segment{seg(SegMoveTo, Point{0, 0}), seg(SegLineTo, Point{10, 0}), seg(SegLineTo, Point{5, 8}), seg(SegClose)},
		},
		{
			"single quadratic",
			contour{on(0, 0), off(5, 10), on(10, 0)},
			[]Segment{seg(SegMoveTo, Point{0, 0}), seg(SegQuadTo, Point{5, 10}, Point{10, 0}), seg(SegClose)},
		},
		{
			"consecutive off-curve implies midpoint",
			contour{on(0, 0), off(4, 10), off(8, 10), on(12, 0)},
			[]Segment{
				seg(SegMoveTo, Point{0, 0}),
				seg(SegQuadTo, Point{4, 10}, Point{6, 10}),
				seg(SegQuadTo, Point{8, 10}, Point{12, 0}),
				seg(SegClose),
			},
		},
		{
			"start off, last on",
			contour{off(5, 10), on(0, 0), on(10, 0)},
			[]Segment{seg(SegMoveTo, Point{10, 0}), seg(SegQuadTo, Point{5, 10}, Point{0, 0}), seg(SegClose)},
		},
		{
			"all off-curve",
			contour{off(0, 0), off(10, 0)},
			[]Segment{
				seg(SegMoveTo, Point{5, 0}),
				seg(SegQuadTo, Point{0, 0}, Point{5, 0}),
				seg(SegQuadTo, Point{10, 0}, Point{5, 0}),
				seg(SegClose),
			},
		},
		{
			"on start, trailing control closes",
			contour{on(0, 0), off(5, 10)},
			[]Segment{seg(SegMoveTo, Point{0, 0}), seg(SegQuadTo, Point{5, 10}, Point{0, 0}), seg(SegClose)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := appendContour(nil, c.in)
			if len(got) != len(c.want) {
				t.Fatalf("segments = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("segment %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestSegmentsToSVG(t *testing.T) {
	seg := func(op SegmentOp, pts ...Point) Segment {
		var s Segment
		s.Op = op
		copy(s.P[:], pts)
		return s
	}
	segs := []Segment{
		seg(SegMoveTo, Point{0, 0}),
		seg(SegLineTo, Point{10, 0}),
		seg(SegQuadTo, Point{15, 5}, Point{10, 10}),
		seg(SegClose),
	}
	// scale 2, Y flipped: (x*2, -y*2).
	got := segmentsToSVG(segs, 2)
	want := "M0 0L20 0Q30 -10 20 -20Z"
	if got != want {
		t.Errorf("segmentsToSVG = %q, want %q", got, want)
	}
}

func TestFtoa(t *testing.T) {
	cases := map[float64]string{
		12.0:     "12",
		1.25:     "1.25",
		-0.5:     "-0.5",
		0:        "0",
		3.14159:  "3.142", // rounded to three decimals
		-12.3400: "-12.34",
	}
	for in, want := range cases {
		if got := ftoa(in); got != want {
			t.Errorf("ftoa(%v) = %q, want %q", in, got, want)
		}
	}
}
